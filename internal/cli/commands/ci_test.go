package commands_test

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

// mutating lists every command that changes something on the server. The
// sweeps below run each of them the way a script would.
var mutating = [][]string{
	{"access", "add", "lab", "--how", "agent"},
	{"access", "remove", "deploy", "--confirm", "deploy"},
	{"hosts", "add", "web-01", "10.0.0.5"},
	{"hosts", "import", "fleet.yaml", "--yes"},
	{"hosts", "remove", "web-01", "--confirm", "web-01"},
	{"hosts", "tag", "web-01", "tier=web"},
	{"hosts", "move", "web-01", "--env", "staging"},
	{"hosts", "trust", "web-01", "--fingerprint", "SHA256:abc"},
	{"hosts", "enroll", "web-01"},
	{"hosts", "revoke-agent", "web-01", "--agent-id", "a1", "--yes"},
	{"envs", "add", "qa"},
	{"envs", "remove", "qa", "--confirm", "qa"},
	{"tokens", "create", "ci", "--role", "reader"},
	{"tokens", "revoke", "ci", "--confirm", "ci"},
	{"secret", "create", "key", "--value-stdin"},
	{"secret", "rotate", "s1", "--expected-version", "1", "--value-stdin"},
	{"secret", "delete", "s1", "--yes"},
	{"secret", "rewrap", "--old-key-id", "k0", "--yes"},
	{"runtime-config", "rollback", "--expected-revision", "3", "--target-revision", "1", "--yes"},
}

func nonGET(s *apiServer) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sent []string
	for _, r := range s.requests {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			sent = append(sent, r.Method+" "+r.Path)
		}
	}

	return sent
}

// S-30 and N-09: --dry-run never changes anything. A command that cannot
// preview refuses the flag (exit 2); none may send a changing request.
func TestDryRunSendsNothingThatChanges(t *testing.T) {
	for _, args := range mutating {
		server := accessEntriesServer(t, false)
		config := writeConfig(t, server.URL, "tok")
		got := execute(t, nil, "v\n", append([]string{"--config", config, "--dry-run"}, args...)...)
		if sent := nonGET(server); len(sent) != 0 {
			t.Errorf("%v with --dry-run sent %v (%+v)", args, sent, got)
		}
		if got.code != 0 && got.code != clierr.ExitUsage {
			t.Errorf("%v with --dry-run exited %d: %s", args, got.code, got.stderr)
		}
	}
}

// N-04, N-05: a command that would prompt never waits. Without a terminal, or
// with --no-input on one, it exits 2 naming a flag, and changes nothing.
func TestPromptingCommandsNeverWait(t *testing.T) {
	prompting := [][]string{
		{"access", "add"},
		{"access", "remove", "deploy"},
		{"hosts", "add"},
		{"hosts", "remove", "web-01"},
		{"envs", "remove", "staging"},
		{"secret", "delete", "s1"},
		{"secret", "rewrap", "--old-key-id", "k0"},
		{"runtime-config", "rollback", "--expected-revision", "3", "--target-revision", "1"},
		{"hosts", "revoke-agent", "web-01", "--agent-id", "a1"},
	}
	for _, args := range prompting {
		for _, terminal := range []bool{false, true} {
			server := hostsGroupServer(t)
			if args[0] == "access" {
				server = accessEntriesServer(t, false)
			}
			var got result
			if terminal {
				got = addRun(t, server, true, "", append(append([]string{}, args...), "--no-input")...)
			} else {
				got = addRun(t, server, false, "", args...)
			}
			if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "--") || got.stdout != "" {
				t.Errorf("%v (terminal=%v) = %+v, want exit 2 naming a flag", args, terminal, got)
			}
			if sent := nonGET(server); len(sent) != 0 {
				t.Errorf("%v (terminal=%v) sent %v", args, terminal, sent)
			}
		}
	}
}

// N-06: the CI variables are honoured and nothing is written to the config dir.
func TestCIEnvironmentWritesNothing(t *testing.T) {
	server := hostsGroupServer(t)
	dir := t.TempDir()
	got := runAuth(t, authRun{config: dir + "/config.yaml", env: map[string]string{
		"MACHINARCHY_TOKEN": "mch_pat_ci-0001", "MACHINARCHY_SERVER": server.URL,
		"MACHINARCHY_NO_INPUT": "1", "MACHINARCHY_OUTPUT": "json",
	}}, "hosts")
	if got.code != 0 || !json.Valid([]byte(got.stdout)) {
		t.Fatalf("hosts = %+v", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("the config dir is not empty: %v", entries)
	}
	if auth := server.last(t).Auth; auth != "Bearer mch_pat_ci-0001" {
		t.Fatalf("the token was not used: %q", auth)
	}
}

// N-07: with -o json an error is JSON on stderr and stdout stays empty.
func TestJSONErrorGoesToStderr(t *testing.T) {
	server := newAPIServer(t, jsonReply(200, `{}`))
	address := server.URL
	server.Close()

	got := execute(t, nil, "", "--config", writeConfig(t, address, "tok"), "hosts", "-o", "json")
	var decoded struct {
		Error struct{ Code, Message, Next string } `json:"error"`
	}
	if got.code != clierr.ExitUnavailable || got.stdout != "" || json.Unmarshal([]byte(got.stderr), &decoded) != nil ||
		decoded.Error.Code == "" || decoded.Error.Message == "" {
		t.Fatalf("result = %+v", got)
	}
}

// N-08: -q prints identifiers only.
func TestQuietPrintsIdentifiersOnly(t *testing.T) {
	server := hostsGroupServer(t)
	got := execute(t, nil, "", "--config", writeConfig(t, server.URL, "tok"), "hosts", "-q")
	if got.code != 0 || strings.TrimSpace(got.stdout) == "" {
		t.Fatalf("hosts -q = %+v", got)
	}
	for _, line := range strings.Split(strings.TrimSpace(got.stdout), "\n") {
		if strings.ContainsAny(line, " \t") {
			t.Errorf("decoration in quiet output: %q", line)
		}
	}
}
