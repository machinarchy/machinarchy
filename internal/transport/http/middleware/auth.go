package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/transport/http/httperr"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/sessionkey"
)

// JWTAuth authenticates a request with a login-session token (cookie or
// bearer) or an API token (bearer "mch_pat_…"). A session token must verify
// against the keyring (by kid) and name a session that is still active
// (AUDIT S-08): one query that also returns the user's current role.
func JWTAuth(keys *sessionkey.Keyring, maxLifetime time.Duration, access domain.AccessStore, sessions domain.SessionStore) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if keys == nil || access == nil || sessions == nil {
				return httperr.Status(c, http.StatusInternalServerError, "authentication is not configured")
			}
			tokenString := tokenFromRequest(c)
			if tokenString == "" {
				return unauthorized(c)
			}
			if strings.HasPrefix(tokenString, "mch_pat_") {
				if bearerTokenFromRequest(c) == "" {
					return unauthorized(c)
				}
				return authenticateAPIToken(c, next, access, tokenString)
			}

			now := time.Now()
			claims, err := keys.Parse(tokenString, maxLifetime, now)
			if err != nil {
				return unauthorized(c)
			}
			role, err := sessions.GetSessionRole(c.Request().Context(), claims.SessionID, claims.UserID, claims.WorkspaceID, now)
			if errors.Is(err, domain.ErrSessionNotFound) {
				return unauthorized(c)
			}
			if err != nil {
				return httperr.Status(c, http.StatusInternalServerError, "unable to validate authorization")
			}

			c.Set("user_id", claims.UserID)
			c.Set("username", claims.Username)
			c.Set("user", claims)
			c.Set("principal", domain.Principal{
				UserID: claims.UserID, WorkspaceID: claims.WorkspaceID,
				Role: role, Username: claims.Username, SessionID: claims.SessionID,
			})
			return next(c)
		}
	}
}

func authenticateAPIToken(c echo.Context, next echo.HandlerFunc, access domain.AccessStore, token string) error {
	hash := sha256.Sum256([]byte(token))
	principal, err := access.ResolveAPIToken(c.Request().Context(), hex.EncodeToString(hash[:]))
	if errors.Is(err, domain.ErrAPITokenNotFound) {
		return unauthorized(c)
	}
	if err != nil {
		return httperr.Status(c, http.StatusInternalServerError, "unable to validate authorization")
	}
	if principal == nil || principal.UserID <= 0 || principal.WorkspaceID <= 0 || !domain.ValidRole(principal.Role) {
		return unauthorized(c)
	}
	c.Set("user_id", principal.UserID)
	c.Set("username", principal.Username)
	c.Set("principal", *principal)
	return next(c)
}

// RequirePermission returns middleware that enforces the specified permission
// for the authenticated principal.
// It rejects requests from principals that lack the required permission.
func RequirePermission(permission domain.Permission) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			principal, ok := Principal(c)
			if !ok {
				return httperr.Unauthorized(c)
			}
			if !principal.Role.Allows(permission) {
				return httperr.Status(c, http.StatusForbidden, "permission denied")
			}
			return next(c)
		}
	}
}

// RequireHuman prevents machine credentials from creating or revoking other
// credentials. API-token principals must be managed through a user session.
func RequireHuman() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			principal, ok := Principal(c)
			if !ok {
				return httperr.Unauthorized(c)
			}
			if principal.APITokenID != 0 {
				return httperr.Status(c, http.StatusForbidden, "a user session is required")
			}
			return next(c)
		}
	}
}

// RequireOwner restricts management endpoints to workspace owners. It is
// intentionally separate from authentication and requires a human session.
func RequireOwner() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			principal, ok := Principal(c)
			if !ok {
				return httperr.Unauthorized(c)
			}
			if principal.Role != domain.RoleOwner || principal.APITokenID != 0 {
				return httperr.Status(c, http.StatusForbidden, "permission denied")
			}
			return next(c)
		}
	}
}

func tokenFromRequest(c echo.Context) string {
	if token := bearerTokenFromRequest(c); token != "" {
		return token
	}
	if cookie, err := c.Cookie("auth_token"); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	return ""
}

func bearerTokenFromRequest(c echo.Context) string {
	const bearerPrefix = "Bearer "
	header := c.Request().Header.Get(echo.HeaderAuthorization)
	if len(header) < len(bearerPrefix) || !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return ""
	}
	return strings.TrimSpace(header[len(bearerPrefix):])
}

func unauthorized(c echo.Context) error {
	return httperr.Status(c, http.StatusUnauthorized, "invalid or expired authorization token")
}

// Principal retrieves the authenticated principal from the Echo context.
// It returns the principal and true if one is available, or a zero-value
// principal and false otherwise.
func Principal(c echo.Context) (domain.Principal, bool) {
	principal, ok := c.Get("principal").(domain.Principal)
	return principal, ok && principal.UserID > 0 && principal.WorkspaceID > 0
}
