package commands_test

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// completions runs a shell-completion request and returns its candidates.
func completions(t *testing.T, config string, args ...string) []string {
	t.Helper()
	got := execute(t, nil, "", append([]string{"--config", config, "__complete"}, args...)...)
	if got.code != 0 || strings.Contains(got.stderr, "Error") {
		t.Fatalf("__complete %v = %+v", args, got)
	}
	var found []string
	for _, line := range dataLines(got.stdout) {
		if strings.HasPrefix(line, ":") {
			break
		}
		found = append(found, line)
	}

	return found
}

func TestCompletionOffersNamesFromTheServer(t *testing.T) {
	hosts := hostsGroupServer(t)
	config := writeConfig(t, hosts.URL, "tok")

	for _, command := range []string{"show", "tag", "move", "trust", "enroll", "revoke-agent", "remove"} {
		got := completions(t, config, "hosts", command, "web")
		if strings.Join(got, ",") != "web-01,web-02" {
			t.Errorf("hosts %s: %v", command, got)
		}
	}
	if got := completions(t, config, "hosts", "show", "web-01", ""); len(got) != 0 {
		t.Errorf("only the first argument is completed: %v", got)
	}
	if got := completions(t, config, "envs", "use", ""); strings.Join(got, ",") != "any,production,staging" {
		t.Errorf("envs use: %v", got)
	}
	if got := completions(t, config, "envs", "remove", "st"); strings.Join(got, ",") != "staging" {
		t.Errorf("envs remove: %v", got)
	}
	if got := completions(t, config, "hosts", "--env", "pro"); strings.Join(got, ",") != "production" {
		t.Errorf("--env: %v", got)
	}

	access := accessServer(t)
	config = writeConfig(t, access.URL, "tok")
	for _, args := range [][]string{{"access", "show", ""}, {"access", "remove", ""}, {"hosts", "add", "--access", ""}} {
		if got := completions(t, config, args...); strings.Join(got, ",") != "agents" {
			t.Errorf("%v: %v", args, got)
		}
	}
}

func TestCompletionNeverFailsLoudlyOrOffersUnsafeNames(t *testing.T) {
	t.Run("no server, no login", func(t *testing.T) {
		if got := completions(t, filepath.Join(t.TempDir(), "absent.yaml"), "hosts", "show", ""); len(got) != 0 {
			t.Errorf("offered %v", got)
		}
	})

	t.Run("a server error offers nothing", func(t *testing.T) {
		server := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
		if got := completions(t, writeConfig(t, server.URL, "tok"), "hosts", "show", ""); len(got) != 0 {
			t.Errorf("offered %v", got)
		}
	})

	t.Run("names with control characters or spaces are dropped", func(t *testing.T) {
		server := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[` +
				`{"metadata":{"id":"1","name":"ok-01","revision":1},"spec":{},"status":{}},` +
				`{"metadata":{"id":"2","name":"bad\u001b[31m","revision":1},"spec":{},"status":{}},` +
				`{"metadata":{"id":"3","name":"two words","revision":1},"spec":{},"status":{}}]}`))
		})
		got := completions(t, writeConfig(t, server.URL, "tok"), "hosts", "show", "")
		if strings.Join(got, ",") != "ok-01" {
			t.Errorf("offered %q", got)
		}
	})
}
