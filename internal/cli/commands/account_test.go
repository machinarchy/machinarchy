package commands_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/config"
	"github.com/machinarchy/machinarchy/internal/cli/cred"
)

func past() *time.Time   { t := time.Now().Add(-time.Hour); return &t }
func future() *time.Time { t := time.Now().Add(48 * time.Hour); return &t }

// B-20
func TestWhoami(t *testing.T) {
	server := newAuthServer(t, false)
	server.addSession("sess-saved")
	server.addToken("mch_pat_device-0001", "device-laptop", "operator")
	server.addToken("mch_pat_env-0001", "env", "reader")

	t.Run("a password session", func(t *testing.T) {
		path := seed(t, server.URL, cred.Credential{Kind: cred.KindSession, Secret: "sess-saved", User: "maxim"})
		got := runAuth(t, authRun{config: path}, "whoami")
		if got.code != 0 {
			t.Fatalf("result = %+v", got)
		}
		for _, want := range []string{"Server:", "lab (" + server.URL + ")", "User:           maxim", "Signed in with: password session",
			"Role:           not reported by this server", "Expires:", "(in 24 hours)", "Env:            none"} {
			if !strings.Contains(got.stdout, want) {
				t.Errorf("whoami is missing %q:\n%s", want, got.stdout)
			}
		}
	})

	t.Run("a device token", func(t *testing.T) {
		path := seed(t, server.URL, cred.Credential{Kind: cred.KindDeviceToken, Secret: "mch_pat_device-0001", User: "maxim", Name: "device-laptop", Role: "operator", ExpiresAt: future()})
		got := runAuth(t, authRun{config: path}, "whoami", "-o", "json")
		var doc map[string]any
		if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil {
			t.Fatalf("whoami -o json: %v\n%s", err, got.stdout)
		}
		if doc["method"] != "device token" || doc["tokenName"] != "device-laptop" || doc["role"] != "operator" || doc["user"] != "maxim" || doc["expiresAt"] == nil {
			t.Fatalf("document = %v", doc)
		}
	})

	t.Run("a token from the environment is not saved anywhere", func(t *testing.T) {
		path := configPath(t)
		got := runAuth(t, authRun{config: path, env: map[string]string{"MACHINARCHY_TOKEN": "mch_pat_env-0001", "MACHINARCHY_SERVER": server.URL}}, "whoami")
		if got.code != 0 || !strings.Contains(got.stdout, "API token from MACHINARCHY_TOKEN") || !strings.Contains(got.stdout, "Expires:        unknown") {
			t.Fatalf("result = %+v", got)
		}
		if _, err := os.Stat(path); err == nil {
			t.Fatal("a token from the environment must never be written to disk")
		}
		if _, err := os.Stat(config.CredentialsPath(path)); err == nil {
			t.Fatal("a token from the environment must never be written to disk")
		}
	})

	t.Run("not signed in", func(t *testing.T) {
		got := runAuth(t, authRun{}, "whoami")
		if got.code != clierr.ExitAuth || !strings.Contains(got.stderr, "Error: you are not signed in to any server\n") || !strings.Contains(got.stderr, "Next:  machinarchy login YOUR-SERVER-URL\n") {
			t.Fatalf("result = %+v", got)
		}
	})
}

// B-14: MACHINARCHY_TOKEN, then the saved token, then the saved session.
func TestCredentialPrecedence(t *testing.T) {
	server := newAuthServer(t, false)
	server.addSession("sess-saved")
	server.addToken("mch_pat_saved-0001", "saved", "operator")
	server.addToken("mch_pat_env-0001", "env", "reader")
	path := seed(t, server.URL,
		cred.Credential{Kind: cred.KindSession, Secret: "sess-saved"},
		cred.Credential{Kind: cred.KindToken, Secret: "mch_pat_saved-0001"})

	lastAuth := func() string {
		hosts := server.saw("GET", "/api/v1/managed/hosts")

		return hosts[len(hosts)-1].Auth
	}

	if got := runAuth(t, authRun{config: path, env: map[string]string{"MACHINARCHY_TOKEN": "mch_pat_env-0001"}}, "get", "hosts", "-o", "json"); got.code != 0 || lastAuth() != "Bearer mch_pat_env-0001" {
		t.Fatalf("the environment token must come first: %+v / %q", got, lastAuth())
	}
	if got := runAuth(t, authRun{config: path}, "get", "hosts", "-o", "json"); got.code != 0 || lastAuth() != "Bearer mch_pat_saved-0001" {
		t.Fatalf("the saved token must come next: %+v / %q", got, lastAuth())
	}
	if err := cred.NewFile(config.CredentialsPath(path)).Delete("lab", cred.SlotToken); err != nil {
		t.Fatal(err)
	}
	if got := runAuth(t, authRun{config: path}, "get", "hosts", "-o", "json"); got.code != 0 || lastAuth() != "Bearer sess-saved" {
		t.Fatalf("the saved session must come last: %+v / %q", got, lastAuth())
	}
}

