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
	if !strings.Contains(got.stderr, "MACHINARCHY_AGENT_NATS_URL") || !strings.Contains(got.stderr, "does not report") {
		t.Errorf("stderr does not name the values the server cannot give: %s", got.stderr)
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

func TestHostsEnrollBrokerFlagsAndSavedDefaults(t *testing.T) {
	server := agentServer(t, false, true)
	creds := filepath.Join(t.TempDir(), "bootstrap.creds")
	if err := os.WriteFile(creds, []byte("-----BEGIN NATS USER JWT-----\nSECRET-CREDS\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := agentRun(t, server, false, "", "hosts", "enroll", "web-01", "--no-input", "--nats-url", "tls://broker.example:4222", "--nats-creds-file", creds)
	if got.code != 0 {
		t.Fatalf("enroll = %+v", got)
	}
	blob := decodeBlob(t, got.stdout)
	if !strings.Contains(blob, "MACHINARCHY_AGENT_NATS_URL=tls://broker.example:4222") ||
		!strings.Contains(blob, "MACHINARCHY_AGENT_NATS_CREDENTIALS_FILE=/etc/machinarchy-agent/agent.creds") {
		t.Errorf("blob = %s", blob)
	}
	if strings.Contains(blob, "SECRET-CREDS") || strings.Contains(got.stdout+got.stderr, "SECRET-CREDS") {
		t.Errorf("the credential content must never be printed")
	}
	if !strings.Contains(got.stderr, "install -m 0640") || !strings.Contains(got.stderr, creds) {
		t.Errorf("stderr does not say how to copy the credential: %s", got.stderr)
	}

	// --remember keeps them for the selected server: the next run needs no flags.
	isolate(t)
	cfg := writeConfig(t, server.URL, "tok")
	run := func(args ...string) result {
		var out, errOut bytes.Buffer
		code := cli.Run(context.Background(), cli.Options{
			Streams:    cli.IOStreams{In: strings.NewReader(""), Out: &out, ErrOut: &errOut},
			Groups:     commands.DefaultGroups(),
			IsTerminal: func(io.Reader) bool { return false },
		}, append([]string{"--config", cfg}, args...))

		return result{code: code, stdout: out.String(), stderr: errOut.String()}
	}
	if got := run("hosts", "enroll", "web-01", "--no-input", "--nats-url", "tls://saved.example:4222", "--remember"); got.code != 0 {
		t.Fatalf("remember = %+v", got)
	}
	got = run("hosts", "enroll", "web-01", "--no-input")
	if got.code != 0 || !strings.Contains(decodeBlob(t, got.stdout), "MACHINARCHY_AGENT_NATS_URL=tls://saved.example:4222") {
		t.Errorf("saved default not used: %+v", got)
	}
	got = run("hosts", "enroll", "web-01", "--no-input", "--nats-url", "tls://other.example:4222")
	if !strings.Contains(decodeBlob(t, got.stdout), "other.example") {
		t.Errorf("a flag must beat the saved default: %+v", got)
	}
}

func TestHostsEnrollRefusesBadBrokerValues(t *testing.T) {
	server := agentServer(t, false, true)
	for _, url := range []string{"http://x:4222", "tls://user:pw@x:4222", "x:4222", "tls://"} {
		got := agentRun(t, server, false, "", "hosts", "enroll", "web-01", "--no-input", "--nats-url", url)
		if got.code != 2 || strings.Contains(got.stderr, "pw@") && strings.Contains(got.stderr, "user:pw") {
			t.Errorf("%q: %+v", url, got)
		}
		if server.countOf("POST", "/api/v1/agents/enrollment-tokens") != 0 {
			t.Fatalf("%q: a token was issued for a refused request", url)
		}
	}
	got := agentRun(t, server, false, "", "hosts", "enroll", "web-01", "--no-input", "--nats-creds-file", filepath.Join(t.TempDir(), "missing"))
	if got.code != 2 {
		t.Errorf("missing creds file exit = %d", got.code)
	}
}
