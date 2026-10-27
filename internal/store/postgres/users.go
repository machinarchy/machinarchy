package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

func (s *Store) GetUserByUsername(ctx context.Context, username string) (*domain.User, error) {
	return s.getUser(ctx, `WHERE username = $1`, strings.TrimSpace(username))
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (*domain.User, error) {
	return s.getUser(ctx, `WHERE email = $1`, strings.TrimSpace(email))
}

func (s *Store) GetUserByIdentifier(ctx context.Context, identifier string) (*domain.User, error) {
	return s.getUser(ctx, `WHERE username = $1 OR email = $1`, strings.TrimSpace(identifier))
}

func (s *Store) getUser(ctx context.Context, predicate, value string) (*domain.User, error) {
	query := `SELECT users.id, users.username, COALESCE(users.email, ''), users.password_hash,
		COALESCE(users.reset_token, ''), users.reset_token_expires, users.created_at,
		workspace_memberships.workspace_id, workspace_memberships.role
		FROM users JOIN workspace_memberships ON workspace_memberships.user_id = users.id ` + predicate
	user, err := scanUser(s.db.QueryRowContext(ctx, query, value))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("query user: %w", err)
	}
	return user, nil
}

func (s *Store) CreateUser(ctx context.Context, username, email, passwordHash string) (*domain.User, error) {
	username, email = strings.TrimSpace(username), strings.TrimSpace(email)
	user := &domain.User{}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create user transaction: %w", err)
	}
	defer tx.Rollback()

	err = tx.QueryRowContext(ctx, `INSERT INTO users (username, email, password_hash)
		VALUES ($1, NULLIF($2, ''), $3)
		RETURNING id, username, COALESCE(email, ''), password_hash, created_at`,
		username, email, passwordHash,
	).Scan(&user.ID, &user.Username, &user.Email, &user.PasswordHash, &user.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: %v", domain.ErrUserAlreadyExists, err)
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}

	err = tx.QueryRowContext(ctx, `UPDATE workspaces SET owner_user_id = $1, name = $2
		WHERE id = 1 AND owner_user_id IS NULL RETURNING id`, user.ID, "workspace-"+username).Scan(&user.WorkspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `INSERT INTO workspaces (name, owner_user_id)
			VALUES ($1, $2) RETURNING id`, "workspace-"+username, user.ID).Scan(&user.WorkspaceID)
	}
	if err != nil {
		return nil, fmt.Errorf("create user workspace: %w", err)
	}

	// [hard fix] Force every user to belong to Workspace ID 1 (Default Workspace)
	user.WorkspaceID = 1
	// user.Role = domain.RoleOwner
	user.Role = domain.RoleReader

	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_memberships (workspace_id, user_id, role)
		VALUES ($1, $2, $3)`, user.WorkspaceID, user.ID, string(domain.RoleOwner)); err != nil {
		return nil, fmt.Errorf("create workspace membership: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create user transaction: %w", err)
	}
	return user, nil
}

func (s *Store) SaveUserResetToken(ctx context.Context, username, token string, expiresAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE users SET reset_token = $1,
		reset_token_expires = $2 WHERE username = $3`, token, expiresAt, username)
	if err != nil {
		return fmt.Errorf("save password reset token: %w", err)
	}
	return affected(result, sql.ErrNoRows)
}

func (s *Store) UpdateUserPassword(ctx context.Context, userID int, passwordHash string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = $1,
		reset_token = NULL, reset_token_expires = NULL WHERE id = $2`, passwordHash, userID)
	if err != nil {
		return fmt.Errorf("update password for user %d: %w", userID, err)
	}
	return affected(result, sql.ErrNoRows)
}

func scanUser(row *sql.Row) (*domain.User, error) {
	user := &domain.User{}
	var resetToken sql.NullString
	var resetExpiry sql.NullTime
	if err := row.Scan(&user.ID, &user.Username, &user.Email, &user.PasswordHash,
		&resetToken, &resetExpiry, &user.CreatedAt, &user.WorkspaceID, &user.Role); err != nil {
		return nil, err
	}
	user.ResetToken = resetToken.String
	if resetExpiry.Valid {
		user.ResetTokenExpires = resetExpiry.Time
	}
	return user, nil
}

const userColumns = `
	users.id, 
	users.username, 
	COALESCE(users.email, ''), 
	users.password_hash,
	COALESCE(users.reset_token, ''), 
	users.reset_token_expires, 
	users.created_at,
	workspace_memberships.workspace_id, 
	workspace_memberships.role,
	COALESCE((SELECT jsonb_object_agg(key, value)::text FROM user_labels WHERE user_labels.user_id = users.id), '{}')
`

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanUserRow(scanner rowScanner) (*domain.User, error) {
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

// GetUsersByLabel queries all users matching a specific label key-value pair in Postgres.
func (s *Store) GetUsersByLabel(ctx context.Context, key, value string) ([]*domain.User, error) {
	query := `
		SELECT ` + userColumns + ` 
		FROM users
		JOIN workspace_memberships ON workspace_memberships.user_id = users.id
		JOIN user_labels ON user_labels.user_id = users.id
		WHERE user_labels.key = $1 AND user_labels.value = $2 AND workspace_memberships.workspace_id = 1`

	rows, err := s.db.QueryContext(ctx, query, strings.TrimSpace(key), strings.TrimSpace(value))
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

// SetUserLabel assigns or updates a key-value label on a user in Postgres.
func (s *Store) SetUserLabel(ctx context.Context, userID int, key, value string) error {
	query := `
		INSERT INTO user_labels (user_id, key, value)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, key) 
		DO UPDATE SET value = EXCLUDED.value`

	_, err := s.db.ExecContext(ctx, query, userID, strings.TrimSpace(key), strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("set user label: %w", err)
	}
	return nil
}
