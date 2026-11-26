package commands_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/commands"
)

const agentHostJSON = `{"metadata":{"id":"h7","name":"web-01","revision":1},"spec":{"accessProfileId":"prof-1"},"status":{"phase":"Pending"}}`

// agentServer serves the routes the agent path uses. noGateway makes the controller key answer 503.
func agentServer(t *testing.T, noGateway, existingEntry bool) *apiServer {
	t.Helper()
	write := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}

	return newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/agents/controller-key":
			if noGateway {
				write(w, 503, `{"error":"no agent gateway"}`)
				return
			}
			write(w, 200, `{"publicKey":"KEY1","publicKeys":["KEY1","KEY2"]}`)
		case "GET /api/v1/managed/access-profiles":
			if existingEntry {
				write(w, 200, `{"items":[{"id":"prof-1","name":"agents","currentRevision":1}]}`)
				return
			}
			write(w, 200, `{"items":[]}`)
		case "GET /api/v1/managed/access-profiles/prof-1":
			write(w, 200, `{"id":"prof-1","name":"agents","currentRevision":1,"spec":{"connectorKind":"agent"}}`)
		case "POST /api/v1/managed/access-profiles":
			write(w, 201, `{"id":"prof-1","name":"agents","currentRevision":1}`)
		case "POST /api/v1/managed/hosts":
			write(w, 201, agentHostJSON)
		case "GET /api/v1/managed/hosts":
			write(w, 200, `{"items":[`+agentHostJSON+`]}`)
		case "POST /api/v1/agents/enrollment-tokens":
			write(w, 201, `{"id":"tok-2","hostId":"h7","createdAt":"2026-11-26T10:00:00Z","expiresAt":"2099-01-01T00:00:00Z","token":"SECRET-TOKEN"}`)
		case "GET /api/v1/agents/enrollment-tokens":
			write(w, 200, `{"items":[{"id":"tok-1","hostId":"h7","expiresAt":"2099-01-01T00:00:00Z"},{"id":"tok-2","hostId":"h7","expiresAt":"2099-01-01T00:00:00Z"},{"id":"tok-3","hostId":"other","expiresAt":"2099-01-01T00:00:00Z"}]}`)
		case "DELETE /api/v1/agents/enrollment-tokens/tok-1", "DELETE /api/v1/agents/enrollment-tokens/tok-2", "POST /api/v1/agents/agent-3f9/revoke":
			w.WriteHeader(http.StatusNoContent)
		default:
			write(w, 404, `{"error":"no route","code":"not_found"}`)
		}
	})
}

func agentRun(t *testing.T, server *apiServer, terminal bool, stdin string, args ...string) result {
	t.Helper()
	isolate(t)
	cfg := writeConfig(t, server.URL, "tok")
	var out, errOut bytes.Buffer
	code := cli.Run(context.Background(), cli.Options{
		Streams:    cli.IOStreams{In: strings.NewReader(stdin), Out: &out, ErrOut: &errOut},
		Groups:     commands.DefaultGroups(),
		IsTerminal: func(io.Reader) bool { return terminal },
	}, append([]string{"--config", cfg}, args...))

	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

func decodeBlob(t *testing.T, stdout string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(stdout))
	if err != nil {
		t.Fatalf("stdout is not one base64 blob: %v (%q)", err, stdout)
	}

	return string(raw)
}

func TestHostsEnrollPrintsBlobOnStdoutOnly(t *testing.T) {
	server := agentServer(t, false, true)
	got := agentRun(t, server, false, "", "hosts", "enroll", "web-01", "--no-input")
	if got.code != 0 {
		t.Fatalf("enroll = %+v", got)
	}
	blob := decodeBlob(t, got.stdout)
	for _, want := range []string{"MACHINARCHY_AGENT_NAME=web-01", "MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY=KEY1,KEY2", "MACHINARCHY_AGENT_ENROLLMENT_TOKEN=SECRET-TOKEN"} {
		if !strings.Contains(blob, want) {
			t.Errorf("blob lacks %q:\n%s", want, blob)
		}
	}
	if strings.Contains(got.stderr, "SECRET-TOKEN") || strings.Contains(got.stderr, got.stdout) {
		t.Errorf("the token or blob leaked to stderr: %s", got.stderr)
	}
	if strings.Contains(got.stderr, "NATS") || strings.Contains(got.stderr, "does not report") {
		t.Errorf("stderr still mentions a broker: %s", got.stderr)
	}
}

