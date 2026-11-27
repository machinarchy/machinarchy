package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/config"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

// access is who may call a route. Everyone outside it must get 401 (no
// credential) or 403.
type access int

const (
	public        access = iota // no credential needed
	authenticated               // any valid credential
	readers                     // inventory:read / operations:read: every role and every API token
	writers                     // inventory:write or runs:execute: operators and owners, operator tokens
	ownerOnly                   // the workspace owner; API tokens can never be owners
	ownerHuman                  // owner session only (RequireOwner + RequireHuman, or the owner-only services)
	humans                      // any login session, never an API token (self-service: own sessions and password)
)

// matrix is the intended authorization of every documented route. A new
// route without an entry here fails the test.
var matrix = map[string]access{
	"GET /healthz":         public,
	"GET /healthz/live":    public,
	"GET /api/v1/version":  public,
	"GET /healthz/ready":   public,
	"GET /metrics":         public,
	"POST /api/auth/login": public, "POST /api/auth/logout": public, "POST /api/auth/signup": public,
	"POST /api/auth/invitations/accept": public,

	"GET /api/v1/me": authenticated,

	// S-08: a user manages their own sessions and password; API tokens cannot.
	"GET /api/v1/sessions": humans, "DELETE /api/v1/sessions": humans, "DELETE /api/v1/sessions/{id}": humans,
	"POST /api/v1/me/password": humans,

	"GET /api/v1/environments": readers, "POST /api/v1/environments": writers,
	"PUT /api/v1/environments/{id}": writers, "DELETE /api/v1/environments/{id}": writers,

	"GET /api/v1/tokens": ownerHuman, "POST /api/v1/tokens": ownerHuman, "DELETE /api/v1/tokens/{id}": ownerHuman,
	"GET /api/v1/members": ownerHuman, "PUT /api/v1/members/{id}/role": ownerHuman, "DELETE /api/v1/members/{id}": ownerHuman,
	"GET /api/v1/members/invitations": ownerHuman, "POST /api/v1/members/invitations": ownerHuman,
	"DELETE /api/v1/members/invitations/{id}": ownerHuman,
	"POST /api/v1/members":                    ownerHuman, "POST /api/v1/members/{id}/link": ownerHuman,
	"GET /api/v1/audit/events": ownerHuman, "GET /api/v1/audit/verify": ownerHuman,
	"POST /api/v1/audit/prune": ownerHuman,

	"GET /api/v1/runtime-config": ownerHuman, "PUT /api/v1/runtime-config": ownerHuman,
	"GET /api/v1/runtime-config/revisions": ownerHuman, "GET /api/v1/runtime-config/revisions/{revision}": ownerHuman,
	"GET /api/v1/runtime-config/status": ownerHuman, "POST /api/v1/runtime-config/rollback": ownerHuman,
	"POST /api/v1/runtime-config/validate": ownerHuman,

	"GET /api/v1/secrets": ownerHuman, "POST /api/v1/secrets": ownerHuman, "GET /api/v1/secrets/{id}": ownerHuman,
	"DELETE /api/v1/secrets/{id}": ownerHuman, "GET /api/v1/secrets/{id}/versions": ownerHuman,
	"POST /api/v1/secrets/{id}/rotate": ownerHuman, "POST /api/v1/secrets/rewrap": ownerHuman,

	"GET /api/v1/agents": readers, "GET /api/v1/agents/controller-key": readers,
	"POST /api/v1/agents/{id}/revoke":      ownerHuman,
	"GET /api/v1/agents/enrollment-tokens": ownerHuman, "POST /api/v1/agents/enrollment-tokens": ownerHuman,
	"DELETE /api/v1/agents/enrollment-tokens/{id}": ownerHuman,
	"GET /api/v1/agents/signing-keys":              ownerHuman, "POST /api/v1/agents/signing-keys/next": ownerHuman,
	"POST /api/v1/agents/signing-keys/next/activate": ownerHuman, "DELETE /api/v1/agents/signing-keys/next": ownerHuman,

	"GET /api/v1/managed/hosts": readers, "GET /api/v1/managed/hosts/{id}": readers,
	"POST /api/v1/managed/hosts": writers, "PUT /api/v1/managed/hosts/{id}": writers,
	"PATCH /api/v1/managed/hosts/{id}/labels": writers, "DELETE /api/v1/managed/hosts/{id}": writers,
	"POST /api/v1/managed/hosts/{id}/trust": ownerOnly, // trusting keys decides where credentials go (S-02)
	"GET /api/v1/managed/access-profiles":   readers, "GET /api/v1/managed/access-profiles/{id}": readers,
	"POST /api/v1/managed/access-profiles": ownerOnly, "PUT /api/v1/managed/access-profiles/{id}": ownerOnly,
	"DELETE /api/v1/managed/access-profiles/{id}": ownerOnly,
	"GET /api/v1/managed/operations":              readers, "GET /api/v1/managed/operations/{id}": readers,
	// Output can carry secrets: only callers allowed to execute runs (AUDIT S-04).
	"GET /api/v1/managed/operations/{id}/targets/{hostId}/output": writers,
	"POST /api/v1/managed/operations":                             writers, "POST /api/v1/managed/operations/preview": readers,
	"POST /api/v1/managed/operations/{id}/cancel": writers,
}

