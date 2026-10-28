package members_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/members"
)

type fakeService struct {
	listErr, setErr error
	gotRole         domain.Role
	gotID           int
	gotPrincipal    domain.Principal
}

func (f *fakeService) List(_ context.Context, p domain.Principal) ([]domain.WorkspaceMember, error) {
	f.gotPrincipal = p
	if f.listErr != nil {
		return nil, f.listErr
	}
	return []domain.WorkspaceMember{{UserID: 1, Username: "owner", Role: domain.RoleOwner}}, nil
}

func (f *fakeService) SetRole(_ context.Context, p domain.Principal, id int, role domain.Role) (*domain.WorkspaceMember, error) {
	f.gotPrincipal, f.gotID, f.gotRole = p, id, role
	if f.setErr != nil {
		return nil, f.setErr
	}
	return &domain.WorkspaceMember{UserID: id, Username: "u", Role: role}, nil
}

// newRouter mounts the real routes behind the same guards the v1 router uses,
// with a stub that injects the authenticated principal.
func newRouter(svc domain.MemberService, principal domain.Principal) *echo.Echo {
	e := echo.New()
	inject := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("principal", principal)
			return next(c)
		}
	}
	g := e.Group("/members", inject,
		middleware.RequirePermission(domain.PermissionMembersManage), middleware.RequireHuman())
	members.RegisterRoutes(g, members.NewHandler(svc))
	return e
}

func do(e *echo.Echo, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

var owner = domain.Principal{UserID: 1, WorkspaceID: 7, Role: domain.RoleOwner}

func TestRoutesAreGuardedByRoleAndCredentialType(t *testing.T) {
	cases := map[string]domain.Principal{
		"operator":  {UserID: 2, WorkspaceID: 7, Role: domain.RoleOperator},
		"reader":    {UserID: 3, WorkspaceID: 7, Role: domain.RoleReader},
		"api token": {UserID: 1, WorkspaceID: 7, Role: domain.RoleOwner, APITokenID: 5},
	}
	for name, principal := range cases {
		t.Run(name, func(t *testing.T) {
			svc := &fakeService{}
			e := newRouter(svc, principal)
			if rec := do(e, http.MethodGet, "/members", ""); rec.Code != http.StatusForbidden {
				t.Errorf("GET status = %d, want 403", rec.Code)
			}
			if rec := do(e, http.MethodPut, "/members/2/role", `{"role":"owner"}`); rec.Code != http.StatusForbidden {
				t.Errorf("PUT status = %d, want 403", rec.Code)
			}
			if svc.gotID != 0 {
				t.Error("a rejected caller must never reach the service")
			}
		})
	}
}

func TestListAndSetRoleSuccess(t *testing.T) {
	svc := &fakeService{}
	e := newRouter(svc, owner)

	rec := do(e, http.MethodGet, "/members", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rec.Code)
	}
	var list []domain.WorkspaceMember
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0].Role != domain.RoleOwner {
		t.Fatalf("GET body = %s (%v)", rec.Body.String(), err)
	}
	if svc.gotPrincipal.WorkspaceID != 7 {
		t.Error("the handler must pass the authenticated principal to the service")
	}

	rec = do(e, http.MethodPut, "/members/5/role", `{"role":" Operator "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body %s", rec.Code, rec.Body.String())
	}
	if svc.gotID != 5 || svc.gotRole != domain.RoleOperator {
		t.Errorf("service got id=%d role=%q, want 5/operator (role normalized)", svc.gotID, svc.gotRole)
	}
}

func TestSetRoleErrorMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		path   string
		body   string
		status int
	}{
		{"bad id", nil, "/members/abc/role", `{"role":"reader"}`, http.StatusBadRequest},
		{"zero id", nil, "/members/0/role", `{"role":"reader"}`, http.StatusBadRequest},
		{"bad json", nil, "/members/2/role", `{`, http.StatusBadRequest},
		{"invalid role", domain.ErrInvalidInput, "/members/2/role", `{"role":"god"}`, http.StatusBadRequest},
		{"not found", domain.ErrUserNotFound, "/members/2/role", `{"role":"reader"}`, http.StatusNotFound},
		{"last owner", domain.ErrLastOwner, "/members/1/role", `{"role":"reader"}`, http.StatusConflict},
		{"denied", domain.ErrPermissionDenied, "/members/2/role", `{"role":"reader"}`, http.StatusForbidden},
		{"unexpected", context.DeadlineExceeded, "/members/2/role", `{"role":"reader"}`, http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newRouter(&fakeService{setErr: tc.err}, owner)
			rec := do(e, http.MethodPut, tc.path, tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "deadline") {
				t.Error("unexpected errors must not leak internal details")
			}
		})
	}
}
