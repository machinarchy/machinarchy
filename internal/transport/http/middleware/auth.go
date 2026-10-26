package middleware

import (
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
func JWTAuth(secret []byte) echo.MiddlewareFunc {
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
			if len(key) == 0 {
				return c.JSON(http.StatusInternalServerError, echo.Map{"error": "authentication is not configured"})
			}
			tokenString := tokenFromRequest(c)
			if tokenString == "" {
				return unauthorized(c)
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
			if claims.UserID <= 0 || strings.TrimSpace(claims.Username) == "" {
				return unauthorized(c)
			}

			c.Set("user_id", claims.UserID)
			c.Set("username", claims.Username)
			c.Set("user", claims)
			return next(c)
		}
	}
}

func tokenFromRequest(c echo.Context) string {
	if cookie, err := c.Cookie("auth_token"); err == nil && cookie.Value != "" {
		return cookie.Value
	}
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
