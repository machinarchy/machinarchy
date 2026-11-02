package runs_test

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
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/runs"
)

type fakeRuns struct {
	request   domain.RunRequest
	principal domain.Principal
	filter    domain.RunFilter
	id        int64
	err       error
	calls     int
}

func (f *fakeRuns) Submit(_ context.Context, p domain.Principal, r domain.RunRequest) (*domain.Run, error) {
	f.calls++
	f.principal, f.request = p, r
	if f.err != nil {
		return nil, f.err
	}
	return &domain.Run{ID: 41, Status: domain.RunQueued, Program: r.Argv[0]}, nil
}

func (f *fakeRuns) Get(_ context.Context, p domain.Principal, id int64) (*domain.Run, error) {
	f.calls++
	f.principal, f.id = p, id
	if f.err != nil {
		return nil, f.err
	}
	return &domain.Run{ID: id, Status: domain.RunSucceeded}, nil
}

func (f *fakeRuns) List(_ context.Context, p domain.Principal, filter domain.RunFilter) ([]domain.Run, error) {
	f.calls++
	f.principal, f.filter = p, filter
	if f.err != nil {
		return nil, f.err
	}
	return []domain.Run{{ID: 2}, {ID: 1}}, nil
}

func (f *fakeRuns) Cancel(_ context.Context, p domain.Principal, id int64) (*domain.Run, error) {
	f.calls++
	f.principal, f.id = p, id
	if f.err != nil {
		return nil, f.err
	}
	return &domain.Run{ID: id, CancelRequested: true}, nil
}

// router mounts the real routes behind the guards the v1 router applies.
func router(svc domain.RunService, principal domain.Principal) *echo.Echo {
	e := echo.New()
	inject := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("principal", principal)
			return next(c)
		}
	}
	g := e.Group("/runs")
	g.Use(inject)
	read := e.Group("/runs", inject, middleware.RequirePermission(domain.PermissionOperationsRead))
	execute := e.Group("/runs", inject, middleware.RequirePermission(domain.PermissionRunsExecute))
	runs.RegisterRoutes(read, execute, runs.NewHandler(svc))
	return e
}

