package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	sqliteDriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const userColumns = `id, username, COALESCE(email, ''), password_hash,
	COALESCE(reset_token, ''), reset_token_expires, created_at`

func (s *SQLiteStore) GetUserByUsername(ctx context.Context, username string) (*domain.User, error) {
	return s.scanUserRow(s.DB.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ?`, strings.TrimSpace(username)))
}

func (s *SQLiteStore) GetUserByEmail(ctx context.Context, email string) (*domain.User, error) {
	return s.scanUserRow(s.DB.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE email = ?`, strings.TrimSpace(email)))
}

func (s *SQLiteStore) GetUserByIdentifier(ctx context.Context, identifier string) (*domain.User, error) {
	identifier = strings.TrimSpace(identifier)
	return s.scanUserRow(s.DB.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ? OR email = ?`, identifier, identifier))
}

func (s *SQLiteStore) CreateUser(ctx context.Context, username, email, passwordHash string) (*domain.User, error) {
	username, email = strings.TrimSpace(username), strings.TrimSpace(email)
	now := time.Now().UTC()
	result, err := s.DB.ExecContext(ctx, `INSERT INTO users (username, email, password_hash, created_at)
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
	return &domain.User{ID: int(id), Username: username, Email: email, PasswordHash: passwordHash, CreatedAt: now}, nil
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

func (s *SQLiteStore) scanUserRow(row *sql.Row) (*domain.User, error) {
	user := new(domain.User)
	var expires sql.NullString
	var createdAt string
	if err := row.Scan(&user.ID, &user.Username, &user.Email, &user.PasswordHash,
		&user.ResetToken, &expires, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("scan user row: %w", err)
	}
	if expires.Valid && expires.String != "" {
		parsed, err := time.Parse(time.RFC3339Nano, expires.String)
		if err != nil {
			return nil, fmt.Errorf("parse user reset token expiry: %w", err)
		}
		user.ResetTokenExpires = parsed
	}
	var err error
	if user.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return nil, fmt.Errorf("parse user creation time: %w", err)
	}
	return user, nil
}
