package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestSecurityHeaders(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hsts      bool
		path      string
		failing   bool
		wantHSTS  bool
		wantStore bool
	}{
		{name: "api in production", hsts: true, path: "/api/v1/me", wantHSTS: true, wantStore: true},
		{name: "api error in production", hsts: true, path: "/api/v1/me", failing: true, wantHSTS: true, wantStore: true},
		{name: "health in development", path: "/healthz/ready"},
		{name: "prefix that is not /api", path: "/apix", hsts: true, wantHSTS: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			e.Use(SecurityHeaders(tc.hsts))
			e.GET(tc.path, func(c echo.Context) error {
				if tc.failing {
					return echo.NewHTTPError(http.StatusUnauthorized, "no")
				}
				return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
			})
			recorder := httptest.NewRecorder()
			e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.path, nil))
			header := recorder.Header()
			for name, want := range map[string]string{
				"X-Content-Type-Options":  "nosniff",
				"X-Frame-Options":         "DENY",
				"Referrer-Policy":         "no-referrer",
				"Content-Security-Policy": DefaultContentSecurityPolicy,
			} {
				if got := header.Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
			if got := header.Get("Strict-Transport-Security") != ""; got != tc.wantHSTS {
				t.Errorf("HSTS present = %v, want %v", got, tc.wantHSTS)
			}
			if got := header.Get("Cache-Control") == "no-store"; got != tc.wantStore {
				t.Errorf("Cache-Control no-store = %v, want %v", got, tc.wantStore)
			}
		})
	}
}

// A handler that serves HTML can replace the default policy.
func TestSecurityHeadersCanBeOverriddenByHandler(t *testing.T) {
	e := echo.New()
	e.Use(SecurityHeaders(false))
	e.GET("/page", func(c echo.Context) error {
		c.Response().Header().Set("Content-Security-Policy", "default-src 'self'")
		return c.HTML(http.StatusOK, "<p>x</p>")
	})
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/page", nil))
	if got := recorder.Header().Get("Content-Security-Policy"); got != "default-src 'self'" {
		t.Fatalf("CSP = %q", got)
	}
}
