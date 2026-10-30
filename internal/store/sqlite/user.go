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

const userColumns = `users.id, users.username, COALESCE(users.email, ''), users.password_hash,
	COALESCE(users.reset_token, ''), users.reset_token_expires, users.created_at,
	workspace_memberships.workspace_id, workspace_memberships.role`

func (s *SQLiteStore) GetUserByUsername(ctx context.Context, username string) (*domain.User, error) {
	return s.scanUserRow(s.DB.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users
		JOIN workspace_memberships ON workspace_memberships.user_id = users.id
		WHERE users.username = ?`, strings.TrimSpace(username)))
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_memberships (workspace_id, user_id, role)
		VALUES (?, ?, ?)`, domain.DefaultWorkspaceID, id, string(domain.RoleReader)); err != nil {
		return nil, fmt.Errorf("create workspace membership: %w", err)
	}
	auditCtx := ctx
	if _, ok := domain.AuditActorFrom(ctx); !ok {
		// Self-registration: the new account is its own actor.
		auditCtx = domain.WithAuditActor(ctx, domain.AuditActor{UserID: int(id), Username: username, Source: "signup"})
	}
	if err := appendAudit(auditCtx, tx, domain.AuditEvent{
		WorkspaceID: domain.DefaultWorkspaceID, Action: domain.AuditUserCreated,
		TargetType: domain.AuditTargetUser, TargetID: fmt.Sprint(id),
		Details: map[string]string{"username": username, "role": string(domain.RoleReader)},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create user transaction: %w", err)
	}
	// Single shared workspace: every new account joins the default workspace with
	// the least-privileged role. Owners are created by the bootstrap flow or
	// promoted with UpdateWorkspaceMemberRole.
	return &domain.User{ID: int(id), WorkspaceID: domain.DefaultWorkspaceID, Role: domain.RoleReader,
		Username: username, Email: email, PasswordHash: passwordHash, CreatedAt: now}, nil
}

func (s *SQLiteStore) UpdateWorkspaceMemberRole(ctx context.Context, workspaceID, userID int, role domain.Role) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin member role transaction: %w", err)
	}
	defer tx.Rollback()

	var previous string
	err = tx.QueryRowContext(ctx, `SELECT role FROM workspace_memberships
		WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("read workspace membership: %w", err)
	}

	// One statement, so the "keep at least one owner" check and the update are
	// atomic. A member who is not an owner, or a change that keeps them an owner,
	// always matches; demoting an owner matches only while another owner exists.
	result, err := tx.ExecContext(ctx, `UPDATE workspace_memberships SET role = ?1
		WHERE workspace_id = ?2 AND user_id = ?3
		AND (role <> 'owner' OR ?1 = 'owner'
			OR (SELECT COUNT(*) FROM workspace_memberships
				WHERE workspace_id = ?2 AND role = 'owner') > 1)`,
		string(role), workspaceID, userID)
	if err != nil {
		return fmt.Errorf("update workspace member role: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read updated membership rows: %w", err)
	}
	if rows == 0 {
		return domain.ErrLastOwner
	}
	if previous != string(role) {
		if err := appendAudit(ctx, tx, domain.AuditEvent{
			WorkspaceID: workspaceID, Action: domain.AuditMemberRoleChanged,
			TargetType: domain.AuditTargetUser, TargetID: fmt.Sprint(userID),
			Details: map[string]string{"previous_role": previous, "new_role": string(role)},
		}); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit member role transaction: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ListWorkspaceMembers(ctx context.Context, workspaceID int) ([]domain.WorkspaceMember, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT users.id, users.username, COALESCE(users.email, ''),
		workspace_memberships.role, users.created_at
		FROM workspace_memberships JOIN users ON users.id = workspace_memberships.user_id
		WHERE workspace_memberships.workspace_id = ?
		ORDER BY users.id LIMIT ?`, workspaceID, domain.MaxWorkspaceMembers)
	if err != nil {
		return nil, fmt.Errorf("list workspace members: %w", err)
	}
	defer rows.Close()
	members := make([]domain.WorkspaceMember, 0)
	for rows.Next() {
		var member domain.WorkspaceMember
		var role, createdAt string
		if err := rows.Scan(&member.UserID, &member.Username, &member.Email, &role, &createdAt); err != nil {
			return nil, fmt.Errorf("scan workspace member: %w", err)
		}
		member.Role = domain.Role(role)
		member.CreatedAt = parseStoredTime(createdAt)
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace members: %w", err)
	}
	return members, nil
}

// parseStoredTime reads timestamps written by CreateUser (RFC 3339) and by
// SQLite's CURRENT_TIMESTAMP default used by older rows. Unparseable values
// yield the zero time rather than failing a listing.
func parseStoredTime(value string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func (s *SQLiteStore) CountWorkspaceOwners(ctx context.Context, workspaceID int) (int, error) {
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_memberships
		WHERE workspace_id = ? AND role = ?`, workspaceID, string(domain.RoleOwner)).Scan(&count); err != nil {
		return 0, fmt.Errorf("count workspace owners: %w", err)
	}
	return count, nil
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
	var role string
	if err := row.Scan(&user.ID, &user.Username, &user.Email, &user.PasswordHash,
		&user.ResetToken, &expires, &createdAt, &user.WorkspaceID, &role); err != nil {
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
	user.Role = domain.Role(role)
	if user.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return nil, fmt.Errorf("parse user creation time: %w", err)
	}
	return user, nil
}
