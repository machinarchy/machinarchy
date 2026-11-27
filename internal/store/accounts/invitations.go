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

var _ domain.InvitationStore = (*Store)(nil)

const kindInvite = "invite"

// CreateInvitation records an invitation. A pending invitation for the same
// email is replaced, expired ones are dropped, and the workspace is capped at
// domain.MaxPendingInvitations. The audit event is written in the same
// transaction and never carries the token or its hash.
func (s *Store) CreateInvitation(ctx context.Context, invitation domain.NewInvitation) (*domain.Invitation, error) {
	email := strings.ToLower(strings.TrimSpace(invitation.Email))
	if invitation.WorkspaceID <= 0 || email == "" || invitation.TokenHash == "" || !domain.ValidRole(invitation.Role) ||
		invitation.Role == domain.RoleOwner {
		return nil, fmt.Errorf("%w: invitation needs a workspace, email, token and operator or reader role", domain.ErrInvalidInput)
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create invitation transaction: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "create invitation")
	if s.dialect == sqlx.PostgreSQL {
		// The cap below must not be passed by two concurrent invitations.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, invitationLockClass, invitation.WorkspaceID); err != nil {
			return nil, fmt.Errorf("lock workspace invitations: %w", err)
		}
	}

	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id, lower(email), expires_at FROM member_onboarding
		WHERE workspace_id = ? AND kind = ?`), invitation.WorkspaceID, kindInvite)
	if err != nil {
		return nil, fmt.Errorf("list pending invitations: %w", err)
	}
	var drop []int
	pending := 0
	for rows.Next() {
		var id int
		var rowEmail, expires string
		if err := rows.Scan(&id, &rowEmail, &expires); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan invitation: %w", err)
		}
		expiry, err := sqlx.ParseTime(expires)
		if err != nil || !expiry.After(now) || rowEmail == email {
			drop = append(drop, id) // expired, unreadable, or replaced by this one
			continue
		}
		pending++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("list pending invitations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close invitation list: %w", err)
	}
	if pending >= domain.MaxPendingInvitations {
		return nil, domain.ErrInvitationLimit
	}
	for _, id := range drop {
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM member_onboarding WHERE id = ?`), id); err != nil {
			return nil, fmt.Errorf("drop replaced invitation: %w", err)
		}
	}

	id, err := insertID(ctx, tx, s.bind(`INSERT INTO member_onboarding
		(workspace_id, kind, token_hash, email, role, created_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		invitation.WorkspaceID, kindInvite, invitation.TokenHash, email, string(invitation.Role), invitation.InvitedBy,
		sqlx.Time(now), sqlx.Time(invitation.ExpiresAt.UTC()))
	if err != nil {
		return nil, fmt.Errorf("insert invitation: %w", err)
	}
	if err := s.appendAudit(ctx, tx, domain.AuditEvent{
		WorkspaceID: invitation.WorkspaceID, Action: domain.AuditMemberInvited,
		TargetType: domain.AuditTargetInvitation, TargetID: fmt.Sprint(id),
		Details: map[string]string{"email": email, "role": string(invitation.Role),
			"expires_at": invitation.ExpiresAt.UTC().Format(time.RFC3339)},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create invitation transaction: %w", err)
	}
	return &domain.Invitation{ID: id, Email: email, Role: invitation.Role, CreatedAt: now, ExpiresAt: invitation.ExpiresAt.UTC()}, nil
}

// ListInvitations returns the unexpired invitations of a workspace, oldest first.
func (s *Store) ListInvitations(ctx context.Context, workspaceID int) ([]domain.Invitation, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT o.id, COALESCE(o.email, ''), o.role, COALESCE(u.username, ''), o.created_at, o.expires_at
		FROM member_onboarding o LEFT JOIN users u ON u.id = o.created_by
		WHERE o.workspace_id = ? AND o.kind = ? ORDER BY o.id LIMIT ?`), workspaceID, kindInvite, domain.MaxPendingInvitations*2)
	if err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	now := time.Now().UTC()
	invitations := []domain.Invitation{}
	for rows.Next() {
		var inv domain.Invitation
		var role, created, expires string
		if err := rows.Scan(&inv.ID, &inv.Email, &role, &inv.InvitedBy, &created, &expires); err != nil {
			return nil, fmt.Errorf("scan invitation: %w", err)
		}
		inv.Role = domain.Role(role)
		if inv.CreatedAt, err = sqlx.ParseTime(created); err != nil {
			return nil, err
		}
		if inv.ExpiresAt, err = sqlx.ParseTime(expires); err != nil {
			return nil, err
		}
		if inv.ExpiresAt.After(now) {
			invitations = append(invitations, inv)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}
	return invitations, nil
}

