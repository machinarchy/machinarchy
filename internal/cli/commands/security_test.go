package commands_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/config"
)

// B-22: a secret is never visible in any output, in any format, with -v, or in
// the config file. The one place a token legitimately appears is the output of
// `tokens create`, once.
func TestNoSecretIsEverVisible(t *testing.T) {
	server := newAuthServer(t, false)
	path := configPath(t)

	login := runAuth(t, authRun{stdin: testPassword + "\n", config: path}, "login", server.URL, "--name", "lab", "--email", testEmail, "--password-stdin", "--stay-signed-in", "-v")
	if login.code != 0 {
		t.Fatalf("login = %+v", login)
	}
	entry := savedCredentials(t, path, "lab")
	secrets := []string{entry.Session.Secret, entry.Token.Secret, testPassword}
	envToken := "mch_pat_env-SECRET-9"
	server.addToken(envToken, "env", "reader")
	secrets = append(secrets, envToken)

	results := []authResult{login}
	for _, args := range [][]string{
		{"whoami"}, {"whoami", "-o", "json"}, {"whoami", "-o", "yaml"}, {"whoami", "-v"},
		{"tokens"}, {"tokens", "-o", "json"}, {"tokens", "-o", "yaml"}, {"tokens", "-v"},
		{"get", "hosts", "-v"}, {"get", "hosts", "-o", "json", "-v"},
		{"switch"}, {"switch", "-o", "json"},
		{"config", "path"}, {"config", "show"}, {"config", "show", "-o", "json"}, {"config", "show", "-o", "yaml"}, {"config", "show", "-v"},
		{"version"}, {"--help"}, {"login", "--help"}, {"tokens", "create", "--help"},
	} {
		results = append(results, runAuth(t, authRun{config: path, env: map[string]string{"MACHINARCHY_TOKEN": envToken}}, args...))
	}
	// Failures must not leak either.
	results = append(results,
		runAuth(t, authRun{config: path, env: map[string]string{"MACHINARCHY_TOKEN": envToken}}, "secret", "list", "-v"),
		runAuth(t, authRun{config: path, env: map[string]string{"MACHINARCHY_TOKEN": envToken, "MACHINARCHY_SERVER": "http://127.0.0.1:1"}}, "get", "hosts", "-v"),
		runAuth(t, authRun{config: path}, "login", server.URL, "--token", "x"),
	)

	text := allText(t, path, results...)
	for _, secret := range secrets {
		if strings.Contains(text, secret) {
			t.Fatalf("the secret %q... appeared in an output or in the config file", secret[:8])
		}
	}
	if !strings.Contains(text, "***") {
		t.Fatal("config show must mark the credentials as ***")
	}
}

func TestTokensCreatePrintsTheTokenOnlyOnce(t *testing.T) {
	server := newAuthServer(t, false)
	path := ownerConfig(t, server)

	got := runAuth(t, authRun{config: path}, "tokens", "create", "once", "--role", "reader", "-v")
	if strings.Count(got.stdout+got.stderr, "mch_pat_once-") != 1 {
		t.Fatalf("the token must appear exactly once, even with -v:\n%s%s", got.stdout, got.stderr)
	}
	for _, cfg := range []string{path, config.CredentialsPath(path)} {
		if data, _ := os.ReadFile(cfg); strings.Contains(string(data), "mch_pat_once-") { // #nosec G304 -- test path
			t.Fatalf("a token created by `tokens create` must not be saved: %s", cfg)
		}
	}
}

// B-24: a secret is never accepted as a flag value.
func TestSecretsAreNeverFlags(t *testing.T) {
	server := newAuthServer(t, false)

	for _, args := range [][]string{
		{"login", server.URL, "--token", "mch_pat_x"},
		{"login", server.URL, "--password", "x"},
		{"login", server.URL, "--with-token", "mch_pat_x"}, // --with-token is a switch; a value is an argument and not a token
		{"tokens", "create", "n", "--role", "reader", "--token", "x"},
		{"config", "set-credentials", "x", "--token", "x"},
		{"config", "set-cluster", "x", "--server", server.URL},
		{"create", "token", "x", "--role", "reader"},
	} {
		got := runAuth(t, authRun{}, args...)
		if got.code != clierr.ExitUsage {
			t.Errorf("%v: exit = %d, want 2\n%s", args, got.code, got.stderr)
		}
		if len(server.log()) != 0 {
			t.Fatalf("%v: a request was sent", args)
		}
	}
}

