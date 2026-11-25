package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/store/sqlx"
)

var _ domain.SessionStore = (*Store)(nil)

// sessionHistory is how long ended sessions are kept before a new login of
// the same user removes them.
const sessionHistory = 7 * 24 * time.Hour

// CreateSession implements domain.SessionStore.
func (s *Store) CreateSession(ctx context.Context, session domain.Session) error {
	if !domain.ValidSessionID(session.ID) || session.UserID <= 0 || session.WorkspaceID <= 0 ||
		session.CreatedAt.IsZero() || !session.ExpiresAt.After(session.CreatedAt) {
		return fmt.Errorf("%w: invalid session", domain.ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create session: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "create session")
	now := session.CreatedAt.UnixNano()
	// Drop this user's sessions that ended long ago.
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM user_sessions
		WHERE user_id = ? AND (expires_at_ns < ? OR (revoked_at_ns > 0 AND revoked_at_ns < ?))`),
		session.UserID, now-int64(sessionHistory), now-int64(sessionHistory)); err != nil {
		return fmt.Errorf("prune old sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO user_sessions
		(id, user_id, workspace_id, created_at_ns, expires_at_ns, user_agent, client_ip) VALUES (?, ?, ?, ?, ?, ?, ?)`),
		session.ID, session.UserID, session.WorkspaceID, now, session.ExpiresAt.UnixNano(),
		truncate(session.UserAgent, 256), truncate(session.ClientIP, 64)); err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	// Bound active sessions: revoke the oldest past the limit. ("LIMIT -1"
	// is SQLite's "no limit"; PostgreSQL spells it "LIMIT ALL".)
	noLimit := "LIMIT -1"
	if s.dialect == sqlx.PostgreSQL {
		noLimit = "LIMIT ALL"
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE user_sessions SET revoked_at_ns = ?, revoke_reason = ?
		WHERE id IN (SELECT id FROM user_sessions WHERE user_id = ? AND revoked_at_ns = 0 AND expires_at_ns > ?
			ORDER BY created_at_ns DESC, id DESC `+noLimit+` OFFSET ?)`),
		now, domain.SessionRevokedLimit, session.UserID, now, domain.MaxActiveSessionsPerUser); err != nil {
		return fmt.Errorf("bound active sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session: %w", err)
	}
	return nil
}

// GetSessionRole implements domain.SessionStore: one query per request, the
// same cost as the membership lookup it replaces.
func (s *Store) GetSessionRole(ctx context.Context, sessionID string, userID, workspaceID int, now time.Time) (domain.Role, error) {
	if !domain.ValidSessionID(sessionID) {
		return "", domain.ErrSessionNotFound
	}
	var role string
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT m.role FROM user_sessions AS us
		JOIN workspace_memberships AS m ON m.user_id = us.user_id AND m.workspace_id = us.workspace_id
		WHERE us.id = ? AND us.user_id = ? AND us.workspace_id = ? AND us.revoked_at_ns = 0 AND us.expires_at_ns > ?`),
		sessionID, userID, workspaceID, now.UnixNano()).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.ErrSessionNotFound
	}
	if err != nil {
		return "", fmt.Errorf("check session: %w", err)
	}
	if !domain.ValidRole(domain.Role(role)) {
		return "", fmt.Errorf("invalid role %q in workspace membership", role)
	}
	return domain.Role(role), nil
}

// ListSessions implements domain.SessionStore.
func (s *Store) ListSessions(ctx context.Context, userID int, now time.Time) ([]domain.Session, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, workspace_id, created_at_ns, expires_at_ns, user_agent, client_ip
		FROM user_sessions WHERE user_id = ? AND revoked_at_ns = 0 AND expires_at_ns > ?
		ORDER BY created_at_ns DESC, id DESC LIMIT ?`), userID, now.UnixNano(), domain.MaxActiveSessionsPerUser)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer sqlx.CloseRows(rows, s.logger, "list sessions")
	sessions := []domain.Session{}
	for rows.Next() {
		session := domain.Session{UserID: userID}
		var created, expires int64
		if err := rows.Scan(&session.ID, &session.WorkspaceID, &created, &expires, &session.UserAgent, &session.ClientIP); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		session.CreatedAt, session.ExpiresAt = time.Unix(0, created).UTC(), time.Unix(0, expires).UTC()
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

// RevokeSession implements domain.SessionStore.
func (s *Store) RevokeSession(ctx context.Context, sessionID string, userID int, reason string, now time.Time) error {
	if !domain.ValidSessionID(sessionID) {
		return domain.ErrSessionNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin revoke session: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "revoke session")
	var workspaceID int
	err = tx.QueryRowContext(ctx, s.bind(`SELECT workspace_id FROM user_sessions
		WHERE id = ? AND user_id = ? AND revoked_at_ns = 0 AND expires_at_ns > ?`), sessionID, userID, now.UnixNano()).Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrSessionNotFound
	}
	if err != nil {
		return fmt.Errorf("read session before revoke: %w", err)
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE user_sessions SET revoked_at_ns = ?, revoke_reason = ?
		WHERE id = ? AND user_id = ? AND revoked_at_ns = 0`), now.UnixNano(), reason, sessionID, userID)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	if err := sqlx.RowsAffected(result, domain.ErrSessionNotFound); err != nil {
		return err
	}
	if err := s.appendAudit(ctx, tx, domain.AuditEvent{WorkspaceID: workspaceID, Action: domain.AuditSessionRevoked,
		TargetType: domain.AuditTargetUser, TargetID: fmt.Sprint(userID),
		Details: map[string]string{"session": sessionID[:8], "reason": reason}}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session revoke: %w", err)
	}
	return nil
}

