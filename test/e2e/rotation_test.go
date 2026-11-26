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

// TestControllerSigningKeyRotation is the runbook procedure of AUDIT M-3
// (Phase 16) with real binaries: the old key file is imported into the Secret
// Store, the owner stages the next key, the agent pins both, the owner
// activates it without a restart, and the same agent process keeps serving
// commands. A restart without the key file keeps the active key.
func TestControllerSigningKeyRotation(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end test")
	}
	t.Parallel() // independent NATS, ports, database and work directory
	monolith, agentBinary := builtBinaries(t)
	work := t.TempDir()
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}

	ns := natstest.Start(t)
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	keyA := agentv1.EncodePublicKey(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	keyFile := secretFile(t, work, "signing.key", base64.StdEncoding.EncodeToString(seed))

	port := freePort(t)
	grpcPort := freePort(t)
	common := []string{
		"MACHINARCHY_ENVIRONMENT=development",
		fmt.Sprintf("MACHINARCHY_LISTEN_ADDR=127.0.0.1:%d", port),
		fmt.Sprintf("MACHINARCHY_AGENT_GATEWAY_GRPC_ADDR=127.0.0.1:%d", grpcPort),
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
	controllerKeys := func() keys {
		var k keys
		if status := client.do(t, http.MethodGet, "/api/v1/agents/controller-key", nil, &k); status != http.StatusOK {
			t.Fatalf("controller-key: %d", status)
		}
		return k
	}

	// 1. The first start imports the key file: enrolled agents keep their pin.
	server := startServer("server-1", "MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE="+keyFile)
	if k := controllerKeys(); k.PublicKey != keyA || len(k.PublicKeys) != 1 {
		t.Fatalf("after import: %+v, want the file's key %s", k, keyA)
	}

	// 2. The owner stages the next key; the endpoint announces both.
	var staged struct {
		ID        string `json:"id"`
		State     string `json:"state"`
		PublicKey string `json:"publicKey"`
	}
	if status := client.do(t, http.MethodPost, "/api/v1/agents/signing-keys/next", nil, &staged); status != http.StatusCreated || staged.State != "next" {
		t.Fatalf("stage: %d %+v", status, staged)
	}
	announced := controllerKeys()
	if announced.PublicKey != keyA || announced.NextPublicKey != staged.PublicKey || len(announced.PublicKeys) != 2 {
		t.Fatalf("announced keys: %+v", announced)
	}

	// 3. The agent pins both, exactly as the endpoint lists them. It serves
	// one managed host, through a token bound to that host.
	hostname, _ := os.Hostname()
	hostID := agentHost(t, client, agentProfile(t, client), "rotation")
	token := hostToken(t, client, hostID, hostname)
	agent := start(t, agentBinary, []string{
		"MACHINARCHY_ENVIRONMENT=development",
		fmt.Sprintf("MACHINARCHY_AGENT_SERVER_URL=http://127.0.0.1:%d", grpcPort),
		"MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY=" + strings.Join(announced.PublicKeys, ","),
		"MACHINARCHY_AGENT_ENROLLMENT_TOKEN_FILE=" + secretFile(t, work, "agent.token", token),
		"MACHINARCHY_AGENT_STATE_FILE=" + filepath.Join(work, "agent", "state.json"),
	}, filepath.Join(work, "agent.log"))
	defer func() {
		if t.Failed() {
			agent.dump(t)
			server.dump(t)
		}
	}()
	var agents agentList
	waitFor(t, 30*time.Second, "agent enrolment", func() bool {
		return client.do(t, http.MethodGet, "/api/v1/agents", nil, &agents) == http.StatusOK && len(agents.Items) == 1 && agents.Items[0].Connected
	})
	invoke := func(when string) {
		t.Helper()
		if op := runCapability(t, client, "runtime.info", hostID); op.Status.State != "Succeeded" {
			t.Fatalf("runtime.info %s: %+v", when, op)
		}
	}
	for i := range 3 {
		invoke(fmt.Sprintf("signed with the imported key (%d)", i+1))
	}

	// 4. The owner activates the next key; no restart.
	if status := client.do(t, http.MethodPost, "/api/v1/agents/signing-keys/next/activate", nil, nil); status != http.StatusOK {
		t.Fatalf("activate: %d", status)
	}
	if k := controllerKeys(); k.PublicKey != staged.PublicKey || len(k.PublicKeys) != 1 {
		t.Fatalf("after activation: %+v", k)
	}
	for i := range 3 {
		invoke(fmt.Sprintf("signed with the new key (%d)", i+1))
	}
	waitFor(t, 30*time.Second, "agent connected", func() bool {
		return client.do(t, http.MethodGet, "/api/v1/agents", nil, &agents) == http.StatusOK && agents.Items[0].Connected
	})
	firstRenewal := agents.Items[0].LastSeenAt
	waitFor(t, 20*time.Second, "a lease renewal answered with the new key", func() bool {
		return client.do(t, http.MethodGet, "/api/v1/agents", nil, &agents) == http.StatusOK && agents.Items[0].LastSeenAt.After(firstRenewal)
	})

	// 5. A restart without the key file keeps the active key (Secret Store),
	// and an Operation submitted just before the restart still finishes: its
	// command and result are durable (ADR-057).
	inFlight := submitCapability(t, client, "runtime.info", hostID)
	if err := server.stop(t); err != nil {
		t.Fatalf("controller did not stop cleanly: %v", err)
	}
	server = startServer("server-2")
	if k := controllerKeys(); k.PublicKey != staged.PublicKey {
		t.Fatalf("after a restart without the key file: %+v", k)
	}
	waitFor(t, 30*time.Second, "agent reconnected", func() bool {
		return client.do(t, http.MethodGet, "/api/v1/agents", nil, &agents) == http.StatusOK && agents.Items[0].Connected
	})
	if op := waitOperation(t, client, inFlight); op.Status.State != "Succeeded" {
		t.Fatalf("Operation across the restart: %+v", op)
	}
	invoke("after a restart without the key file")

	var list struct {
		Items []struct {
			State  string `json:"state"`
			Origin string `json:"origin"`
		} `json:"items"`
	}
	if status := client.do(t, http.MethodGet, "/api/v1/agents/signing-keys", nil, &list); status != http.StatusOK || len(list.Items) != 2 ||
		list.Items[0].State != "active" || list.Items[1].State != "retired" || list.Items[1].Origin != "imported" {
		t.Fatalf("signing keys: %d %+v", status, list)
	}
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
