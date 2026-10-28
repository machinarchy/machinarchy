package host

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
)

func TestHostServiceErrorStatusMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{"duplicate host", fmt.Errorf("persist host: %w", domain.ErrHostAlreadyExists), http.StatusConflict},
		{"forbidden target", fmt.Errorf("dial: %w", domain.ErrTargetNotAllowed), http.StatusBadRequest},
		{"invalid input", fmt.Errorf("%w: bad port", domain.ErrInvalidInput), http.StatusBadRequest},
		{"unknown environment", domain.ErrEnvironmentNotFound, http.StatusBadRequest},
		{"bad credentials", domain.ErrAuthenticationFailed, http.StatusUnauthorized},
		{"busy", domain.ErrServiceBusy, http.StatusServiceUnavailable},
		{"unexpected", errors.New("pq: password authentication failed for user machinarchy"), http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			rec := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequest(http.MethodPost, "/hosts", nil), rec)
			if err := hostServiceError(c, tc.err); err != nil {
				t.Fatal(err)
			}
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "password") {
				t.Errorf("unexpected errors must not leak details: %s", rec.Body.String())
			}
		})
	}
}
