package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

func TestAuditActorMiddlewareAttributesRequests(t *testing.T) {
	var got domain.AuditActor
	var ok bool
	e := echo.New()
	e.GET("/x", func(c echo.Context) error {
		got, ok = domain.AuditActorFrom(c.Request().Context())
		return c.NoContent(http.StatusOK)
	}, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Header().Set(echo.HeaderXRequestID, "req-9")
			c.Set("principal", domain.Principal{UserID: 4, Username: "alice", APITokenID: 12, WorkspaceID: 1, Role: domain.RoleOperator})
			return next(c)
		}
	}, middleware.AuditActor())

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusOK || !ok {
		t.Fatalf("status %d, actor present %v", rec.Code, ok)
	}
	want := domain.AuditActor{UserID: 4, Username: "alice", APITokenID: 12, RequestID: "req-9", Source: "api"}
	if got != want {
		t.Errorf("actor = %+v, want %+v", got, want)
	}
}

func TestAuditActorMiddlewareWithoutAPrincipalAddsNoActor(t *testing.T) {
	var ok bool
	e := echo.New()
	e.GET("/x", func(c echo.Context) error {
		_, ok = domain.AuditActorFrom(c.Request().Context())
		return c.NoContent(http.StatusOK)
	}, middleware.AuditActor())
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if ok {
		t.Error("an unauthenticated request must not be given an actor")
	}
}
