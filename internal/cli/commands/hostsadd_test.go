package commands_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/commands"
)

// addScenario scripts what the fake server says about the new host: each read
// of the host serves the next stage, the last one stays. After a trust request
// the host is ready (or afterTrust).
type addScenario struct {
	stages      []string // pending, key, ready, noconnect
	afterTrust  string
	duplicate   bool
	failProfile bool
}

const addProfiles = `{"items":[{"id":"prof-1","name":"agents","currentRevision":1},{"id":"prof-2","name":"deploy","currentRevision":2},{"id":"prof-3","name":"lab","currentRevision":1}]}`

func addServer(t *testing.T, scenario addScenario) *apiServer {
	t.Helper()
	var mu sync.Mutex
	reads, trusted := 0, false

	hostJSON := func(stage string) string {
		const meta = `"metadata":{"id":"h9","name":"web-01","generation":3,"revision":1},`
		trust := ""
		if trusted {
			trust = `,"trust":["` + keyA + `"]`
		}
		spec := `"spec":{"accessProfileId":"prof-2","accessTarget":{"endpoint":"10.0.0.5:22"` + trust + `}},`
		observed := `"observedHostKeys":[{"algorithm":"ssh-ed25519","fingerprint":"` + keyA + `"}]`
		switch stage {
		case "key":
			return `{` + meta + spec + `"status":{"phase":"Active","conditions":[{"type":"Ready","status":"False","reason":"HostKeyPendingApproval"}],` + observed + `}}`
		case "ready":
			return `{` + meta + spec + `"status":{"phase":"Active","conditions":[{"type":"Ready","status":"True","reason":"Ready"}],` + observed + `}}`
		case "noconnect":
			return `{` + meta + spec + `"status":{"phase":"Active","conditions":[{"type":"Ready","status":"False","reason":"ConnectionFailed","message":"dial tcp 10.0.0.5:22: connection refused"}]}}`
		default:
			return `{` + meta + spec + `"status":{"phase":"Pending"}}`
		}
	}
	write := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}

	return newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/managed/access-profiles":
			write(w, 200, addProfiles)
		case "GET /api/v1/managed/access-profiles/prof-1":
			write(w, 200, `{"id":"prof-1","name":"agents","currentRevision":1,"spec":{"connectorKind":"agent"}}`)
		case "GET /api/v1/managed/access-profiles/prof-2":
			write(w, 200, `{"id":"prof-2","name":"deploy","currentRevision":2,"spec":{"connectorKind":"ssh","username":"deploy","authenticationMethod":"private-key","hostKeyPolicy":"approve"}}`)
		case "GET /api/v1/managed/access-profiles/prof-3":
			write(w, 200, `{"id":"prof-3","name":"lab","currentRevision":1,"spec":{"connectorKind":"ssh","username":"lab","authenticationMethod":"private-key","hostKeyPolicy":"tofu"}}`)
		case "POST /api/v1/secrets":
			write(w, 201, `{"id":"sec-9","name":"x","currentVersion":1}`)
		case "DELETE /api/v1/secrets/sec-9":
			w.WriteHeader(http.StatusNoContent)
		case "POST /api/v1/managed/access-profiles":
			if scenario.failProfile {
				write(w, 409, `{"error":"name taken","code":"conflict"}`)
				return
			}
			write(w, 201, `{"id":"prof-2","name":"deploy-key","currentRevision":1}`)
		case "POST /api/v1/managed/hosts":
			if scenario.duplicate {
				write(w, 409, `{"error":"already exists","code":"already_exists"}`)
				return
			}
			write(w, 201, hostJSON("pending"))
		case "GET /api/v1/managed/hosts/h9":
			stage := "pending"
			if len(scenario.stages) > 0 {
				stage = scenario.stages[min(reads, len(scenario.stages)-1)]
			}
			reads++
			if trusted {
				stage = "ready"
				if scenario.afterTrust != "" {
					stage = scenario.afterTrust
				}
			}
			write(w, 200, hostJSON(stage))
		case "POST /api/v1/managed/hosts/h9/trust":
			trusted = true
			write(w, 200, hostJSON("ready"))
		default:
			write(w, 404, `{"error":"no route","code":"not_found"}`)
		}
	})
}

