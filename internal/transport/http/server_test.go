package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

func TestRequestDeadlineBoundsHandlers(t *testing.T) {
	e := echo.New()
	var deadline time.Time
	handler := requestDeadline(time.Second)(func(c echo.Context) error {
		deadline, _ = c.Request().Context().Deadline()
		return nil
	})
	recorder := httptest.NewRecorder()
	if err := handler(e.NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/me", nil), recorder)); err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > time.Second {
		t.Fatalf("request deadline in %s, want within 1s", remaining)
	}
	if RequestTimeout >= 30*time.Second {
		t.Fatal("the request deadline must be shorter than the 30s write timeout")
	}
}
