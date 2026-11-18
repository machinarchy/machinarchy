package httperr

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

func serve(t *testing.T, e *echo.Echo, method, path string) (int, Body) {
	t.Helper()
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
	var body Body
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	return recorder.Code, body
}

// TestOneErrorShape covers AUDIT X-07: handler errors, mapped domain errors and
// the framework's own errors all produce {"error", "code"}.
func TestOneErrorShape(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = Handler
	e.GET("/mapped", func(c echo.Context) error {
		return FromError(c, fmt.Errorf("wrapped: %w", resource.ErrConflict))
	})
	e.GET("/input", func(c echo.Context) error {
		return FromError(c, fmt.Errorf("%w: name is too long", domain.ErrInvalidInput))
	})
	e.GET("/unknown", func(c echo.Context) error { return FromError(c, errors.New("db password=hunter2")) })
	e.GET("/escaped", func(echo.Context) error { return errors.New("boom") })

	for _, tc := range []struct {
		path, code, message string
		status              int
	}{
		{"/mapped", CodeConflict, "resource revision conflict", http.StatusConflict},
		{"/input", CodeInvalidRequest, "invalid input: name is too long", http.StatusBadRequest},
		{"/unknown", CodeInternal, "request failed", http.StatusInternalServerError}, // never leaks detail
		{"/escaped", CodeInternal, "request failed", http.StatusInternalServerError},
		{"/no-such-route", CodeNotFound, "Not Found", http.StatusNotFound},
	} {
		status, body := serve(t, e, http.MethodGet, tc.path)
		if status != tc.status || body.Code != tc.code || body.Error != tc.message {
			t.Errorf("%s = %d %+v, want %d %s %q", tc.path, status, body, tc.status, tc.code, tc.message)
		}
	}
	if status, body := serve(t, e, http.MethodPost, "/mapped"); status != http.StatusMethodNotAllowed || body.Code != CodeInvalidRequest {
		t.Errorf("method not allowed = %d %+v", status, body)
	}
}
