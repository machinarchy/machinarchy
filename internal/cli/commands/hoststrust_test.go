package commands_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/commands"
)

const (
	keyA = "SHA256:Qm3vL0xk2G7c9R1tYp8aWuHn5dEoZf4sBjIiK6cXe9k"
	keyB = "SHA256:Zt9aP4rWq1mN6bV3cX8yL2kJ5hG7fD0sA9eU4iO1pRw"
)

// trustFixtures has web-01 waiting for approval, web-02 with a changed key and
// db-01 (in the protected env "dev") waiting for approval.
func trustFixtures() *fakeClient {
	client := fixtures()
	pending := func(host api.Host, keys ...string) api.Host {
		host.Metadata.Generation = 4
		host.Status.Conditions = []api.Condition{{Type: "Ready", Status: "False", Reason: "HostKeyPendingApproval"}}
		for _, key := range keys {
			host.Status.ObservedHostKeys = append(host.Status.ObservedHostKeys, api.ObservedHostKey{Algorithm: "ssh-ed25519", Fingerprint: key})
		}

		return host
	}
	client.hosts[0] = pending(client.hosts[0], keyA)
	client.hosts[1] = pending(client.hosts[1], keyB)
	client.hosts[1].Spec.AccessTarget.Trust = []string{keyA}
	client.hosts[1].Status.Conditions[0].Reason = "HostKeyChanged"
	client.hosts[2] = pending(client.hosts[2], keyA)

	return client
}

