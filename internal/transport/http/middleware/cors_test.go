package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

func preflight(t *testing.T, origin, method string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	e.Use(middleware.CORS([]string{"https://console.example.test"}))
	e.PATCH("/hosts/:id/labels", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	req := httptest.NewRequest(http.MethodOptions, "/hosts/1/labels", nil)
	req.Header.Set(echo.HeaderOrigin, origin)
	req.Header.Set(echo.HeaderAccessControlRequestMethod, method)
	req.Header.Set(echo.HeaderAccessControlRequestHeaders, "authorization,content-type")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestCORSPreflightAllowsEveryMethodTheAPIUses(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := preflight(t, "https://console.example.test", method)
		if rec.Code != http.StatusNoContent {
			t.Errorf("%s preflight status = %d, want 204", method, rec.Code)
		}
		if !strings.Contains(rec.Header().Get(echo.HeaderAccessControlAllowMethods), method) {
			t.Errorf("%s preflight allow-methods = %q; browsers would block the call", method, rec.Header().Get(echo.HeaderAccessControlAllowMethods))
		}
	}
}

func TestCORSPreflightRejectsUnlistedOrigins(t *testing.T) {
	rec := preflight(t, "https://evil.example.test", http.MethodPatch)
	if got := rec.Header().Get(echo.HeaderAccessControlAllowOrigin); got != "" {
		t.Errorf("allow-origin = %q for an unlisted origin, want none", got)
	}
}