// R3: a saved credential goes only to the server it was saved for (B-12).
func TestSavedCredentialsNeverGoToAnotherServer(t *testing.T) {
	lab, other := newAuthServer(t, false), newAuthServer(t, false)
	lab.addToken("mch_pat_lab-0001", "lab", "operator")
	path := seed(t, lab.URL, cred.Credential{Kind: cred.KindToken, Secret: "mch_pat_lab-0001"})

	for name, run := range map[string]authRun{
		"--server":           {config: path},
		"MACHINARCHY_SERVER": {config: path, env: map[string]string{"MACHINARCHY_SERVER": other.URL}},
	} {
		args := []string{"get", "hosts"}
		if name == "--server" {
			args = append([]string{"--server", other.URL}, args...)
		}
		got := runAuth(t, run, args...)
		if got.code != clierr.ExitAuth || !strings.Contains(got.stderr, "you have no credential for "+other.URL) ||
			!strings.Contains(got.stderr, "saved credentials are only sent to the server they were saved for") {
			t.Fatalf("%s: result = %+v", name, got)
		}
	}
	if len(other.log()) != 0 {
		t.Fatalf("the other server received requests: %+v", other.log())
	}

	// A saved server can be chosen by name; that is the same server.
	got := runAuth(t, authRun{config: path}, "--server", "lab", "get", "hosts")
	if got.code != 0 {
		t.Fatalf("result = %+v", got)
	}
}

// The requirement: an owner-only action with an operator token gives the
// explanation and the next command, and sends nothing.
func TestOwnerOnlyActionWithAnOperatorToken(t *testing.T) {
	server := newAuthServer(t, false)
	server.addToken("mch_pat_device-0002", "device-laptop", "operator")
	path := seed(t, server.URL, cred.Credential{Kind: cred.KindDeviceToken, Secret: "mch_pat_device-0002", User: "maxim", Name: "device-laptop", Role: "operator", ExpiresAt: future()})

	for _, args := range [][]string{
		{"tokens"},
		{"tokens", "create", "ci", "--role", "reader"},
		{"tokens", "revoke", "ci", "--confirm", "ci"},
		{"secret", "list"},
		{"runtime-config", "get"},
		{"hosts", "trust", "web-01", "--fingerprint", "SHA256:abcdefghijklmnop"},
		{"create", "profile", "p", "--connector", "agent"},
		{"get", "agent-tokens"},
	} {
		server.reset()
		got := runAuth(t, authRun{config: path}, args...)
		if got.code != clierr.ExitAuth {
			t.Errorf("%v: exit = %d, want 3\n%s", args, got.code, got.stderr)
		}
		want := "Error: this needs an owner signed in with a password; you are using an API token (device-laptop, operator)\n" +
			"Why:   API tokens can never act as the owner\n" +
			"Next:  machinarchy login\n"
		if got.stderr != want || got.stdout != "" {
			t.Errorf("%v: stderr = %q", args, got.stderr)
		}
		if len(server.log()) != 0 {
			t.Errorf("%v: a request was sent before the refusal: %+v", args, server.log())
		}
		if strings.Contains(got.stderr, "SECRET") || strings.Contains(got.stderr, "mch_pat_device") {
			t.Errorf("%v: the token was printed", args)
		}
	}

	// A token from the environment says where it came from.
	got := runAuth(t, authRun{config: configPath(t), env: map[string]string{"MACHINARCHY_TOKEN": "mch_pat_env-0002", "MACHINARCHY_SERVER": server.URL}}, "tokens")
	if got.code != clierr.ExitAuth || !strings.Contains(got.stderr, "you are using an API token (from MACHINARCHY_TOKEN)") || !strings.Contains(got.stderr, "Next:  machinarchy login "+server.URL+"\n") {
		t.Fatalf("env token result = %+v", got)
	}
}

