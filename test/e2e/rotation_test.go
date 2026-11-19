package e2e

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/testutil/natstest"
)

// TestControllerSigningKeyRotation is the runbook procedure of AUDIT M-3 with
// real binaries: the controller announces the next key, the agent pins both,
// the controller switches keys across a restart, and the same agent process
// keeps serving commands without a failure or a discarded message.
func TestControllerSigningKeyRotation(t *testing.T) {
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

	ns := natstest.Start(t)
	newSeed := func() ([]byte, string) {
		seed := make([]byte, 32)
		if _, err := rand.Read(seed); err != nil {
			t.Fatal(err)
		}
		return seed, agentv1.EncodePublicKey(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	}
	seedA, keyA := newSeed()
	seedB, keyB := newSeed()
	fileA := secretFile(t, work, "signing-a.key", base64.StdEncoding.EncodeToString(seedA))
	fileB := secretFile(t, work, "signing-b.key", base64.StdEncoding.EncodeToString(seedB))

	port := freePort(t)
	common := []string{
		"MACHINARCHY_ENVIRONMENT=development",
		fmt.Sprintf("MACHINARCHY_LISTEN_ADDR=127.0.0.1:%d", port),
		"MACHINARCHY_DATABASE_PATH=" + filepath.Join(work, "data", "machinarchy.db"),
		"MACHINARCHY_OPERATOR_TOKEN=" + randomText(t, 24),
		"MACHINARCHY_ENCRYPTION_SECRET=" + randomText(t, 24),
		"MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD=e2e-admin-password",
		"MACHINARCHY_AGENT_GATEWAY_NATS_URL=" + ns.URL,
		"MACHINARCHY_AGENT_GATEWAY_NATS_CREDENTIALS_FILE=" + ns.ControllerCredentials(t, agentgateway.ControllerPermissions()),
		"MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT=" + ns.Account,
		"MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE=" + ns.SignerFile(t),
		"MACHINARCHY_AGENT_GATEWAY_LEASE_TTL=20s",
		"MACHINARCHY_AGENT_GATEWAY_HEARTBEAT=5s",
	}
	client := &api{base: fmt.Sprintf("http://127.0.0.1:%d", port)}
	startServer := func(name string, extra ...string) *process {
		server := start(t, monolith, append(append([]string{}, common...), extra...), filepath.Join(work, name+".log"))
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
		return server
	}
	type keys struct {
		PublicKey     string   `json:"publicKey"`
		NextPublicKey string   `json:"nextPublicKey"`
		PublicKeys    []string `json:"publicKeys"`
	}

	// Step 1: the controller signs with A and announces B.
	before := startServer("server-a", "MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE="+fileA, "MACHINARCHY_AGENT_GATEWAY_NEXT_PUBLIC_KEY="+keyB)
	var announced keys
	if status := client.do(t, http.MethodGet, "/api/v1/agents/controller-key", nil, &announced); status != http.StatusOK ||
		announced.PublicKey != keyA || announced.NextPublicKey != keyB || strings.Join(announced.PublicKeys, ",") != keyA+","+keyB {
		t.Fatalf("announced keys: %d %+v", status, announced)
	}

	// Step 2: the agent pins both, exactly as the endpoint lists them.
	hostname, _ := os.Hostname()
	var issued struct {
		Token string `json:"token"`
	}
	if status := client.do(t, http.MethodPost, "/api/v1/agents/enrollment-tokens", map[string]any{"hostname": hostname, "ttlSeconds": 600}, &issued); status != http.StatusCreated {
		t.Fatalf("issue token: %d", status)
	}
	agent := start(t, agentBinary, []string{
		"MACHINARCHY_ENVIRONMENT=development",
		"MACHINARCHY_AGENT_NAME=e2e-rotation",
		"MACHINARCHY_AGENT_NATS_URL=" + ns.URL,
		"MACHINARCHY_AGENT_NATS_CREDENTIALS_FILE=" + ns.BootstrapCredentials(t),
		"MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY=" + strings.Join(announced.PublicKeys, ","),
		"MACHINARCHY_AGENT_ENROLLMENT_TOKEN_FILE=" + secretFile(t, work, "agent.token", issued.Token),
		"MACHINARCHY_AGENT_STATE_FILE=" + filepath.Join(work, "agent", "state.json"),
	}, filepath.Join(work, "agent.log"))
	defer func() {
		if t.Failed() {
			agent.dump(t)
		}
	}()
	var agents agentList
	waitFor(t, 30*time.Second, "agent enrolment", func() bool {
		return client.do(t, http.MethodGet, "/api/v1/agents", nil, &agents) == http.StatusOK && len(agents.Items) == 1 && agents.Items[0].Connected
	})
	agentID := agents.Items[0].ID
	invoke := func(when string) {
		t.Helper()
		var result struct {
			Status string `json:"status"`
		}
		if status := client.do(t, http.MethodPost, "/api/v1/agents/"+agentID+"/invoke",
			map[string]any{"capability": "runtime.info", "timeoutSeconds": 20}, &result); status != http.StatusOK || result.Status != "succeeded" {
			t.Fatalf("invoke %s: %d %+v", when, status, result)
		}
	}
	for i := range 3 {
		invoke(fmt.Sprintf("signed with A (%d)", i+1))
	}

	// Step 3: the controller switches to B (a restart, as an operator would).
	if err := before.stop(t); err != nil {
		t.Fatalf("controller A did not stop cleanly: %v", err)
	}
	after := startServer("server-b", "MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE="+fileB)
	defer func() {
		if t.Failed() {
			after.dump(t)
		}
	}()
	var current keys
	if status := client.do(t, http.MethodGet, "/api/v1/agents/controller-key", nil, &current); status != http.StatusOK ||
		current.PublicKey != keyB || current.NextPublicKey != "" || len(current.PublicKeys) != 1 {
		t.Fatalf("keys after the switch: %d %+v", status, current)
	}
	// The same agent process renews its lease with B's responses and runs
	// commands signed by B.
	waitFor(t, 30*time.Second, "the agent connected to the new controller", func() bool {
		return client.do(t, http.MethodGet, "/api/v1/agents", nil, &agents) == http.StatusOK && len(agents.Items) == 1 && agents.Items[0].Connected
	})
	for i := range 3 {
		invoke(fmt.Sprintf("signed with B (%d)", i+1))
	}
	firstRenewalUnderB := agents.Items[0].LastSeenAt
	waitFor(t, 20*time.Second, "a lease renewal answered with key B", func() bool {
		return client.do(t, http.MethodGet, "/api/v1/agents", nil, &agents) == http.StatusOK && agents.Items[0].LastSeenAt.After(firstRenewalUnderB)
	})
	invoke("after a renewal under B")

	select {
	case <-agent.exited:
		t.Fatalf("the agent exited during the rotation: %v", agent.err)
	default:
	}
	log, _ := os.ReadFile(agent.log)
	for _, bad := range []string{"discarding", "unauthenticated", "signature", `"level":"ERROR"`} {
		if strings.Contains(string(log), bad) {
			t.Fatalf("agent log mentions %q during the rotation", bad)
		}
	}
}
