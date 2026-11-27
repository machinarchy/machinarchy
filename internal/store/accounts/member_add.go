package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/store/sqlx"
)

const kindAdd = "add"

// AddMember creates an account that cannot sign in (its password hash is
// domain.LockedPasswordHash), its membership and a one-time activation token
// in one transaction, with a member.added audit event. The token itself never
// reaches the store, only its hash.
func (s *Store) AddMember(ctx context.Context, m domain.NewMember) (*domain.User, error) {
	if m.WorkspaceID <= 0 || m.Username == "" || m.TokenHash == "" || (m.Role != domain.RoleOperator && m.Role != domain.RoleReader) {
		return nil, fmt.Errorf("%w: a new member needs a workspace, username, token and operator or reader role", domain.ErrInvalidInput)
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin add member transaction: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "add member")

	userID, err := insertID(ctx, tx, s.bind(`INSERT INTO users (username, email, password_hash, created_at)
		VALUES (?, NULLIF(?, ''), ?, ?)`), m.Username, m.Email, domain.LockedPasswordHash, sqlx.Time(now))
	if err != nil {
		if sqlx.IsUniqueViolation(err) {
			return nil, fmt.Errorf("%w: %w", domain.ErrUserAlreadyExists, err)
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO workspace_memberships (workspace_id, user_id, role, created_at)
		VALUES (?, ?, ?, ?)`), m.WorkspaceID, userID, string(m.Role), sqlx.Time(now)); err != nil {
		return nil, fmt.Errorf("insert workspace membership: %w", err)
	}
	if err := s.insertAddToken(ctx, tx, m.WorkspaceID, m.Username, m.Email, m.Role, m.CreatedBy, m.TokenHash, now, m.ExpiresAt); err != nil {
		return nil, err
	}
	if err := s.appendAudit(ctx, tx, domain.AuditEvent{
		WorkspaceID: m.WorkspaceID, Action: domain.AuditMemberAdded,
		TargetType: domain.AuditTargetUser, TargetID: fmt.Sprint(userID),
		Details: map[string]string{"username": m.Username, "email": m.Email, "role": string(m.Role),
			"expires_at": m.ExpiresAt.UTC().Format(time.RFC3339)},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit add member transaction: %w", err)
	}
	return &domain.User{ID: userID, WorkspaceID: m.WorkspaceID, Role: m.Role, Username: m.Username, Email: m.Email, CreatedAt: now}, nil
}

func (s *Store) insertAddToken(ctx context.Context, tx *sql.Tx, workspaceID int, username, email string, role domain.Role, createdBy int, tokenHash string, now, expires time.Time) error {
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO member_onboarding
		(workspace_id, kind, token_hash, email, username, role, created_by, created_at, expires_at)
		VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?)`),
		workspaceID, kindAdd, tokenHash, email, username, string(role), createdBy, sqlx.Time(now), sqlx.Time(expires.UTC())); err != nil {
		return fmt.Errorf("insert activation token: %w", err)
	}
	return nil
}

// ReissueMemberLink replaces the activation token of a member who has not
// activated yet, so the old token stops working. It returns ErrUserNotFound
// when the user is not a member of the workspace and ErrMemberActive when the
// account already has a password: a link must never be a way to take over a
// working account.
func (s *Store) ReissueMemberLink(ctx context.Context, workspaceID, userID, createdBy int, tokenHash string, expires time.Time) error {
	if tokenHash == "" {
		return fmt.Errorf("%w: token is required", domain.ErrInvalidInput)
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin reissue link transaction: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "reissue link")

	var username, email, hash, role string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT users.username, COALESCE(users.email, ''), users.password_hash, m.role
		FROM users JOIN workspace_memberships m ON m.user_id = users.id
		WHERE users.id = ? AND m.workspace_id = ?`), userID, workspaceID).Scan(&username, &email, &hash, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("read member: %w", err)
	}
	if hash != domain.LockedPasswordHash || (role != string(domain.RoleOperator) && role != string(domain.RoleReader)) {
		return domain.ErrMemberActive
	}
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM member_onboarding WHERE workspace_id = ? AND kind = ? AND lower(username) = lower(?)`),
		workspaceID, kindAdd, username); err != nil {
		return fmt.Errorf("drop old activation token: %w", err)
	}
	if err := s.insertAddToken(ctx, tx, workspaceID, username, email, domain.Role(role), createdBy, tokenHash, now, expires); err != nil {
		return err
	}
	if err := s.appendAudit(ctx, tx, domain.AuditEvent{
		WorkspaceID: workspaceID, Action: domain.AuditMemberLinkReissued,
		TargetType: domain.AuditTargetUser, TargetID: fmt.Sprint(userID),
		Details: map[string]string{"username": username, "expires_at": expires.UTC().Format(time.RFC3339)},
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit reissue link transaction: %w", err)
	}
	return nil
}

// activateAddedMember sets the first password of an added member. The token row
// was already deleted in tx. The update only matches an account that is still
// locked and in the token's workspace, so a token can never change the
// password of a working account, and the username given must be the one the
// owner added.
func (s *Store) activateAddedMember(ctx context.Context, tx *sql.Tx, workspaceID, tokenID int, addedUsername, givenUsername, passwordHash, role string, now time.Time) (*domain.User, error) {
	if !strings.EqualFold(addedUsername, givenUsername) {
		return nil, domain.ErrInvitationInvalid
	}
	var userID int
	var email string
	err := tx.QueryRowContext(ctx, s.bind(`SELECT users.id, COALESCE(users.email, '') FROM users
		JOIN workspace_memberships m ON m.user_id = users.id
		WHERE lower(users.username) = lower(?) AND m.workspace_id = ? AND users.password_hash = ?`),
		addedUsername, workspaceID, domain.LockedPasswordHash).Scan(&userID, &email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrInvitationInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("read added member: %w", err)
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE users SET password_hash = ? WHERE id = ? AND password_hash = ?`),
		passwordHash, userID, domain.LockedPasswordHash)
	if err != nil {
		return nil, fmt.Errorf("set first password: %w", err)
	}
	if err := sqlx.RowsAffected(result, domain.ErrInvitationInvalid); err != nil {
		return nil, err
	}
	auditCtx := domain.WithAuditActor(ctx, domain.AuditActor{UserID: userID, Username: addedUsername, Source: "onboarding"})
	if err := s.appendAudit(auditCtx, tx, domain.AuditEvent{
		WorkspaceID: workspaceID, Action: domain.AuditMemberJoined,
		TargetType: domain.AuditTargetUser, TargetID: fmt.Sprint(userID),
		Details: map[string]string{"username": addedUsername, "role": role, "onboarding_id": fmt.Sprint(tokenID)},
	}); err != nil {
		return nil, err
	}
	return &domain.User{ID: userID, WorkspaceID: workspaceID, Role: domain.Role(role), Username: addedUsername, Email: email, CreatedAt: now}, nil
}
