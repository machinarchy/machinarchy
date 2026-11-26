package commands_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/commands"
	"github.com/machinarchy/machinarchy/internal/cli/config"
	"github.com/machinarchy/machinarchy/internal/cli/cred"
)

// homeHost is one host of a home fixture: the Ready reason decides its state.
type homeHost struct {
	name, reason string
	ready        bool
	labels       string
}

func homeHostJSON(h homeHost) string {
	status := "False"
	if h.ready {
		status = "True"
	}
	labels := h.labels
	if labels == "" {
		labels = "{}"
	}

	return fmt.Sprintf(`{"metadata":{"id":"id-%s","name":%q,"labels":%s,"revision":1},"spec":{},"status":{"phase":"Active","conditions":[{"type":"Ready","status":%q,"reason":%q}]}}`,
		h.name, h.name, labels, status, h.reason)
}

// homeServer answers the three requests of the home screen.
func homeServer(t *testing.T, hosts []homeHost, runs string, fail map[string]int) *apiServer {
	t.Helper()
	var items []string
	for _, h := range hosts {
		items = append(items, homeHostJSON(h))
	}
	routes := map[string]string{
		"/api/v1/me":                 `{"id":1,"username":"maxim","email":"maxim@example.com"}`,
		"/api/v1/environments":       `[{"id":1,"name":"production"}]`,
		"/api/v1/managed/hosts":      `{"items":[` + strings.Join(items, ",") + `]}`,
		"/api/v1/managed/operations": runs,
	}

	return newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if status := fail[r.URL.Path]; status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"boom"}`))

			return
		}
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)

			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

func twoRuns(now time.Time) string {
	return fmt.Sprintf(`{"items":[`+
		`{"id":"r-4k2p9x1a","state":"Succeeded","createdAt":%q,"targets":{"Succeeded":14}},`+
		`{"id":"r-8d1q0b7c","state":"Failed","createdAt":%q,"targets":{"Succeeded":11,"Failed":3}}]}`,
		now.Add(-2*time.Minute).Format(time.RFC3339), now.Add(-time.Hour).Format(time.RFC3339))
}

// runHome runs the bare command with the home screen wired in.
func runHome(t *testing.T, path string, env map[string]string, args ...string) result {
	t.Helper()
	isolate(t)
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "en_US.UTF-8")
	for k, v := range env {
		t.Setenv(k, v)
	}
	var out, errOut bytes.Buffer
	code := cli.Run(context.Background(), cli.Options{
		Streams:    cli.IOStreams{In: strings.NewReader(""), Out: &out, ErrOut: &errOut},
		Groups:     commands.DefaultGroups(),
		Home:       commands.Home,
		IsTerminal: func(io.Reader) bool { return false },
	}, append([]string{"--config", path}, args...))

	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

func sessionConfig(t *testing.T, url string, expires *time.Time) string {
	t.Helper()

	return seed(t, url, cred.Credential{Kind: cred.KindSession, Secret: "sess", User: "maxim", ExpiresAt: expires})
}

func inHours(h int) *time.Time { v := time.Now().Add(time.Duration(h) * time.Hour); return &v }

func ready(name string) homeHost { return homeHost{name: name, ready: true, reason: "Ready"} }

func TestHomeNotSignedIn(t *testing.T) {
	path := configPath(t)
	got := runHome(t, path, nil)
	if got.code != clierr.ExitAuth || got.stderr != "" {
		t.Fatalf("result = %+v", got)
	}
	want := "Not signed in.\n  Another machine will not manage itself.\n  Next:  machinarchy login\n"
	if got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}

	plain := runHome(t, path, nil, "--plain")
	if strings.Contains(plain.stdout, "Another machine") || !strings.Contains(plain.stdout, "Next:  machinarchy login") {
		t.Errorf("--plain must drop the flavour line:\n%s", plain.stdout)
	}
}

func TestHomeExpired(t *testing.T) {
	t.Run("saved session past its end", func(t *testing.T) {
		server := homeServer(t, nil, `{"items":[]}`, nil)
		past := time.Now().Add(-time.Hour)
		got := runHome(t, sessionConfig(t, server.URL, &past), nil)
		if got.code != clierr.ExitAuth || !strings.Contains(got.stdout, "Your session on lab expired at ") ||
			!strings.Contains(got.stdout, "Next:  machinarchy login") || got.stderr != "" || strings.Contains(got.stdout, "HTTP") {
			t.Fatalf("result = %+v", got)
		}
		if server.count() != 0 {
			t.Errorf("an expired credential must not be sent (%d requests)", server.count())
		}
	})

	t.Run("server refuses the credential", func(t *testing.T) {
		server := homeServer(t, nil, `{"items":[]}`, map[string]int{"/api/v1/me": http.StatusUnauthorized})
		got := runHome(t, sessionConfig(t, server.URL, inHours(5)), nil)
		if got.code != clierr.ExitAuth || !strings.Contains(got.stdout, "did not accept") || !strings.Contains(got.stdout, "Next:  machinarchy login") ||
			strings.Contains(got.stdout, "HTTP") || strings.Contains(got.stdout, "boom") {
			t.Fatalf("result = %+v", got)
		}
	})
}

func TestHomeUnreachable(t *testing.T) {
	server := homeServer(t, nil, `{"items":[]}`, nil)
	path := sessionConfig(t, server.URL, inHours(5))
	server.Close()
	got := runHome(t, path, nil)
	if got.code != clierr.ExitUnavailable || !strings.Contains(got.stdout, "Cannot reach the server") ||
		!strings.Contains(got.stdout, "Next:  check the address, or switch server: machinarchy switch") || !strings.Contains(got.stdout, "lab (") {
		t.Fatalf("result = %+v", got)
	}
}

func TestHomeHealthy(t *testing.T) {
	server := homeServer(t, []homeHost{ready("a"), {name: "b", ready: true, reason: "Ready", labels: `{"tier":"web"}`}}, twoRuns(time.Now()), nil)
	got := runHome(t, sessionConfig(t, server.URL, inHours(21)), nil)
	if got.code != 0 || got.stderr != "" {
		t.Fatalf("result = %+v", got)
	}
	for _, want := range []string{"machinarchy · lab · none", "maxim (owner)", "Session ends in ", "Hosts      2 ready\n",
		"Last runs  r-4k2p9x1a  ✔ 14 ok", "2 min ago", "r-8d1q0b7c  ⚠ 11 ok, 3 failed", "1 h ago",
		"  Next:  machinarchy run --on tier=web -- uptime\n"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("home is missing %q:\n%s", want, got.stdout)
		}
	}
	if strings.Contains(got.stdout, "\x1b") {
		t.Errorf("a pipe must get no colour codes:\n%q", got.stdout)
	}
	if server.count() != 3 { // me, hosts, operations; no env is set, so no environments request
		t.Errorf("requests = %d, want 3", server.count())
	}
}

func TestHomeSuggestionPriority(t *testing.T) {
	cases := []struct {
		name  string
		hosts []homeHost
		next  string
		state string
	}{
		{"key changed first", []homeHost{{name: "w1", reason: "HostKeyPendingApproval"}, {name: "w2", reason: "HostKeyChanged"}, {name: "w3", reason: "ConnectionFailed"}}, "machinarchy hosts show w2", "attention"},
		{"then waiting for key", []homeHost{{name: "w3", reason: "ConnectionFailed"}, {name: "w1", reason: "HostKeyPendingApproval"}, ready("ok")}, "machinarchy hosts trust w1", "attention"},
		{"then cannot connect", []homeHost{ready("ok"), {name: "w3", reason: "ConnectionFailed"}}, "machinarchy hosts show w3", "attention"},
		{"no hosts", nil, "machinarchy hosts add", "no_hosts"},
		{"healthy, no tags", []homeHost{ready("a")}, "machinarchy run --on all -- uptime", "ok"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := homeServer(t, c.hosts, `{"items":[]}`, nil)
			path := sessionConfig(t, server.URL, inHours(5))
			got := runHome(t, path, nil)
			if got.code != 0 || !strings.HasSuffix(got.stdout, "  Next:  "+c.next+"\n") {
				t.Fatalf("result = %+v\nwant next %q", got, c.next)
			}
			var doc map[string]any
			js := runHome(t, path, nil, "-o", "json")
			if err := json.Unmarshal([]byte(js.stdout), &doc); err != nil || doc["state"] != c.state || doc["next"] != c.next {
				t.Fatalf("json = %s (%v)", js.stdout, err)
			}
		})
	}
}

func TestHomeAttentionScreen(t *testing.T) {
	hosts := []homeHost{ready("a"), {name: "web-14", reason: "HostKeyPendingApproval"}, {name: "web-09", reason: "ConnectionFailed"}}
	server := homeServer(t, hosts, `{"items":[]}`, nil)
	got := runHome(t, sessionConfig(t, server.URL, nil), nil)
	for _, want := range []string{"Hosts      1 ready · 1 waiting for key approval · 1 cannot connect\n",
		"           web-14  waiting for key approval\n", "           web-09  cannot connect\n", "Last runs  none yet\n"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("missing %q:\n%s", want, got.stdout)
		}
	}
}

func TestHomeNoHostsScreen(t *testing.T) {
	server := homeServer(t, nil, `{"items":[]}`, nil)
	got := runHome(t, sessionConfig(t, server.URL, nil), nil)
	if !strings.Contains(got.stdout, "\nYou have no machines yet.\n") || !strings.HasSuffix(got.stdout, "  Next:  machinarchy hosts add\n") {
		t.Fatalf("result = %+v", got)
	}
}

func TestHomeDegradesOneLine(t *testing.T) {
	t.Run("hosts fail", func(t *testing.T) {
		server := homeServer(t, nil, twoRuns(time.Now()), map[string]int{"/api/v1/managed/hosts": http.StatusInternalServerError})
		got := runHome(t, sessionConfig(t, server.URL, nil), nil)
		if got.code != 0 || !strings.Contains(got.stdout, "Hosts      ⚠ not available:") || !strings.Contains(got.stdout, "Last runs  r-4k2p9x1a") ||
			!strings.HasSuffix(got.stdout, "  Next:  machinarchy hosts\n") {
			t.Fatalf("result = %+v", got)
		}
	})
	t.Run("runs fail", func(t *testing.T) {
		server := homeServer(t, []homeHost{ready("a")}, "", map[string]int{"/api/v1/managed/operations": http.StatusInternalServerError})
		got := runHome(t, sessionConfig(t, server.URL, nil), nil)
		if got.code != 0 || !strings.Contains(got.stdout, "Hosts      1 ready") || !strings.Contains(got.stdout, "Last runs  ⚠ not available:") {
			t.Fatalf("result = %+v", got)
		}
	})
}

func TestHomeEnvironmentScope(t *testing.T) {
	var hostQuery string
	server := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/me":
			_, _ = w.Write([]byte(`{"id":1,"username":"maxim"}`))
		case "/api/v1/environments":
			_, _ = w.Write([]byte(`[{"id":7,"name":"production"}]`))
		case "/api/v1/managed/hosts":
			hostQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"items":[` + homeHostJSON(ready("a")) + `]}`))
		default:
			_, _ = w.Write([]byte(`{"items":[]}`))
		}
	})
	got := runHome(t, sessionConfig(t, server.URL, nil), nil, "--env", "production")
	if got.code != 0 || !strings.Contains(got.stdout, "machinarchy · lab · production") {
		t.Fatalf("result = %+v", got)
	}
	if !strings.Contains(hostQuery, "environment=7") || !strings.Contains(hostQuery, "limit=500") {
		t.Errorf("the host request was not scoped to the env: %q", hostQuery)
	}
}

