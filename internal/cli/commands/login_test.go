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

func TestLoginWithPasswordFromStdin(t *testing.T) {
	server := newAuthServer(t, false)
	path := configPath(t)

	got := runAuth(t, authRun{stdin: testPassword + "\n", config: path},
		"login", server.URL, "--name", "lab", "--email", testEmail, "--password-stdin", "--no-stay-signed-in")
	if got.code != 0 {
		t.Fatalf("login = %+v", got)
	}
	if !strings.Contains(got.stdout, "Signed in to lab as maxim.") || !strings.Contains(got.stdout, "The session lasts") ||
		!strings.Contains(got.stdout, "Role: not reported by this server.") {
		t.Fatalf("stdout = %q", got.stdout)
	}

	entry := savedCredentials(t, path, "lab")
	if entry.Session == nil || entry.Token != nil || entry.Session.Kind != cred.KindSession || entry.Session.User != "maxim" || entry.Session.Secret != "sess-1" {
		t.Fatalf("saved = %+v", entry)
	}
	if entry.Session.ExpiresAt == nil || time.Until(*entry.Session.ExpiresAt) < 23*time.Hour {
		t.Fatalf("the session expiry must come from the server: %+v", entry.Session.ExpiresAt)
	}

	var body map[string]string
	if err := json.Unmarshal(server.saw("POST", "/api/auth/login")[0].Body, &body); err != nil || body["email"] != testEmail || body["password"] != testPassword {
		t.Fatalf("login body = %v, %v", body, err)
	}
	if auth := server.saw("GET", "/api/v1/sessions")[0].Auth; auth != "Bearer sess-1" {
		t.Fatalf("the session list was asked with %q", auth)
	}

	cfg, _ := config.ResolvePath(path)
	data, err := os.ReadFile(cfg) // #nosec G304 -- test path
	if err != nil || !strings.Contains(string(data), "active: lab") || strings.Contains(string(data), "sess-1") || strings.Contains(string(data), testPassword) {
		t.Fatalf("config = %q, %v", data, err)
	}
	if strings.Contains(allText(t, path, got), testPassword) {
		t.Fatal("the password was printed or saved in the config")
	}
}

// The guided path of docs/cli/spec/03-ux.md 2.1: server, name, method, email,
// a hidden password, stay signed in.
func TestLoginGuidedOnATerminal(t *testing.T) {
	server := newAuthServer(t, false)
	path := configPath(t)

	got := runAuth(t, authRun{terminal: true, config: path, secrets: []string{testPassword},
		stdin: server.URL + "\n\n1\n" + testEmail + "\ny\n"}, "login")
	if got.code != 0 {
		t.Fatalf("login = %+v", got)
	}

	for _, want := range []string{
		"Server address (for example https://machinarchy.example.com): ",
		"Name for this server [127-0-0-1-",
		"How do you want to sign in?\n  1) Email and password",
		"  2) API token",
		"Choose [1]: ",
		"Email or username: ",
		"Stay signed in on this computer? A 90-day device token (operator) will be saved",
		"[Y/n]: ",
	} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the prompts are missing %q:\n%s", want, got.stderr)
		}
	}
	if strings.Join(got.secretsAsked, "|") != "Password: " {
		t.Fatalf("hidden prompts = %v", got.secretsAsked)
	}
	if strings.Contains(got.stdout+got.stderr, testPassword) {
		t.Fatal("the password was echoed")
	}

	name := "127-0-0-1-" + server.URL[strings.LastIndex(server.URL, ":")+1:]
	entry := savedCredentials(t, path, name)
	if entry.Session == nil || entry.Token == nil {
		t.Fatalf("both a session and a device token must be saved: %+v", entry)
	}
	device := entry.Token
	if device.Kind != cred.KindDeviceToken || device.Role != "operator" || !strings.HasPrefix(device.Name, "device-maxims-macbook-") || device.TokenID == 0 {
		t.Fatalf("device token = %+v", device)
	}
	if device.ExpiresAt == nil || time.Until(*device.ExpiresAt) < 89*24*time.Hour {
		t.Fatalf("a device token lasts 90 days: %v", device.ExpiresAt)
	}

	created := server.saw("POST", "/api/v1/tokens")
	if len(created) != 1 || created[0].Auth != "Bearer sess-1" {
		t.Fatalf("the device token must be created with the session: %+v", created)
	}
	var request map[string]any
	if err := json.Unmarshal(created[0].Body, &request); err != nil || request["role"] != "operator" || request["expiresInHours"] != float64(2160) {
		t.Fatalf("device token request = %v, %v", request, err)
	}
	if !strings.Contains(got.stdout, "Device token saved (operator, 90 days).") {
		t.Fatalf("stdout = %q", got.stdout)
	}
}