func TestHostsEnrollTokenOutIsPrivateAndNeverOverwrites(t *testing.T) {
	server := agentServer(t, false, true)
	path := filepath.Join(t.TempDir(), "web-01.blob")
	got := agentRun(t, server, false, "", "hosts", "enroll", "web-01", "--no-input", "--token-out", path)
	if got.code != 0 || strings.TrimSpace(got.stdout) != "" {
		t.Fatalf("enroll --token-out = %+v", got)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, %v", info, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(decodeBlob(t, string(data)), "SECRET-TOKEN") {
		t.Errorf("file does not hold the blob")
	}

	before := server.countOf("POST", "/api/v1/agents/enrollment-tokens")
	again := agentRun(t, server, false, "", "hosts", "enroll", "web-01", "--no-input", "--token-out", path)
	if again.code == 0 || server.countOf("POST", "/api/v1/agents/enrollment-tokens") != before {
		t.Errorf("an existing file must be refused before a token is issued: %+v", again)
	}
}

func TestHostsEnrollRevokeOldKeepsTheNewTokenAndOtherHosts(t *testing.T) {
	server := agentServer(t, false, true)
	got := agentRun(t, server, false, "", "hosts", "enroll", "web-01", "--no-input", "--revoke-old")
	if got.code != 0 || server.countOf("DELETE", "/api/v1/agents/enrollment-tokens/tok-1") != 1 ||
		server.countOf("DELETE", "/api/v1/agents/enrollment-tokens/tok-2") != 0 || server.countOf("DELETE", "/api/v1/agents/enrollment-tokens/tok-3") != 0 {
		t.Fatalf("revoke-old = %+v", got)
	}
}

func TestHostsEnrollRefusesWithoutGatewayAndBadTTL(t *testing.T) {
	server := agentServer(t, true, true)
	got := agentRun(t, server, false, "", "hosts", "enroll", "web-01", "--no-input")
	if got.code == 0 || !strings.Contains(got.stderr, "no agent gateway") || server.countOf("POST", "/api/v1/agents/enrollment-tokens") != 0 {
		t.Errorf("no gateway = %+v", got)
	}
	got = agentRun(t, agentServer(t, false, true), false, "", "hosts", "enroll", "web-01", "--no-input", "--ttl", "10s")
	if got.code != 2 {
		t.Errorf("bad ttl exit = %d", got.code)
	}
}

func TestHostsAddAgentRegistersAndIssues(t *testing.T) {
	server := agentServer(t, false, false)
	got := agentRun(t, server, false, "", "hosts", "add", "web-01", "--agent", "--tag", "tier=web", "--no-input")
	if got.code != 0 {
		t.Fatalf("add --agent = %+v", got)
	}
	if server.countOf("POST", "/api/v1/managed/access-profiles") != 1 || server.countOf("POST", "/api/v1/managed/hosts") != 1 {
		t.Errorf("expected the shared agents entry and the host to be created")
	}
	body := string(server.lastOf("POST", "/api/v1/managed/hosts").Body)
	if !strings.Contains(body, `"accessProfileId":"prof-1"`) || strings.Contains(body, "endpoint") || !strings.Contains(body, "tier") {
		t.Errorf("host body = %s", body)
	}
	if !strings.Contains(decodeBlob(t, got.stdout), "SECRET-TOKEN") {
		t.Errorf("no blob on stdout: %q", got.stdout)
	}

	// An existing "agents" entry is reused, an address is refused, no gateway changes nothing.
	server = agentServer(t, false, true)
	if got := agentRun(t, server, false, "", "hosts", "add", "web-01", "--agent", "--no-input"); got.code != 0 || server.countOf("POST", "/api/v1/managed/access-profiles") != 0 {
		t.Errorf("reuse = %+v", got)
	}
	if got := agentRun(t, agentServer(t, false, true), false, "", "hosts", "add", "web-01", "10.0.0.5", "--agent", "--no-input"); got.code != 2 {
		t.Errorf("address with --agent exit = %d", got.code)
	}
	server = agentServer(t, true, false)
	if got := agentRun(t, server, false, "", "hosts", "add", "web-01", "--agent", "--no-input"); got.code == 0 || server.countOf("POST", "/api/v1/managed/hosts") != 0 {
		t.Errorf("no gateway must create nothing: %+v", got)
	}
}