type principal struct {
	name  string
	token string
	role  domain.Role
	api   bool // an API token rather than a session
}

func (p principal) allowed(a access) bool {
	switch a {
	case public:
		return true
	case authenticated, readers:
		return p.token != ""
	case writers:
		return p.role == domain.RoleOperator || p.role == domain.RoleOwner
	case ownerOnly:
		return p.role == domain.RoleOwner
	case ownerHuman:
		return p.role == domain.RoleOwner && !p.api
	case humans:
		return p.token != "" && !p.api
	}
	return false
}

type apiClient struct {
	t       *testing.T
	handler http.Handler
	calls   *int
}

func (c apiClient) call(method, path, token string, body any) (int, []byte) {
	c.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, path, reader)
	// A distinct client address per call keeps the per-IP rate limiter out
	// of an authorization test.
	*c.calls++
	request.RemoteAddr = fmt.Sprintf("10.%d.%d.%d:40000", *c.calls>>16&255, *c.calls>>8&255, *c.calls&255)
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	c.handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

func (c apiClient) mustJSON(method, path, token string, body any, want int, out any) {
	c.t.Helper()
	status, data := c.call(method, path, token, body)
	if status != want {
		c.t.Fatalf("%s %s = %d %s, want %d", method, path, status, data, want)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			c.t.Fatalf("%s %s: %v", method, path, err)
		}
	}
}

