package httperr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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
	e.GET("/policy", func(c echo.Context) error {
		return FromError(c, fmt.Errorf("%w: program \"rm\" is not on the allowed list", domain.ErrExecutionNotAllowed))
	})
	e.GET("/unknown", func(c echo.Context) error { return FromError(c, errors.New("db password=hunter2")) })
	e.GET("/escaped", func(echo.Context) error { return errors.New("boom") })

	for _, tc := range []struct {
		path, code, message string
		status              int
	}{
		{"/mapped", CodeConflict, "resource revision conflict", http.StatusConflict},
		{"/input", CodeInvalidRequest, "invalid input: name is too long", http.StatusBadRequest},
		{"/policy", CodeExecutionNotAllowed, `command execution is not permitted: program "rm" is not on the allowed list`, http.StatusForbidden},
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

// TestUnexpectedErrorsAreLogged (AUDIT/20 F20-06): an unknown error becomes
// a generic 500 for the client, and its cause is logged with the request ID,
// so an operator can find out what failed.
func TestUnexpectedErrorsAreLogged(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	e := echo.New()
	e.GET("/boom", func(c echo.Context) error {
		c.Response().Header().Set(echo.HeaderXRequestID, "req-123")
		return FromError(c, errors.New("begin Operation creation: context deadline exceeded"))
	})
	status, body := serve(t, e, http.MethodGet, "/boom")
	if status != http.StatusInternalServerError || body.Error != "request failed" {
		t.Fatalf("response = %d %+v", status, body)
	}
	line := logs.String()
	for _, want := range []string{`"error":"begin Operation creation: context deadline exceeded"`, `"request_id":"req-123"`, `"route":"/boom"`} {
		if !strings.Contains(line, want) {
			t.Errorf("log %q lacks %s", line, want)
		}
	}
}