func TestHostsRevokeAgentNeedsTypedNameAndID(t *testing.T) {
	server := agentServer(t, false, true)
	if got := agentRun(t, server, false, "", "hosts", "revoke-agent", "web-01", "--no-input", "--yes"); got.code != 2 {
		t.Errorf("without --agent-id exit = %d", got.code)
	}
	if got := agentRun(t, server, false, "", "hosts", "revoke-agent", "web-01", "--agent-id", "agent-3f9", "--no-input", "--yes"); got.code == 0 || server.countOf("POST", "/api/v1/agents/agent-3f9/revoke") != 0 {
		t.Errorf("--yes must not be enough: %+v", got)
	}
	got := agentRun(t, server, false, "", "hosts", "revoke-agent", "web-01", "--agent-id", "agent-3f9", "--no-input", "--confirm", "web-01")
	if got.code != 0 || server.countOf("POST", "/api/v1/agents/agent-3f9/revoke") != 1 || !strings.Contains(got.stdout, "Not done") {
		t.Errorf("revoke = %+v", got)
	}
}

func (s *apiServer) lastOf(method, path string) apiRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.requests) - 1; i >= 0; i-- {
		if s.requests[i].Method == method && s.requests[i].Path == path {
			return s.requests[i]
		}
	}

	return apiRequest{}
}

func TestHostsEnrollBlobHoldsServerURLTokenAndKeyOnly(t *testing.T) {
	server := agentServer(t, false, true)
	got := agentRun(t, server, false, "", "hosts", "enroll", "web-01", "--no-input")
	if got.code != 0 {
		t.Fatalf("enroll = %+v", got)
	}
	blob := decodeBlob(t, got.stdout)
	for _, want := range []string{"MACHINARCHY_AGENT_SERVER_URL=" + server.URL, "MACHINARCHY_AGENT_ENROLLMENT_TOKEN=SECRET-TOKEN", "MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY=KEY1,KEY2"} {
		if !strings.Contains(blob, want) {
			t.Errorf("blob lacks %q: %s", want, blob)
		}
	}
	if strings.Contains(strings.ToLower(blob+got.stderr), "nats") || strings.Contains(got.stderr, "not reported by this server") {
		t.Errorf("no broker value may appear: blob=%s stderr=%s", blob, got.stderr)
	}
	if strings.Contains(got.stderr, "SECRET-TOKEN") {
		t.Errorf("the token must not be repeated in the instructions: %s", got.stderr)
	}
}

func TestRetiredBrokerFlagsAreRefusedWithReplacement(t *testing.T) {
	server := agentServer(t, false, true)
	for _, args := range [][]string{
		{"hosts", "enroll", "web-01", "--nats-url", "tls://b:4222"},
		{"hosts", "enroll", "web-01", "--nats-creds-file", "x"},
		{"hosts", "enroll", "web-01", "--remember"},
		{"hosts", "add", "web-01", "--agent", "--nats-url", "tls://b:4222"},
	} {
		got := agentRun(t, server, false, "", append(args, "--no-input")...)
		if got.code != 2 || !strings.Contains(got.stderr, "was removed") || !strings.Contains(got.stderr, "machinarchy hosts enroll NAME") {
			t.Errorf("%v: %+v", args, got)
		}
		if server.countOf("POST", "/api/v1/agents/enrollment-tokens") != 0 || server.countOf("POST", "/api/v1/managed/hosts") != 0 {
			t.Fatalf("%v: something was sent for a refused request", args)
		}
	}
}

