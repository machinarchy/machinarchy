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
)

func (s *SQLiteStore) CreateHost(ctx context.Context, host *domain.Host) error {
	if host == nil || host.SSH == nil {
		return fmt.Errorf("%w: host and SSH access settings are required", domain.ErrInvalidInput)
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
		environment_id, name, hostname, ip_address, connection, labels, port,
		username, password, private_key, passphrase, expected_fingerprint,
		status, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create host transaction: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, query,
		host.EnvironmentID, host.Name, host.Hostname, host.IPAddress,
		string(host.Connection), labelsJSON, host.SSH.Port, host.SSH.Username,
		password, privateKey, passphrase, host.SSH.AuthType.ExpectedFingerprint,
		string(host.SSH.Status), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	)
	if err != nil {
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
	const query = `SELECT id, environment_id, name, hostname, ip_address, connection,
		labels, port, username, password, private_key, passphrase,
		expected_fingerprint, status, created_at, updated_at
		FROM hosts WHERE id = ?`

	var host domain.Host
	host.SSH = &domain.SSHAccess{}
	var connection, status, createdAt, updatedAt string
	var labelsJSON []byte
	var password, privateKey, passphrase string
	err := s.DB.QueryRowContext(ctx, query, strconv.Itoa(id)).Scan(
		&host.ID, &host.EnvironmentID, &host.Name, &host.Hostname, &host.IPAddress,
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

func (s *SQLiteStore) ListAllHosts(ctx context.Context) ([]*domain.Host, error) {
	const query = `SELECT id, environment_id, name, hostname, ip_address, connection,
		labels, port, username, status, created_at, updated_at
		FROM hosts ORDER BY CAST(id AS INTEGER) DESC`
	rows, err := s.DB.QueryContext(ctx, query)
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

func (s *SQLiteStore) DeleteHost(ctx context.Context, id string) error {
	result, err := s.DB.ExecContext(ctx, `DELETE FROM hosts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete host %s: %w", id, err)
	}
	return requireRowsAffected(result, domain.ErrHostNotFound)
}

func (s *SQLiteStore) UpdateHost(ctx context.Context, host *domain.Host) error {
	if host == nil || host.SSH == nil || host.ID == "" {
		return fmt.Errorf("%w: host ID and SSH access settings are required", domain.ErrInvalidInput)
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
	result, err := s.DB.ExecContext(ctx, `UPDATE hosts SET
		environment_id = ?, name = ?, hostname = ?, ip_address = ?, connection = ?,
		labels = ?, port = ?, username = ?, password = ?, private_key = ?,
		passphrase = ?, expected_fingerprint = ?, status = ?, updated_at = ?
		WHERE id = ?`,
		host.EnvironmentID, host.Name, host.Hostname, host.IPAddress, string(host.Connection),
		labelsJSON, host.SSH.Port, host.SSH.Username, password, privateKey,
		passphrase, host.SSH.AuthType.ExpectedFingerprint, string(host.SSH.Status),
		now.Format(time.RFC3339Nano), host.ID,
	)
	if err != nil {
		return fmt.Errorf("update host %s: %w", host.ID, err)
	}
	if err := requireRowsAffected(result, domain.ErrHostNotFound); err != nil {
		return err
	}
	host.Labels = labels
	host.UpdatedAt = now
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
