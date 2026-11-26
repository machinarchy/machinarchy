package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/cred"
)

type memoryCreds map[string]cred.Entry

func (m memoryCreds) Get(server string) (cred.Entry, error) { return m[server], nil }
func (m memoryCreds) Put(server string, c cred.Credential) error {
	entry := m[server]
	if cred.SlotOf(c.Kind) == cred.SlotSession {
		entry.Session = &c
	} else {
		entry.Token = &c
	}
	m[server] = entry

	return nil
}
func (m memoryCreds) Delete(string, ...cred.Slot) error { return nil }
func (m memoryCreds) Where() string                     { return "memory" }

var now = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func expires(d time.Duration) *time.Time { t := now.Add(d); return &t }

const twoServers = "version: 2\nactive: lab\nservers:\n  lab:\n    url: https://lab.example\n    env: production\n    ca-file: ca/lab.pem\n  staging:\n    url: https://staging.example\n"

func TestResolveChoosesTheServer(t *testing.T) {
	creds := memoryCreds{
		"lab":     {Token: &cred.Credential{Kind: cred.KindToken, Secret: "lab-token"}},
		"staging": {Token: &cred.Credential{Kind: cred.KindToken, Secret: "staging-token"}},
	}

	t.Run("the active server", func(t *testing.T) {
		target, err := load(t, twoServers).Resolve(creds, NeedAny, now)
		if err != nil || target.ServerName != "lab" || target.Server != "https://lab.example" || target.Token != "lab-token" || target.Environment != "production" {
			t.Fatalf("target = %+v, %v", target, err)
		}
		if !strings.HasSuffix(target.CAFile, filepath.Join("ca", "lab.pem")) || !filepath.IsAbs(target.CAFile) {
			t.Fatalf("the CA path is relative to the config directory: %q", target.CAFile)
		}
	})

	t.Run("a saved name", func(t *testing.T) {
		target, err := load(t, twoServers, "--server", "staging").Resolve(creds, NeedAny, now)
		if err != nil || target.ServerName != "staging" || target.Token != "staging-token" || target.Environment != "" {
			t.Fatalf("target = %+v, %v", target, err)
		}
	})

	t.Run("an address is for one command and takes no saved credential", func(t *testing.T) {
		_, err := load(t, twoServers, "--server", "https://other.example").Resolve(creds, NeedAny, now)
		if err == nil || !strings.Contains(err.Error(), "you have no credential for https://other.example") ||
			!strings.Contains(clierr.Why(err), "only sent to the server they were saved for") ||
			!strings.Contains(clierr.Hint(err), "MACHINARCHY_TOKEN=...") {
			t.Fatalf("err = %v", err)
		}
		if code, _ := clierr.Classify(err); code != clierr.ExitAuth {
			t.Fatalf("exit code = %d", code)
		}
	})

	t.Run("an address with MACHINARCHY_TOKEN", func(t *testing.T) {
		store := load(t, twoServers, "--server", "other.example")
		t.Setenv("MACHINARCHY_TOKEN", "env-token")
		target, err := store.Resolve(creds, NeedAny, now)
		if err != nil || target.Server != "https://other.example" || target.Token != "env-token" || !target.FromEnv || target.ServerName != "" {
			t.Fatalf("target = %+v, %v", target, err)
		}
	})

	t.Run("an unknown word is not an address", func(t *testing.T) {
		_, err := load(t, twoServers, "--server", "nope").Resolve(creds, NeedAny, now)
		if err == nil || !strings.Contains(err.Error(), `no saved server is called "nope"`) || !strings.Contains(clierr.Why(err), "saved servers: lab, staging") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("no server at all", func(t *testing.T) {
		_, err := load(t, "").Resolve(creds, NeedAny, now)
		if err == nil || !strings.Contains(err.Error(), "you are not signed in to any server") || clierr.Hint(err) != "machinarchy login YOUR-SERVER-URL" {
			t.Fatalf("err = %v / %q", err, clierr.Hint(err))
		}
	})

	t.Run("the active server was removed from the file", func(t *testing.T) {
		_, err := load(t, "version: 2\nactive: gone\n").Resolve(creds, NeedAny, now)
		if err == nil || !strings.Contains(err.Error(), `no saved server is called "gone"`) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("the environment variable picks the server too", func(t *testing.T) {
		store := load(t, twoServers)
		t.Setenv("MACHINARCHY_SERVER", "staging")
		if target, err := store.Resolve(creds, NeedAny, now); err != nil || target.ServerName != "staging" {
			t.Fatalf("target = %+v, %v", target, err)
		}
		if name := store.ResolveServerName(); name != "staging" {
			t.Fatalf("ResolveServerName = %q", name)
		}
	})
}

func TestResolveRefusesPlainHTTPUnlessTheServerAllowsIt(t *testing.T) {
	creds := memoryCreds{"lab": {Token: &cred.Credential{Kind: cred.KindToken, Secret: "t"}}}

	_, err := load(t, "version: 2\nactive: lab\nservers:\n  lab:\n    url: http://machinarchy.example\n").Resolve(creds, NeedAny, now)
	if err == nil || !strings.Contains(err.Error(), "refusing to send your credentials over plain http://") {
		t.Fatalf("err = %v", err)
	}
	if _, err := load(t, "version: 2\nactive: lab\nservers:\n  lab:\n    url: http://machinarchy.example\n    insecure-http: true\n").Resolve(creds, NeedAny, now); err != nil {
		t.Fatalf("the server's own opt-in must be honoured: %v", err)
	}
}

func TestResolveEnvironmentScope(t *testing.T) {
	creds := memoryCreds{"lab": {Token: &cred.Credential{Kind: cred.KindToken, Secret: "t"}}}

	store := load(t, twoServers)
	if target, _ := store.Resolve(creds, NeedAny, now); target.Environment != "production" || store.Environment() != "production" {
		t.Fatalf("the server's default environment applies: %q / %q", target.Environment, store.Environment())
	}
	flagged := load(t, twoServers, "--env", "staging")
	if target, _ := flagged.Resolve(creds, NeedAny, now); target.Environment != "staging" || flagged.Environment() != "staging" {
		t.Fatalf("--env wins: %q", target.Environment)
	}
	var nilStore *Store
	if nilStore.Environment() != "" {
		t.Fatal("a nil store has no environment")
	}
}

func TestPick(t *testing.T) {
	session := &cred.Credential{Kind: cred.KindSession, Secret: "session"}
	device := &cred.Credential{Kind: cred.KindDeviceToken, Secret: "device", Name: "device-laptop", Role: "operator"}
	saved := &cred.Credential{Kind: cred.KindToken, Secret: "saved", Name: "ci"}

	tests := []struct {
		name       string
		need       Need
		env        string
		entry      cred.Entry
		wantSecret string
		wantErr    string
	}{
		// B-14: environment, then saved token, then saved session.
		{"environment first", NeedAny, "env", cred.Entry{Session: session, Token: saved}, "env", ""},
		{"saved token next", NeedAny, "", cred.Entry{Session: session, Token: saved}, "saved", ""},
		{"saved session last", NeedAny, "", cred.Entry{Session: session}, "session", ""},
		{"a known-expired token is skipped for the session", NeedAny, "", cred.Entry{Session: session, Token: &cred.Credential{Kind: cred.KindToken, Secret: "old", ExpiresAt: expires(-time.Hour)}}, "session", ""},
		{"nothing saved", NeedAny, "", cred.Entry{}, "", "you are not signed in to lab"},
		{"only an expired token", NeedAny, "", cred.Entry{Token: &cred.Credential{Kind: cred.KindToken, Secret: "old", ExpiresAt: expires(-time.Hour)}}, "", "the saved API token for lab expired at"},
		{"only an expired session", NeedAny, "", cred.Entry{Session: &cred.Credential{Kind: cred.KindSession, Secret: "old", ExpiresAt: expires(-time.Hour)}}, "", "your session on lab expired at"},

		// An owner command takes the session and nothing else.
		{"owner with a session", NeedOwner, "env", cred.Entry{Session: session, Token: saved}, "session", ""},
		{"owner with a device token only", NeedOwner, "", cred.Entry{Token: device}, "", "this needs an owner signed in with a password; you are using an API token (device-laptop, operator)"},
		{"owner with a token without a name", NeedOwner, "", cred.Entry{Token: &cred.Credential{Kind: cred.KindToken, Secret: "x"}}, "", "you are using an API token\n"},
		{"owner with an environment token", NeedOwner, "env", cred.Entry{}, "", "you are using an API token (from MACHINARCHY_TOKEN)"},
		{"owner with an expired session", NeedOwner, "", cred.Entry{Session: &cred.Credential{Kind: cred.KindSession, Secret: "old", ExpiresAt: expires(-time.Hour)}, Token: device}, "", "your session on lab expired at"},
		{"owner with nothing", NeedOwner, "", cred.Entry{}, "", "you are not signed in to lab"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pick(tc.need, tc.env, tc.entry, now, "lab", "machinarchy login")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error()+"\n", tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				if code, _ := clierr.Classify(err); code != clierr.ExitAuth {
					t.Errorf("exit code = %d, want 3", code)
				}
				if clierr.Hint(err) != "machinarchy login" {
					t.Errorf("next = %q", clierr.Hint(err))
				}
				if strings.Contains(err.Error()+clierr.Why(err), "session") && strings.Contains(err.Error(), "secret") {
					t.Error("the error mentions a secret")
				}

				return
			}
			if err != nil || got.Token != tc.wantSecret {
				t.Fatalf("token = %q, %v; want %q", got.Token, err, tc.wantSecret)
			}
		})
	}
}

func TestPickReportsWhatTheCredentialIs(t *testing.T) {
	entry := cred.Entry{Token: &cred.Credential{Kind: cred.KindDeviceToken, Secret: "s", Name: "device-laptop", Role: "operator"}}
	got, err := pick(NeedAny, "", entry, now, "lab", "machinarchy login")
	if err != nil || got.Kind != cred.KindDeviceToken || got.CredentialName != "device-laptop" || got.Role != "operator" || got.FromEnv {
		t.Fatalf("target = %+v, %v", got, err)
	}
	if env, _ := pick(NeedAny, "e", cred.Entry{}, now, "lab", "x"); !env.FromEnv || env.Kind != cred.KindToken {
		t.Fatalf("env target = %+v", env)
	}
}

func TestLoginCommandNamesTheRightServer(t *testing.T) {
	creds := memoryCreds{}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "machinarchy login"},
		{[]string{"--server", "staging"}, "machinarchy login staging"},
		{[]string{"--server", "lab"}, "machinarchy login lab"},
	} {
		_, err := load(t, twoServers, tc.args...).Resolve(creds, NeedAny, now)
		if err == nil || clierr.Hint(err) != tc.want {
			t.Errorf("%v: next = %q, want %q (err %v)", tc.args, clierr.Hint(err), tc.want, err)
		}
	}
}

func TestResolveRejectsAnInvalidToken(t *testing.T) {
	store := load(t, twoServers)
	t.Setenv("MACHINARCHY_TOKEN", "has a space")
	if _, err := store.Resolve(memoryCreds{}, NeedAny, now); err == nil || strings.Contains(err.Error(), "has a space") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveOnAnUnloadedStore(t *testing.T) {
	var nilStore *Store
	if _, err := nilStore.Resolve(memoryCreds{}, NeedAny, now); err == nil {
		t.Fatal("a nil store resolved")
	}
	if nilStore.ResolveServerName() != "" {
		t.Fatal("a nil store has no server")
	}
}
