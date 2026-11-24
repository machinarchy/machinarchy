package managedresource_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/service"
	"github.com/machinarchy/machinarchy/internal/store"
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/managedresource"
)

type apiFixture struct {
	t       *testing.T
	server  *echo.Echo
	hosts   []*resource.Host
	profile *resource.AccessProfile
	app     *service.ManagedResourceService
}

// newAPI serves /managed over a real SQLite store. The X-Role header picks
// the caller's role; without it the request is unauthenticated.
func newAPI(t *testing.T) *apiFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stores, err := store.Open(ctx, store.Config{Engine: store.EngineSQLite, SQLitePath: filepath.Join(dir, "api.db"),
		EncryptionSecret: "handler-test-key-material-that-is-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	secretService, err := secrets.New(stores.Management, &secrets.StaticKeyProvider{KeyID: "k", Key: []byte("0123456789abcdef0123456789abcdef")}, secrets.DenyAllResolutionAuthorizer{})
	if err != nil {
		t.Fatal(err)
	}
	policy := domain.ExecutionPolicy{Enabled: true, AllowAny: true}
	application, err := service.NewManagedResourceService(stores.ResourceHosts, stores.ResourceProfiles, stores.Operations,
		func() domain.ExecutionPolicy { return policy }, secretService)
	if err != nil {
		t.Fatal(err)
	}
	application.UseEnvironments(stores.Environments)

	owner := domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner}
	secret, err := secretService.Create(ctx, owner, secrets.Input{Name: "ssh", Purpose: secrets.PurposeSSHCredential, Value: "top-secret-value"})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := application.CreateAccessProfile(ctx, owner, "deploy", "", resource.AccessProfileSpec{Connector: resource.ConnectorSSH,
		Username: "deploy", Authentication: resource.AuthenticationPassword,
		Credential: resource.SecretReference{ID: secret.ID, Version: secret.CurrentVersion}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	f := &apiFixture{t: t, profile: profile, app: application}
	for i, tier := range []string{"web", "web", "db"} {
		host, err := application.CreateHost(ctx, owner, fmt.Sprintf("host-%d", i), map[string]string{"tier": tier},
			resource.HostSpec{AccessProfileID: profile.ID, AccessTarget: resource.HostAccessTarget{Endpoint: fmt.Sprintf("10.0.0.%d:22", i+1)}})
		if err != nil {
			t.Fatal(err)
		}
		f.hosts = append(f.hosts, host)
	}

	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if role := c.Request().Header.Get("X-Role"); role != "" {
				c.Set("principal", domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.Role(role)})
			}
			return next(c)
		}
	})
	managedresource.RegisterRoutes(e.Group("/managed"), managedresource.NewHandler(application))
	f.server = e
	return f
}

type response struct {
	status int
	body   map[string]any
	raw    string
}

func (f *apiFixture) do(role domain.Role, method, path string, body any) response {
	f.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	if role != "" {
		request.Header.Set("X-Role", string(role))
	}
	recorder := httptest.NewRecorder()
	f.server.ServeHTTP(recorder, request)
	out := response{status: recorder.Code, raw: recorder.Body.String()}
	_ = json.Unmarshal(recorder.Body.Bytes(), &out.body)
	return out
}

func (f *apiFixture) expect(got response, status int, what string) response {
	f.t.Helper()
	if got.status != status {
		f.t.Fatalf("%s: status %d, want %d (body %s)", what, got.status, status, got.raw)
	}
	if status >= 400 && (got.body["code"] == nil || got.body["error"] == nil) {
		f.t.Fatalf("%s: error body %s lacks error/code", what, got.raw)
	}
	return got
}

func items(r response) []any {
	list, _ := r.body["items"].([]any)
	return list
}

