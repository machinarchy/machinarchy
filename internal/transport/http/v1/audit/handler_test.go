package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/audit"
)

type fakeAudit struct {
	filter    domain.AuditFilter
	principal domain.Principal
	err       error
	calls     int
}

func (f *fakeAudit) List(_ context.Context, p domain.Principal, filter domain.AuditFilter) ([]domain.AuditEvent, error) {
	f.calls++
	f.principal, f.filter = p, filter
	if f.err != nil {
		return nil, f.err
	}
	return []domain.AuditEvent{{ID: 5, Action: domain.AuditHostCreated, Outcome: "success", Details: map[string]string{"name": "web-01"}}}, nil
}

func (f *fakeAudit) Verify(_ context.Context, p domain.Principal) (*domain.AuditVerification, error) {
	f.calls++
	f.principal = p
	if f.err != nil {
		return nil, f.err
	}
	return &domain.AuditVerification{Valid: true, Events: 1, HeadHash: "abc"}, nil
}

// router mounts the real routes behind the same guards the v1 router uses.
func router(svc domain.AuditService, principal domain.Principal) *echo.Echo {
	e := echo.New()
	inject := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("principal", principal)
			return next(c)
		}
	}
	g := e.Group("/audit", inject, middleware.RequirePermission(domain.PermissionAuditRead), middleware.RequireHuman())
	audit.RegisterRoutes(g, audit.NewHandler(svc))
	return e
}

func get(e *echo.Echo, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

var owner = domain.Principal{UserID: 1, WorkspaceID: 7, Role: domain.RoleOwner}

func TestAuditRoutesAreOwnerOnlyAndHumanOnly(t *testing.T) {
	cases := map[string]domain.Principal{
		"operator":  {UserID: 2, WorkspaceID: 7, Role: domain.RoleOperator},
		"reader":    {UserID: 3, WorkspaceID: 7, Role: domain.RoleReader},
		"api token": {UserID: 1, WorkspaceID: 7, Role: domain.RoleOwner, APITokenID: 5},
	}
	for name, principal := range cases {
		t.Run(name, func(t *testing.T) {
			svc := &fakeAudit{}
			e := router(svc, principal)
			for _, path := range []string{"/audit/events", "/audit/verify"} {
				if rec := get(e, path); rec.Code != http.StatusForbidden {
					t.Errorf("GET %s status = %d, want 403", path, rec.Code)
				}
			}
			if svc.calls != 0 {
				t.Error("a rejected caller must never reach the service")
			}
		})
	}
}

func TestListPassesFilterAndPrincipal(t *testing.T) {
	svc := &fakeAudit{}
	rec := get(router(svc, owner), "/audit/events?action=host.created&before_id=42&limit=25")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if svc.filter.Action != "host.created" || svc.filter.BeforeID != 42 || svc.filter.Limit != 25 || svc.principal.WorkspaceID != 7 {
		t.Errorf("service saw filter %+v principal %+v", svc.filter, svc.principal)
	}
	var events []domain.AuditEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil || len(events) != 1 || events[0].ID != 5 {
		t.Errorf("body = %s (%v)", rec.Body.String(), err)
	}
	if strings.Contains(rec.Body.String(), "workspace") {
		t.Errorf("events must not expose internal workspace IDs: %s", rec.Body.String())
	}
}

func TestVerifyReturnsTheResult(t *testing.T) {
	rec := get(router(&fakeAudit{}, owner), "/audit/verify")
	var result domain.AuditVerification
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &result) != nil || !result.Valid || result.HeadHash != "abc" {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestAuditErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		err    error
		status int
	}{
		{"non-numeric limit", "/audit/events?limit=abc", nil, http.StatusBadRequest},
		{"non-numeric cursor", "/audit/events?before_id=x", nil, http.StatusBadRequest},
		{"invalid filter", "/audit/events", domain.ErrInvalidInput, http.StatusBadRequest},
		{"denied", "/audit/verify", domain.ErrPermissionDenied, http.StatusForbidden},
		{"unexpected", "/audit/events", errors.New("pq: password authentication failed"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := get(router(&fakeAudit{err: tc.err}, owner), tc.target)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "pq") {
				t.Errorf("internal details leaked: %s", rec.Body.String())
			}
		})
	}
}
