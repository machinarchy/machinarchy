package middleware

import (
	"strings"

	"github.com/labstack/echo/v4"
)

// DefaultContentSecurityPolicy forbids every resource, script and framing.
// The API serves JSON, which needs none of them; a handler that serves HTML
// (the API reference) replaces it with its own policy.
const DefaultContentSecurityPolicy = "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// SecurityHeaders sets browser hardening headers on every response (AUDIT
// S-16). HSTS is sent only when hsts is true (production): a development
// server on plain HTTP must not pin browsers to HTTPS for localhost.
// Responses under /api also get Cache-Control: no-store, because they carry
// account data.
func SecurityHeaders(hsts bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			header := c.Response().Header()
			header.Set("X-Content-Type-Options", "nosniff")
			header.Set("X-Frame-Options", "DENY")
			header.Set("Referrer-Policy", "no-referrer")
			header.Set("Cross-Origin-Opener-Policy", "same-origin")
			header.Set("Cross-Origin-Resource-Policy", "same-origin")
			header.Set("Content-Security-Policy", DefaultContentSecurityPolicy)
			if hsts {
				header.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			if path := c.Request().URL.Path; path == "/api" || strings.HasPrefix(path, "/api/") {
				header.Set("Cache-Control", "no-store")
			}
			return next(c)
		}
	}
}
