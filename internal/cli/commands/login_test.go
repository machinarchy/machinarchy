package commands_test

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

func TestLoginStoresTheSessionToken(t *testing.T) {
	var server *apiServer
	server = newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.Unmarshal(server.last(t).Body, &body) // the test server already read r.Body
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/auth/login" || body["username"] != "admin" || body["password"] != "correct horse" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"Invalid credentials","code":"unauthorized"}`))
			return
		}
		_, _ = w.Write([]byte(`{"token":"mch_session_new","user":{"username":"admin"}}`))
	})

	config := writeConfig(t, server.URL, "old-token")
	got := execute(t, nil, "correct horse\n", "--config", config, "login", "--username", "admin", "--password-stdin")
	if got.code != 0 || !strings.Contains(got.stdout, "Logged in to") || strings.Contains(got.stdout+got.stderr, "correct horse") {
		t.Fatalf("login = %+v", got)
	}
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "mch_session_new") || strings.Contains(string(data), "old-token") || strings.Contains(string(data), "correct horse") {
		t.Fatalf("config after login:\n%s", data)
	}
	// The request carried no old bearer token.
	if auth := server.last(t).Auth; auth != "" {
		t.Errorf("login sent Authorization %q", auth)
	}

	wrong := execute(t, nil, "nope\n", "--config", writeConfig(t, server.URL, "old-token"), "login", "--username", "admin", "--password-stdin")
	if wrong.code != clierr.ExitAuth || !strings.Contains(wrong.stderr, "Invalid credentials") {
		t.Fatalf("wrong password = %+v", wrong)
	}
	noTTY := execute(t, nil, "", "--config", writeConfig(t, server.URL, "old-token"), "login", "--username", "admin")
	if noTTY.code != clierr.ExitUsage || !strings.Contains(noTTY.stderr, "--password-stdin") {
		t.Fatalf("no terminal = %+v", noTTY)
	}
	if missing := execute(t, nil, "", "--config", writeConfig(t, server.URL, "old-token"), "login"); missing.code != clierr.ExitUsage {
		t.Fatalf("no username = %+v", missing)
	}
}

func TestFirstLoginCreatesTheContext(t *testing.T) {
	server := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"mch_session_first","user":{"username":"admin"}}`))
	})
	config := t.TempDir() + "/config.yaml"

	noServer := execute(t, nil, "pw\n", "--config", config, "login", "--username", "admin", "--password-stdin")
	if noServer.code != clierr.ExitUsage || !strings.Contains(noServer.stderr, "--server") {
		t.Fatalf("first login without --server = %+v", noServer)
	}

	got := execute(t, nil, "pw\n", "--config", config, "--server", server.URL, "--context", "lab",
		"login", "--username", "admin", "--password-stdin")
	if got.code != 0 {
		t.Fatalf("first login = %+v", got)
	}
	// The new context is current and authenticates the next command.
	view := execute(t, nil, "", "--config", config, "config", "current-context")
	if view.code != 0 || strings.TrimSpace(view.stdout) != "lab" {
		t.Fatalf("current-context = %+v", view)
	}
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "mch_session_first") || !strings.Contains(string(data), server.URL) {
		t.Fatalf("config after first login:\n%s", data)
	}
}