func TestHomePlainIsASCII(t *testing.T) {
	hosts := []homeHost{ready("a"), {name: "web-14", reason: "HostKeyPendingApproval"}}
	server := homeServer(t, hosts, twoRuns(time.Now()), nil)
	path := sessionConfig(t, server.URL, inHours(5))
	for _, args := range [][]string{{"--plain"}, nil} {
		env := map[string]string{"LANG": "C", "LC_ALL": "C"}
		got := runHome(t, path, env, args...)
		for _, r := range got.stdout {
			if r > 127 || r == '\r' || r == 0x1b {
				t.Fatalf("args %v: non-ASCII or control %q in:\n%s", args, r, got.stdout)
			}
		}
	}
}

func TestHomeJSONHasNoSecrets(t *testing.T) {
	server := homeServer(t, []homeHost{ready("a")}, twoRuns(time.Now()), nil)
	path := sessionConfig(t, server.URL, inHours(5))
	got := runHome(t, path, nil, "-o", "json")
	var doc map[string]any
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil || doc["state"] != "ok" || doc["user"] != "maxim" || doc["role"] != "owner" {
		t.Fatalf("json = %s (%v)", got.stdout, err)
	}
	if strings.Contains(got.stdout, "sess") && strings.Contains(strings.ToLower(got.stdout), "secret") {
		t.Errorf("document leaks a credential: %s", got.stdout)
	}
	saved, _ := os.ReadFile(config.CredentialsPath(path))
	if strings.Contains(got.stdout, `"sess"`) || len(saved) == 0 {
		t.Errorf("the saved secret must never be printed")
	}
}