// "back" is not a name or an email: it ends the sign-in, and nothing is saved.
func TestLoginBackEndsTheSignInWithoutSavingAnything(t *testing.T) {
	server := newAuthServer(t, false)
	path := configPath(t)

	got := runAuth(t, authRun{terminal: true, config: path, stdin: server.URL + "\nback\n"}, "login")
	if got.code != clierr.ExitFailure || !strings.Contains(got.stderr, "Error: aborted; nothing was changed\n") {
		t.Fatalf("login = %+v", got)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a sign-in that was ended saved a config")
	}
	if len(server.saw("POST", "/api/auth/login")) != 0 {
		t.Fatal("nothing may be sent after \"back\"")
	}
}

func TestWrongPassword(t *testing.T) {
	server := newAuthServer(t, false)
	path := configPath(t)

	got := runAuth(t, authRun{stdin: "not the password\n", config: path},
		"login", server.URL, "--name", "lab", "--email", testEmail, "--password-stdin")
	if got.code != clierr.ExitAuth || got.stdout != "" {
		t.Fatalf("result = %+v", got)
	}
	if !strings.Contains(got.stderr, "Error: sign-in failed: the email or password is not right\n") || !strings.Contains(got.stderr, "Next:  machinarchy login\n") {
		t.Fatalf("stderr = %q", got.stderr)
	}
	// The error must not say which of the two was wrong.
	got = runAuth(t, authRun{stdin: testPassword + "\n", config: path}, "login", server.URL, "--name", "lab", "--email", "nobody@example.com", "--password-stdin")
	if !strings.Contains(got.stderr, "the email or password is not right") {
		t.Fatalf("an unknown user must read the same: %q", got.stderr)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a failed sign-in saved a config file")
	}
	if _, err := os.Stat(config.CredentialsPath(path)); err == nil {
		t.Fatal("a failed sign-in saved credentials")
	}
}

func TestThrottledLogin(t *testing.T) {
	server := newAuthServer(t, false)

	got := runAuth(t, authRun{stdin: "x\n"}, "login", server.URL, "--name", "lab", "--email", "throttled", "--password-stdin")
	if got.code != clierr.ExitUnavailable || !strings.Contains(got.stderr, "Error: too many sign-in attempts\n") || !strings.Contains(got.stderr, "Why:   try again in 40 seconds\n") {
		t.Fatalf("result = %+v", got)
	}
	if len(server.saw("POST", "/api/auth/login")) != 1 {
		t.Fatal("a throttled sign-in must not be retried")
	}
}

// B-09: the token is checked with the server before anything is saved.
func TestLoginWithTokenFromStdin(t *testing.T) {
	server := newAuthServer(t, false)
	server.addToken("mch_pat_ci-SECRET-0001", "ci", "operator")
	path := configPath(t)

	got := runAuth(t, authRun{stdin: "mch_pat_ci-SECRET-0001\n", config: path}, "login", server.URL, "--name", "lab", "--with-token", "--no-input")
	if got.code != 0 {
		t.Fatalf("login = %+v", got)
	}
	if !strings.Contains(got.stdout, "Signed in to lab as maxim.") || !strings.Contains(got.stdout, "Role: not reported by this server. Expires: unknown.") {
		t.Fatalf("stdout = %q", got.stdout)
	}
	if me := server.saw("GET", "/api/v1/me"); len(me) != 1 || me[0].Auth != "Bearer mch_pat_ci-SECRET-0001" {
		t.Fatalf("the token must be checked with /api/v1/me: %+v", me)
	}

	entry := savedCredentials(t, path, "lab")
	if entry.Token == nil || entry.Session != nil || entry.Token.Kind != cred.KindToken || entry.Token.User != "maxim" {
		t.Fatalf("saved = %+v", entry)
	}
	if strings.Contains(got.stdout+got.stderr, "SECRET-0001") {
		t.Fatal("the token was printed")
	}
	if got := len(server.saw("POST", "/api/auth/login")); got != 0 {
		t.Fatalf("a token login must not call the password login: %d", got)
	}
}

func TestLoginWithTokenFromAHiddenPrompt(t *testing.T) {
	server := newAuthServer(t, false)
	server.addToken("mch_pat_ci-SECRET-0002", "ci", "reader")

	got := runAuth(t, authRun{terminal: true, secrets: []string{"mch_pat_ci-SECRET-0002"}, stdin: "\n"}, "login", server.URL, "--name", "lab", "--with-token")
	if got.code != 0 || strings.Join(got.secretsAsked, "|") != "API token: " || strings.Contains(got.stdout+got.stderr, "SECRET-0002") {
		t.Fatalf("result = %+v, hidden prompts %v", got, got.secretsAsked)
	}
}