// With a login session an owner command runs, and it runs as the session.
func TestOwnerCommandsUseTheSessionEvenWithATokenSaved(t *testing.T) {
	server := newAuthServer(t, false)
	server.addSession("sess-owner")
	server.addToken("mch_pat_device-0003", "device-laptop", "operator")
	path := seed(t, server.URL,
		cred.Credential{Kind: cred.KindSession, Secret: "sess-owner"},
		cred.Credential{Kind: cred.KindDeviceToken, Secret: "mch_pat_device-0003", Name: "device-laptop", Role: "operator"})

	if got := runAuth(t, authRun{config: path}, "tokens"); got.code != 0 {
		t.Fatalf("result = %+v", got)
	}
	if list := server.saw("GET", "/api/v1/tokens"); len(list) != 1 || list[0].Auth != "Bearer sess-owner" {
		t.Fatalf("an owner command must run as the session: %+v", list)
	}
	// The same server, an ordinary command: the token comes first.
	if got := runAuth(t, authRun{config: path}, "get", "hosts"); got.code != 0 {
		t.Fatalf("result = %+v", got)
	}
	if hosts := server.saw("GET", "/api/v1/managed/hosts"); len(hosts) != 1 || hosts[0].Auth != "Bearer mch_pat_device-0003" {
		t.Fatalf("an ordinary command uses the token: %+v", hosts)
	}
}