// trustRun runs the CLI with a fake terminal (or none) and the given answers.
func trustRun(t *testing.T, client *fakeClient, terminal bool, stdin string, args ...string) result {
	t.Helper()
	isolate(t)
	cfg := writeConfig(t, "http://127.0.0.1:1", "tok")
	// dev (env 2) is guarded on this computer.
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, append(data, []byte("    protected-envs: [dev]\n")...), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := cli.Run(context.Background(), cli.Options{
		Streams:       cli.IOStreams{In: strings.NewReader(stdin), Out: &out, ErrOut: &errOut},
		ClientBuilder: fake(client),
		Groups:        commands.DefaultGroups(),
		IsTerminal:    func(io.Reader) bool { return terminal },
	}, append([]string{"--config", cfg}, args...))

	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

// S-06: the first trust needs a typed yes on a terminal, never the --yes flag.
func TestHostsTrustTypedPrefix(t *testing.T) {
	client := trustFixtures()
	got := trustRun(t, client, true, "yes\n", "hosts", "trust", "web-01")
	if got.code != 0 || strings.Join(client.trusted, ";") != "1="+keyA || !strings.Contains(got.stdout, keyA) || !strings.Contains(got.stdout, "Key trusted") {
		t.Fatalf("typed yes = %+v, trusted %v", got, client.trusted)
	}

	for name, stdin := range map[string]string{"empty answer": "\n", "wrong text": "nope\n", "no": "no\n", "the key prefix": prefixOf(keyA) + "\n", "end of input": ""} {
		client := trustFixtures()
		got := trustRun(t, client, true, stdin, "hosts", "trust", "web-01", "--yes")
		if got.code == 0 || len(client.trusted) != 0 {
			t.Errorf("%s: trusted without typing yes: %+v", name, got)
		}
	}
	// y and yes (any case) trust; the --yes flag alone never does (see TestHostsTrustNeverFollowsYes).
	for _, answer := range []string{"y\n", "YES\n"} {
		client = trustFixtures()
		if got := trustRun(t, client, true, answer, "hosts", "trust", "web-01"); got.code != 0 || len(client.trusted) != 1 {
			t.Fatalf("%q = %+v", answer, got)
		}
	}
}

// S-07 and B5: with nothing to ask, --yes and --no-input trust nothing.
func TestHostsTrustNeverFollowsYes(t *testing.T) {
	for name, args := range map[string][]string{
		"yes":                {"hosts", "trust", "web-01", "--yes"},
		"yes and no-input":   {"hosts", "trust", "web-01", "--yes", "--no-input"},
		"all-pending yes":    {"hosts", "trust", "--all-pending", "--yes"},
		"all-pending script": {"hosts", "trust", "--all-pending", "--yes", "--no-input"},
	} {
		client := trustFixtures()
		got := trustRun(t, client, false, "", args...)
		if got.code != clierr.ExitUsage || len(client.trusted) != 0 {
			t.Errorf("%s = %+v, trusted %v", name, got, client.trusted)
		}
	}
	// Even on a terminal, --no-input refuses --all-pending (B10).
	client := trustFixtures()
	if got := trustRun(t, client, true, prefixOf(keyA)+"\n", "hosts", "trust", "--all-pending", "--no-input"); got.code != clierr.ExitUsage || len(client.trusted) != 0 {
		t.Fatalf("all-pending --no-input = %+v", got)
	}
	// The refusal names the way for a script.
	got := trustRun(t, trustFixtures(), false, "", "hosts", "trust", "web-01", "--yes")
	if !strings.Contains(got.stderr, "--fingerprint "+keyA) || !strings.Contains(got.stderr, "--yes does not approve") {
		t.Fatalf("refusal = %q", got.stderr)
	}
}

func prefixOf(key string) string { return strings.TrimPrefix(key, "SHA256:")[:8] }

// S-08: --fingerprint must be a key the server saw.
func TestHostsTrustFingerprint(t *testing.T) {
	client := trustFixtures()
	if got := trustRun(t, client, false, "", "hosts", "trust", "web-01", "--fingerprint", keyA); got.code != 0 || strings.Join(client.trusted, ";") != "1="+keyA {
		t.Fatalf("--fingerprint = %+v, trusted %v", got, client.trusted)
	}
	for name, fingerprint := range map[string]string{"unseen key": keyB, "malformed": "abc", "control bytes": "SHA256:abcdefghij\x1b[2J"} {
		client := trustFixtures()
		got := trustRun(t, client, false, "", "hosts", "trust", "web-01", "--fingerprint", fingerprint)
		if got.code != clierr.ExitUsage || len(client.trusted) != 0 {
			t.Errorf("%s = %+v", name, got)
		}
		noControlBytes(t, name, got.stdout+got.stderr)
	}
}

func TestHostsTrustRefusesWithoutAKeyOrWhenAlreadyTrusted(t *testing.T) {
	client := trustFixtures()
	client.hosts[3].Status.ObservedHostKeys = nil
	for name, args := range map[string][]string{
		"no key seen yet":              {"hosts", "trust", "dup", "--env", "prod", "--fingerprint", keyA},
		"already trusted, no replace":  {"hosts", "trust", "web-02", "--fingerprint", keyB},
		"replace with nothing trusted": {"hosts", "trust", "web-01", "--replace", "--fingerprint", keyA},
		"both name and all-pending":    {"hosts", "trust", "web-01", "--all-pending"},
		"neither":                      {"hosts", "trust"},
		"all-pending with replace":     {"hosts", "trust", "--all-pending", "--replace"},
	} {
		got := trustRun(t, client, true, "", args...)
		if got.code != clierr.ExitUsage || len(client.trusted) != 0 {
			t.Errorf("%s = %+v", name, got)
		}
	}
	if got := trustRun(t, client, true, "", "hosts", "trust", "web-02", "--fingerprint", keyB); !strings.Contains(got.stderr, "--replace") {
		t.Fatalf("already trusted: %q", got.stderr)
	}
}

// S-09: a changed key needs two typed answers; --yes is not one of them.
func TestHostsTrustReplace(t *testing.T) {
	client := trustFixtures()
	got := trustRun(t, client, true, prefixOf(keyB)+"\nweb-02\n", "hosts", "trust", "web-02", "--replace")
	if got.code != 0 || strings.Join(client.trusted, ";") != "2="+keyB || !strings.Contains(got.stdout, "Old   "+keyA) ||
		!strings.Contains(got.stdout, "New   "+keyB) || !strings.Contains(got.stdout, "new key") {
		t.Fatalf("replace = %+v, trusted %v", got, client.trusted)
	}

	for name, stdin := range map[string]string{
		"wrong prefix": "nope\nweb-02\n", "wrong name": prefixOf(keyB) + "\nweb-01\n",
		"no name": prefixOf(keyB) + "\n", "the old key's prefix": prefixOf(keyA) + "\nweb-02\n",
	} {
		client := trustFixtures()
		if got := trustRun(t, client, true, stdin, "hosts", "trust", "web-02", "--replace", "--yes"); got.code == 0 || len(client.trusted) != 0 {
			t.Errorf("%s: %+v", name, got)
		}
	}

	// Scripts: --fingerprint and --confirm NAME; --yes alone is not enough.
	client = trustFixtures()
	if got := trustRun(t, client, false, "", "hosts", "trust", "web-02", "--replace", "--fingerprint", keyB, "--yes"); got.code != clierr.ExitUsage || len(client.trusted) != 0 {
		t.Fatalf("replace without --confirm = %+v", got)
	}
	if got := trustRun(t, client, false, "", "hosts", "trust", "web-02", "--replace", "--fingerprint", keyB, "--confirm", "web-01"); got.code != clierr.ExitUsage || len(client.trusted) != 0 {
		t.Fatalf("replace with the wrong --confirm = %+v", got)
	}
	if got := trustRun(t, client, false, "", "hosts", "trust", "web-02", "--replace", "--fingerprint", keyB, "--confirm", "web-02"); got.code != 0 || len(client.trusted) != 1 {
		t.Fatalf("replace in a script = %+v", got)
	}
	// A key the host already trusts is not a replacement.
	client = trustFixtures()
	if got := trustRun(t, client, false, "", "hosts", "trust", "web-02", "--replace", "--fingerprint", keyA, "--confirm", "web-02"); got.code != clierr.ExitUsage || len(client.trusted) != 0 {
		t.Fatalf("replace with the old key = %+v", got)
	}
}

// A protected env also needs its name, for first trust and replacement.
func TestHostsTrustInAProtectedEnv(t *testing.T) {
	client := trustFixtures()
	if got := trustRun(t, client, false, "", "hosts", "trust", "db-01", "--fingerprint", keyA); got.code != clierr.ExitUsage || len(client.trusted) != 0 ||
		!strings.Contains(got.stderr, "--confirm-env dev") {
		t.Fatalf("protected, no confirmation = %+v", got)
	}
	if got := trustRun(t, client, false, "", "hosts", "trust", "db-01", "--fingerprint", keyA, "--confirm-env", "dev", "--yes"); got.code != 0 || len(client.trusted) != 1 {
		t.Fatalf("protected, confirmed = %+v", got)
	}
	client = trustFixtures()
	if got := trustRun(t, client, true, "dev\nyes\n", "hosts", "trust", "db-01"); got.code != 0 || len(client.trusted) != 1 {
		t.Fatalf("protected, typed = %+v", got)
	}
	client = trustFixtures()
	if got := trustRun(t, client, true, "prod\nyes\n", "hosts", "trust", "db-01"); got.code == 0 || len(client.trusted) != 0 {
		t.Fatalf("protected, wrong env name = %+v", got)
	}
}

func TestHostsTrustAllPendingAsksPerHost(t *testing.T) {
	// web-01 is typed, db-01 (protected dev) is skipped; web-02 is not pending.
	client := trustFixtures()
	got := trustRun(t, client, true, "yes\n"+"\n", "hosts", "trust", "--all-pending", "-l", "tier=web")
	if got.code != 0 || strings.Join(client.trusted, ";") != "1="+keyA || !strings.Contains(got.stdout, "1 trusted, 0 skipped") {
		t.Fatalf("all-pending = %+v, trusted %v", got, client.trusted)
	}

	client = trustFixtures()
	got = trustRun(t, client, true, "\ndev\n\n", "hosts", "trust", "--all-pending")
	if got.code != 0 || len(client.trusted) != 0 || !strings.Contains(got.stdout, "0 trusted, 2 skipped") {
		t.Fatalf("all skipped = %+v", got)
	}
	// One typed answer never approves the next host.
	client = trustFixtures()
	got = trustRun(t, client, true, "yes\n", "hosts", "trust", "--all-pending")
	if len(client.trusted) > 1 {
		t.Fatalf("one answer approved several hosts: %v", client.trusted)
	}
}

func TestOldTrustHostCommandIsGone(t *testing.T) {
	// The old command is gone.
	got := trustRun(t, trustFixtures(), true, "", "trust", "host", "web-01")
	if got.code == 0 || !strings.Contains(got.stderr, "was removed") || !strings.Contains(got.stderr, "hosts trust NAME") {
		t.Fatalf("old trust host = %+v", got)
	}
}

// F6: each fingerprint says which key type it is.
func TestHostsTrustLabelsKeyType(t *testing.T) {
	got := trustRun(t, trustFixtures(), true, "\n", "hosts", "trust", "web-01")
	if !strings.Contains(got.stdout, keyA+"  (ssh-ed25519)") {
		t.Fatalf("key type not shown:\n%s", got.stdout)
	}
}
