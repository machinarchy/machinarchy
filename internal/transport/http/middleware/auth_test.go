package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/service"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

type accessStoreStub struct {
	principal domain.Principal
	gotHash   string
}

func (s *accessStoreStub) GetWorkspaceRole(context.Context, int, int) (domain.Role, error) {
	return "", domain.ErrMembershipNotFound
}
func (s *accessStoreStub) ResolveAPIToken(_ context.Context, hash string) (*domain.Principal, error) {
	s.gotHash = hash
	return &s.principal, nil
}
func (*accessStoreStub) ListAPITokens(context.Context, int, int) ([]domain.APIToken, error) {
	return nil, nil
}
func (*accessStoreStub) CreateAPIToken(context.Context, *domain.APIToken) error { return nil }
func (*accessStoreStub) RevokeAPIToken(context.Context, int, int, int) error    { return nil }

func TestAPITokenAuthenticationUsesScopedPrincipalAndPermissions(t *testing.T) {
	const rawToken = "mch_pat_example-secret"
	access := &accessStoreStub{principal: domain.Principal{
		UserID: 7, WorkspaceID: 11, Role: domain.RoleReader, APITokenID: 42, Username: "automation",
	}}
	e := echo.New()
	endpoint := func(c echo.Context) error {
		principal, ok := middleware.Principal(c)
		if !ok {
			return c.NoContent(http.StatusUnauthorized)
		}
		if principal.WorkspaceID != 11 || principal.APITokenID != 42 {
			return c.NoContent(http.StatusInternalServerError)
		}
		return c.NoContent(http.StatusNoContent)
	}
	e.GET("/read", endpoint,
		middleware.JWTAuth([]byte("unused-for-pat"), access),
		middleware.RequirePermission(domain.PermissionOperationsRead),
	)
	e.GET("/write", endpoint,
		middleware.JWTAuth([]byte("unused-for-pat"), access),
		middleware.RequirePermission(domain.PermissionInventoryWrite),
	)
	e.GET("/cookie", endpoint,
		middleware.JWTAuth([]byte("unused-for-pat"), access),
	)

	for _, tt := range []struct {
		name       string
		path       string
		wantStatus int
	}{
		{name: "reader permission allowed", path: "/read", wantStatus: http.StatusNoContent},
		{name: "write denied", path: "/write", wantStatus: http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set(echo.HeaderAuthorization, "Bearer "+rawToken)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
	if want := service.HashAPIToken(rawToken); access.gotHash != want {
		t.Fatalf("resolved token hash = %q, want SHA-256 digest %q", access.gotHash, want)
	}

	req := httptest.NewRequest(http.MethodGet, "/cookie", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: rawToken})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("API token cookie status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAPITokenCannotUseHumanOnlyTokenManagement(t *testing.T) {
	const rawToken = "mch_pat_owner-shaped-secret"
	access := &accessStoreStub{principal: domain.Principal{
		UserID: 7, WorkspaceID: 11, Role: domain.RoleOwner, APITokenID: 42, Username: "automation",
	}}
	e := echo.New()
	e.POST("/tokens", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) },
		middleware.JWTAuth([]byte("unused-for-pat"), access),
		middleware.RequirePermission(domain.PermissionTokenManage),
		middleware.RequireHuman(),
	)
	req := httptest.NewRequest(http.MethodPost, "/tokens", nil)
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+rawToken)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("owner-shaped API token status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}
