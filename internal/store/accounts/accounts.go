// Package accounts persists users, workspace memberships, API tokens,
// environments and the audit trail for both SQLite and PostgreSQL. It replaces
// the two near-identical per-dialect adapters (AUDIT X-01): queries are
// written once with ? placeholders, timestamps are stored as RFC 3339 text in
// both dialects, and new IDs come from RETURNING.
package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/store/sqlx"
)

// Store implements domain.UserStore, AccessStore, EnvironmentStore and
// AuditStore.
type Store struct {
	db      *sql.DB
	dialect sqlx.Dialect
	logger  *slog.Logger
}

var (
	_ domain.UserStore        = (*Store)(nil)
	_ domain.AccessStore      = (*Store)(nil)
	_ domain.EnvironmentStore = (*Store)(nil)
	_ domain.AuditStore       = (*Store)(nil)
)

// New creates a Store over db.
func New(db *sql.DB, dialect sqlx.Dialect, logger *slog.Logger) (*Store, error) {
	if db == nil || !dialect.Valid() {
		return nil, errors.New("account store requires a database and a supported dialect")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{db: db, dialect: dialect, logger: logger}, nil
}

func (s *Store) bind(query string) string { return s.dialect.Bind(query) }

// insertID runs an INSERT ... RETURNING id statement.
func insertID(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, query string, args ...any) (int, error) {
	var id int
	err := q.QueryRowContext(ctx, query+` RETURNING id`, args...).Scan(&id)
	return id, err
}

// ---- users and memberships --------------------------------------------------

const userColumns = `users.id, users.username, COALESCE(users.email, ''), users.password_hash,
	COALESCE(users.reset_token, ''), users.reset_token_expires, users.created_at,
	workspace_memberships.workspace_id, workspace_memberships.role`

const userFrom = ` FROM users JOIN workspace_memberships ON workspace_memberships.user_id = users.id `

// GetUserByUsername finds a user by username, ignoring case. It returns
// sql.ErrNoRows when there is none.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (*domain.User, error) {
	return s.getUser(ctx, `WHERE lower(users.username) = lower(?)`, strings.TrimSpace(username))
}

// GetUserByEmail finds a user by email, ignoring case.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*domain.User, error) {
	return s.getUser(ctx, `WHERE lower(users.email) = lower(?)`, strings.TrimSpace(email))
}

// GetUserByIdentifier finds a user by username or email, ignoring case.
func (s *Store) GetUserByIdentifier(ctx context.Context, identifier string) (*domain.User, error) {
	identifier = strings.TrimSpace(identifier)
	return s.getUser(ctx, `WHERE lower(users.username) = lower(?) OR lower(users.email) = lower(?)`, identifier, identifier)
}

func (s *Store) getUser(ctx context.Context, predicate string, args ...any) (*domain.User, error) {
	user := new(domain.User)
	var expires sql.NullString
	var createdAt, role string
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT `+userColumns+userFrom+predicate), args...).Scan(
		&user.ID, &user.Username, &user.Email, &user.PasswordHash, &user.ResetToken, &expires, &createdAt,
		&user.WorkspaceID, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("query user: %w", err)
	}
	user.Role = domain.Role(role)
	if expiry, err := sqlx.ParseNullTime(expires); err != nil {
		return nil, err
	} else if expiry != nil {
		user.ResetTokenExpires = *expiry
	}
	if user.CreatedAt, err = sqlx.ParseTime(createdAt); err != nil {
		return nil, err
	}
	return user, nil
}

// CreateUser creates an account in the shared workspace with the reader role.
// Owners come from the bootstrap flow or UpdateWorkspaceMemberRole.
func (s *Store) CreateUser(ctx context.Context, username, email, passwordHash string) (*domain.User, error) {
	return s.createUser(ctx, username, email, passwordHash, domain.RoleReader)
}

// CreateOwner creates the account and its owner membership in one
// transaction, so no other process can see it as a plain user (the first
// owner, AUDIT/20 F20-03).
func (s *Store) CreateOwner(ctx context.Context, username, email, passwordHash string) (*domain.User, error) {
	return s.createUser(ctx, username, email, passwordHash, domain.RoleOwner)
}

func (s *Store) createUser(ctx context.Context, username, email, passwordHash string, role domain.Role) (*domain.User, error) {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create user transaction: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "create user")

	id, err := insertID(ctx, tx, s.bind(`INSERT INTO users (username, email, password_hash, created_at)
		VALUES (?, NULLIF(?, ''), ?, ?)`), username, email, passwordHash, sqlx.Time(now))
	if err != nil {
		if sqlx.IsUniqueViolation(err) {
			return nil, fmt.Errorf("%w: %w", domain.ErrUserAlreadyExists, err)
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO workspace_memberships (workspace_id, user_id, role, created_at)
		VALUES (?, ?, ?, ?)`), domain.DefaultWorkspaceID, id, string(role), sqlx.Time(now)); err != nil {
		return nil, fmt.Errorf("insert workspace membership: %w", err)
	}
	auditCtx := ctx
	if _, ok := domain.AuditActorFrom(ctx); !ok {
		auditCtx = domain.WithAuditActor(ctx, domain.AuditActor{UserID: id, Username: username, Source: "signup"})
	}
	if err := s.appendAudit(auditCtx, tx, domain.AuditEvent{
		WorkspaceID: domain.DefaultWorkspaceID, Action: domain.AuditUserCreated,
		TargetType: domain.AuditTargetUser, TargetID: fmt.Sprint(id),
		Details: map[string]string{"username": username, "role": string(role)},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create user transaction: %w", err)
	}
	return &domain.User{ID: id, WorkspaceID: domain.DefaultWorkspaceID, Role: role,
		Username: username, Email: email, PasswordHash: passwordHash, CreatedAt: now}, nil
}