// RevokeInvitation deletes an invitation, so its token stops working.
func (s *Store) RevokeInvitation(ctx context.Context, workspaceID, id int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin revoke invitation transaction: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "revoke invitation")
	var email, role string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT COALESCE(email, ''), role FROM member_onboarding
		WHERE id = ? AND workspace_id = ? AND kind = ?`), id, workspaceID, kindInvite).Scan(&email, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrInvitationNotFound
	}
	if err != nil {
		return fmt.Errorf("read invitation: %w", err)
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM member_onboarding WHERE id = ? AND workspace_id = ?`), id, workspaceID)
	if err != nil {
		return fmt.Errorf("delete invitation: %w", err)
	}
	if err := sqlx.RowsAffected(result, domain.ErrInvitationNotFound); err != nil {
		return err
	}
	if err := s.appendAudit(ctx, tx, domain.AuditEvent{
		WorkspaceID: workspaceID, Action: domain.AuditMemberInviteRevoked,
		TargetType: domain.AuditTargetInvitation, TargetID: fmt.Sprint(id),
		Details: map[string]string{"email": email, "role": role},
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit revoke invitation transaction: %w", err)
	}
	return nil
}

// AcceptInvitation turns an invitation into an account and a membership. The
// invitation row is deleted first, in the same transaction, so two concurrent
// redemptions of one token cannot both succeed; a taken username rolls the
// whole transaction back and leaves the invitation usable.
func (s *Store) AcceptInvitation(ctx context.Context, tokenHash, username, passwordHash string, now time.Time) (*domain.User, error) {
	if tokenHash == "" {
		return nil, domain.ErrInvitationInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin accept invitation transaction: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "accept invitation")

	var id, workspaceID int
	var kind, email, role, expires, addedUsername string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id, workspace_id, kind, COALESCE(email, ''), role, expires_at, COALESCE(username, '')
		FROM member_onboarding WHERE token_hash = ?`), tokenHash).Scan(&id, &workspaceID, &kind, &email, &role, &expires, &addedUsername)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrInvitationInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("read invitation: %w", err)
	}
	expiry, err := sqlx.ParseTime(expires)
	if err != nil || !expiry.After(now) {
		return nil, domain.ErrInvitationInvalid
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM member_onboarding WHERE id = ? AND token_hash = ?`), id, tokenHash)
	if err != nil {
		return nil, fmt.Errorf("consume invitation: %w", err)
	}
	if err := sqlx.RowsAffected(result, domain.ErrInvitationInvalid); err != nil {
		return nil, err
	}
	if kind == kindAdd {
		user, err := s.activateAddedMember(ctx, tx, workspaceID, id, addedUsername, username, passwordHash, role, now)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit accept invitation transaction: %w", err)
		}
		return user, nil
	}

	created := now.UTC()
	userID, err := insertID(ctx, tx, s.bind(`INSERT INTO users (username, email, password_hash, created_at)
		VALUES (?, NULLIF(?, ''), ?, ?)`), username, email, passwordHash, sqlx.Time(created))
	if err != nil {
		if sqlx.IsUniqueViolation(err) {
			return nil, fmt.Errorf("%w: %w", domain.ErrUserAlreadyExists, err)
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO workspace_memberships (workspace_id, user_id, role, created_at)
		VALUES (?, ?, ?, ?)`), workspaceID, userID, role, sqlx.Time(created)); err != nil {
		return nil, fmt.Errorf("insert workspace membership: %w", err)
	}
	auditCtx := domain.WithAuditActor(ctx, domain.AuditActor{UserID: userID, Username: username, Source: "invitation"})
	if err := s.appendAudit(auditCtx, tx, domain.AuditEvent{
		WorkspaceID: workspaceID, Action: domain.AuditMemberJoined,
		TargetType: domain.AuditTargetUser, TargetID: fmt.Sprint(userID),
		Details: map[string]string{"username": username, "email": email, "role": role, "invitation_id": fmt.Sprint(id)},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit accept invitation transaction: %w", err)
	}
	return &domain.User{ID: userID, WorkspaceID: workspaceID, Role: domain.Role(role), Username: username, Email: email, CreatedAt: created}, nil
}

// invitationLockClass namespaces the advisory lock for invitations ("IN").
const invitationLockClass = 0x494E