// B-10: a rejected token is never saved.
func TestLoginWithRejectedToken(t *testing.T) {
	server := newAuthServer(t, false)
	path := configPath(t)

	got := runAuth(t, authRun{stdin: "mch_pat_wrong-0000\n", config: path}, "login", server.URL, "--name", "lab", "--with-token", "--no-input")
	if got.code != clierr.ExitAuth ||
		!strings.Contains(got.stderr, "Error: the server did not accept this token\n") ||
		!strings.Contains(got.stderr, "Why:   it may be wrong, expired or revoked\n") ||
		!strings.Contains(got.stderr, "Next:  machinarchy login --with-token\n") {
		t.Fatalf("result = %+v", got)
	}
	if _, err := os.Stat(config.CredentialsPath(path)); err == nil {
		t.Fatal("a rejected token was saved")
	}
	if strings.Contains(got.stderr, "wrong-0000") {
		t.Fatal("the token was printed")
	}
}

// N-04 and N-05: nothing is ever asked when nothing can be asked.
func TestLoginNeverWaitsForInputItCannotGet(t *testing.T) {
	server := newAuthServer(t, false)

	for name, run := range map[string]authRun{
		"stdin is not a terminal":  {},
		"--no-input on a terminal": {terminal: true},
	} {
		args := []string{"login", server.URL, "--name", "lab"}
		if run.terminal {
			args = append(args, "--no-input")
		}
		got := runAuth(t, run, args...)
		if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "Error: --email is required when nothing can be asked.\n") ||
			!strings.Contains(got.stderr, "--password-stdin") || got.stdout != "" || len(got.secretsAsked) != 0 {
			t.Errorf("%s: result = %+v", name, got)
		}
	}

	got := runAuth(t, authRun{}, "login")
	if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "the server address is required when nothing can be asked") ||
		!strings.Contains(got.stderr, "machinarchy login https://your-server") {
		t.Fatalf("login without a server = %+v", got)
	}

	got = runAuth(t, authRun{stdin: ""}, "login", server.URL, "--name", "lab", "--with-token")
	if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "a token on stdin is required") {
		t.Fatalf("login --with-token without a token = %+v", got)
	}
}

func TestLoginAgainUsesTheActiveServer(t *testing.T) {
	server := newAuthServer(t, false)
	path := seed(t, server.URL, cred.Credential{Kind: cred.KindSession, Secret: "old-session"})

	got := runAuth(t, authRun{stdin: testPassword + "\n", config: path}, "login", "--email", testEmail, "--password-stdin", "--no-stay-signed-in")
	if got.code != 0 || !strings.Contains(got.stdout, "Signed in to lab as maxim.") {
		t.Fatalf("result = %+v", got)
	}
	if entry := savedCredentials(t, path, "lab"); entry.Session == nil || entry.Session.Secret != "sess-1" {
		t.Fatalf("the new session must replace the old one: %+v", entry)
	}
}

func TestLoginRefusesANameTakenByAnotherAddress(t *testing.T) {
	first, second := newAuthServer(t, false), newAuthServer(t, false)
	path := seed(t, first.URL)

	got := runAuth(t, authRun{stdin: testPassword + "\n", config: path}, "login", second.URL, "--name", "lab", "--email", testEmail, "--password-stdin")
	if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, `a server called "lab" is already saved for a different address`) {
		t.Fatalf("result = %+v", got)
	}
	if len(second.saw("POST", "/api/auth/login")) != 0 {
		t.Fatal("the password was sent before the name was settled")
	}
}

func TestLoginRefusesPlainHTTPToAnotherHost(t *testing.T) {
	got := runAuth(t, authRun{stdin: "x\n"}, "login", "http://machinarchy.example.com", "--name", "lab", "--email", testEmail, "--password-stdin")
	if got.code != clierr.ExitUsage ||
		!strings.Contains(got.stderr, "refusing to send your credentials over plain http:// to machinarchy.example.com") ||
		!strings.Contains(got.stderr, "--insecure-http") {
		t.Fatalf("result = %+v", got)
	}
}

func TestLoginRemembersInsecureHTTPPerServer(t *testing.T) {
	server := newAuthServer(t, false)
	path := configPath(t)

	got := runAuth(t, authRun{stdin: testPassword + "\n", config: path}, "login", server.URL, "--name", "lab", "--email", testEmail,
		"--password-stdin", "--no-stay-signed-in", "--insecure-http")
	if got.code != 0 {
		t.Fatalf("result = %+v", got)
	}
	data, _ := os.ReadFile(path) // #nosec G304 -- test path
	if !strings.Contains(string(data), "insecure-http: true") {
		t.Fatalf("config = %q", data)
	}
}

