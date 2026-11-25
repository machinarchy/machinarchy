package domain

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Server-side login sessions (AUDIT S-08). Every login creates a session row;
// the session JWT carries its ID ("sid"), and each request checks that the
// session is still active. Logout, "log out everywhere" and a password change
// revoke sessions, which ends them at once instead of at token expiry.

// ErrSessionNotFound reports a session that does not exist, is revoked or has
// expired, or belongs to someone else.
var ErrSessionNotFound = errors.New("session not found")

// MaxActiveSessionsPerUser bounds a user's active sessions; a new login past
// it revokes the oldest one.
const MaxActiveSessionsPerUser = 50

// Session is one login.
type Session struct {
	ID          string    `json:"id"`
	UserID      int       `json:"-"`
	WorkspaceID int       `json:"-"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
	UserAgent   string    `json:"userAgent,omitempty"`
	ClientIP    string    `json:"clientIp,omitempty"`
	// Current marks the session the request was made with.
	Current bool `json:"current"`
}

// Session revocation reasons, recorded with the session and in audit events.
const (
	SessionRevokedLogout          = "logout"
	SessionRevokedByUser          = "revoked"
	SessionRevokedAll             = "logout_everywhere"
	SessionRevokedPasswordChanged = "password_changed"
	SessionRevokedLimit           = "session_limit"
)

// SessionStore persists sessions.
type SessionStore interface {
	// CreateSession stores a new active session. Past
	// MaxActiveSessionsPerUser it revokes the user's oldest active session.
	CreateSession(ctx context.Context, session Session) error
	// GetSessionRole returns the user's workspace role if the session is
	// active and belongs to that user and workspace; ErrSessionNotFound
	// otherwise. It is the per-request check.
	GetSessionRole(ctx context.Context, sessionID string, userID, workspaceID int, now time.Time) (Role, error)
	// ListSessions returns a user's active sessions, newest first.
	ListSessions(ctx context.Context, userID int, now time.Time) ([]Session, error)
	// RevokeSession ends one of the user's active sessions (audited).
	RevokeSession(ctx context.Context, sessionID string, userID int, reason string, now time.Time) error
	// RevokeUserSessions ends all of the user's active sessions (audited)
	// and returns how many.
	RevokeUserSessions(ctx context.Context, userID int, reason string, now time.Time) (int, error)
	// ChangePassword stores a new password hash and revokes every active
	// session of the user in one transaction (audited). It returns how many
	// sessions ended.
	ChangePassword(ctx context.Context, userID int, passwordHash string, now time.Time) (int, error)
}

// NewSessionID returns 128 random bits, hex encoded.
func NewSessionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate session ID: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// ValidSessionID reports whether id has the NewSessionID shape.
func ValidSessionID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
