package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/lib/pq"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/pkg/crypto"
)

type Store struct {
	db            *sql.DB
	encryptionKey []byte
}

var (
	_ domain.UserStore        = (*Store)(nil)
	_ domain.MetricsStore     = (*Store)(nil)
	_ domain.HostStore        = (*Store)(nil)
	_ domain.JobStore         = (*Store)(nil)
	_ domain.EnvironmentStore = (*Store)(nil)
)

func NewStore(db *sql.DB, secret string) (*Store, error) {
	if db == nil {
		return nil, errors.New("PostgreSQL database handle is required")
	}
	key, err := crypto.DeriveKey(secret, "postgres-host-credentials")
	if err != nil {
		return nil, fmt.Errorf("derive PostgreSQL credential encryption key: %w", err)
	}
	return &Store{db: db, encryptionKey: key}, nil
}

func (s *Store) CreateHost(ctx context.Context, host *domain.Host) error {
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create host transaction: %w", err)
	}
	defer tx.Rollback()
	if err := validateHostEnvironment(ctx, tx, host.WorkspaceID, host.EnvironmentID); err != nil {
		return err
	}
	now := time.Now().UTC()
	err = tx.QueryRowContext(ctx, `INSERT INTO hosts (
		id, workspace_id, environment_id, name, hostname, ip_address, connection, labels, port,
		username, password, private_key, passphrase, expected_fingerprint,
		status, created_at, updated_at
	) VALUES (nextval('hosts_id_seq')::TEXT, $1, $2, $3, $4, $5, $6, $7, $8, $9,
		$10, $11, $12, $13, $14, $15, $16) RETURNING id`,
		host.WorkspaceID, host.EnvironmentID, host.Name, host.Hostname, host.IPAddress,
		string(host.Connection), string(labelsJSON), host.SSH.Port, host.SSH.Username,
		password, privateKey, passphrase, host.SSH.AuthType.ExpectedFingerprint,
		string(host.SSH.Status), now, now,
	).Scan(&host.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: %v", domain.ErrHostAlreadyExists, err)
		}
		return fmt.Errorf("insert PostgreSQL host: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create host transaction: %w", err)
	}
	host.Labels = labels
	host.CreatedAt, host.UpdatedAt = now, now
	return nil
}

func (s *Store) GetHostByID(ctx context.Context, id int) (*domain.Host, error) {
	return s.getHostByID(ctx, 0, id)
}

func (s *Store) GetHostByIDInWorkspace(ctx context.Context, workspaceID, id int) (*domain.Host, error) {
	if workspaceID <= 0 {
		return nil, fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	return s.getHostByID(ctx, workspaceID, id)
}

func (s *Store) getHostByID(ctx context.Context, workspaceID, id int) (*domain.Host, error) {
	query := `SELECT id, workspace_id, environment_id, name, hostname, ip_address, connection,
		labels, port, username, password, private_key, passphrase,
		expected_fingerprint, status, created_at, updated_at
		FROM hosts WHERE id = $1`
	args := []any{strconv.Itoa(id)}
	if workspaceID > 0 {
		query += ` AND workspace_id = $2`
		args = append(args, workspaceID)
	}
	var host domain.Host
	host.SSH = &domain.SSHAccess{}
	var connection, status string
	var labelsJSON []byte
	var password, privateKey, passphrase string
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&host.ID, &host.WorkspaceID, &host.EnvironmentID, &host.Name, &host.Hostname, &host.IPAddress,
		&connection, &labelsJSON, &host.SSH.Port, &host.SSH.Username,
		&password, &privateKey, &passphrase, &host.SSH.AuthType.ExpectedFingerprint,
		&status, &host.CreatedAt, &host.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrHostNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query PostgreSQL host %d: %w", id, err)
	}
	if err := json.Unmarshal(labelsJSON, &host.Labels); err != nil {
		return nil, fmt.Errorf("decode labels for host %s: %w", host.ID, err)
	}
	if host.SSH.AuthType.Password, err = crypto.Decrypt(password, s.encryptionKey); err != nil {
		return nil, fmt.Errorf("decrypt password for host %s: %w", host.ID, err)
	}
	if host.SSH.AuthType.PrivateKey, err = crypto.Decrypt(privateKey, s.encryptionKey); err != nil {
		return nil, fmt.Errorf("decrypt private key for host %s: %w", host.ID, err)
	}
	if host.SSH.AuthType.Passphrase, err = crypto.Decrypt(passphrase, s.encryptionKey); err != nil {
		return nil, fmt.Errorf("decrypt passphrase for host %s: %w", host.ID, err)
	}
	if host.SSH.AuthType.PrivateKey != "" {
		host.SSH.AuthType.Method = domain.AuthTypePrivateKey
	} else if host.SSH.AuthType.Password != "" {
		host.SSH.AuthType.Method = domain.AuthTypePassword
	}
	host.Connection, host.SSH.Status = domain.ConnectionMethod(connection), domain.HostStatus(status)
	return &host, nil
}

