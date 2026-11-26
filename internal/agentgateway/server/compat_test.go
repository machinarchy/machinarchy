package server_test

import (
	"context"
	"crypto/ed25519"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// TestRealAgentBinaryAgainstGateway is the cross-repo compatibility run
// (plan step 8.4): the machinarchy-agent binary built from the sibling
// repository enrols with only a server URL and token, runs a command through
// this gateway, and after a restart receives a command queued while it was
// down. It runs only when MACHINARCHY_AGENT_BIN names the binary:
//
//	(cd ../machinarchy-agent && make build) &&
//	MACHINARCHY_AGENT_BIN=$PWD/../machinarchy-agent/bin/machinarchy-agent go test -run RealAgent ./internal/agentgateway/server/
func TestRealAgentBinaryAgainstGateway(t *testing.T) {
	bin := os.Getenv("MACHINARCHY_AGENT_BIN")
	if bin == "" {
		t.Skip("MACHINARCHY_AGENT_BIN is not set")
	}
	e := newEnv(t, nil)
	results := make(chan agentv1.Result, 16)
	e.gateway.SetResultHandler(func(_ context.Context, result agentv1.Result) error {
		results <- result
		return nil
	})
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(stateDir, "state.json")
	var mu sync.Mutex
	start := func(token string) (stop func()) {
		cmd := exec.Command(bin) // #nosec G204 -- test-selected binary
		cmd.Env = []string{
			"MACHINARCHY_ENVIRONMENT=development",
			"MACHINARCHY_AGENT_SERVER_URL=http://" + e.conn.Target(),
			"MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY=" + agentv1.EncodePublicKey(e.signer.Public().(ed25519.PublicKey)),
			"MACHINARCHY_AGENT_STATE_FILE=" + stateFile,
			"MACHINARCHY_AGENT_ENROLLMENT_TOKEN=" + token,
			"MACHINARCHY_LOG_LEVEL=debug",
		}
		output := &syncBuffer{}
		cmd.Stdout, cmd.Stderr = output, output
		t.Cleanup(func() {
			if t.Failed() {
				t.Logf("agent output:\n%s", output.String())
			}
		})
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		stopOnce := sync.OnceFunc(func() {
			mu.Lock()
			defer mu.Unlock()
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		})
		t.Cleanup(stopOnce)
		return stopOnce
	}
	agentID := func() string {
		e.store.mu.Lock()
		defer e.store.mu.Unlock()
		for id := range e.store.agents {
			return id
		}
		return ""
	}
	await := func(operation string) agentv1.Result {
		t.Helper()
		deadline := time.After(30 * time.Second)
		for {
			select {
			case result := <-results:
				if result.OperationID == operation && result.Status == agentv1.StatusSucceeded {
					return result
				}
			case <-deadline:
				t.Fatalf("no result for %s", operation)
			}
		}
	}

	stop := start(sharedToken)
	for deadline := time.Now().Add(20 * time.Second); agentID() == ""; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("agent did not enrol")
		}
	}
	id := agentID()
	// The first command may race the stream opening; the queue delivers it.
	e.publish(id, "op-compat-0001")
	await("op-compat-0001")

	stop()
	e.publish(id, "op-compat-0002") // queued while the agent is down
	start("")                       // restart without a token: the stored identity suffices
	await("op-compat-0002")
}