// revokeByNameServer answers the routes `hosts revoke-agent NAME` needs, with
// the given enrolment tokens and agents (raw JSON arrays).
func revokeByNameServer(t *testing.T, tokens, agents string) *apiServer {
	t.Helper()
	write := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}

	return newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/managed/hosts":
			write(w, 200, `{"items":[`+agentHostJSON+`]}`)
		case r.Method == "GET" && r.URL.Path == "/api/v1/agents/enrollment-tokens":
			write(w, 200, `{"items":`+tokens+`}`)
		case r.Method == "GET" && r.URL.Path == "/api/v1/agents":
			write(w, 200, `{"items":`+agents+`}`)
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/api/v1/agents/") && strings.HasSuffix(r.URL.Path, "/revoke"):
			w.WriteHeader(http.StatusNoContent)
		default:
			write(w, 404, `{"error":"no route","code":"not_found"}`)
		}
	})
}

func TestHostsRevokeAgentFindsTheAgentByHostName(t *testing.T) {
	const exp = `"expiresAt":"2099-01-01T00:00:00Z"`
	tokens := `[{"id":"t1","hostId":"h7","usedByAgent":"agent-old",` + exp + `},{"id":"t2","hostId":"h7","usedByAgent":"agent-new",` + exp + `},` +
		`{"id":"t3","hostId":"h7",` + exp + `},{"id":"t4","hostId":"other","usedByAgent":"agent-x",` + exp + `}]`
	agents := `[{"id":"agent-old","revokedAt":"2026-11-01T00:00:00Z"},{"id":"agent-new"},{"id":"agent-x"}]`

	server := revokeByNameServer(t, tokens, agents)
	got := agentRun(t, server, false, "", "hosts", "revoke-agent", "web-01", "--no-input", "--confirm", "web-01")
	if got.code != 0 || server.countOf("POST", "/api/v1/agents/agent-new/revoke") != 1 ||
		server.countOf("POST", "/api/v1/agents/agent-old/revoke") != 0 || server.countOf("POST", "/api/v1/agents/agent-x/revoke") != 0 {
		t.Fatalf("revoke by name = %+v", got)
	}

	// Typing the name is still required: --yes is not enough.
	server = revokeByNameServer(t, tokens, agents)
	if got := agentRun(t, server, false, "", "hosts", "revoke-agent", "web-01", "--no-input", "--yes"); got.code == 0 || server.countOf("POST", "/api/v1/agents/agent-new/revoke") != 0 {
		t.Fatalf("--yes must not be enough: %+v", got)
	}

	// --agent-id overrides the lookup.
	server = revokeByNameServer(t, tokens, agents)
	if got := agentRun(t, server, false, "", "hosts", "revoke-agent", "web-01", "--agent-id", "agent-old", "--no-input", "--confirm", "web-01"); got.code != 0 ||
		server.countOf("POST", "/api/v1/agents/agent-old/revoke") != 1 {
		t.Fatalf("override = %+v", got)
	}
}

func TestHostsRevokeAgentRefusesWhenAmbiguousOrUnknown(t *testing.T) {
	const exp = `"expiresAt":"2099-01-01T00:00:00Z"`
	two := `[{"id":"t1","hostId":"h7","usedByAgent":"agent-a",` + exp + `},{"id":"t2","hostId":"h7","usedByAgent":"agent-b",` + exp + `}]`
	for name, tokens := range map[string]string{
		"two active agents":  two,
		"no used token":      `[{"id":"t1","hostId":"h7",` + exp + `}]`,
		"old server no host": `[{"id":"t1","usedByAgent":"agent-a",` + exp + `}]`,
	} {
		server := revokeByNameServer(t, tokens, `[{"id":"agent-a"},{"id":"agent-b"}]`)
		got := agentRun(t, server, false, "", "hosts", "revoke-agent", "web-01", "--no-input", "--confirm", "web-01")
		if got.code != 2 || !strings.Contains(got.stderr, "--agent-id") {
			t.Errorf("%s: %+v", name, got)
		}
		for _, id := range []string{"agent-a", "agent-b"} {
			if server.countOf("POST", "/api/v1/agents/"+id+"/revoke") != 0 {
				t.Errorf("%s: revoked %s without being sure", name, id)
			}
		}
	}
}
