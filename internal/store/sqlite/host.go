package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/pkg/crypto"
	sqliteDriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func (s *SQLiteStore) CreateHost(ctx context.Context, host *domain.Host) error {
	if host == nil || host.SSH == nil || host.WorkspaceID <= 0 {
		return fmt.Errorf("%w: host, workspace, and SSH access settings are required", domain.ErrInvalidInput)
	}

	labels := host.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return fmt.Errorf("marshal host labels: %w", err)
	}
	password, privateKey, passphrase, err := s.encryptCredentials(host.SSH.AuthType)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	const query = `INSERT INTO hosts (
		workspace_id, environment_id, name, hostname, ip_address, connection, labels, port,
		username, password, private_key, passphrase, expected_fingerprint,
		status, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create host transaction: %w", err)
	}
	defer tx.Rollback()
	if err := validateHostEnvironment(ctx, tx, host.WorkspaceID, host.EnvironmentID); err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, query,
		host.WorkspaceID, host.EnvironmentID, host.Name, host.Hostname, host.IPAddress,
		string(host.Connection), labelsJSON, host.SSH.Port, host.SSH.Username,
		password, privateKey, passphrase, host.SSH.AuthType.ExpectedFingerprint,
		string(host.SSH.Status), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return fmt.Errorf("%w: %v", domain.ErrHostAlreadyExists, err)
		}
		return fmt.Errorf("insert host: %w", err)
	}

	rowID, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("read inserted host row ID: %w", err)
	}
	// The current schema uses a text primary key. Populate it with the SQLite
	// row ID in the same transaction so the returned identifier is queryable.
	hostID := strconv.FormatInt(rowID, 10)
	if _, err := tx.ExecContext(ctx, `UPDATE hosts SET id = ? WHERE rowid = ?`, hostID, rowID); err != nil {
		return fmt.Errorf("assign host ID: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create host transaction: %w", err)
	}

	host.ID = hostID
	host.Labels = labels
	host.CreatedAt = now
	host.UpdatedAt = now
	return nil
}

func (s *SQLiteStore) GetHostByID(ctx context.Context, id int) (*domain.Host, error) {
	return s.getHostByID(ctx, 0, id)
}

func (s *SQLiteStore) GetHostByIDInWorkspace(ctx context.Context, workspaceID, id int) (*domain.Host, error) {
	if workspaceID <= 0 {
		return nil, fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	return s.getHostByID(ctx, workspaceID, id)
}

func (s *SQLiteStore) getHostByID(ctx context.Context, workspaceID, id int) (*domain.Host, error) {
	query := `SELECT id, workspace_id, environment_id, name, hostname, ip_address, connection,
		labels, port, username, password, private_key, passphrase,
		expected_fingerprint, status, created_at, updated_at
		FROM hosts WHERE id = ?`
	args := []any{strconv.Itoa(id)}
	if workspaceID > 0 {
		query += ` AND workspace_id = ?`
		args = append(args, workspaceID)
	}

	var host domain.Host
	host.SSH = &domain.SSHAccess{}
	var connection, status, createdAt, updatedAt string
	var labelsJSON []byte
	var password, privateKey, passphrase string
	err := s.DB.QueryRowContext(ctx, query, args...).Scan(
		&host.ID, &host.WorkspaceID, &host.EnvironmentID, &host.Name, &host.Hostname, &host.IPAddress,
		&connection, &labelsJSON, &host.SSH.Port, &host.SSH.Username,
		&password, &privateKey, &passphrase, &host.SSH.AuthType.ExpectedFingerprint,
		&status, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrHostNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query host %d: %w", id, err)
	}

	if len(labelsJSON) != 0 {
		if err := json.Unmarshal(labelsJSON, &host.Labels); err != nil {
			return nil, fmt.Errorf("decode labels for host %d: %w", id, err)
		}
	}
	host.Connection = domain.ConnectionMethod(connection)
	host.SSH.Status = domain.HostStatus(status)
	if host.SSH.AuthType.Password, err = crypto.Decrypt(password, s.encryptionKey); err != nil {
		return nil, fmt.Errorf("decrypt password for host %d: %w", id, err)
	}
	if host.SSH.AuthType.PrivateKey, err = crypto.Decrypt(privateKey, s.encryptionKey); err != nil {
		return nil, fmt.Errorf("decrypt private key for host %d: %w", id, err)
	}
	if host.SSH.AuthType.Passphrase, err = crypto.Decrypt(passphrase, s.encryptionKey); err != nil {
		return nil, fmt.Errorf("decrypt passphrase for host %d: %w", id, err)
	}
	if host.SSH.AuthType.PrivateKey != "" {
		host.SSH.AuthType.Method = domain.AuthTypePrivateKey
	} else if host.SSH.AuthType.Password != "" {
		host.SSH.AuthType.Method = domain.AuthTypePassword
	}
	if host.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return nil, fmt.Errorf("parse creation time for host %d: %w", id, err)
	}
	if host.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return nil, fmt.Errorf("parse update time for host %d: %w", id, err)
	}
	return &host, nil
}

func (s *SQLiteStore) ListAllHosts(ctx context.Context, workspaceID int) ([]*domain.Host, error) {
	if workspaceID <= 0 {
		return nil, fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	const query = `SELECT id, environment_id, name, hostname, ip_address, connection,
		labels, port, username, status, created_at, updated_at
		FROM hosts WHERE workspace_id = ? ORDER BY CAST(id AS INTEGER) DESC`
	rows, err := s.DB.QueryContext(ctx, query, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	defer rows.Close()

	hosts := make([]*domain.Host, 0)
	for rows.Next() {
		host := &domain.Host{SSH: &domain.SSHAccess{}}
		var connection, status, createdAt, updatedAt string
		var labelsJSON []byte
		if err := rows.Scan(
			&host.ID, &host.EnvironmentID, &host.Name, &host.Hostname,
			&host.IPAddress, &connection, &labelsJSON, &host.SSH.Port,
			&host.SSH.Username, &status, &createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan host row: %w", err)
		}
		if len(labelsJSON) != 0 {
			if err := json.Unmarshal(labelsJSON, &host.Labels); err != nil {
				return nil, fmt.Errorf("decode labels for host %s: %w", host.ID, err)
			}
		}
		host.Connection = domain.ConnectionMethod(connection)
		host.SSH.Status = domain.HostStatus(status)
		if host.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
			return nil, fmt.Errorf("parse creation time for host %s: %w", host.ID, err)
		}
		if host.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
			return nil, fmt.Errorf("parse update time for host %s: %w", host.ID, err)
		}
		hosts = append(hosts, host)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate host rows: %w", err)
	}
	return hosts, nil
}

func (s *SQLiteStore) UpdateHostStatus(ctx context.Context, id string, status domain.HostStatus) error {
	result, err := s.DB.ExecContext(ctx,
		`UPDATE hosts SET status = ?, updated_at = ? WHERE id = ?`,
		string(status), time.Now().UTC().Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return fmt.Errorf("update host %s status: %w", id, err)
	}
	return requireRowsAffected(result, domain.ErrHostNotFound)
}

func (s *SQLiteStore) UpdateHostStatusWithGuard(ctx context.Context, id int, expected, next domain.HostStatus) error {
	result, err := s.DB.ExecContext(ctx,
		`UPDATE hosts SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		string(next), time.Now().UTC().Format(time.RFC3339Nano), strconv.Itoa(id), string(expected),
	)
	if err != nil {
		return fmt.Errorf("update host %d status with guard: %w", id, err)
	}
	return requireRowsAffected(result, domain.ErrConcurrentConflict)
}

