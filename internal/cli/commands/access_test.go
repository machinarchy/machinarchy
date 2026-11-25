package commands_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

// accessServer answers the routes the access commands use.
func accessServer(t *testing.T) *apiServer {
	t.Helper()
	routes := map[string]string{
		"GET /api/v1/secrets":                           `[{"id":"sec-1","name":"lab-key","purpose":"ssh-credential","currentVersion":3}]`,
		"POST /api/v1/managed/access-profiles":          `{"id":"prof-1","name":"p","currentRevision":1}`,
		"GET /api/v1/managed/access-profiles":           `{"items":[{"id":"prof-1","name":"agents","currentRevision":1}]}`,
		"DELETE /api/v1/managed/access-profiles/prof-1": ``,
		"POST /api/v1/tokens":                           `{"id":7,"name":"ci","role":"operator","createdAt":"2026-11-25T10:00:00Z","token":"mch_pat_secret"}`,
		"GET /api/v1/tokens":                            `[{"id":7,"name":"ci","role":"operator","createdAt":"2026-11-25T10:00:00Z"}]`,
		"DELETE /api/v1/tokens/7":                       ``,
		"GET /api/v1/managed/hosts":                     `{"items":[{"metadata":{"id":"host-1","name":"web-01","revision":1},"spec":{},"status":{}}]}`,
		"POST /api/v1/agents/enrollment-tokens":         `{"id":"tok-1","hostId":"host-1","createdAt":"2026-11-25T10:00:00Z","expiresAt":"2026-11-25T11:00:00Z","token":"mae_secret"}`,
		"GET /api/v1/agents/enrollment-tokens":          `{"items":[{"id":"tok-1","hostId":"host-1","createdAt":"2026-11-25T10:00:00Z","expiresAt":"2999-01-01T00:00:00Z","usedAt":"2026-11-25T10:01:00Z","usedByAgent":"agent-9"}]}`,
	}
	return newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no route","code":"not_found"}`))
			return
		}
		if body == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
		}
		_, _ = w.Write([]byte(body))
	})
}

func lastBody(t *testing.T, server *apiServer) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(server.last(t).Body, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestAccessCommands(t *testing.T) {
	server := accessServer(t)
	config := writeConfig(t, server.URL, "owner-session")

	t.Run("create profile agent", func(t *testing.T) {
		got := execute(t, nil, "", "--config", config, "create", "profile", "agents", "--connector", "agent")
		spec, _ := lastBody(t, server)["spec"].(map[string]any)
		if got.code != 0 || !strings.Contains(got.stdout, "prof-1") || spec["connectorKind"] != "agent" || spec["username"] != nil {
			t.Fatalf("result %+v, spec %v", got, spec)
		}
		if bad := execute(t, nil, "", "--config", config, "create", "profile", "x", "--connector", "agent", "--username", "root"); bad.code != clierr.ExitUsage {
			t.Fatalf("agent profile with a username = %+v", bad)
		}
	})

	t.Run("create profile ssh resolves the secret by name", func(t *testing.T) {
		got := execute(t, nil, "", "--config", config, "create", "profile", "lab", "--connector", "ssh", "--username", "testuser",
			"--secret", "lab-key", "--auth", "private-key", "--supervisor", "setsid")
		spec, _ := lastBody(t, server)["spec"].(map[string]any)
		credential, _ := spec["credential"].(map[string]any)
		if got.code != 0 || spec["username"] != "testuser" || spec["authenticationMethod"] != "private-key" ||
			credential["id"] != "sec-1" || credential["version"] != float64(3) {
			t.Fatalf("result %+v, spec %v", got, spec)
		}
		if missing := execute(t, nil, "", "--config", config, "create", "profile", "x", "--connector", "ssh", "--username", "u"); missing.code != clierr.ExitUsage {
			t.Fatalf("ssh profile without --secret = %+v", missing)
		}
		if unknown := execute(t, nil, "", "--config", config, "create", "profile", "x", "--connector", "ssh", "--username", "u", "--secret", "nope"); unknown.code != clierr.ExitNotFound {
			t.Fatalf("unknown secret = %+v", unknown)
		}
	})

	t.Run("create token shows the secret once; -q prints it alone", func(t *testing.T) {
		got := execute(t, nil, "", "--config", config, "create", "token", "ci", "--role", "operator", "--expires-hours", "720")
		if body := lastBody(t, server); got.code != 0 || body["role"] != "operator" || body["expiresInHours"] != float64(720) ||
			!strings.Contains(got.stdout, "shown only now") || !strings.HasSuffix(got.stdout, "mch_pat_secret\n") {
			t.Fatalf("result %+v, body %v", got, body)
		}
		if quiet := execute(t, nil, "", "--config", config, "create", "token", "ci", "--role", "operator", "-q"); quiet.stdout != "mch_pat_secret\n" {
			t.Fatalf("-q = %q", quiet.stdout)
		}
		if bad := execute(t, nil, "", "--config", config, "create", "token", "ci", "--role", "owner"); bad.code != clierr.ExitUsage {
			t.Fatalf("owner role = %+v", bad)
		}
	})

	t.Run("get tokens never shows secrets", func(t *testing.T) {
		got := execute(t, nil, "", "--config", config, "get", "tokens")
		if got.code != 0 || !strings.Contains(got.stdout, "ci") || strings.Contains(got.stdout, "mch_pat_secret") {
			t.Fatalf("result %+v", got)
		}
	})

	t.Run("create agent-token binds to the named host", func(t *testing.T) {
		got := execute(t, nil, "", "--config", config, "create", "agent-token", "--host", "web-01", "--hostname", "web-01.example")
		body := lastBody(t, server)
		if got.code != 0 || body["hostId"] != "host-1" || body["hostname"] != "web-01.example" || body["ttlSeconds"] != float64(3600) ||
			!strings.HasSuffix(got.stdout, "mae_secret\n") {
			t.Fatalf("result %+v, body %v", got, body)
		}
		if bad := execute(t, nil, "", "--config", config, "create", "agent-token"); bad.code != clierr.ExitUsage {
			t.Fatalf("no --host = %+v", bad)
		}
	})

	t.Run("get agent-tokens shows who used them", func(t *testing.T) {
		got := execute(t, nil, "", "--config", config, "get", "agent-tokens")
		if got.code != 0 || !strings.Contains(got.stdout, "used by agent-9") || strings.Contains(got.stdout, "mae_secret") {
			t.Fatalf("result %+v", got)
		}
	})

	t.Run("delete profile and token", func(t *testing.T) {
		if got := execute(t, nil, "", "--config", config, "delete", "profile", "agents", "--yes"); got.code != 0 || server.last(t).Method != http.MethodDelete {
			t.Fatalf("delete profile = %+v", got)
		}
		if got := execute(t, nil, "", "--config", config, "delete", "token", "7", "--yes"); got.code != 0 || server.last(t).Path != "/api/v1/tokens/7" {
			t.Fatalf("delete token = %+v", got)
		}
	})
}
