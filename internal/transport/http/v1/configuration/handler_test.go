package configuration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/runtimeconfig"
	"github.com/machinarchy/machinarchy/internal/store"
)

func newRouter(t *testing.T) (func(principal *domain.Principal, method, path, body string) *httptest.ResponseRecorder, *runtimeconfig.Service) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stores, err := store.Open(context.Background(), store.Config{Engine: store.EngineSQLite, SQLitePath: filepath.Join(dir, "c.db"),
		EncryptionSecret: "configuration-handler-test-key-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	snapshots, err := runtimeconfig.NewSnapshotManager(stores.Management, "p1", "api")
	if err != nil {
		t.Fatal(err)
	}
	service, err := runtimeconfig.NewService(stores.Management, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Bootstrap(context.Background(), runtimeconfig.Defaults()); err != nil {
		t.Fatal(err)
	}
	return func(principal *domain.Principal, method, path, body string) *httptest.ResponseRecorder {
		e := echo.New()
		group := e.Group("/api/v1", func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				if principal != nil {
					c.Set("principal", *principal)
				}
				return next(c)
			}
		})
		RegisterRoutes(group, NewHandler(service))
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, request)
		return recorder
	}, service
}

func configJSON(t *testing.T, mutate func(*runtimeconfig.Config)) string {
	t.Helper()
	config := runtimeconfig.Defaults()
	if mutate != nil {
		mutate(&config)
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRuntimeConfigEndpoints(t *testing.T) {
	call, service := newRouter(t)
	owner := &domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner}
	operator := &domain.Principal{UserID: 2, WorkspaceID: 1, Role: domain.RoleOperator}
	signupOn := configJSON(t, func(c *runtimeconfig.Config) { c.Features.SignupEnabled = true })

	for _, step := range []struct {
		name               string
		principal          *domain.Principal
		method, path, body string
		want               int
	}{
		{"current", owner, http.MethodGet, "/api/v1/runtime-config", "", http.StatusOK},
		{"list", owner, http.MethodGet, "/api/v1/runtime-config/revisions?limit=5", "", http.StatusOK},
		{"list bad limit", owner, http.MethodGet, "/api/v1/runtime-config/revisions?limit=x", "", http.StatusBadRequest},
		{"list bad before", owner, http.MethodGet, "/api/v1/runtime-config/revisions?before=-1", "", http.StatusBadRequest},
		{"get", owner, http.MethodGet, "/api/v1/runtime-config/revisions/1", "", http.StatusOK},
		{"get missing", owner, http.MethodGet, "/api/v1/runtime-config/revisions/9", "", http.StatusNotFound},
		{"get bad", owner, http.MethodGet, "/api/v1/runtime-config/revisions/x", "", http.StatusBadRequest},
		{"validate", owner, http.MethodPost, "/api/v1/runtime-config/validate", `{"config":` + signupOn + `}`, http.StatusOK},
		{"validate invalid", owner, http.MethodPost, "/api/v1/runtime-config/validate",
			`{"config":` + configJSON(t, func(c *runtimeconfig.Config) { c.SSH.AllowedPorts = []int{0} }) + `}`, 422},
		{"unknown field", owner, http.MethodPost, "/api/v1/runtime-config/validate", `{"config":{},"extra":1}`, http.StatusBadRequest},
		{"two JSON values", owner, http.MethodPost, "/api/v1/runtime-config/validate", `{"config":{}} {}`, http.StatusBadRequest},
		{"publish as operator", operator, http.MethodPut, "/api/v1/runtime-config", `{"expectedRevision":1,"config":` + signupOn + `}`, http.StatusForbidden},
		{"publish without principal", nil, http.MethodPut, "/api/v1/runtime-config", `{"expectedRevision":1,"config":` + signupOn + `}`, http.StatusUnauthorized},
		{"publish", owner, http.MethodPut, "/api/v1/runtime-config", `{"expectedRevision":1,"config":` + signupOn + `,"changeNote":"on"}`, http.StatusCreated},
		{"publish stale", owner, http.MethodPut, "/api/v1/runtime-config", `{"expectedRevision":1,"config":` + signupOn + `}`, http.StatusConflict},
		{"publish restart field", owner, http.MethodPut, "/api/v1/runtime-config",
			`{"expectedRevision":2,"config":` + configJSON(t, func(c *runtimeconfig.Config) { c.Features.SignupEnabled = true; c.Operations.Workers++ }) + `}`, http.StatusCreated},
		{"publish bad body", owner, http.MethodPut, "/api/v1/runtime-config", `{`, http.StatusBadRequest},
		{"rollback as operator", operator, http.MethodPost, "/api/v1/runtime-config/rollback", `{"expectedRevision":2,"targetRevision":1}`, http.StatusForbidden},
		{"rollback without principal", nil, http.MethodPost, "/api/v1/runtime-config/rollback", `{"expectedRevision":2,"targetRevision":1}`, http.StatusUnauthorized},
		{"rollback bad body", owner, http.MethodPost, "/api/v1/runtime-config/rollback", `{"expected":2}`, http.StatusBadRequest},
		{"rollback", owner, http.MethodPost, "/api/v1/runtime-config/rollback", `{"expectedRevision":3,"targetRevision":1,"changeNote":"off"}`, http.StatusCreated},
		{"rollback missing target", owner, http.MethodPost, "/api/v1/runtime-config/rollback", `{"expectedRevision":4,"targetRevision":9}`, http.StatusNotFound},
		{"status", owner, http.MethodGet, "/api/v1/runtime-config/status", "", http.StatusOK},
	} {
		got := call(step.principal, step.method, step.path, step.body)
		if got.Code != step.want {
			t.Errorf("%s: %d %s, want %d", step.name, got.Code, got.Body, step.want)
		}
		// F21-01: a restart-required change is stored and named in the response.
		if step.name == "publish restart field" && !strings.Contains(got.Body.String(), `"restartRequired":["operations.workers"]`) {
			t.Errorf("%s: %s lacks restartRequired", step.name, got.Body)
		}
	}
	snapshot, _ := service.Snapshot()
	if snapshot.Revision.Revision != 4 || snapshot.Revision.Config.Features.SignupEnabled {
		t.Fatalf("after publish and rollback the live revision is %d (signup %v), want 4 with signup off",
			snapshot.Revision.Revision, snapshot.Revision.Config.Features.SignupEnabled)
	}
}
