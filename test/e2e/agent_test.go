// Package e2e runs real machinarchy and machinarchy-agent binaries against
// an embedded NATS server (controller side) and drives them through the
// public HTTP API. Agents connect to the controller's gRPC gateway (ADR-059).
//
// The agent is built from the sibling checkout ../machinarchy-agent, or from
// MACHINARCHY_AGENT_SOURCE. Without it the test is skipped, unless
// MACHINARCHY_E2E_REQUIRED=1 (CI) makes that a failure.
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/testutil/natstest"
)

func required() bool { return os.Getenv("MACHINARCHY_E2E_REQUIRED") == "1" }

func agentSource(t *testing.T) string {
	t.Helper()
	source := os.Getenv("MACHINARCHY_AGENT_SOURCE")
	if source == "" {
		source = filepath.Join("..", "..", "..", "machinarchy-agent")
	}
	abs, err := filepath.Abs(source)
	if err == nil {
		if _, err = os.Stat(filepath.Join(abs, "cmd", "machinarchy-agent")); err == nil {
			return abs
		}
	}
	if required() {
		t.Fatalf("machinarchy-agent source not found at %s (set MACHINARCHY_AGENT_SOURCE)", source)
	}
	t.Skipf("machinarchy-agent source not found at %s; set MACHINARCHY_AGENT_SOURCE to run the agent end-to-end test", source)
	return ""
}

// binaries builds the monolith and the agent once per test run and shares
// them between tests, so the package fits in a short test timeout.
var binaries = struct {
	once            sync.Once
	dir             string
	monolith, agent string
	err             error
}{}

func builtBinaries(t *testing.T) (monolith, agent string) {
	t.Helper()
	agentDir := agentSource(t)
	binaries.once.Do(func() {
		repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			binaries.err = err
			return
		}
		if binaries.dir, binaries.err = os.MkdirTemp("", "machinarchy-e2e-*"); binaries.err != nil {
			return
		}
		binaries.monolith = filepath.Join(binaries.dir, "machinarchy-monolith")
		binaries.agent = filepath.Join(binaries.dir, "machinarchy-agent")
		if binaries.err = buildBinary(repoRoot, "./cmd/monolith", binaries.monolith); binaries.err != nil {
			return
		}
		binaries.err = buildBinary(agentDir, "./cmd/machinarchy-agent", binaries.agent)
	})
	if binaries.err != nil {
		t.Fatal(binaries.err)
	}
	return binaries.monolith, binaries.agent
}

// TestMain removes the shared binaries.
func TestMain(m *testing.M) {
	code := m.Run()
	if binaries.dir != "" {
		_ = os.RemoveAll(binaries.dir)
	}
	os.Exit(code)
}

func buildBinary(dir, pkg, out string) error {
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build %s: %w\n%s", pkg, err, output)
	}
	return nil
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func secretFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func randomText(t *testing.T, n int) string {
	t.Helper()
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw)
}

// process runs a binary, logs its output to a file and stops it with SIGTERM.
type process struct {
	cmd    *exec.Cmd
	log    string
	exited chan struct{}
	err    error // set before exited is closed
}

func start(t *testing.T, binary string, env []string, logFile string) *process {
	t.Helper()
	out, err := os.Create(logFile)
	if err != nil {
		t.Fatal(err)
	}
	// A test timeout panics without running cleanups, which would leave the
	// child running. Kill it shortly before the test binary's deadline.
	ctx, cancel := context.Background(), context.CancelFunc(func() {})
	if deadline, ok := t.Deadline(); ok {
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-2*time.Second))
	}
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, binary)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &process{cmd: cmd, log: logFile, exited: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		_ = out.Close()
		close(p.exited)
	}()
	t.Cleanup(func() { p.stop(t) })
	return p
}

func (p *process) stop(t *testing.T) error {
	select {
	case <-p.exited:
		return p.err
	default:
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.exited:
	case <-time.After(30 * time.Second):
		_ = p.cmd.Process.Kill()
		t.Errorf("%s did not stop within 30s of SIGTERM", filepath.Base(p.cmd.Path))
		<-p.exited
	}
	return p.err
}