func (s *SQLiteStore) DeleteHost(ctx context.Context, workspaceID int, id string) error {
	if workspaceID <= 0 {
		return fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	result, err := s.DB.ExecContext(ctx, `DELETE FROM hosts WHERE id = ? AND workspace_id = ?`, id, workspaceID)
	if err != nil {
		return fmt.Errorf("delete host %s: %w", id, err)
	}
	return requireRowsAffected(result, domain.ErrHostNotFound)
}

func (s *SQLiteStore) UpdateHost(ctx context.Context, host *domain.Host) error {
	if host == nil || host.SSH == nil || host.ID == "" || host.WorkspaceID <= 0 {
		return fmt.Errorf("%w: host ID, workspace, and SSH access settings are required", domain.ErrInvalidInput)
	}
	labels := host.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return fmt.Errorf("marshal host labels: %w", err)
	}
	password, privateKey, passphrase, err := s.encryptCredentials(host.SSH.AuthType)
	if err != nil {
		return err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin update host transaction: %w", err)
	}
	defer tx.Rollback()
	if err := validateHostEnvironment(ctx, tx, host.WorkspaceID, host.EnvironmentID); err != nil {
		return err
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE hosts SET
		environment_id = ?, name = ?, hostname = ?, ip_address = ?, connection = ?,
		labels = ?, port = ?, username = ?, password = ?, private_key = ?,
		passphrase = ?, expected_fingerprint = ?, status = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ?`,
		host.EnvironmentID, host.Name, host.Hostname, host.IPAddress, string(host.Connection),
		labelsJSON, host.SSH.Port, host.SSH.Username, password, privateKey,
		passphrase, host.SSH.AuthType.ExpectedFingerprint, string(host.SSH.Status),
		now.Format(time.RFC3339Nano), host.ID, host.WorkspaceID,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return fmt.Errorf("%w: %v", domain.ErrHostAlreadyExists, err)
		}
		return fmt.Errorf("update host %s: %w", host.ID, err)
	}
	if err := requireRowsAffected(result, domain.ErrHostNotFound); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit update host transaction: %w", err)
	}
	host.Labels = labels
	host.UpdatedAt = now
	return nil
}

func validateHostEnvironment(ctx context.Context, tx *sql.Tx, workspaceID, environmentID int) error {
	if environmentID < 0 {
		return fmt.Errorf("%w: environment ID must be non-negative", domain.ErrInvalidInput)
	}
	if environmentID == 0 {
		return nil
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM environments
		WHERE id = ? AND workspace_id = ?)`, environmentID, workspaceID).Scan(&exists); err != nil {
		return fmt.Errorf("validate host environment scope: %w", err)
	}
	if !exists {
		return domain.ErrEnvironmentNotFound
	}
	return nil
}