func TestHomeSanitisesHostNames(t *testing.T) {
	hosts := []homeHost{{name: "web\x1b[31m-1", reason: "ConnectionFailed"}}
	server := homeServer(t, hosts, `{"items":[]}`, nil)
	got := runHome(t, sessionConfig(t, server.URL, nil), nil)
	if strings.Contains(got.stdout, "\x1b") {
		t.Errorf("a host name reached the terminal unsanitised: %q", got.stdout)
	}
}

func TestHomeKeepsUsageErrorsAndHelp(t *testing.T) {
	path := configPath(t)
	if got := runHome(t, path, nil, "lst"); got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "unknown command") {
		t.Errorf("a typo must stay a usage error: %+v", got)
	}
	if got := runHome(t, path, nil, "--help"); got.code != 0 || !strings.Contains(got.stdout, "Usage:") {
		t.Errorf("--help must still print help: %+v", got)
	}
}

func TestHomeSlowRequestGivesUpAfterTheBudget(t *testing.T) {
	defer commands.SetHomeBudget(50 * time.Millisecond)()
	server := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/me":
			_, _ = w.Write([]byte(`{"id":1,"username":"maxim"}`))
		case "/api/v1/managed/operations":
			_, _ = w.Write([]byte(`{"items":[]}`))
		default:
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
		}
	})
	start := time.Now()
	got := runHome(t, sessionConfig(t, server.URL, nil), nil)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("the screen waited %v for a slow request", elapsed)
	}
	if got.code != 0 || !strings.Contains(got.stdout, "Hosts      ⚠ not available: no answer within 50ms") || !strings.Contains(got.stdout, "Last runs  none yet") {
		t.Fatalf("result = %+v", got)
	}
}