// A config in the format of earlier versions is refused, named, and left as it is.
func TestAnOlderConfigIsRefusedAndLeftAlone(t *testing.T) {
	path := configPath(t)
	old := "apiVersion: machinarchy.io/v1\ncurrent-context: lab\nclusters:\n  lab:\n    server: https://x.example\nusers:\n  lab:\n    token: OLD-TOKEN-VALUE\ncontexts:\n  lab:\n    cluster: lab\n    user: lab\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	got := runAuth(t, authRun{config: path}, "whoami")
	if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "is in an older format that this version no longer reads") ||
		!strings.Contains(got.stderr, "its tokens were not copied") || !strings.Contains(got.stderr, "mv \""+path+"\" \""+path+".old\"") ||
		!strings.Contains(got.stderr, "machinarchy login YOUR-SERVER-URL") {
		t.Fatalf("result = %+v", got)
	}
	if strings.Contains(got.stderr, "OLD-TOKEN-VALUE") {
		t.Fatal("the old token was printed")
	}
	if data, _ := os.ReadFile(path); string(data) != old { // #nosec G304 -- test path
		t.Fatal("the old config was changed")
	}
	if _, err := os.Stat(config.CredentialsPath(path)); err == nil {
		t.Fatal("an old token was copied into the credential store")
	}

	// Commands that do not need the config still work.
	if got := runAuth(t, authRun{config: path}, "version"); got.code != 0 {
		t.Fatalf("version = %+v", got)
	}
}

func TestConfigPath(t *testing.T) {
	path := configPath(t)

	got := runAuth(t, authRun{config: path}, "config", "path")
	if got.code != 0 || got.stdout != path+"\n"+config.CredentialsPath(path)+"\n" {
		t.Fatalf("result = %+v", got)
	}
	if q := runAuth(t, authRun{config: path}, "config", "path", "-q"); q.stdout != path+"\n" {
		t.Fatalf("-q = %q", q.stdout)
	}
	// It is hidden from the help.
	for _, line := range strings.Split(runAuth(t, authRun{config: path}, "--help").stdout, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "config ") {
			t.Fatalf("config must be hidden from the help: %q", line)
		}
	}
}

// B-23: while a login waits for the token on stdin, the process list shows no
// secret, and no flag can carry one. The binary is built and run for real.
func TestTokenIsNeverInTheProcessList(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ps is not available")
	}
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("ps is not available")
	}

	server := newAuthServer(t, false)
	const token = "mch_pat_PROCESS-LIST-SECRET-0001"
	server.addToken(token, "ci", "operator")

	binary := filepath.Join(t.TempDir(), "machinarchy")
	build := exec.Command("go", "build", "-o", binary, "github.com/machinarchy/machinarchy/cmd/machinarchy") // #nosec G204 -- fixed arguments
	build.Dir = "../../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}

	dir := t.TempDir()
	command := exec.Command(binary, "--config", filepath.Join(dir, "config.yaml"), "login", server.URL, "--name", "lab", "--with-token") // #nosec G204 -- the binary built above
	command.Env = append(os.Environ(), "MACHINARCHY_CONFIG="+filepath.Join(dir, "config.yaml"))
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	// The process now waits for stdin. Look at what the system says about it.
	time.Sleep(300 * time.Millisecond)
	listing, err := exec.Command("ps", "-ww", "-o", "args=", "-p", itoa(command.Process.Pid)).CombinedOutput() // #nosec G204 -- fixed arguments
	if err != nil {
		t.Fatalf("ps: %v\n%s", err, listing)
	}
	if strings.Contains(string(listing), "SECRET") || strings.Contains(string(listing), token) || !strings.Contains(string(listing), "--with-token") {
		t.Fatalf("the process list shows %q", listing)
	}

	if _, err := stdin.Write([]byte(token + "\n")); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	if err := command.Wait(); err != nil {
		t.Fatalf("login failed: %v\n%s", err, output.String())
	}
	if strings.Contains(output.String(), "SECRET") {
		t.Fatalf("the token was printed:\n%s", output.String())
	}

	// The token is saved, in a file only its owner can read.
	info, err := os.Stat(filepath.Join(dir, "config.credentials.yaml"))
	if err != nil {
		t.Fatalf("the credentials were not saved: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials mode = %v", info.Mode().Perm())
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "config.yaml")); strings.Contains(string(data), "SECRET") { // #nosec G304 -- test path
		t.Fatal("the config file holds a secret")
	}

	// A token given as a flag is not a thing.
	flagged := exec.Command(binary, "login", server.URL, "--token", token) // #nosec G204 -- the binary built above
	flagged.Env = append(os.Environ(), "MACHINARCHY_CONFIG="+filepath.Join(t.TempDir(), "x.yaml"))
	out, err := flagged.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "unknown flag: --token") || strings.Contains(string(out), token) {
		t.Fatalf("--token must be an unknown flag that does not echo the value: %v\n%s", err, out)
	}
}