func do(e *echo.Echo, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

var (
	operator = domain.Principal{UserID: 1, Username: "ops", WorkspaceID: 7, Role: domain.RoleOperator}
	reader   = domain.Principal{UserID: 2, Username: "reader", WorkspaceID: 7, Role: domain.RoleReader}
)

func TestStartingAndStoppingRunsNeedTheExecutePermission(t *testing.T) {
	svc := &fakeRuns{}
	e := router(svc, reader)
	for _, tc := range []struct{ method, target, body string }{
		{http.MethodPost, "/runs", `{"argv":["uptime"],"all":true}`},
		{http.MethodPost, "/runs/5/cancel", ``},
	} {
		if rec := do(e, tc.method, tc.target, tc.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a reader: status = %d, want 403", tc.method, tc.target, rec.Code)
		}
	}
	if svc.calls != 0 {
		t.Error("a rejected caller must never reach the service")
	}
	// A reader may still look.
	if rec := do(e, http.MethodGet, "/runs", ""); rec.Code != http.StatusOK {
		t.Errorf("GET /runs as a reader: status = %d, want 200", rec.Code)
	}
	if rec := do(e, http.MethodGet, "/runs/5", ""); rec.Code != http.StatusOK {
		t.Errorf("GET /runs/5 as a reader: status = %d, want 200", rec.Code)
	}
	noRole := domain.Principal{UserID: 3, WorkspaceID: 7}
	if rec := do(router(&fakeRuns{}, noRole), http.MethodGet, "/runs", ""); rec.Code != http.StatusForbidden {
		t.Errorf("GET /runs without any permission: status = %d, want 403", rec.Code)
	}
}

func TestSubmitMapsTheRequestAndReturnsAccepted(t *testing.T) {
	svc := &fakeRuns{}
	rec := do(router(svc, operator), http.MethodPost, "/runs", `{
		"argv": ["systemctl", "restart", "nginx"],
		"selector": "role=web", "include": ["db-01"], "exclude": ["web-02"],
		"timeoutSeconds": 30, "concurrency": 4, "expectTargetsHash": "abc"
	}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "/api/v1/runs/41" {
		t.Errorf("Location = %q", rec.Header().Get("Location"))
	}
	got := svc.request
	if strings.Join(got.Argv, " ") != "systemctl restart nginx" || got.Rule.Selector != "role=web" ||
		got.Rule.Include[0] != "db-01" || got.Rule.Exclude[0] != "web-02" || got.Rule.All ||
		got.TimeoutSeconds != 30 || got.Concurrency != 4 || got.ExpectTargetsHash != "abc" || got.DryRun {
		t.Errorf("request = %+v", got)
	}
	if svc.principal.WorkspaceID != 7 || svc.principal.UserID != 1 {
		t.Errorf("principal = %+v", svc.principal)
	}
	var run domain.Run
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil || run.ID != 41 || run.Status != domain.RunQueued {
		t.Errorf("body = %s (%v)", rec.Body.String(), err)
	}
}

func TestDryRunReturnsThePlanWithOK(t *testing.T) {
	svc := &fakeRuns{}
	rec := do(router(svc, operator), http.MethodPost, "/runs", `{"argv":["uptime"],"all":true,"dryRun":true}`)
	if rec.Code != http.StatusOK || !svc.request.DryRun || !svc.request.Rule.All {
		t.Fatalf("status = %d, request %+v", rec.Code, svc.request)
	}
	if rec.Header().Get("Location") != "" {
		t.Error("a dry run creates nothing, so there is nothing to locate")
	}
}

func TestListAndGetAndCancelPassTheirArguments(t *testing.T) {
	svc := &fakeRuns{}
	e := router(svc, operator)
	if rec := do(e, http.MethodGet, "/runs?status=failed&before_id=9&limit=5", ""); rec.Code != http.StatusOK ||
		svc.filter.Status != "failed" || svc.filter.BeforeID != 9 || svc.filter.Limit != 5 {
		t.Errorf("list: status %d filter %+v", rec.Code, svc.filter)
	}
	if rec := do(e, http.MethodGet, "/runs/12", ""); rec.Code != http.StatusOK || svc.id != 12 {
		t.Errorf("get: status %d id %d", rec.Code, svc.id)
	}
	rec := do(e, http.MethodPost, "/runs/13/cancel", "")
	var run domain.Run
	if rec.Code != http.StatusAccepted || svc.id != 13 || json.Unmarshal(rec.Body.Bytes(), &run) != nil || !run.CancelRequested {
		t.Errorf("cancel: status %d id %d body %s", rec.Code, svc.id, rec.Body.String())
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		body   string
		err    error
		status int
	}{
		{"bad json", http.MethodPost, "/runs", `{`, nil, http.StatusBadRequest},
		{"invalid input", http.MethodPost, "/runs", `{"argv":["x"]}`, domain.ErrInvalidInput, http.StatusBadRequest},
		{"execution not allowed", http.MethodPost, "/runs", `{"argv":["x"]}`, domain.ErrExecutionNotAllowed, http.StatusForbidden},
		{"permission denied", http.MethodPost, "/runs", `{"argv":["x"]}`, domain.ErrPermissionDenied, http.StatusForbidden},
		{"busy", http.MethodPost, "/runs", `{"argv":["x"]}`, domain.ErrServiceBusy, http.StatusServiceUnavailable},
		{"unexpected", http.MethodPost, "/runs", `{"argv":["x"]}`, errors.New("pq: connection refused"), http.StatusInternalServerError},
		{"run not found", http.MethodGet, "/runs/5", "", domain.ErrRunNotFound, http.StatusNotFound},
		{"cancel not found", http.MethodPost, "/runs/5/cancel", "", domain.ErrRunNotFound, http.StatusNotFound},
		{"cancel finished", http.MethodPost, "/runs/5/cancel", "", domain.ErrRunFinished, http.StatusConflict},
		{"bad id", http.MethodGet, "/runs/abc", "", nil, http.StatusBadRequest},
		{"zero id", http.MethodGet, "/runs/0", "", nil, http.StatusBadRequest},
		{"bad cancel id", http.MethodPost, "/runs/x/cancel", "", nil, http.StatusBadRequest},
		{"bad limit", http.MethodGet, "/runs?limit=abc", "", nil, http.StatusBadRequest},
		{"bad cursor", http.MethodGet, "/runs?before_id=x", "", nil, http.StatusBadRequest},
		{"list invalid filter", http.MethodGet, "/runs?status=weird", "", domain.ErrInvalidInput, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(router(&fakeRuns{err: tc.err}, operator), tc.method, tc.target, tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "pq") {
				t.Errorf("internal details leaked: %s", rec.Body.String())
			}
			if tc.status == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") == "" {
				t.Error("a busy response should tell the client when to retry")
			}
		})
	}
}

func TestChangedTargetsAreReportedWithTheNewSet(t *testing.T) {
	svc := &fakeRuns{err: &domain.TargetsChangedError{TargetsHash: "newhash", Total: 5}}
	rec := do(router(svc, operator), http.MethodPost, "/runs", `{"argv":["uptime"],"selector":"role=web","expectTargetsHash":"old"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	var body struct {
		Error       string `json:"error"`
		TargetsHash string `json:"targetsHash"`
		Total       int    `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.TargetsHash != "newhash" || body.Total != 5 || body.Error == "" {
		t.Errorf("body = %s (%v); the client needs the new hash and count to review and resubmit", rec.Body.String(), err)
	}
}
