package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	sqliteDriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const userColumns = `
	users.id, users.username, COALESCE(users.email, ''), users.password_hash,
	COALESCE(users.reset_token, ''), users.reset_token_expires, 
	users.created_at,
	workspace_memberships.workspace_id, workspace_memberships.role,
	COALESCE((SELECT json_group_object(key, value) FROM user_labels WHERE user_labels.user_id = users.id), '{}')
`

type rowScanner interface {
	Scan(dest ...any) error
}

// func (s *SQLiteStore) scanUserRow(row *sql.Row) (*domain.User, error) {
func (s *SQLiteStore) scanUserRow(scanner rowScanner) (*domain.User, error) {
	user := new(domain.User)
	var expires sql.NullString
	var createdAt string
	var role string
	var labelsJSON string

	if err := scanner.Scan(&user.ID, &user.Username, &user.Email, &user.PasswordHash,
		&user.ResetToken, &expires, &createdAt, &user.WorkspaceID, &role, &labelsJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("scan user row: %w", err)
	}

	// Parse JSON labels string into map[string]string
	if labelsJSON != "" && labelsJSON != "{}" {
		if err := json.Unmarshal([]byte(labelsJSON), &user.Labels); err != nil {
			return nil, fmt.Errorf("parse user labels json: %w", err)
		}
	}

	if expires.Valid && expires.String != "" {
		parsed, err := time.Parse(time.RFC3339Nano, expires.String)
		if err != nil {
			return nil, fmt.Errorf("parse user reset token expiry: %w", err)
		}
		user.ResetTokenExpires = parsed
	}

	var err error
	user.Role = domain.Role(role)
	if user.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return nil, fmt.Errorf("parse user creation time: %w", err)
	}

	return user, nil
}

func (s *SQLiteStore) GetUserByUsername(ctx context.Context, username string) (*domain.User, error) {
	return s.scanUserRow(s.DB.QueryRowContext(ctx, `
		SELECT `+userColumns+` 
		FROM users
		JOIN workspace_memberships ON workspace_memberships.user_id = users.id
		WHERE users.username = ?`, strings.TrimSpace(username)))
}

func (s *SQLiteStore) GetUsersByLabel(ctx context.Context, key, value string) ([]*domain.User, error) {
	query := `
		SELECT ` + userColumns + ` 
		FROM users
		JOIN workspace_memberships ON workspace_memberships.user_id = users.id
		JOIN user_labels ON user_labels.user_id = users.id
		WHERE user_labels.key = ? AND user_labels.value = ? AND workspace_memberships.workspace_id = 1`

	rows, err := s.DB.QueryContext(ctx, query, strings.TrimSpace(key), strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("query users by label: %w", err)
	}
	defer rows.Close()

	var users []*domain.User
	for rows.Next() {
		user, err := s.scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate users by label rows: %w", err)
	}

	return users, nil
}

func (s *SQLiteStore) SetUserLabel(ctx context.Context, userID int, key, value string) error {
	query := `
		INSERT INTO user_labels (user_id, key, value)
		VALUES (?, ?, ?)
		ON CONFLICT (user_id, key) 
		DO UPDATE SET value = EXCLUDED.value`

	_, err := s.DB.ExecContext(ctx, query, userID, strings.TrimSpace(key), strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("set user label: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetUserByEmail(ctx context.Context, email string) (*domain.User, error) {
	return s.scanUserRow(s.DB.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users
		JOIN workspace_memberships ON workspace_memberships.user_id = users.id
		WHERE users.email = ?`, strings.TrimSpace(email)))
}

func (s *SQLiteStore) GetUserByIdentifier(ctx context.Context, identifier string) (*domain.User, error) {
	identifier = strings.TrimSpace(identifier)
	return s.scanUserRow(s.DB.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users
		JOIN workspace_memberships ON workspace_memberships.user_id = users.id
		WHERE users.username = ? OR users.email = ?`, identifier, identifier))
}

func (s *SQLiteStore) CreateUser(ctx context.Context, username, email, passwordHash string) (*domain.User, error) {
	username, email = strings.TrimSpace(username), strings.TrimSpace(email)
	now := time.Now().UTC()

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create user transaction: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `INSERT INTO users (username, email, password_hash, created_at)
		VALUES (?, NULLIF(?, ''), ?, ?)`, username, email, passwordHash, now.Format(time.RFC3339Nano))
	if err != nil {
		var sqliteErr *sqliteDriver.Error
		if errors.As(err, &sqliteErr) && (sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY) {
			return nil, fmt.Errorf("%w: %v", domain.ErrUserAlreadyExists, err)
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("read inserted user ID: %w", err)
	}
	workspaceID, err := createUserWorkspace(ctx, tx, int(id), username)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create user transaction: %w", err)
	}
	// [HARD_CODE] role to role-reader
	return &domain.User{ID: int(id), WorkspaceID: workspaceID, Role: domain.RoleReader,
		Username: username, Email: email, PasswordHash: passwordHash, CreatedAt: now}, nil
}

func createUserWorkspace(ctx context.Context, tx *sql.Tx, userID int, username string) (int, error) {
	result, err := tx.ExecContext(ctx, `UPDATE workspaces SET owner_user_id = ?, name = ?
		WHERE id = 1 AND owner_user_id IS NULL`, userID, "workspace-"+username)
	if err != nil {
		return 0, fmt.Errorf("claim initial workspace: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("check initial workspace claim: %w", err)
	}
	workspaceID := int64(1)
	if count == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO workspaces (name, owner_user_id, created_at)
			VALUES (?, ?, ?)`, "workspace-"+username, userID, time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return 0, fmt.Errorf("create user workspace: %w", err)
		}
		// workspaceID, err = result.LastInsertId()
		// if err != nil {
		// 	return 0, fmt.Errorf("read workspace ID: %w", err)
		// }
	}
	// [HARD_CODE] role to role-reader
	workspaceID = int64(1)
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_memberships (workspace_id, user_id, role)
		VALUES (?, ?, ?)`, workspaceID, userID, string(domain.RoleReader)); err != nil {
		return 0, fmt.Errorf("create workspace membership: %w", err)
	}
	return int(workspaceID), nil
}

func (s *SQLiteStore) UpdateWorkspaceMemberRole(ctx context.Context, workspaceID, userID int, role string) error {
	query := `UPDATE workspace_memberships SET role = $1 WHERE workspace_id = $2 AND user_id = $3`
	result, err := s.DB.ExecContext(ctx, query, role, workspaceID, userID)
	if err != nil {
		return fmt.Errorf("update workspace member role: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check rows affected: %w", err)
	}
	if rows == 0 {
		return errors.New("workspace membership not found for role update")
	}

	return nil
}

func (s *SQLiteStore) SaveUserResetToken(ctx context.Context, username, token string, expiresAt time.Time) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE users SET reset_token = ?, reset_token_expires = ? WHERE username = ?`,
		token, expiresAt.UTC().Format(time.RFC3339Nano), strings.TrimSpace(username))
	if err != nil {
		return fmt.Errorf("save password reset token: %w", err)
	}
	return requireRowsAffected(result, domain.ErrUserNotFound)
}

func (s *SQLiteStore) UpdateUserPassword(ctx context.Context, userID int, passwordHash string) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE users SET password_hash = ?,
		reset_token = NULL, reset_token_expires = NULL WHERE id = ?`, passwordHash, userID)
	if err != nil {
		return fmt.Errorf("update password for user %d: %w", userID, err)
	}
	return requireRowsAffected(result, domain.ErrUserNotFound)
}
