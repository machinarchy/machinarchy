package fleet_test

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
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/fleet"
)

type fakeTargets struct {
	rule      domain.TargetRule
	principal domain.Principal
	err       error
}

func (f *fakeTargets) Preview(_ context.Context, p domain.Principal, rule domain.TargetRule) (*domain.TargetPreview, error) {
	f.principal, f.rule = p, rule
	if f.err != nil {
		return nil, f.err
	}
	return &domain.TargetPreview{Total: 1, Hosts: []domain.TargetHost{{ID: "1", Name: "web-01"}}}, nil
}

func router(svc domain.TargetService, principal *domain.Principal) *echo.Echo {
	e := echo.New()
	inject := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if principal != nil {
				c.Set("principal", *principal)
			}
			return next(c)
		}
	}
	// The same guard the v1 router applies to read routes.
	g := e.Group("/fleet", inject, middleware.RequirePermission(domain.PermissionInventoryRead))
	fleet.RegisterRoutes(g, fleet.NewHandler(svc))
	return e
}

func post(e *echo.Echo, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/fleet/targets/preview", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestPreviewPassesTheRuleAndPrincipal(t *testing.T) {
	svc := &fakeTargets{}
	principal := domain.Principal{UserID: 2, WorkspaceID: 7, Role: domain.RoleReader}
	rec := post(router(svc, &principal), `{"selector":"role=web","include":["db-01"],"exclude":["web-02"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if svc.rule.Selector != "role=web" || len(svc.rule.Include) != 1 || svc.rule.Exclude[0] != "web-02" || svc.rule.All {
		t.Errorf("rule = %+v", svc.rule)
	}
	if svc.principal.WorkspaceID != 7 {
		t.Error("the authenticated principal must reach the service")
	}
	var preview domain.TargetPreview
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil || preview.Total != 1 {
		t.Errorf("body = %s (%v)", rec.Body.String(), err)
	}
}

func TestPreviewStatusMapping(t *testing.T) {
	principal := domain.Principal{UserID: 2, WorkspaceID: 7, Role: domain.RoleReader}
	tests := []struct {
		name   string
		err    error
		body   string
		status int
	}{
		{"invalid rule", domain.ErrInvalidInput, `{}`, http.StatusBadRequest},
		{"bad json", nil, `{`, http.StatusBadRequest},
		{"denied", domain.ErrPermissionDenied, `{"all":true}`, http.StatusForbidden},
		{"unexpected", context.DeadlineExceeded, `{"all":true}`, http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(router(&fakeTargets{err: tc.err}, &principal), tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "deadline") {
				t.Errorf("internal details leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestPreviewRequiresAnAuthenticatedPrincipalWithReadAccess(t *testing.T) {
	svc := &fakeTargets{}
	if rec := post(router(svc, nil), `{"all":true}`); rec.Code == http.StatusOK {
		t.Errorf("unauthenticated status = %d, want a rejection", rec.Code)
	}
	noRole := domain.Principal{UserID: 3, WorkspaceID: 7}
	if rec := post(router(svc, &noRole), `{"all":true}`); rec.Code != http.StatusForbidden {
		t.Errorf("no-permission status = %d, want 403", rec.Code)
	}
}