// B-03, B-05 and B-19: expired and revoked credentials.
func TestExpiredAndRevokedCredentials(t *testing.T) {
	server := newAuthServer(t, false)

	t.Run("an expired session is named, and nothing is sent", func(t *testing.T) {
		path := seed(t, server.URL, cred.Credential{Kind: cred.KindSession, Secret: "sess-old", ExpiresAt: past()})
		server.reset()
		got := runAuth(t, authRun{config: path}, "get", "hosts")
		if got.code != clierr.ExitAuth || !strings.Contains(got.stderr, "Error: your session on lab expired at ") || !strings.Contains(got.stderr, "Next:  machinarchy login\n") {
			t.Fatalf("result = %+v", got)
		}
		if len(server.log()) != 0 {
			t.Fatalf("a request was sent with a credential known to be expired: %+v", server.log())
		}
	})

	t.Run("an expired device token is named", func(t *testing.T) {
		path := seed(t, server.URL, cred.Credential{Kind: cred.KindDeviceToken, Secret: "mch_pat_old", ExpiresAt: past()})
		got := runAuth(t, authRun{config: path}, "get", "hosts")
		if got.code != clierr.ExitAuth || !strings.Contains(got.stderr, "the saved API token for lab expired at ") || !strings.Contains(got.stderr, "Next:  machinarchy login\n") {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("a device token with an expired session cannot do owner actions", func(t *testing.T) {
		server.addToken("mch_pat_device-0004", "device-laptop", "operator")
		path := seed(t, server.URL,
			cred.Credential{Kind: cred.KindSession, Secret: "sess-old", ExpiresAt: past()},
			cred.Credential{Kind: cred.KindDeviceToken, Secret: "mch_pat_device-0004", Name: "device-laptop", Role: "operator", ExpiresAt: future()})
		server.reset()
		got := runAuth(t, authRun{config: path}, "tokens", "create", "x", "--role", "reader")
		if got.code != clierr.ExitAuth || !strings.Contains(got.stderr, "Error: your session on lab expired at ") ||
			!strings.Contains(got.stderr, "Why:   this command acts as the owner, and only a login session can\n") || !strings.Contains(got.stderr, "Next:  machinarchy login\n") {
			t.Fatalf("result = %+v", got)
		}
		if len(server.log()) != 0 {
			t.Fatalf("a request was sent: %+v", server.log())
		}
		// Ordinary commands still work with the token.
		if got := runAuth(t, authRun{config: path}, "get", "hosts"); got.code != 0 {
			t.Fatalf("the device token must still work for ordinary commands: %+v", got)
		}
	})

	t.Run("a revoked token is not called a permission problem", func(t *testing.T) {
		path := seed(t, server.URL, cred.Credential{Kind: cred.KindDeviceToken, Secret: "mch_pat_revoked-0001", ExpiresAt: future()})
		got := runAuth(t, authRun{config: path}, "get", "hosts")
		if got.code != clierr.ExitAuth || !strings.Contains(got.stderr, "Why:   The server did not accept the credential; it may have expired or been revoked.\n") ||
			!strings.Contains(got.stderr, "Next:  machinarchy login\n") {
			t.Fatalf("result = %+v", got)
		}
	})
}

// B-15: logging out ends the session on the server too.
func TestLogoutEndsTheSessionOnTheServer(t *testing.T) {
	server := newAuthServer(t, false)
	path := configPath(t)

	got := runAuth(t, authRun{stdin: testPassword + "\n", config: path}, "login", server.URL, "--name", "lab", "--email", testEmail, "--password-stdin", "--no-stay-signed-in")
	if got.code != 0 {
		t.Fatalf("login = %+v", got)
	}
	session := savedCredentials(t, path, "lab").Session.Secret

	got = runAuth(t, authRun{config: path}, "logout")
	if got.code != 0 || !strings.Contains(got.stdout, "Signed out of lab.") {
		t.Fatalf("logout = %+v", got)
	}
	if out := server.saw("POST", "/api/auth/logout"); len(out) != 1 || out[0].Auth != "Bearer "+session {
		t.Fatalf("the server must be told: %+v", out)
	}
	if kind, _ := server.identify(session); kind != "" {
		t.Fatal("the session still works on the server after logout")
	}
	if entry := savedCredentials(t, path, "lab"); !entry.Empty() {
		t.Fatalf("credentials remain on this computer: %+v", entry)
	}
	// The server itself stays saved, so signing in again needs no address.
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "lab:") { // #nosec G304 -- test path
		t.Fatalf("config = %q", data)
	}
	if got := runAuth(t, authRun{config: path}, "get", "hosts"); got.code != clierr.ExitAuth || !strings.Contains(got.stderr, "you are not signed in to lab") {
		t.Fatalf("after logout = %+v", got)
	}
}

// Logging out also revokes the device token this CLI created.
func TestLogoutRevokesTheDeviceToken(t *testing.T) {
	server := newAuthServer(t, false)
	path := configPath(t)

	runAuth(t, authRun{stdin: testPassword + "\n", config: path}, "login", server.URL, "--name", "lab", "--email", testEmail, "--password-stdin", "--stay-signed-in")
	device := savedCredentials(t, path, "lab").Token
	if device == nil {
		t.Fatal("no device token was saved")
	}

	got := runAuth(t, authRun{config: path}, "logout")
	if got.code != 0 {
		t.Fatalf("logout = %+v", got)
	}
	if revoked := server.saw("DELETE", "/api/v1/tokens/"+itoa(device.TokenID)); len(revoked) != 1 {
		t.Fatalf("the device token must be revoked on the server: %+v", server.log())
	}
	if kind, _ := server.identify(device.Secret); kind != "" {
		t.Fatal("the device token still works after logout")
	}
}

// B-16: an API token that was supplied by the person is only removed locally.
func TestLogoutWithAnAPITokenOnlyRemovesTheLocalCopy(t *testing.T) {
	server := newAuthServer(t, false)
	server.addToken("mch_pat_ci-0001", "ci", "operator")
	path := seed(t, server.URL, cred.Credential{Kind: cred.KindToken, Secret: "mch_pat_ci-0001", Name: "ci"})

	got := runAuth(t, authRun{config: path}, "logout")
	if got.code != 0 || !strings.Contains(got.stdout, "The saved token was removed from this computer. It still works until it expires or is revoked.") ||
		!strings.Contains(got.stdout, "Next:  machinarchy tokens revoke ci") {
		t.Fatalf("result = %+v", got)
	}
	if len(server.log()) != 0 {
		t.Fatalf("nothing may be sent for a token logout: %+v", server.log())
	}
	if kind, _ := server.identify("mch_pat_ci-0001"); kind != "token" {
		t.Fatal("the token must keep working: it was not revoked")
	}
	if entry := savedCredentials(t, path, "lab"); !entry.Empty() {
		t.Fatalf("the local copy remains: %+v", entry)
	}
}

// B-17
func TestLogoutEverywhere(t *testing.T) {
	server := newAuthServer(t, false)
	server.addSession("sess-a")
	server.addSession("sess-b")

	t.Run("it asks first, and --yes answers", func(t *testing.T) {
		path := seed(t, server.URL, cred.Credential{Kind: cred.KindSession, Secret: "sess-a"})
		got := runAuth(t, authRun{config: path}, "logout", "--everywhere")
		if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "confirmation required") {
			t.Fatalf("without --yes = %+v", got)
		}
		if len(server.log()) != 0 || savedCredentials(t, path, "lab").Empty() {
			t.Fatal("nothing may change before the confirmation")
		}

		got = runAuth(t, authRun{config: path}, "logout", "--everywhere", "--yes")
		if got.code != 0 || !strings.Contains(got.stdout, "Every login session of your account has ended.") {
			t.Fatalf("with --yes = %+v", got)
		}
		if len(server.saw("DELETE", "/api/v1/sessions")) != 1 {
			t.Fatalf("log = %+v", server.log())
		}
		if kind, _ := server.identify("sess-b"); kind != "" {
			t.Fatal("another session survived --everywhere")
		}
	})

	t.Run("it needs a login session", func(t *testing.T) {
		server.addToken("mch_pat_ci-0002", "ci", "operator")
		path := seed(t, server.URL, cred.Credential{Kind: cred.KindToken, Secret: "mch_pat_ci-0002"})
		got := runAuth(t, authRun{config: path}, "logout", "--everywhere", "--yes")
		if got.code != clierr.ExitAuth || !strings.Contains(got.stderr, "--everywhere needs a login session") {
			t.Fatalf("result = %+v", got)
		}
	})
}

// If the server cannot be told, the credentials still leave this computer and
// the command says the session may still be active.
func TestLogoutWhenTheServerIsUnreachable(t *testing.T) {
	server := newAuthServer(t, false)
	url := server.URL
	server.Close()
	path := seed(t, url, cred.Credential{Kind: cred.KindSession, Secret: "sess-gone"})

	got := runAuth(t, authRun{config: path}, "logout")
	if got.code != clierr.ExitUnavailable || !strings.Contains(got.stderr, "Signed out locally, but the server could not be told") ||
		!strings.Contains(got.stderr, "The session may still be active on the server.") || !strings.Contains(got.stderr, "machinarchy logout") {
		t.Fatalf("result = %+v", got)
	}
	if entry := savedCredentials(t, path, "lab"); !entry.Empty() {
		t.Fatalf("the credentials must be removed anyway: %+v", entry)
	}
}

func TestLogoutWhenNothingIsSaved(t *testing.T) {
	server := newAuthServer(t, false)
	path := seed(t, server.URL)

	got := runAuth(t, authRun{config: path}, "logout")
	if got.code != 0 || !strings.Contains(got.stdout, "You are not signed in to lab.") {
		t.Fatalf("result = %+v", got)
	}
}

func TestSwitch(t *testing.T) {
	first, second := newAuthServer(t, false), newAuthServer(t, false)
	path := seed(t, first.URL, cred.Credential{Kind: cred.KindSession, Secret: "sess-1"})
	store := cred.NewFile(config.CredentialsPath(path))
	if err := os.WriteFile(path, []byte("version: 2\nactive: lab\nservers:\n  lab:\n    url: "+first.URL+"\n  staging:\n    url: "+second.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Put("staging", cred.Credential{Kind: cred.KindToken, Secret: "mch_pat_s", ExpiresAt: past()}); err != nil {
		t.Fatal(err)
	}

	got := runAuth(t, authRun{config: path}, "switch")
	if got.code != 0 || !strings.Contains(got.stdout, "NAME") || !strings.Contains(got.stdout, "*  lab") ||
		!strings.Contains(got.stdout, "staging") || !strings.Contains(got.stdout, "expired") || !strings.Contains(got.stdout, "yes") {
		t.Fatalf("list = %+v", got)
	}

	got = runAuth(t, authRun{config: path}, "switch", "staging")
	if got.code != 0 || !strings.Contains(got.stdout, "Active server: staging.") {
		t.Fatalf("switch = %+v", got)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "active: staging") { // #nosec G304 -- test path
		t.Fatalf("config = %q", data)
	}

	got = runAuth(t, authRun{config: path}, "switch", "nope")
	if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, `no saved server is called "nope"`) || !strings.Contains(got.stderr, "saved servers: lab, staging") {
		t.Fatalf("unknown = %+v", got)
	}

	// On a terminal the list is a numbered choice.
	got = runAuth(t, authRun{config: path, terminal: true, stdin: "1\n"}, "switch")
	if got.code != 0 || !strings.Contains(got.stderr, "Make which server active?") || !strings.Contains(got.stdout, "Active server: lab.") {
		t.Fatalf("interactive = %+v", got)
	}

	if got := runAuth(t, authRun{config: configPath(t)}, "switch"); got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "no server has been saved yet") {
		t.Fatalf("no servers = %+v", got)
	}
	_ = filepath.Join
}

func itoa(n int) string {
	data, _ := json.Marshal(n)

	return string(data)
}
