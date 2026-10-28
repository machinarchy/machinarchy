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
	// Single shared workspace: every new account joins the default workspace with
	// the least-privileged role. Owners are created by the bootstrap flow or
	// promoted with UpdateWorkspaceMemberRole.
	user.WorkspaceID = domain.DefaultWorkspaceID
	user.Role = domain.RoleReader
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_memberships (workspace_id, user_id, role)
		VALUES ($1, $2, $3)`, user.WorkspaceID, user.ID, string(user.Role)); err != nil {
		return nil, fmt.Errorf("create workspace membership: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create user transaction: %w", err)
	}
	return user, nil
}

func (s *Store) UpdateWorkspaceMemberRole(ctx context.Context, workspaceID, userID int, role domain.Role) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin member role transaction: %w", err)
	}
	defer tx.Rollback()

	// Lock the current owners (in a fixed order) so concurrent demotions are
	// serialized and each one re-reads the committed owner set.
	rows, err := tx.QueryContext(ctx, `SELECT user_id FROM workspace_memberships
		WHERE workspace_id = $1 AND role = 'owner' ORDER BY user_id FOR UPDATE`, workspaceID)
	if err != nil {
		return fmt.Errorf("lock workspace owners: %w", err)
	}
	owners := 0
	for rows.Next() {
		owners++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read workspace owners: %w", err)
	}
	rows.Close()

	var current string
	err = tx.QueryRowContext(ctx, `SELECT role FROM workspace_memberships
		WHERE workspace_id = $1 AND user_id = $2 FOR UPDATE`, workspaceID, userID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("read workspace membership: %w", err)
	}
	if current == string(domain.RoleOwner) && role != domain.RoleOwner && owners <= 1 {
		return domain.ErrLastOwner
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_memberships SET role = $1
		WHERE workspace_id = $2 AND user_id = $3`, string(role), workspaceID, userID); err != nil {
		return fmt.Errorf("update workspace member role: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit member role transaction: %w", err)
	}
	return nil
}

func (s *Store) ListWorkspaceMembers(ctx context.Context, workspaceID int) ([]domain.WorkspaceMember, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT users.id, users.username, COALESCE(users.email, ''),
		workspace_memberships.role, users.created_at
		FROM workspace_memberships JOIN users ON users.id = workspace_memberships.user_id
		WHERE workspace_memberships.workspace_id = $1
		ORDER BY users.id LIMIT $2`, workspaceID, domain.MaxWorkspaceMembers)
	if err != nil {
		return nil, fmt.Errorf("list workspace members: %w", err)
	}
	defer rows.Close()
	members := make([]domain.WorkspaceMember, 0)
	for rows.Next() {
		var member domain.WorkspaceMember
		if err := rows.Scan(&member.UserID, &member.Username, &member.Email, &member.Role, &member.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan workspace member: %w", err)
		}
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace members: %w", err)
	}
	return members, nil
}

func (s *Store) CountWorkspaceOwners(ctx context.Context, workspaceID int) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_memberships
		WHERE workspace_id = $1 AND role = $2`, workspaceID, string(domain.RoleOwner)).Scan(&count); err != nil {
		return 0, fmt.Errorf("count workspace owners: %w", err)
	}
	return count, nil
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
