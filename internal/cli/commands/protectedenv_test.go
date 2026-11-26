package commands_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

// protectedServer is a server that holds environments as protected (C-02): it
// reports `protected` and refuses a guarded call without the env's name.
func protectedServer(t *testing.T) *apiServer {
	t.Helper()

	return newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/environments":
			_, _ = w.Write([]byte(`[{"id":1,"name":"production","description":"","protected":true},{"id":2,"name":"dev","description":"","protected":false}]`))
		case "POST /api/v1/environments":
			_, _ = w.Write([]byte(`{"id":3,"name":"vault","description":"","protected":true}`))
		case "GET /api/v1/managed/hosts":
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"id":"h1","name":"web-01","environmentId":1,"revision":3,"generation":1},"spec":{},"status":{}},` +
				`{"metadata":{"id":"h2","name":"db-01","environmentId":2,"revision":1,"generation":1},"spec":{},"status":{}}]}`))
		case "DELETE /api/v1/managed/hosts/h1":
			if !strings.Contains(r.URL.RawQuery, "confirmEnvironment=production") {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":"protected environment confirmation required: production","code":"protected_environment_confirmation_required"}`))

				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "DELETE /api/v1/managed/hosts/h2":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no route","code":"not_found"}`))
		}
	})
}

func TestServerHeldProtectionNeedsTheEnvNameAndIsSent(t *testing.T) {
	server := protectedServer(t)
	config := writeConfig(t, server.URL, "tok")

	// The guard comes from the server: nothing is configured on this computer.
	if got := execute(t, nil, "", "--config", config, "hosts", "remove", "web-01", "--confirm", "web-01"); got.code != clierr.ExitUsage ||
		server.countOf(http.MethodDelete, "/api/v1/managed/hosts/h1") != 0 {
		t.Fatalf("server-protected remove without --confirm-env = %+v", got)
	}
	if got := execute(t, nil, "", "--config", config, "hosts", "remove", "web-01", "--confirm", "web-01", "--confirm-env", "dev"); got.code != clierr.ExitUsage ||
		server.countOf(http.MethodDelete, "/api/v1/managed/hosts/h1") != 0 {
		t.Fatalf("wrong --confirm-env = %+v", got)
	}
	if got := execute(t, nil, "", "--config", config, "hosts", "remove", "web-01", "--confirm", "web-01", "--confirm-env", "production"); got.code != 0 ||
		server.countOf(http.MethodDelete, "/api/v1/managed/hosts/h1") != 1 {
		t.Fatalf("confirmed remove = %+v", got)
	}
	// Typed on a terminal, the env's name is sent too.
	if got := execute(t, nil, "", "--config", config, "hosts", "remove", "db-01", "--confirm", "db-01"); got.code != 0 {
		t.Fatalf("unprotected remove = %+v", got)
	}
}

func TestEnvsListShowsServerHeldProtection(t *testing.T) {
	server := protectedServer(t)
	config := writeConfig(t, server.URL, "tok")
	got := execute(t, nil, "", "--config", config, "envs")
	if got.code != 0 || !strings.Contains(got.stdout, "yes") || strings.Contains(got.stdout, "(local)") {
		t.Fatalf("envs = %+v", got)
	}
	if json := execute(t, nil, "", "--config", config, "envs", "-o", "json"); !strings.Contains(json.stdout, `"protected": true`) {
		t.Fatalf("envs -o json = %+v", json)
	}
}

func TestEnvsAddProtectedIsHeldByANewServer(t *testing.T) {
	server := protectedServer(t)
	config := writeConfig(t, server.URL, "tok")
	// No local-guard consent is needed: the server enforces it.
	got := execute(t, nil, "", "--config", config, "envs", "add", "vault", "--protected")
	if got.code != 0 || !strings.Contains(string(server.lastOf(http.MethodPost, "/api/v1/environments").Body), `"protected":true`) ||
		strings.Contains(readFile(t, config), "protected-envs") || strings.Contains(got.stdout, "local guard") {
		t.Fatalf("envs add --protected = %+v", got)
	}
}
