package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestSignUpDisabled(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", nil)
	recorder := httptest.NewRecorder()
	ctx := e.NewContext(req, recorder)

	handler := NewHandler(nil, false, false)
	if err := handler.SignUp(ctx); err != nil {
		t.Fatalf("SignUp() error = %v", err)
	}

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}