func TestManagedAPIAuthorizationAndQueries(t *testing.T) {
	f := newAPI(t)
	const owner, operator, reader = domain.RoleOwner, domain.RoleOperator, domain.RoleReader
	host := f.hosts[0]

	f.expect(f.do("", http.MethodGet, "/managed/hosts", nil), http.StatusUnauthorized, "anonymous list")

	// Listing with keyset pagination and selectors.
	all := f.expect(f.do(reader, http.MethodGet, "/managed/hosts", nil), http.StatusOK, "reader list")
	if len(items(all)) != 3 {
		t.Fatalf("hosts = %d, want 3", len(items(all)))
	}
	first := f.expect(f.do(reader, http.MethodGet, "/managed/hosts?selector=tier%3Dweb&limit=1", nil), http.StatusOK, "selector page 1")
	cursor, _ := first.body["nextCursor"].(string)
	if len(items(first)) != 1 || cursor == "" {
		t.Fatalf("first selector page = %s", first.raw)
	}
	second := f.expect(f.do(reader, http.MethodGet, "/managed/hosts?selector=tier%3Dweb&limit=1&cursor="+cursor, nil), http.StatusOK, "selector page 2")
	if len(items(second)) != 1 || items(second)[0].(map[string]any)["metadata"].(map[string]any)["name"] == items(first)[0].(map[string]any)["metadata"].(map[string]any)["name"] {
		t.Fatalf("second selector page = %s", second.raw)
	}
	f.expect(f.do(reader, http.MethodGet, "/managed/hosts?selector=tier+in+(", nil), http.StatusBadRequest, "bad selector")

	// Labels and deletion need inventory:write and the current revision.
	labelsPath := "/managed/hosts/" + string(host.Metadata.ID) + "/labels"
	f.expect(f.do(reader, http.MethodPatch, labelsPath, map[string]any{"revision": host.Metadata.Revision, "labels": map[string]string{"tier": "canary"}}), http.StatusForbidden, "reader labels")
	f.expect(f.do(operator, http.MethodPatch, labelsPath, map[string]any{"revision": 99, "labels": map[string]string{"tier": "canary"}}), http.StatusConflict, "stale labels")
	labelled := f.expect(f.do(operator, http.MethodPatch, labelsPath, map[string]any{"revision": host.Metadata.Revision, "labels": map[string]string{"tier": "canary"}}), http.StatusOK, "operator labels")
	revision := labelled.body["metadata"].(map[string]any)["revision"].(float64)

	hostPath := "/managed/hosts/" + string(host.Metadata.ID)
	f.expect(f.do(reader, http.MethodDelete, fmt.Sprintf("%s?revision=%d", hostPath, int(revision)), nil), http.StatusForbidden, "reader delete")
	f.expect(f.do(operator, http.MethodDelete, hostPath, nil), http.StatusBadRequest, "delete without revision")
	f.expect(f.do(operator, http.MethodDelete, hostPath+"?revision=1", nil), http.StatusConflict, "stale delete")

	// Access Profiles: references only, owner-only deletion, refused while bound.
	profiles := f.expect(f.do(reader, http.MethodGet, "/managed/access-profiles", nil), http.StatusOK, "list profiles")
	if len(items(profiles)) != 1 || strings.Contains(profiles.raw, "top-secret-value") {
		t.Fatalf("profiles = %s", profiles.raw)
	}
	profilePath := "/managed/access-profiles/" + string(f.profile.ID)
	view := f.expect(f.do(reader, http.MethodGet, profilePath, nil), http.StatusOK, "get profile")
	if view.body["spec"] == nil || strings.Contains(view.raw, "top-secret-value") {
		t.Fatalf("profile view = %s", view.raw)
	}
	f.expect(f.do(operator, http.MethodDelete, profilePath, nil), http.StatusForbidden, "operator profile delete")
	f.expect(f.do(owner, http.MethodDelete, profilePath, nil), http.StatusConflict, "delete bound profile")

	// Preview, then submit bound to the previewed set.
	rule := map[string]any{"selector": "tier=web"}
	f.expect(f.do(reader, http.MethodPost, "/managed/operations/preview", map[string]any{"targets": map[string]any{}}), http.StatusBadRequest, "empty rule")
	preview := f.expect(f.do(reader, http.MethodPost, "/managed/operations/preview", map[string]any{"targets": rule}), http.StatusOK, "preview")
	hash, _ := preview.body["targetsHash"].(string)
	if len(preview.body["hosts"].([]any)) != 1 || hash == "" { // host-0 is now tier=canary
		t.Fatalf("preview = %s", preview.raw)
	}
	run := map[string]any{"argv": []string{"uptime"}, "timeoutSeconds": 30, "concurrency": 1}
	f.expect(f.do(reader, http.MethodPost, "/managed/operations", map[string]any{"targets": rule, "run": run}), http.StatusForbidden, "reader submit")
	changed := f.expect(f.do(operator, http.MethodPost, "/managed/operations", map[string]any{"targets": rule, "expectTargetsHash": "stale", "run": run}), http.StatusConflict, "stale hash")
	if changed.body["code"] != "targets_changed" {
		t.Fatalf("stale hash body = %s", changed.raw)
	}
	f.expect(f.do(operator, http.MethodPost, "/managed/operations", map[string]any{"targets": rule, "hostIds": []string{string(host.Metadata.ID)}, "run": run}), http.StatusBadRequest, "hostIds and targets")
	f.expect(f.do(operator, http.MethodPost, "/managed/operations", map[string]any{"targets": rule, "expectTargetsHash": hash, "run": run}), http.StatusAccepted, "submit")

	// Operation summaries are visible to readers and carry no argv.
	ops := f.expect(f.do(reader, http.MethodGet, "/managed/operations", nil), http.StatusOK, "list operations")
	if len(items(ops)) != 1 || strings.Contains(ops.raw, "uptime") {
		t.Fatalf("operations = %s", ops.raw)
	}
	f.expect(f.do(reader, http.MethodGet, "/managed/operations?cursor=%21%21", nil), http.StatusBadRequest, "bad cursor")
	f.expect(f.do(reader, http.MethodGet, "/managed/operations?createdAfter=yesterday", nil), http.StatusBadRequest, "bad time")
	f.expect(f.do(reader, http.MethodGet, "/managed/operations?host="+string(f.hosts[2].Metadata.ID), nil), http.StatusOK, "host filter")

	// A host with an active Operation cannot be deleted.
	busy := f.hosts[1]
	f.expect(f.do(operator, http.MethodDelete, fmt.Sprintf("/managed/hosts/%s?revision=%d", busy.Metadata.ID, busy.Metadata.Revision), nil), http.StatusConflict, "delete busy host")
	idle := f.hosts[2]
	f.expect(f.do(operator, http.MethodDelete, fmt.Sprintf("/managed/hosts/%s?revision=%d", idle.Metadata.ID, idle.Metadata.Revision), nil), http.StatusNoContent, "delete idle host")
	f.expect(f.do(reader, http.MethodGet, "/managed/hosts/"+string(idle.Metadata.ID), nil), http.StatusNotFound, "deleted host")
}