func addRun(t *testing.T, server *apiServer, terminal bool, stdin string, args ...string) result {
	t.Helper()
	isolate(t)
	t.Cleanup(commands.SetHostsAddPoll(time.Millisecond))
	cfg := writeConfig(t, server.URL, "tok")

	var out, errOut bytes.Buffer
	code := cli.Run(context.Background(), cli.Options{
		Streams:    cli.IOStreams{In: strings.NewReader(stdin), Out: &out, ErrOut: &errOut},
		Groups:     commands.DefaultGroups(),
		IsTerminal: func(io.Reader) bool { return terminal },
	}, append([]string{"--config", cfg}, args...))

	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

func (s *apiServer) countOf(method, path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if r.Method == method && r.Path == path {
			n++
		}
	}

	return n
}

func (s *apiServer) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.requests)
}

const (
	hostsPath = "/api/v1/managed/hosts"
	trustPath = "/api/v1/managed/hosts/h9/trust"
)

// H-01 and B5: a script registers the host and trusts nothing, --yes included.
func TestHostsAddScriptLeavesTheKeyUntrusted(t *testing.T) {
	for _, extra := range [][]string{nil, {"--yes"}} {
		server := addServer(t, addScenario{stages: []string{"pending", "key"}})
		args := append([]string{"hosts", "add", "web-01", "10.0.0.5", "--access", "deploy", "--no-input", "--tag", "tier=web"}, extra...)
		got := addRun(t, server, false, "", args...)
		if got.code != 0 || server.countOf(http.MethodPost, trustPath) != 0 {
			t.Fatalf("%v = %+v", extra, got)
		}
		if !strings.Contains(got.stdout, "waiting for key approval") || !strings.Contains(got.stdout, "hosts trust web-01 --fingerprint "+keyA) {
			t.Fatalf("output = %+v", got)
		}
		body := server.body(http.MethodPost, hostsPath)
		for _, want := range []string{`"name":"web-01"`, `"endpoint":"10.0.0.5:22"`, `"accessProfileId":"prof-2"`, `"tier":"web"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("host body lacks %s: %s", want, body)
			}
		}
	}
}

// H-02: --trust-fingerprint equal to an observed key trusts it and waits for ready.
func TestHostsAddTrustsAPinnedFingerprint(t *testing.T) {
	server := addServer(t, addScenario{stages: []string{"pending", "key"}})
	got := addRun(t, server, false, "", "hosts", "add", "web-01", "10.0.0.5:22", "--access", "deploy", "--no-input", "--trust-fingerprint", keyA)
	if got.code != 0 || !strings.Contains(server.body(http.MethodPost, trustPath), keyA) || !strings.Contains(got.stdout, "is ready") {
		t.Fatalf("pinned = %+v", got)
	}
}

// H-03: a fingerprint the server did not see is refused and says what it saw.
func TestHostsAddRefusesAnUnseenFingerprint(t *testing.T) {
	server := addServer(t, addScenario{stages: []string{"key"}})
	got := addRun(t, server, false, "", "hosts", "add", "web-01", "10.0.0.5", "--access", "deploy", "--no-input", "--trust-fingerprint", keyB)
	if got.code != clierr.ExitUsage || server.countOf(http.MethodPost, trustPath) != 0 || !strings.Contains(got.stderr, keyA) {
		t.Fatalf("unseen = %+v", got)
	}
}

// H-04: with the lab policy nothing is asked and nothing is trusted by the CLI.
func TestHostsAddTofuAsksNothing(t *testing.T) {
	server := addServer(t, addScenario{stages: []string{"pending", "ready"}})
	got := addRun(t, server, true, "", "hosts", "add", "web-01", "10.0.0.5", "--access", "lab")
	if got.code != 0 || server.countOf(http.MethodPost, trustPath) != 0 || !strings.Contains(got.stdout, "is ready") ||
		!strings.Contains(got.stderr, "lab setting") || strings.Contains(got.stderr, "Type the first") {
		t.Fatalf("tofu = %+v", got)
	}
}

// S-06: on a terminal the key is trusted only after yes is typed.
func TestHostsAddWizardTypedYes(t *testing.T) {
	server := addServer(t, addScenario{stages: []string{"pending", "key"}})
	got := addRun(t, server, true, "web-01\n10.0.0.5\n3\nyes\n", "hosts", "add")
	if got.code != 0 || !strings.Contains(server.body(http.MethodPost, trustPath), keyA) || !strings.Contains(got.stdout, "is ready") {
		t.Fatalf("wizard = %+v", got)
	}
	// The wizard says how to do the same without questions, and that command is the same request.
	want := "hosts add web-01 10.0.0.5:22 --access deploy --no-input"
	if !strings.Contains(got.stderr, want) {
		t.Fatalf("twin missing from %q", got.stderr)
	}
	twin := addServer(t, addScenario{stages: []string{"pending", "key"}})
	again := addRun(t, twin, false, "", strings.Fields(want)...)
	if again.code != 0 || twin.body(http.MethodPost, hostsPath) != server.body(http.MethodPost, hostsPath) {
		t.Fatalf("twin = %+v\n%s\n%s", again, twin.body(http.MethodPost, hostsPath), server.body(http.MethodPost, hostsPath))
	}
}

// H-05 and S-07: Enter, "no" and end of input skip; the --yes flag never trusts.
func TestHostsAddWizardNeverTrustsWithoutYes(t *testing.T) {
	for name, answer := range map[string]string{"enter": "\n", "no": "no\n", "closed": ""} {
		server := addServer(t, addScenario{stages: []string{"key"}})
		got := addRun(t, server, true, "web-01\n10.0.0.5\n3\n"+answer, "--yes", "hosts", "add")
		if server.countOf(http.MethodPost, trustPath) != 0 {
			t.Errorf("%s: trusted: %+v", name, got)
		}
		if name == "enter" && (got.code != 0 || !strings.Contains(got.stdout, "waiting for key approval")) {
			t.Errorf("enter = %+v", got)
		}
	}
}

// H-06: "back" returns to the previous question and nothing is sent before the end.
func TestHostsAddWizardBack(t *testing.T) {
	server := addServer(t, addScenario{stages: []string{"key"}})
	got := addRun(t, server, true, "web-01\n10.0.0.5\nback\n10.0.0.9:2222\n3\n\n", "hosts", "add")
	if got.code != 0 || server.countOf(http.MethodPost, hostsPath) != 1 || !strings.Contains(server.body(http.MethodPost, hostsPath), `"endpoint":"10.0.0.9:2222"`) {
		t.Fatalf("back = %+v", got)
	}

	// Back at the first question stops with no request that changes anything.
	server = addServer(t, addScenario{})
	got = addRun(t, server, true, "back\n", "hosts", "add")
	if got.code == 0 || server.countOf(http.MethodPost, hostsPath) != 0 || server.countOf(http.MethodPost, "/api/v1/secrets") != 0 {
		t.Fatalf("back at the start = %+v", got)
	}

	// Back over a question that was answered by a flag skips it.
	server = addServer(t, addScenario{stages: []string{"key"}})
	got = addRun(t, server, true, "10.0.0.5\nback\nback\n", "hosts", "add", "web-01")
	if got.code == 0 || server.countOf(http.MethodPost, hostsPath) != 0 {
		t.Fatalf("back over a given answer = %+v", got)
	}
}

// The key is stored as a secret and never printed; a failed entry creates no host.
func TestHostsAddCreatesAccessWithAKey(t *testing.T) {
	server := addServer(t, addScenario{stages: []string{"key"}})
	key := writeKey(t, testPrivateKey)
	got := addRun(t, server, false, "", "hosts", "add", "web-01", "10.0.0.5", "--user", "deploy", "--key-file", key, "--no-input")
	if got.code != 0 || strings.Contains(got.stdout+got.stderr, "SUPERSECRETKEYBODY") ||
		!strings.Contains(server.body(http.MethodPost, "/api/v1/secrets"), "SUPERSECRETKEYBODY") {
		t.Fatalf("key = %+v", got)
	}
	profile := server.body(http.MethodPost, "/api/v1/managed/access-profiles")
	for _, want := range []string{`"name":"deploy-key"`, `"private-key"`, `"approve"`, `"sec-9"`} {
		if !strings.Contains(profile, want) {
			t.Fatalf("profile lacks %s: %s", want, profile)
		}
	}

	server = addServer(t, addScenario{failProfile: true})
	got = addRun(t, server, false, "", "hosts", "add", "web-01", "10.0.0.5", "--user", "deploy", "--key-file", key, "--no-input")
	if got.code == 0 || server.countOf(http.MethodPost, hostsPath) != 0 || server.countOf(http.MethodDelete, "/api/v1/secrets/sec-9") != 1 {
		t.Fatalf("failed entry = %+v", got)
	}
}

func TestHostsAddPasswordFromStdin(t *testing.T) {
	server := addServer(t, addScenario{stages: []string{"key"}})
	got := addRun(t, server, false, "s3cretpw\n", "hosts", "add", "web-01", "10.0.0.5", "--user", "ops", "--password-stdin", "--no-input")
	if got.code != 0 || strings.Contains(got.stdout+got.stderr, "s3cretpw") || !strings.Contains(server.body(http.MethodPost, "/api/v1/secrets"), "s3cretpw") ||
		!strings.Contains(server.body(http.MethodPost, "/api/v1/managed/access-profiles"), `"name":"ops-password"`) {
		t.Fatalf("password = %+v", got)
	}
}

// O-03 and the other local refusals: no request at all.
func TestHostsAddRefusesBadInputLocally(t *testing.T) {
	cases := map[string][]string{
		"address":         {"hosts", "add", "web-01", "bad address", "--access", "deploy"},
		"port":            {"hosts", "add", "web-01", "10.0.0.5:99999", "--access", "deploy"},
		"name":            {"hosts", "add", "web 01", "10.0.0.5", "--access", "deploy"},
		"tag":             {"hosts", "add", "web-01", "10.0.0.5", "--access", "deploy", "--tag", "bad key=1"},
		"tag removal":     {"hosts", "add", "web-01", "10.0.0.5", "--access", "deploy", "--tag", "tier-"},
		"fingerprint":     {"hosts", "add", "web-01", "10.0.0.5", "--access", "deploy", "--trust-fingerprint", "abc"},
		"access and user": {"hosts", "add", "web-01", "10.0.0.5", "--access", "deploy", "--user", "x"},
		"agent entry":     {"hosts", "add", "web-01", "10.0.0.5", "--access", "agents"},
		"missing access":  {"hosts", "add", "web-01", "10.0.0.5", "--no-input"},
		"missing name":    {"hosts", "add", "--no-input", "--access", "deploy"},
	}
	for name, args := range cases {
		server := addServer(t, addScenario{})
		got := addRun(t, server, false, "", args...)
		if got.code != clierr.ExitUsage || server.countOf(http.MethodPost, hostsPath) != 0 || server.countOf(http.MethodPost, "/api/v1/secrets") != 0 {
			t.Errorf("%s = %+v", name, got)
		}
	}
	// The env of a new host cannot be set yet (C-01): refused before any request.
	server := addServer(t, addScenario{})
	if got := addRun(t, server, false, "", "hosts", "add", "web-01", "10.0.0.5", "--access", "deploy", "--env", "prod"); got.code == 0 || server.total() != 0 {
		t.Fatalf("--env = %+v, %d requests", got, server.total())
	}
}

// O-05: a duplicate name is a conflict that points at the existing host.
func TestHostsAddDuplicate(t *testing.T) {
	server := addServer(t, addScenario{duplicate: true})
	got := addRun(t, server, false, "", "hosts", "add", "web-01", "10.0.0.5", "--access", "deploy", "--no-input")
	if got.code != clierr.ExitConflict || !strings.Contains(got.stderr, "already exists") || !strings.Contains(got.stderr, "hosts show web-01") {
		t.Fatalf("duplicate = %+v", got)
	}
}

// O-04: an unreachable host is registered and the command says why, exit 0.
func TestHostsAddCannotConnect(t *testing.T) {
	server := addServer(t, addScenario{stages: []string{"pending", "noconnect"}})
	got := addRun(t, server, false, "", "hosts", "add", "web-01", "10.0.0.5", "--access", "lab", "--no-input")
	if got.code != 0 || !strings.Contains(got.stdout, "cannot connect") || !strings.Contains(got.stdout, "connection refused") ||
		!strings.Contains(got.stdout, "hosts show web-01") {
		t.Fatalf("unreachable = %+v", got)
	}
}

// The wait ends at --wait and --wait 0 does not wait at all.
func TestHostsAddWaitLimit(t *testing.T) {
	server := addServer(t, addScenario{stages: []string{"pending"}})
	got := addRun(t, server, false, "", "hosts", "add", "web-01", "10.0.0.5", "--access", "deploy", "--no-input", "--wait", "30ms")
	if got.code != 0 || !strings.Contains(got.stdout, "not ready yet") {
		t.Fatalf("limit = %+v", got)
	}
	server = addServer(t, addScenario{stages: []string{"pending"}})
	got = addRun(t, server, false, "", "hosts", "add", "web-01", "10.0.0.5", "--access", "deploy", "--no-input", "--wait", "0")
	if got.code != 0 || server.countOf(http.MethodGet, hostsPath+"/h9") != 0 {
		t.Fatalf("no wait = %+v", got)
	}
}

func TestHostsAddJSON(t *testing.T) {
	server := addServer(t, addScenario{stages: []string{"key"}})
	got := addRun(t, server, false, "", "hosts", "add", "web-01", "10.0.0.5", "--access", "deploy", "--no-input", "-o", "json")
	if got.code != 0 || !strings.Contains(got.stdout, `"metadata"`) || !strings.Contains(got.stdout, `"observedHostKeys"`) {
		t.Fatalf("json = %+v", got)
	}
}
