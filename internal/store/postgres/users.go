package postgres

import (
	"context"
	"database/sql"
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
	query := `SELECT id, username, COALESCE(email, ''), password_hash,
		COALESCE(reset_token, ''), reset_token_expires, created_at FROM users ` + predicate
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
	err := s.db.QueryRowContext(ctx, `INSERT INTO users (username, email, password_hash)
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
		&resetToken, &resetExpiry, &user.CreatedAt); err != nil {
		return nil, err
	}
	user.ResetToken = resetToken.String
	if resetExpiry.Valid {
		user.ResetTokenExpires = resetExpiry.Time
	}
	return user, nil
}