func (s *SQLiteStore) encryptCredentials(auth domain.SSHAuthType) (password, privateKey, passphrase string, err error) {
	if password, err = crypto.Encrypt(auth.Password, s.encryptionKey); err != nil {
		return "", "", "", fmt.Errorf("encrypt host password: %w", err)
	}
	if privateKey, err = crypto.Encrypt(auth.PrivateKey, s.encryptionKey); err != nil {
		return "", "", "", fmt.Errorf("encrypt host private key: %w", err)
	}
	if passphrase, err = crypto.Encrypt(auth.Passphrase, s.encryptionKey); err != nil {
		return "", "", "", fmt.Errorf("encrypt host passphrase: %w", err)
	}
	return password, privateKey, passphrase, nil
}

func requireRowsAffected(result sql.Result, notFound error) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected row count: %w", err)
	}
	if rows == 0 {
		return notFound
	}
	return nil
}

func isUniqueConstraint(err error) bool {
	var sqliteErr *sqliteDriver.Error
	return errors.As(err, &sqliteErr) &&
		(sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY)
}

func (s *SQLiteStore) PatchHostLabels(ctx context.Context, workspaceID, id int, set map[string]string, remove []string) (map[string]string, error) {
	if workspaceID <= 0 || id <= 0 {
		return nil, fmt.Errorf("%w: workspace and host IDs must be positive", domain.ErrInvalidInput)
	}
	if err := domain.ValidateLabelPatch(set, remove); err != nil {
		return nil, err
	}
	// SQLite serves this store through one connection, so the transaction below
	// excludes every other reader and writer until it ends.
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin label patch transaction: %w", err)
	}
	defer tx.Rollback()

	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT labels FROM hosts WHERE id = ? AND workspace_id = ?`,
		strconv.Itoa(id), workspaceID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrHostNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read host labels: %w", err)
	}
	current := map[string]string{}
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &current); err != nil {
			return nil, fmt.Errorf("decode labels for host %d: %w", id, err)
		}
	}
	merged, err := domain.ApplyLabelPatch(current, set, remove)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("marshal host labels: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hosts SET labels = ?, updated_at = ? WHERE id = ? AND workspace_id = ?`,
		encoded, time.Now().UTC().Format(time.RFC3339Nano), strconv.Itoa(id), workspaceID); err != nil {
		return nil, fmt.Errorf("update host labels: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit label patch: %w", err)
	}
	return merged, nil
}