func TestCapabilityOperationsAPI(t *testing.T) {
	f := newAPI(t)
	const operator, reader = domain.RoleOperator, domain.RoleReader
	ctx := context.Background()
	owner := domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner}
	agents, err := f.app.CreateAccessProfile(ctx, owner, "agents", "", resource.AccessProfileSpec{Connector: resource.ConnectorAgent, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	agentHost, err := f.app.CreateHost(ctx, owner, "agent-0", map[string]string{"tier": "agent"}, resource.HostSpec{AccessProfileID: agents.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.CreateAccessProfile(ctx, owner, "bad", "", resource.AccessProfileSpec{Connector: resource.ConnectorAgent, Enabled: true, Username: "root"}); err == nil {
		t.Fatal("an agent profile with a username was accepted")
	}

	capability := map[string]any{"name": "service.status", "input": map[string]any{"service": "nginx.service"}, "timeoutSeconds": 30}
	run := map[string]any{"argv": []string{"uptime"}, "timeoutSeconds": 30, "concurrency": 1}
	agentIDs := []string{string(agentHost.Metadata.ID)}
	f.expect(f.do(reader, http.MethodPost, "/managed/operations", map[string]any{"hostIds": agentIDs, "capability": capability}), http.StatusForbidden, "reader capability")
	f.expect(f.do(operator, http.MethodPost, "/managed/operations", map[string]any{"hostIds": agentIDs}), http.StatusBadRequest, "neither run nor capability")
	f.expect(f.do(operator, http.MethodPost, "/managed/operations", map[string]any{"hostIds": agentIDs, "run": run, "capability": capability}), http.StatusBadRequest, "run and capability")
	f.expect(f.do(operator, http.MethodPost, "/managed/operations", map[string]any{"hostIds": agentIDs, "capability": map[string]any{"name": "Bad Name!"}}), http.StatusBadRequest, "bad capability name")
	// Each kind needs hosts of its own connector.
	f.expect(f.do(operator, http.MethodPost, "/managed/operations", map[string]any{"hostIds": agentIDs, "run": run}), http.StatusBadRequest, "run on an agent host")
	f.expect(f.do(operator, http.MethodPost, "/managed/operations", map[string]any{"hostIds": []string{string(f.hosts[0].Metadata.ID)}, "capability": capability}), http.StatusBadRequest, "capability on an SSH host")

	submitted := f.expect(f.do(operator, http.MethodPost, "/managed/operations", map[string]any{"targets": map[string]any{"selector": "tier=agent"}, "capability": capability}), http.StatusAccepted, "submit capability")
	if submitted.body["kind"] != "Capability" {
		t.Fatalf("submitted = %s", submitted.raw)
	}
	id, _ := submitted.body["id"].(string)
	view := f.expect(f.do(reader, http.MethodGet, "/managed/operations/"+id, nil), http.StatusOK, "get capability Operation")
	if !strings.Contains(view.raw, "service.status") || strings.Contains(view.raw, "nginx.service") {
		t.Fatalf("capability view must name the capability and hide its input: %s", view.raw)
	}
}
