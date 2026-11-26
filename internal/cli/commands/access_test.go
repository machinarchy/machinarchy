package commands_test

import (
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

// The old resource-verb commands were removed with no aliases (M3); their
// replacements are covered by the access, hosts and envs tests.
func TestOldResourceCommandsAreGone(t *testing.T) {
	server := accessServer(t)
	config := writeConfig(t, server.URL, "owner-session")

	for _, args := range [][]string{
		{"get", "hosts"}, {"get", "profiles"},
		{"create", "host", "x"}, {"create", "profile", "x"}, {"create", "agent-token"},
		{"delete", "host", "x"}, {"delete", "profile", "x"}, {"delete", "agent-token", "x"},
		{"label", "host", "x", "a=b"}, {"trust", "host", "x"},
	} {
		got := execute(t, nil, "", append([]string{"--config", config}, args...)...)
		if got.code != clierr.ExitUsage || got.stdout != "" {
			t.Errorf("%v: result = %+v, want a usage error", args, got)
		}
	}
	if n := server.count(); n != 0 {
		t.Errorf("the server received %d requests", n)
	}
}

// `get agent-tokens` stays: it is where the agent ID for `hosts revoke-agent` comes from.
func TestGetAgentTokensShowsWhoUsedThem(t *testing.T) {
	server := accessServer(t)
	config := writeConfig(t, server.URL, "owner-session")
	got := execute(t, nil, "", "--config", config, "get", "agent-tokens")
	if got.code != 0 || !strings.Contains(got.stdout, "used by agent-9") || strings.Contains(got.stdout, "mae_secret") {
		t.Fatalf("result %+v", got)
	}
}
