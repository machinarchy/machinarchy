// Package e2e runs real machinarchy and machinarchy-agent binaries against
// an embedded NATS server and drives them through the public HTTP API.
//
// The agent is built from the sibling checkout ../machinarchy-agent, or from
// MACHINARCHY_AGENT_SOURCE. Without it the test is skipped, unless
// MACHINARCHY_E2E_REQUIRED=1 (CI) makes that a failure.
package e2e

import (
	"bytes"
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

func build(t *testing.T, dir, pkg, out string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, output)
	}
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
	cmd := exec.Command(binary)
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
// of AUDIT/04 M-1 to M-8: enrolment, lease renewal, a signed capability call,
// a verified result, revocation and graceful shutdown of both processes.
func TestAgentEnrolsAndRunsCapabilitiesThroughMachinarchy(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end test")
	}
	agentDir := agentSource(t)
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	monolith, agentBinary := filepath.Join(work, "machinarchy-monolit"), filepath.Join(work, "machinarchy-agent")
	build(t, repoRoot, "./cmd/monolit", monolith)
	build(t, agentDir, "./cmd/machinarchy-agent", agentBinary)

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
	server := start(t, monolith, []string{
		"MACHINARCHY_ENVIRONMENT=development",
		fmt.Sprintf("MACHINARCHY_LISTEN_ADDR=127.0.0.1:%d", port),
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

	// An owner issues a single-use token bound to this host's name (M-4).
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	var issued struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if status := client.do(t, http.MethodPost, "/api/v1/agents/enrollment-tokens",
		map[string]any{"hostname": hostname, "ttlSeconds": 600}, &issued); status != http.StatusCreated || issued.Token == "" {
		t.Fatalf("issue enrolment token: %d %+v", status, issued)
	}
	tokenFile := secretFile(t, work, "agent.token", issued.Token)
	bootstrapCreds := ns.BootstrapCredentials(t)
	agentEnv := func(name, state string) []string {
		return []string{
			"MACHINARCHY_ENVIRONMENT=development",
			"MACHINARCHY_AGENT_NAME=" + name,
			"MACHINARCHY_AGENT_NATS_URL=" + natsURL,
			"MACHINARCHY_AGENT_NATS_CREDENTIALS_FILE=" + bootstrapCreds,
			"MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY=" + key.PublicKey,
			"MACHINARCHY_AGENT_ENROLLMENT_TOKEN_FILE=" + tokenFile,
			"MACHINARCHY_AGENT_STATE_FILE=" + state,
		}
	}
	agentState := filepath.Join(work, "agent", "state.json")
	agent := start(t, agentBinary, agentEnv("e2e-agent", agentState), filepath.Join(work, "agent.log"))
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
	second := start(t, agentBinary, agentEnv("e2e-second", filepath.Join(work, "second", "state.json")), filepath.Join(work, "second.log"))
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
			UsedByAgent string `json:"usedByAgent"`
		} `json:"items"`
	}
	if status := client.do(t, http.MethodGet, "/api/v1/agents/enrollment-tokens", nil, &tokens); status != http.StatusOK ||
		len(tokens.Items) != 1 || tokens.Items[0].UsedByAgent != agentID {
		t.Fatalf("enrolment tokens after use: %d %+v", status, tokens)
	}
	if strings.Contains(fmt.Sprint(tokens), issued.Token) {
		t.Fatal("the token list exposes the secret")
	}

	// The agent now holds its own NATS credential.
	stateData, err := os.ReadFile(agentState)
	if err != nil || !bytes.Contains(stateData, []byte(`"nats_user_jwt"`)) {
		t.Fatalf("the agent did not store its NATS user JWT (%v)", err)
	}

	// 2. A capability call round-trips through JetStream, signed both ways.
	var result struct {
		Status string          `json:"status"`
		Output json.RawMessage `json:"output"`
	}
	if status := client.do(t, http.MethodPost, "/api/v1/agents/"+agentID+"/invoke",
		map[string]any{"capability": "runtime.info", "timeoutSeconds": 20}, &result); status != http.StatusOK || result.Status != "succeeded" {
		t.Fatalf("invoke runtime.info: %d %+v", status, result)
	}
	var info struct {
		OS           string   `json:"os"`
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(result.Output, &info); err != nil || info.OS == "" || !strings.Contains(strings.Join(info.Capabilities, ","), "runtime.info") {
		t.Fatalf("runtime.info output = %s (%v)", result.Output, err)
	}
	// The agent itself refuses capabilities it does not offer: no generic exec.
	var refused struct {
		Status string `json:"status"`
		Code   string `json:"code"`
	}
	if status := client.do(t, http.MethodPost, "/api/v1/agents/"+agentID+"/invoke",
		map[string]any{"capability": "shell.exec", "timeoutSeconds": 10}, &refused); status != http.StatusOK || refused.Status != "rejected" {
		t.Fatalf("shell.exec: %d %+v, want a rejected result", status, refused)
	}
	var bad map[string]string

	// 3. Lease renewal moves last-seen forward (heartbeat 5s).
	waitFor(t, 20*time.Second, "a lease renewal", func() bool {
		return client.do(t, http.MethodGet, "/api/v1/agents", nil, &agents) == http.StatusOK &&
			agents.Items[0].LastSeenAt.After(firstSeen)
	})

	// 4. Revocation: the next renewal is refused and the agent is disconnected.
	if status := client.do(t, http.MethodPost, "/api/v1/agents/"+agentID+"/revoke", nil, nil); status != http.StatusNoContent {
		t.Fatalf("revoke status %d", status)
	}
	if status := client.do(t, http.MethodPost, "/api/v1/agents/"+agentID+"/invoke",
		map[string]any{"capability": "runtime.info"}, &bad); status != http.StatusServiceUnavailable {
		t.Fatalf("invoke on a revoked agent status = %d %v", status, bad)
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
		"machinarchy_operationworker_operations_in_progress", `machinarchy_agent_gateway_registrations_total{outcome="registered"} 1`,
		`machinarchy_agent_gateway_results_total{status="succeeded"}`} {
		if !strings.Contains(string(metrics), name) {
			t.Errorf("/metrics lacks %s", name)
		}
	}

	// 6. machinarchy stops cleanly on SIGTERM (the agent already exited).
	if err := server.stop(t); err != nil {
		t.Fatalf("machinarchy did not stop cleanly: %v", err)
	}
}
