package commands_test

import (
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

const (
	hostileName   = "web-01\x1b[2J\x1b]52;c;ZXZpbA==\x07"
	hostileOutput = "start \x1b[31mred\x1b[0m \x1b]52;c;Zm9v\x07 end\n"
)

func noControlBytes(t *testing.T, name, text string) {
	t.Helper()

	for _, r := range text {
		if r == 0x1b || r == 0x07 || (r >= 0x80 && r <= 0x9f) || r == 0x7f {
			t.Fatalf("%s contains control character %U:\n%q", name, r, text)
		}
	}
}

// S-13 / S-14: nothing a server or a remote host sends reaches the terminal
// as a control sequence.
func TestRemoteTextCannotDriveTheTerminal(t *testing.T) {
	cfg := writeConfig(t, "http://127.0.0.1:1", "tok")
	run := func(t *testing.T, client *fakeClient, args ...string) result {
		t.Helper()

		return execute(t, fake(client), "", append([]string{"--config", cfg}, args...)...)
	}

	t.Run("host names in a table", func(t *testing.T) {
		client := fixtures()
		client.hosts[0].Metadata.Name = hostileName
		got := run(t, client, "hosts")
		if got.code != 0 {
			t.Fatalf("result = %+v", got)
		}
		noControlBytes(t, "hosts", got.stdout+got.stderr)
		if !strings.Contains(got.stdout, "web-01") {
			t.Fatalf("the name must survive, cleaned:\n%s", got.stdout)
		}
	})

	t.Run("the trust prompt", func(t *testing.T) {
		client := fixtures()
		client.hosts[0].Metadata.Name = hostileName
		client.hosts[0].Metadata.Generation = 4
		client.hosts[0].Spec.AccessTarget.Endpoint = "10.0.0.1:22\x1b[2J"
		client.hosts[0].Status.Conditions = []api.Condition{{Type: "Ready", Status: "False", Reason: "HostKeyPendingApproval"}}
		client.hosts[0].Status.ObservedHostKeys = []api.ObservedHostKey{{Algorithm: "ssh-ed25519", Fingerprint: "SHA256:abcdefghijklmnop"}}

		got := run(t, client, "hosts", "trust", client.hosts[0].Metadata.ID)
		noControlBytes(t, "trust prompt", got.stdout+got.stderr)
		if got.code != clierr.ExitUsage || !strings.Contains(got.stdout, "SHA256:abcdefghijklmnop") {
			t.Fatalf("result = %+v", got)
		}

		// A key with control bytes is refused outright, never shown as a prompt.
		client.hosts[0].Status.ObservedHostKeys[0].Fingerprint = "SHA256:abc\x1b[31m"
		got = run(t, client, "hosts", "trust", client.hosts[0].Metadata.ID)
		noControlBytes(t, "trust refusal", got.stdout+got.stderr)
		if got.code != clierr.ExitFailure || len(client.trusted) != 0 {
			t.Fatalf("hostile key = %+v", got)
		}
	})
}