func (s *Store) ListAllHosts(ctx context.Context, workspaceID int) ([]*domain.Host, error) {
	if workspaceID <= 0 {
		return nil, fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, environment_id, name, hostname,
		ip_address, connection, labels, port, username, status, created_at, updated_at
		FROM hosts WHERE workspace_id = $1 ORDER BY id::BIGINT DESC`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list PostgreSQL hosts: %w", err)
	}
	defer rows.Close()

	hosts := make([]*domain.Host, 0)
	for rows.Next() {
		host := &domain.Host{SSH: &domain.SSHAccess{}}
		var connection, status string
		var labelsJSON []byte
		if err := rows.Scan(&host.ID, &host.EnvironmentID, &host.Name, &host.Hostname,
			&host.IPAddress, &connection, &labelsJSON, &host.SSH.Port,
			&host.SSH.Username, &status, &host.CreatedAt, &host.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan PostgreSQL host row: %w", err)
		}
		if err := json.Unmarshal(labelsJSON, &host.Labels); err != nil {
			return nil, fmt.Errorf("decode labels for host %s: %w", host.ID, err)
		}
		host.Connection, host.SSH.Status = domain.ConnectionMethod(connection), domain.HostStatus(status)
		hosts = append(hosts, host)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate PostgreSQL hosts: %w", err)
	}
	return hosts, nil
}

func (s *Store) UpdateHostStatus(ctx context.Context, id string, status domain.HostStatus) error {
	result, err := s.db.ExecContext(ctx, `UPDATE hosts SET status = $1, updated_at = $2 WHERE id = $3`,
		string(status), time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("update PostgreSQL host %s status: %w", id, err)
	}
	return affected(result, domain.ErrHostNotFound)
}

func (s *Store) UpdateHostStatusWithGuard(ctx context.Context, id int, expected, next domain.HostStatus) error {
	result, err := s.db.ExecContext(ctx, `UPDATE hosts SET status = $1, updated_at = $2
		WHERE id = $3 AND status = $4`, string(next), time.Now().UTC(), strconv.Itoa(id), string(expected))
	if err != nil {
		return fmt.Errorf("update PostgreSQL host %d status with guard: %w", id, err)
	}
	return affected(result, domain.ErrConcurrentConflict)
}

func (s *Store) DeleteHost(ctx context.Context, workspaceID int, id string) error {
	if workspaceID <= 0 {
		return fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM hosts WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if err != nil {
		return fmt.Errorf("delete PostgreSQL host %s: %w", id, err)
	}
	return affected(result, domain.ErrHostNotFound)
}

func (s *Store) UpdateHost(ctx context.Context, host *domain.Host) error {
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin update host transaction: %w", err)
	}
	defer tx.Rollback()
	if err := validateHostEnvironment(ctx, tx, host.WorkspaceID, host.EnvironmentID); err != nil {
		return err
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE hosts SET environment_id = $1, name = $2,
		hostname = $3, ip_address = $4, connection = $5, labels = $6, port = $7,
		username = $8, password = $9, private_key = $10, passphrase = $11,
		expected_fingerprint = $12, status = $13, updated_at = $14
		WHERE id = $15 AND workspace_id = $16`,
		host.EnvironmentID, host.Name, host.Hostname, host.IPAddress, string(host.Connection),
		string(labelsJSON), host.SSH.Port, host.SSH.Username, password, privateKey, passphrase,
		host.SSH.AuthType.ExpectedFingerprint, string(host.SSH.Status), now, host.ID, host.WorkspaceID)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: %v", domain.ErrHostAlreadyExists, err)
		}
		return fmt.Errorf("update PostgreSQL host %s: %w", host.ID, err)
	}
	if err := affected(result, domain.ErrHostNotFound); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit update host transaction: %w", err)
	}
	host.Labels, host.UpdatedAt = labels, now
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
		WHERE id = $1 AND workspace_id = $2)`, environmentID, workspaceID).Scan(&exists); err != nil {
		return fmt.Errorf("validate host environment scope: %w", err)
	}
	if !exists {
		return domain.ErrEnvironmentNotFound
	}
	return nil
}

func (s *Store) encryptCredentials(auth domain.SSHAuthType) (password, privateKey, passphrase string, err error) {
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

func affected(result sql.Result, notFound error) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected row count: %w", err)
	}
	if count == 0 {
		return notFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var postgresErr *pq.Error
	return errors.As(err, &postgresErr) && postgresErr.Code == "23505"
}