// RevokeUserSessions implements domain.SessionStore.
func (s *Store) RevokeUserSessions(ctx context.Context, userID int, reason string, now time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin revoke sessions: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "revoke sessions")
	revoked, workspaceID, err := s.revokeAll(ctx, tx, userID, reason, now)
	if err != nil {
		return 0, err
	}
	if workspaceID > 0 {
		if err := s.appendAudit(ctx, tx, domain.AuditEvent{WorkspaceID: workspaceID, Action: domain.AuditSessionsRevoked,
			TargetType: domain.AuditTargetUser, TargetID: fmt.Sprint(userID),
			Details: map[string]string{"sessions": fmt.Sprint(revoked), "reason": reason}}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit sessions revoke: %w", err)
	}
	return revoked, nil
}

// ChangePassword implements domain.SessionStore.
func (s *Store) ChangePassword(ctx context.Context, userID int, passwordHash string, now time.Time) (int, error) {
	if userID <= 0 || passwordHash == "" {
		return 0, fmt.Errorf("%w: user and password hash are required", domain.ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin password change: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "password change")
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE users SET password_hash = ?, reset_token = NULL, reset_token_expires = NULL WHERE id = ?`),
		passwordHash, userID)
	if err != nil {
		return 0, fmt.Errorf("update password: %w", err)
	}
	if err := sqlx.RowsAffected(result, domain.ErrUserNotFound); err != nil {
		return 0, err
	}
	revoked, _, err := s.revokeAll(ctx, tx, userID, domain.SessionRevokedPasswordChanged, now)
	if err != nil {
		return 0, err
	}
	var workspaceID int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT workspace_id FROM workspace_memberships WHERE user_id = ? ORDER BY workspace_id LIMIT 1`),
		userID).Scan(&workspaceID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("read membership for audit: %w", err)
	}
	if workspaceID > 0 {
		if err := s.appendAudit(ctx, tx, domain.AuditEvent{WorkspaceID: workspaceID, Action: domain.AuditPasswordChanged,
			TargetType: domain.AuditTargetUser, TargetID: fmt.Sprint(userID),
			Details: map[string]string{"sessions_revoked": fmt.Sprint(revoked)}}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit password change: %w", err)
	}
	return revoked, nil
}

// revokeAll ends every active session of userID and returns the count and
// the workspace of one of them (for the audit event).
func (s *Store) revokeAll(ctx context.Context, tx *sql.Tx, userID int, reason string, now time.Time) (int, int, error) {
	var workspaceID int
	err := tx.QueryRowContext(ctx, s.bind(`SELECT workspace_id FROM user_sessions
		WHERE user_id = ? AND revoked_at_ns = 0 ORDER BY created_at_ns DESC LIMIT 1`), userID).Scan(&workspaceID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, fmt.Errorf("read sessions before revoke: %w", err)
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE user_sessions SET revoked_at_ns = ?, revoke_reason = ?
		WHERE user_id = ? AND revoked_at_ns = 0 AND expires_at_ns > ?`), now.UnixNano(), reason, userID, now.UnixNano())
	if err != nil {
		return 0, 0, fmt.Errorf("revoke sessions: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, 0, fmt.Errorf("count revoked sessions: %w", err)
	}
	return int(n), workspaceID, nil
}

// truncate bounds a client-supplied value without splitting a UTF-8
// sequence (PostgreSQL rejects invalid UTF-8).
func truncate(value string, limit int) string {
	value = strings.ToValidUTF8(value, "?")
	for len(value) > limit {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return value
}
