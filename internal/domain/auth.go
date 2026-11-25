// Package domain defines the core domain models and interfaces for the application.
package domain

import "context"

// ClientInfo describes where a login comes from. It is recorded with the
// session and used for login throttling; it is never trusted for
// authorization.
type ClientInfo struct {
	// IP is the client address as resolved by the HTTP layer (the direct
	// peer, or the forwarded client behind a trusted proxy).
	IP        string
	UserAgent string
}

// AuthSignUpRequest represents the request payload for user sign-up.
type AuthSignUpRequest struct {
	Username string
	Email    string
	Password string
	Client   ClientInfo
}

// AuthLoginRequest represents the request payload for user login.
type AuthLoginRequest struct {
	Username string
	Email    string
	Password string
	Client   ClientInfo
}

// AuthTokenResponse represents the response payload for authentication tokens.
type AuthTokenResponse struct {
	Token string       `json:"token"`
	User  UserResponse `json:"user"`
}

// AuthService defines the interface for authentication-related operations.
type AuthService interface {
	SignUp(ctx context.Context, req AuthSignUpRequest) (*AuthTokenResponse, error)
	Login(ctx context.Context, req AuthLoginRequest) (*AuthTokenResponse, error)
	VerifySession(ctx context.Context, username string) (*UserResponse, error)
	// Logout revokes the session of a valid session token. An invalid,
	// expired or already revoked token is not an error: logout always ends
	// with no usable session.
	Logout(ctx context.Context, token string) error
	// ListSessions returns the principal's active sessions.
	ListSessions(ctx context.Context, principal Principal) ([]Session, error)
	// RevokeSession ends one of the principal's own sessions.
	RevokeSession(ctx context.Context, principal Principal, sessionID string) error
	// RevokeAllSessions ends every session of the principal ("log out
	// everywhere"), including the current one.
	RevokeAllSessions(ctx context.Context, principal Principal) (int, error)
	// ChangePassword replaces the principal's password after checking the
	// current one, and revokes all of the user's sessions.
	ChangePassword(ctx context.Context, principal Principal, currentPassword, newPassword string) error
}