// UpdateWorkspaceMemberRole changes a member's role. Demoting the last owner
// fails with domain.ErrLastOwner; the check and the update are one statement.
func (s *Store) UpdateWorkspaceMemberRole(ctx context.Context, workspaceID, userID int, role domain.Role) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin member role transaction: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "member role")
	if s.dialect == sqlx.PostgreSQL {
		// Serialise role changes per workspace so two concurrent demotions
		// cannot both see another owner; SQLite uses one connection.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, memberLockClass, workspaceID); err != nil {
			return fmt.Errorf("lock workspace members: %w", err)
		}
	}

	var previous string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT role FROM workspace_memberships WHERE workspace_id = ? AND user_id = ?`),
		workspaceID, userID).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("read workspace membership: %w", err)
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE workspace_memberships SET role = ?
		WHERE workspace_id = ? AND user_id = ?
		AND (role <> 'owner' OR ? = 'owner'
			OR (SELECT COUNT(*) FROM workspace_memberships WHERE workspace_id = ? AND role = 'owner') > 1)`),
		string(role), workspaceID, userID, string(role), workspaceID)
	if err != nil {
		return fmt.Errorf("update workspace member role: %w", err)
	}
	if err := sqlx.RowsAffected(result, domain.ErrLastOwner); err != nil {
		return err
	}
	if previous != string(role) {
		if err := s.appendAudit(ctx, tx, domain.AuditEvent{
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

// RemoveWorkspaceMember deletes a membership and, through the foreign key, the
// member's API tokens. Removing the last owner fails with domain.ErrLastOwner;
// the check and the delete are one statement.
func (s *Store) RemoveWorkspaceMember(ctx context.Context, workspaceID, userID int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin remove member transaction: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "remove member")
	if s.dialect == sqlx.PostgreSQL {
		// Same per-workspace lock as role changes: a removal and a demotion
		// must not both see another owner.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, memberLockClass, workspaceID); err != nil {
			return fmt.Errorf("lock workspace members: %w", err)
		}
	}
	var previous string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT role FROM workspace_memberships WHERE workspace_id = ? AND user_id = ?`),
		workspaceID, userID).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("read workspace membership: %w", err)
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM workspace_memberships
		WHERE workspace_id = ? AND user_id = ?
		AND (role <> 'owner'
			OR (SELECT COUNT(*) FROM workspace_memberships WHERE workspace_id = ? AND role = 'owner') > 1)`),
		workspaceID, userID, workspaceID)
	if err != nil {
		return fmt.Errorf("delete workspace member: %w", err)
	}
	if err := sqlx.RowsAffected(result, domain.ErrLastOwner); err != nil {
		return err
	}
	if err := s.appendAudit(ctx, tx, domain.AuditEvent{
		WorkspaceID: workspaceID, Action: domain.AuditMemberRemoved,
		TargetType: domain.AuditTargetUser, TargetID: fmt.Sprint(userID),
		Details: map[string]string{"previous_role": previous},
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit remove member transaction: %w", err)
	}
	return nil
}

// memberLockClass namespaces the advisory lock for role changes ("ME").
const memberLockClass = 0x4D45

// ListWorkspaceMembers lists up to domain.MaxWorkspaceMembers members.
func (s *Store) ListWorkspaceMembers(ctx context.Context, workspaceID int) ([]domain.WorkspaceMember, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT users.id, users.username, COALESCE(users.email, ''),
		workspace_memberships.role, users.created_at
		FROM workspace_memberships JOIN users ON users.id = workspace_memberships.user_id
		WHERE workspace_memberships.workspace_id = ? ORDER BY users.id LIMIT ?`), workspaceID, domain.MaxWorkspaceMembers)
	if err != nil {
		return nil, fmt.Errorf("list workspace members: %w", err)
	}
	defer sqlx.CloseRows(rows, s.logger, "list workspace members")
	members := make([]domain.WorkspaceMember, 0)
	for rows.Next() {
		var member domain.WorkspaceMember
		var role, createdAt string
		if err := rows.Scan(&member.UserID, &member.Username, &member.Email, &role, &createdAt); err != nil {
			return nil, fmt.Errorf("scan workspace member: %w", err)
		}
		member.Role = domain.Role(role)
		if member.CreatedAt, err = sqlx.ParseTime(createdAt); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace members: %w", err)
	}
	return members, nil
}

// CountWorkspaceOwners counts the owners of a workspace.
func (s *Store) CountWorkspaceOwners(ctx context.Context, workspaceID int) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM workspace_memberships WHERE workspace_id = ? AND role = ?`),
		workspaceID, string(domain.RoleOwner)).Scan(&count); err != nil {
		return 0, fmt.Errorf("count workspace owners: %w", err)
	}
	return count, nil
}

// SaveUserResetToken stores a password reset token.
func (s *Store) SaveUserResetToken(ctx context.Context, username, token string, expiresAt time.Time) error {
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE users SET reset_token = ?, reset_token_expires = ? WHERE lower(username) = lower(?)`),
		token, sqlx.Time(expiresAt), strings.TrimSpace(username))
	if err != nil {
		return fmt.Errorf("save password reset token: %w", err)
	}
	return sqlx.RowsAffected(result, domain.ErrUserNotFound)
}

// UpdateUserPassword replaces a password hash and clears any reset token.
func (s *Store) UpdateUserPassword(ctx context.Context, userID int, passwordHash string) error {
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE users SET password_hash = ?, reset_token = NULL, reset_token_expires = NULL WHERE id = ?`),
		passwordHash, userID)
	if err != nil {
		return fmt.Errorf("update password for user %d: %w", userID, err)
	}
	return sqlx.RowsAffected(result, domain.ErrUserNotFound)
}