// TestRouteAuthorizationMatrix drives the fully wired API server (real
// SQLite stores, services, JWT and role middleware) and checks every
// documented route against every kind of principal.
func TestRouteAuthorizationMatrix(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := config.Config{
		ListenAddr: "127.0.0.1:0", Environment: config.Development, SignupEnabled: true, APIDocsEnabled: true,
		OperatorToken:    "matrix-operator-token-0123456789abcdef",
		EncryptionSecret: "matrix-encryption-secret-0123456789abcdef",
		ShutdownTimeout:  10 * time.Second,
		Database:         config.DatabaseConfig{Engine: config.SQLite, Path: filepath.Join(dir, "m.db"), ConnectTimeout: time.Second, MaxOpenConns: 1, MaxIdleConns: 1},
		BootstrapAdmin:   config.BootstrapAdminConfig{Username: "admin", Email: "admin@example.test", Password: "matrix-admin-password"},
	}
	runtime, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	server, err := runtime.NewAPIServer(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	c := apiClient{t: t, handler: server.Handler(), calls: new(int)}

	// Principals: owner session, operator and reader sessions, and API tokens.
	var session struct {
		Token string `json:"token"`
		User  struct {
			ID int `json:"id"`
		} `json:"user"`
	}
	c.mustJSON(http.MethodPost, "/api/auth/login", "", map[string]string{"username": "admin", "password": "matrix-admin-password"}, http.StatusOK, &session)
	owner := principal{name: "owner", token: session.Token, role: domain.RoleOwner}
	signup := func(name string) (string, int) {
		var created struct {
			Token string `json:"token"`
			User  struct {
				ID int `json:"id"`
			} `json:"user"`
		}
		c.mustJSON(http.MethodPost, "/api/auth/signup", "", map[string]string{"username": name, "email": name + "@example.test",
			"password": name + "-password-123"}, http.StatusCreated, &created)
		return created.Token, created.User.ID
	}
	readerToken, _ := signup("matrixreader")
	_, operatorID := signup("matrixoperator")
	// A throwaway member for the owner's DELETE /members/{id}, so the matrix
	// keeps its operator for the routes that run after it.
	_, removableID := signup("matrixremovable")
	c.mustJSON(http.MethodPut, fmt.Sprintf("/api/v1/members/%d/role", operatorID), owner.token, map[string]string{"role": "operator"}, http.StatusOK, nil)
	c.mustJSON(http.MethodPost, "/api/auth/login", "", map[string]string{"username": "matrixoperator", "password": "matrixoperator-password-123"}, http.StatusOK, &session)
	operator := principal{name: "operator", token: session.Token, role: domain.RoleOperator}
	reader := principal{name: "reader", token: readerToken, role: domain.RoleReader}
	apiToken := func(role string) string {
		var created struct {
			Token string `json:"token"`
		}
		c.mustJSON(http.MethodPost, "/api/v1/tokens", owner.token, map[string]string{"name": "matrix-" + role, "role": role}, http.StatusCreated, &created)
		return created.Token
	}
	principals := []principal{
		{name: "anonymous"},
		{name: "reader token", token: apiToken("reader"), role: domain.RoleReader, api: true},
		reader,
		{name: "operator token", token: apiToken("operator"), role: domain.RoleOperator, api: true},
		operator,
		owner, // last, so destructive owner calls cannot hide another principal's result
	}

	// Real resources, so path IDs exist and denials are not masked by 404s.
	var env struct {
		ID int `json:"id"`
	}
	c.mustJSON(http.MethodPost, "/api/v1/environments", owner.token, map[string]string{"name": "matrix"}, http.StatusCreated, &env)
	var secret struct {
		ID      string `json:"id"`
		Version uint64 `json:"version"`
	}
	c.mustJSON(http.MethodPost, "/api/v1/secrets", owner.token, map[string]string{"name": "matrix-key", "purpose": "ssh-credential",
		"value": "-----BEGIN OPENSSH PRIVATE KEY-----\nmatrix\n-----END OPENSSH PRIVATE KEY-----\n"}, http.StatusCreated, &secret)
	if secret.Version == 0 {
		secret.Version = 1
	}
	profileSpec := map[string]any{"connectorKind": "ssh", "authenticationMethod": "private-key", "username": "deploy", "enabled": true,
		"credential": map[string]any{"id": secret.ID, "version": secret.Version}}
	var profile struct {
		Metadata struct {
			ID string `json:"id"`
		} `json:"metadata"`
		ID string `json:"id"`
	}
	c.mustJSON(http.MethodPost, "/api/v1/managed/access-profiles", owner.token, map[string]any{"name": "matrix-profile", "spec": profileSpec}, http.StatusCreated, &profile)
	profileID := profile.ID
	if profileID == "" {
		profileID = profile.Metadata.ID
	}
	var host struct {
		Metadata struct {
			ID string `json:"id"`
		} `json:"metadata"`
	}
	c.mustJSON(http.MethodPost, "/api/v1/managed/hosts", owner.token, map[string]any{"name": "matrix-host", "labels": map[string]string{"tier": "web"},
		"spec": map[string]any{"presence": "Managed"}}, http.StatusCreated, &host)
	// A well-formed ID of an Operation that does not exist: allowed callers
	// get 404, others must still get 403.
	missingOperation, err := resource.NewResourceID()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{
		"/api/v1/managed/operations/{id}":             string(missingOperation),
		"/api/v1/environments/{id}":                   fmt.Sprint(env.ID),
		"/api/v1/secrets/{id}":                        secret.ID,
		"/api/v1/managed/access-profiles/{id}":        profileID,
		"/api/v1/managed/hosts/{id}":                  host.Metadata.ID,
		"/api/v1/members/{id}":                        fmt.Sprint(operatorID),
		"/api/v1/runtime-config/revisions/{revision}": "1",
	}
	resolve := func(path string) string {
		for prefix, id := range ids {
			if base := strings.TrimSuffix(prefix, "/{id}"); strings.HasPrefix(path, prefix) || strings.HasPrefix(path, base+"/{id}") {
				path = strings.Replace(path, "{id}", id, 1)
				path = strings.Replace(path, "{revision}", id, 1)
			}
		}
		return strings.NewReplacer("{id}", "missing", "{revision}", "1", "{hostId}", host.Metadata.ID).Replace(path)
	}
	// Bodies that pass validation where it is cheap, so a 400 cannot mask a
	// missing authorization check.
	bodies := map[string]any{
		"POST /api/v1/environments":               map[string]string{"name": "matrix-new"},
		"PUT /api/v1/environments/{id}":           map[string]string{"name": "matrix"},
		"POST /api/v1/managed/hosts":              map[string]any{"name": "matrix-new", "spec": map[string]any{"presence": "Managed"}},
		"PATCH /api/v1/managed/hosts/{id}/labels": map[string]any{"labels": map[string]string{"tier": "web"}},
		"POST /api/v1/managed/hosts/{id}/trust": map[string]any{"generation": 1,
			"fingerprints": []string{"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}},
		"POST /api/v1/managed/operations/preview": map[string]any{"selector": "tier=web"},
		"POST /api/v1/managed/access-profiles":    map[string]any{"name": "matrix-other", "spec": profileSpec},
		"POST /api/v1/agents/enrollment-tokens":   map[string]any{},
		"PUT /api/v1/members/{id}/role":           map[string]string{"role": "operator"},
		// Authorized callers reach validation (400): no password changes.
		"POST /api/v1/me/password": map[string]string{"currentPassword": "x", "newPassword": "short"},
	}

	// Every documented route must be in the matrix, and vice versa.
	status, specData := c.call(http.MethodGet, "/openapi.json", "", nil)
	if status != http.StatusOK {
		t.Fatalf("openapi.json = %d", status)
	}
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(specData, &spec); err != nil {
		t.Fatal(err)
	}
	var routes []string
	for path, methods := range spec.Paths {
		for method := range methods {
			routes = append(routes, strings.ToUpper(method)+" "+path)
		}
	}
	// Reads first, deletions last, so destructive calls run after everything
	// that needs the resources.
	weight := map[string]int{"GET": 0, "POST": 1, "PUT": 1, "PATCH": 1, "DELETE": 2}
	// "Log out everywhere" ends the caller's own sessions, so it runs last.
	last := func(route string) bool { return route == "DELETE /api/v1/sessions" }
	sort.Slice(routes, func(i, j int) bool {
		if last(routes[i]) != last(routes[j]) {
			return last(routes[j])
		}
		mi, mj := strings.Fields(routes[i])[0], strings.Fields(routes[j])[0]
		if weight[mi] != weight[mj] {
			return weight[mi] < weight[mj]
		}
		return routes[i] < routes[j]
	})
	for _, route := range routes {
		if _, ok := matrix[route]; !ok {
			t.Errorf("route %s has no entry in the authorization matrix", route)
		}
	}
	for route := range matrix {
		if _, ok := spec.Paths[strings.SplitN(route, " ", 2)[1]]; !ok {
			t.Errorf("matrix entry %s is not a documented route", route)
		}
	}

	for _, route := range routes {
		want, ok := matrix[route]
		if !ok || strings.HasPrefix(route, "POST /api/auth/") {
			continue // auth endpoints are covered by their own tests
		}
		method, path := strings.Fields(route)[0], strings.Fields(route)[1]
		for _, p := range principals {
			target := resolve(path)
			if route == "DELETE /api/v1/managed/hosts/{id}" {
				target += "?revision=1"
			}
			if route == "DELETE /api/v1/members/{id}" {
				target = fmt.Sprintf("/api/v1/members/%d", removableID)
			}
			status, body := c.call(method, target, p.token, bodies[route])
			denied := status == http.StatusUnauthorized || status == http.StatusForbidden
			switch {
			case p.allowed(want) && denied:
				t.Errorf("%s as %s: denied with %d %s, want access", route, p.name, status, body)
			case !p.allowed(want) && p.token == "" && status != http.StatusUnauthorized:
				t.Errorf("%s as %s: %d, want 401", route, p.name, status)
			case !p.allowed(want) && p.token != "" && status != http.StatusForbidden:
				t.Errorf("%s as %s: %d %s, want 403", route, p.name, status, body)
			}
		}
	}
}
