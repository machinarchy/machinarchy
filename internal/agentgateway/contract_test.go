package agentgateway

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// TestProtocolGoldenFixtures decodes and verifies every golden wire fixture
// that the protocol module ships, with its published test keys (seed 0x01
// repeated for the controller, 0x02 for the agent). A wire change on either
// side fails here before it fails in production (AUDIT M-11).
func TestProtocolGoldenFixtures(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/machinarchy/machinarchy-agent/protocol").Output()
	if err != nil {
		t.Fatalf("locate the protocol module: %v", err)
	}
	dir := filepath.Join(strings.TrimSpace(string(out)), "agentv1", "testdata")
	fixtures, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no golden fixtures in %s (%v)", dir, err)
	}
	controllerKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	agentKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{2}, ed25519.SeedSize)).Public().(ed25519.PublicKey)

	// Each check verifies with the given controller and agent keys; called
	// again with the keys swapped, it must fail.
	verify := map[string]func(raw []byte, controller, agent ed25519.PublicKey) error{
		"command.json": func(raw []byte, controller, _ ed25519.PublicKey) error {
			var cmd agentv1.Command
			return agentv1.Open(controller, agentv1.KindCommand, raw, agentv1.MaxCommandMessageBytes, &cmd)
		},
		"register_response.json": func(raw []byte, controller, _ ed25519.PublicKey) error {
			var response agentv1.RegisterResponse
			if err := agentv1.Open(controller, agentv1.KindRegisterResponse, raw, agentv1.MaxControlMessageBytes, &response); err != nil {
				return err
			}
			if response.NATSUserJWT == "" {
				t.Error("register_response.json should exercise nats_user_jwt")
			}
			return nil
		},
		"renew_response.json": func(raw []byte, controller, _ ed25519.PublicKey) error {
			var response agentv1.RenewResponse
			return agentv1.Open(controller, agentv1.KindRenewResponse, raw, agentv1.MaxControlMessageBytes, &response)
		},
		"result.json": func(raw []byte, _, agent ed25519.PublicKey) error {
			var result agentv1.Result
			keyFor := func(string) (ed25519.PublicKey, bool) { return agent, true }
			return agentv1.OpenFromAgent(agentv1.KindResult, raw, agentv1.MaxResultMessageBytes, keyFor, &result)
		},
	}
	seen := map[string]bool{}
	for _, path := range fixtures {
		name := filepath.Base(path)
		check, known := verify[name]
		if !known {
			t.Errorf("fixture %s is not verified here: add it to this test", name)
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		raw = bytes.TrimSpace(raw)
		if err := check(raw, controllerKey, agentKey); err != nil {
			t.Errorf("%s does not verify: %v", name, err)
		}
		if check(raw, agentKey, controllerKey) == nil {
			t.Errorf("%s verifies with the other party's key", name)
		}
		seen[name] = true
	}
	for name := range verify {
		if !seen[name] {
			t.Errorf("expected fixture %s is missing from %s", name, dir)
		}
	}
}
