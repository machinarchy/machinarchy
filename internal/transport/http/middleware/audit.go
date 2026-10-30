package middleware

import (
	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
)

// AuditActor attaches the authenticated principal and the request ID to the
// request context so stores can attribute the audit events they write in the
// same transaction as a change. It must run after authentication.
func AuditActor() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if principal, ok := Principal(c); ok {
				actor := domain.AuditActor{
					UserID:     principal.UserID,
					Username:   principal.Username,
					APITokenID: principal.APITokenID,
					RequestID:  c.Response().Header().Get(echo.HeaderXRequestID),
					Source:     "api",
				}
				c.SetRequest(c.Request().WithContext(domain.WithAuditActor(c.Request().Context(), actor)))
			}
			return next(c)
		}
	}
}