func (p *process) dump(t *testing.T) {
	data, _ := os.ReadFile(p.log)
	t.Logf("--- %s log ---\n%s", filepath.Base(p.cmd.Path), data)
}

type api struct {
	base  string
	token string
}

func (a *api) do(t *testing.T, method, path string, body any, out any) int {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, a.base+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if a.token != "" {
		request.Header.Set("Authorization", "Bearer "+a.token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0 // not reachable yet; callers check the status
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: decode %s: %v", method, path, data, err)
		}
	}
	return response.StatusCode
}

func waitFor(t *testing.T, timeout time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

type agentList struct {
	Items []struct {
		ID         string    `json:"id"`
		Hostname   string    `json:"hostname"`
		Connected  bool      `json:"connected"`
		LastSeenAt time.Time `json:"lastSeenAt"`
		RevokedAt  *string   `json:"revokedAt"`
	} `json:"items"`
}

// TestAgentEnrolsAndRunsCapabilitiesThroughMachinarchy is the end-to-end path
// of AUDIT/04 M-1 to M-8 and ADR-057: per-host enrolment, lease renewal, a
// capability Operation over two agents with signed commands and verified
// results, revocation and graceful shutdown of every process.
func TestAgentEnrolsAndRunsCapabilitiesThroughMachinarchy(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end test")
	}
	t.Parallel() // independent NATS, ports, database and work directory
	monolith, agentBinary := builtBinaries(t)
	work := t.TempDir()
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}

	// NATS in operator mode, as deployments/nats/generate sets it up: the
	// gateway has the controller permissions, agents start with the bootstrap
	// credential and receive their own JWT (AUDIT M-10).
	ns := natstest.Start(t)
	natsURL := ns.URL
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	grpcPort := freePort(t)
	server := start(t, monolith, []string{
		"MACHINARCHY_ENVIRONMENT=development",
		fmt.Sprintf("MACHINARCHY_LISTEN_ADDR=127.0.0.1:%d", port),
		fmt.Sprintf("MACHINARCHY_AGENT_GATEWAY_GRPC_ADDR=127.0.0.1:%d", grpcPort),
		"MACHINARCHY_DATABASE_PATH=" + filepath.Join(work, "data", "machinarchy.db"),
		"MACHINARCHY_OPERATOR_TOKEN=" + randomText(t, 24),
		"MACHINARCHY_ENCRYPTION_SECRET=" + randomText(t, 24),
		"MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD=e2e-admin-password",
		"MACHINARCHY_AGENT_GATEWAY_NATS_URL=" + natsURL,
		"MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE=" + secretFile(t, work, "signing.key", base64.StdEncoding.EncodeToString(seed)),
		"MACHINARCHY_AGENT_GATEWAY_NATS_CREDENTIALS_FILE=" + ns.ControllerCredentials(t, agentgateway.ControllerPermissions()),
		"MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT=" + ns.Account,
		"MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE=" + ns.SignerFile(t),
		"MACHINARCHY_AGENT_GATEWAY_LEASE_TTL=12s",
		"MACHINARCHY_AGENT_GATEWAY_HEARTBEAT=5s",
	}, filepath.Join(work, "server.log"))
	client := &api{base: fmt.Sprintf("http://127.0.0.1:%d", port)}
	defer func() {
		if t.Failed() {
			server.dump(t)
		}
	}()
	waitFor(t, 30*time.Second, "machinarchy readiness", func() bool {
		var ready map[string]any
		return client.do(t, http.MethodGet, "/healthz/ready", nil, &ready) == http.StatusOK
	})

	var login struct {
		Token string `json:"token"`
	}
	if status := client.do(t, http.MethodPost, "/api/auth/login", map[string]string{"username": "admin", "password": "e2e-admin-password"}, &login); status != http.StatusOK {
		t.Fatalf("login status %d", status)
	}
	client.token = login.Token

	var key struct {
		PublicKey string `json:"publicKey"`
	}
	if status := client.do(t, http.MethodGet, "/api/v1/agents/controller-key", nil, &key); status != http.StatusOK || key.PublicKey == "" {
		t.Fatalf("controller key: %d %+v", status, key)
	}

	// Two managed hosts reached through their agents. An owner issues one
	// single-use token per host, bound to the host and this machine's name
	// (M-4, ADR-057).
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	profileID := agentProfile(t, client)
	hostA, hostB := agentHost(t, client, profileID, "agent-a"), agentHost(t, client, profileID, "agent-b")
	tokenA := hostToken(t, client, hostA, hostname)
	tokenFile := secretFile(t, work, "agent.token", tokenA)
	agentEnv := func(state, tokenFile string) []string {
		return []string{
			"MACHINARCHY_ENVIRONMENT=development",
			fmt.Sprintf("MACHINARCHY_AGENT_SERVER_URL=http://127.0.0.1:%d", grpcPort),
			"MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY=" + key.PublicKey,
			"MACHINARCHY_AGENT_ENROLLMENT_TOKEN_FILE=" + tokenFile,
			"MACHINARCHY_AGENT_STATE_FILE=" + state,
		}
	}
	agentState := filepath.Join(work, "agent", "state.json")
	agent := start(t, agentBinary, agentEnv(agentState, tokenFile), filepath.Join(work, "agent.log"))
	defer func() {
		if t.Failed() {
			agent.dump(t)
		}
	}()

	// 1. Enrolment: the agent appears, connected.
	var agents agentList
	waitFor(t, 30*time.Second, "agent enrolment", func() bool {
		return client.do(t, http.MethodGet, "/api/v1/agents", nil, &agents) == http.StatusOK &&
			len(agents.Items) == 1 && agents.Items[0].Connected
	})
	agentID, firstSeen := agents.Items[0].ID, agents.Items[0].LastSeenAt

	// The token is now used up: a second host with the same token is refused
	// and exits 78, and the token records who used it.
	second := start(t, agentBinary, agentEnv(filepath.Join(work, "second", "state.json"), tokenFile), filepath.Join(work, "second.log"))
	select {
	case <-second.exited:
	case <-time.After(30 * time.Second):
		t.Fatal("a second agent enrolled with a used token")
	}
	var usedToken *exec.ExitError
	if !errors.As(second.err, &usedToken) || usedToken.ExitCode() != 78 {
		t.Fatalf("second agent with a used token exited with %v, want status 78", second.err)
	}
	var tokens struct {
		Items []struct {
			ID          string `json:"id"`
			HostID      string `json:"hostId"`
			UsedByAgent string `json:"usedByAgent"`
		} `json:"items"`
	}
	if status := client.do(t, http.MethodGet, "/api/v1/agents/enrollment-tokens", nil, &tokens); status != http.StatusOK ||
		len(tokens.Items) != 1 || tokens.Items[0].UsedByAgent != agentID || tokens.Items[0].HostID != hostA {
		t.Fatalf("enrolment tokens after use: %d %+v", status, tokens)
	}
	if strings.Contains(fmt.Sprint(tokens), tokenA) {
		t.Fatal("the token list exposes the secret")
	}

	// The agent stored its enrolment (lease credential) and no NATS material.
	stateData, err := os.ReadFile(agentState)
	if err != nil || bytes.Contains(stateData, []byte("nats_user_jwt")) {
		t.Fatalf("agent state file: %v, NATS credential stored: %v", err, bytes.Contains(stateData, []byte("nats_user_jwt")))
	}

	// A second agent enrols for the second host.
	agentB := start(t, agentBinary, agentEnv(filepath.Join(work, "agent-b", "state.json"),
		secretFile(t, work, "agent-b.token", hostToken(t, client, hostB, hostname))), filepath.Join(work, "agent-b.log"))
	defer func() {
		if t.Failed() {
			agentB.dump(t)
		}
	}()
	waitFor(t, 30*time.Second, "second agent enrolment", func() bool {
		agents = agentList{}
		return client.do(t, http.MethodGet, "/api/v1/agents", nil, &agents) == http.StatusOK && len(agents.Items) == 2 &&
			agents.Items[0].Connected && agents.Items[1].Connected
	})

	// 2. A capability Operation over both hosts: one signed command per
	// target through JetStream, one verified result each.
	op := runCapability(t, client, "runtime.info", hostA, hostB)
	if op.Status.State != "Succeeded" {
		t.Fatalf("runtime.info Operation = %+v", op)
	}
	for _, host := range []string{hostA, hostB} {
		state, output, _ := op.target(host)
		var info struct {
			OS           string   `json:"os"`
			Capabilities []string `json:"capabilities"`
		}
		if err := json.Unmarshal([]byte(output), &info); state != "Succeeded" || err != nil || info.OS == "" ||
			!strings.Contains(strings.Join(info.Capabilities, ","), "runtime.info") {
			t.Fatalf("host %s: %s, runtime.info output = %s (%v)", host, state, output, err)
		}
	}
	// The agent itself refuses capabilities it does not offer: no generic exec.
	refused := runCapability(t, client, "shell.exec", hostA)
	if state, _, class := refused.target(hostA); state != "Failed" || class == "" {
		t.Fatalf("shell.exec target: %s/%s, want Failed with the agent's code (%+v)", state, class, refused)
	}

	// 3. Lease renewal moves last-seen forward (heartbeat 5s).
	waitFor(t, 20*time.Second, "a lease renewal", func() bool {
		agents = agentList{}
		if client.do(t, http.MethodGet, "/api/v1/agents", nil, &agents) != http.StatusOK {
			return false
		}
		for _, a := range agents.Items {
			if a.ID == agentID && a.LastSeenAt.After(firstSeen) {
				return true
			}
		}
		return false
	})

	// 4. Revocation: the next renewal is refused and the agent is disconnected.
	if status := client.do(t, http.MethodPost, "/api/v1/agents/"+agentID+"/revoke", nil, nil); status != http.StatusNoContent {
		t.Fatalf("revoke status %d", status)
	}
	// Its host is no longer served: the target fails without a command sent.
	revoked := runCapability(t, client, "runtime.info", hostA)
	if state, _, class := revoked.target(hostA); state != "Failed" || class != "agent_not_bound" {
		t.Fatalf("Operation on the revoked agent's host: %s/%s", state, class)
	}
	// The agent learns of the revocation at its next renewal (heartbeat 5s)
	// and exits 78 by itself, which tells supervisors not to restart it.
	select {
	case <-agent.exited:
	case <-time.After(30 * time.Second):
		t.Fatal("revoked agent kept running")
	}
	var exit *exec.ExitError
	if !errors.As(agent.err, &exit) || exit.ExitCode() != 78 {
		t.Fatalf("revoked agent exited with %v, want exit status 78", agent.err)
	}

	// 5. /metrics serves HTTP, control-loop, worker and gateway metrics.
	response, err := http.Get(client.base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	metrics, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	for _, name := range []string{"machinarchy_http_requests_total", "machinarchy_controlmanager_queue_depth",
		"machinarchy_operationworker_operations_in_progress", `machinarchy_agent_gateway_registrations_total{outcome="registered"} 2`,
		`machinarchy_agent_gateway_results_total{status="succeeded"}`} {
		if !strings.Contains(string(metrics), name) {
			t.Errorf("/metrics lacks %s", name)
		}
	}

	// 6. The second agent and machinarchy stop cleanly on SIGTERM (the
	// revoked agent already exited).
	if err := agentB.stop(t); err != nil {
		t.Fatalf("agent did not stop cleanly: %v", err)
	}
	if err := server.stop(t); err != nil {
		t.Fatalf("machinarchy did not stop cleanly: %v", err)
	}
}
