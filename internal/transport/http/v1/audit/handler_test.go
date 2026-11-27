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
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/audit"
)

type fakeAudit struct {
	filter    domain.AuditFilter
	principal domain.Principal
	err       error
	calls     int
	prune     domain.AuditPruneRequest
}

func (f *fakeAudit) Prune(_ context.Context, p domain.Principal, req domain.AuditPruneRequest) (*domain.AuditCheckpoint, error) {
	f.calls++
	f.principal, f.prune = p, req
	if f.err != nil {
		return nil, f.err
	}
	return &domain.AuditCheckpoint{ID: 1, LastEventID: req.BeforeID - 1, LastHash: req.ArchiveHeadHash, EventsRemoved: req.ArchiveCount}, nil
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
	g := e.Group("", inject)
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
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/audit/prune", strings.NewReader(`{"beforeId":5}`)))
			if rec.Code != http.StatusForbidden {
				t.Errorf("POST /audit/prune status = %d, want 403", rec.Code)
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

func post(e *echo.Echo, target, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(rec, req)
	return rec
}

func TestPrunePassesTheRequestAndMapsErrors(t *testing.T) {
	hash := strings.Repeat("c", 64)
	body := `{"beforeId":9,"archiveHeadHash":"` + hash + `","archiveCount":8}`
	svc := &fakeAudit{}
	rec := post(router(svc, owner), "/audit/prune", body)
	var cp domain.AuditCheckpoint
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &cp) != nil || cp.LastEventID != 8 || cp.LastHash != hash {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if svc.prune.BeforeID != 9 || svc.prune.ArchiveCount != 8 || svc.principal.WorkspaceID != 7 {
		t.Errorf("service saw %+v for %+v", svc.prune, svc.principal)
	}
	for _, tc := range []struct {
		name   string
		body   string
		err    error
		status int
	}{
		{"malformed body", `{`, nil, http.StatusBadRequest},
		{"invalid", body, domain.ErrInvalidInput, http.StatusBadRequest},
		{"archive mismatch", body, domain.ErrAuditArchiveMismatch, http.StatusConflict},
		{"denied", body, domain.ErrPermissionDenied, http.StatusForbidden},
		{"unexpected", body, errors.New("pq: connection refused"), http.StatusInternalServerError},
	} {
		rec := post(router(&fakeAudit{err: tc.err}, owner), "/audit/prune", tc.body)
		if rec.Code != tc.status {
			t.Errorf("%s: status = %d, want %d (body %s)", tc.name, rec.Code, tc.status, rec.Body.String())
		}
		if tc.status == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "pq") {
			t.Errorf("internal details leaked: %s", rec.Body.String())
		}
	}
}