// The password is not replayed to another location (S-28).
func TestLoginDoesNotFollowRedirects(t *testing.T) {
	elsewhere := newAuthServer(t, false)
	server := newAuthServer(t, false)
	server.redirect = elsewhere.URL + "/api/auth/login"

	got := runAuth(t, authRun{stdin: testPassword + "\n"}, "login", server.URL, "--name", "lab", "--email", testEmail, "--password-stdin")
	if got.code == 0 {
		t.Fatalf("a redirected sign-in must fail: %+v", got)
	}
	if len(elsewhere.log()) != 0 {
		t.Fatalf("the redirect was followed: %+v", elsewhere.log())
	}
}

// S-01 to S-03: a certificate the computer does not trust is refused, and the
// way out is a CA file, never a switch that turns verification off.
func TestLoginWithAPrivateCertificate(t *testing.T) {
	server := newAuthServer(t, true)
	path := configPath(t)

	got := runAuth(t, authRun{stdin: testPassword + "\n", config: path}, "login", server.URL, "--name", "lab", "--email", testEmail, "--password-stdin")
	if got.code != clierr.ExitUnavailable ||
		!strings.Contains(got.stderr, "is not trusted (certificate signed by unknown authority)") ||
		!strings.Contains(got.stderr, "--ca-file ca.pem") {
		t.Fatalf("result = %+v", got)
	}
	if len(server.saw("POST", "/api/auth/login")) != 0 {
		t.Fatal("a password was sent over a connection that was not trusted")
	}

	// There is no flag that skips verification.
	got = runAuth(t, authRun{}, "login", server.URL, "--insecure-skip-verify")
	if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "unknown flag: --insecure-skip-verify") {
		t.Fatalf("result = %+v", got)
	}

	ca := writeServerCA(t, server)
	got = runAuth(t, authRun{stdin: testPassword + "\n", config: path}, "login", server.URL, "--name", "lab", "--email", testEmail,
		"--password-stdin", "--no-stay-signed-in", "--ca-file", ca)
	if got.code != 0 {
		t.Fatalf("login with the CA = %+v", got)
	}

	saved := filepath.Join(filepath.Dir(path), "ca", "lab.pem")
	if info, err := os.Stat(saved); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the CA must be copied next to the config, private: %v, %v", info, err)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "ca-file: "+filepath.Join("ca", "lab.pem")) { // #nosec G304 -- test path
		t.Fatalf("config = %q", data)
	}

	// Later commands trust that CA for that server, from the saved copy.
	if got := runAuth(t, authRun{config: path}, "whoami"); got.code != 0 || !strings.Contains(got.stdout, "maxim") {
		t.Fatalf("whoami over the private CA = %+v", got)
	}
}

// The completion hint is shown once per computer, on a terminal, on stderr.
func TestLoginShowsTheCompletionHintOnce(t *testing.T) {
	server := newAuthServer(t, false)
	path := configPath(t)
	args := []string{"login", server.URL, "--name", "lab", "--email", testEmail, "--no-stay-signed-in"}

	first := runAuth(t, authRun{terminal: true, config: path, secrets: []string{testPassword}}, args...)
	if first.code != 0 || !strings.Contains(first.stderr, "machinarchy completion --help") {
		t.Fatalf("first login = %+v", first)
	}
	if strings.Contains(first.stdout, "completion") {
		t.Fatalf("the hint belongs on stderr: %q", first.stdout)
	}

	again := runAuth(t, authRun{terminal: true, config: path, secrets: []string{testPassword}}, args...)
	if again.code != 0 || strings.Contains(again.stderr, "machinarchy completion") {
		t.Fatalf("the hint must appear once: %+v", again)
	}
}

func TestLoginShowsNoCompletionHintWithoutATerminalOrInPlain(t *testing.T) {
	server := newAuthServer(t, false)

	piped := runAuth(t, authRun{stdin: testPassword + "\n"},
		"login", server.URL, "--name", "lab", "--email", testEmail, "--password-stdin", "--no-stay-signed-in")
	if piped.code != 0 || strings.Contains(piped.stdout+piped.stderr, "completion") {
		t.Fatalf("no hint without a terminal: %+v", piped)
	}

	plain := runAuth(t, authRun{terminal: true, secrets: []string{testPassword}},
		"--plain", "login", server.URL, "--name", "lab", "--email", testEmail, "--no-stay-signed-in")
	if plain.code != 0 || strings.Contains(plain.stdout+plain.stderr, "completion") {
		t.Fatalf("no hint with --plain: %+v", plain)
	}
}
