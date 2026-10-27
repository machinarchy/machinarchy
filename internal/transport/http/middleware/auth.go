package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
)

const (
	maxTokenAge      = 24 * time.Hour
	maxTokenLifespan = 24 * time.Hour
	allowedClockSkew = time.Minute
)

// JWTAuth validates bearer or cookie credentials against the explicitly
// injected signing key. It never reads package-global configuration.
func JWTAuth(secret []byte, access domain.AccessStore) echo.MiddlewareFunc {
	key := append([]byte(nil), secret...)
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer("machinarchy"),
		jwt.WithLeeway(allowedClockSkew),
		jwt.WithIssuedAt(),
		jwt.WithExpirationRequired(),
	)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if len(key) == 0 || access == nil {
				return c.JSON(http.StatusInternalServerError, echo.Map{"error": "authentication is not configured"})
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

			claims := new(domain.JWTCustomClaims)
			token, err := parser.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
				if token.Method != jwt.SigningMethodHS256 {
					return nil, errors.New("unexpected JWT signing method")
				}
				return key, nil
			})
			if err != nil || token == nil || !token.Valid || claims.IssuedAt == nil || claims.ExpiresAt == nil {
				return unauthorized(c)
			}

			now := time.Now()
			issuedAt := claims.IssuedAt.Time
			if issuedAt.After(now.Add(allowedClockSkew)) || now.Sub(issuedAt) > maxTokenAge || claims.ExpiresAt.Time.Sub(issuedAt) > maxTokenLifespan {
				return unauthorized(c)
			}
			if claims.UserID <= 0 || claims.WorkspaceID <= 0 || strings.TrimSpace(claims.Username) == "" {
				return unauthorized(c)
			}
			role, err := access.GetWorkspaceRole(c.Request().Context(), claims.UserID, claims.WorkspaceID)
			if errors.Is(err, domain.ErrMembershipNotFound) {
				return unauthorized(c)
			}
			if err != nil {
				return c.JSON(http.StatusInternalServerError, echo.Map{"error": "unable to validate authorization"})
			}

			c.Set("user_id", claims.UserID)
			c.Set("username", claims.Username)
			c.Set("user", claims)
			c.Set("principal", domain.Principal{
				UserID: claims.UserID, WorkspaceID: claims.WorkspaceID,
				Role: role, Username: claims.Username,
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
		return c.JSON(http.StatusInternalServerError, echo.Map{"error": "unable to validate authorization"})
	}
	if principal == nil || principal.UserID <= 0 || principal.WorkspaceID <= 0 || !domain.ValidRole(principal.Role) {
		return unauthorized(c)
	}
	c.Set("user_id", principal.UserID)
	c.Set("username", principal.Username)
	c.Set("principal", *principal)
	return next(c)
}

func RequirePermission(permission domain.Permission) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			principal, ok := c.Get("principal").(domain.Principal)
			if !ok || !principal.Role.Allows(permission) {
				return c.JSON(http.StatusForbidden, echo.Map{"error": "permission denied"})
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
			principal, ok := c.Get("principal").(domain.Principal)
			if !ok || principal.APITokenID != 0 {
				return c.JSON(http.StatusForbidden, echo.Map{"error": "a user session is required"})
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
	return c.JSON(http.StatusUnauthorized, echo.Map{"error": "invalid or expired authorization token"})
}

func Principal(c echo.Context) (domain.Principal, bool) {
	principal, ok := c.Get("principal").(domain.Principal)
	return principal, ok && principal.UserID > 0 && principal.WorkspaceID > 0
}
