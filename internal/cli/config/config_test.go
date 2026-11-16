package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

const sampleConfig = `apiVersion: machinarchy.io/v1
current-context: local
clusters:
  local:
    server: http://file.example:8080/
users:
  local:
    token: file-token
contexts:
  local:
    cluster: local
    user: local
    environment: staging
`

// isolateEnvironment clears every variable the CLI reads so the developer's
// shell cannot influence a test.
func isolateEnvironment(t *testing.T) {
	t.Helper()

	for _, name := range []string{
		"MACHINARCHY_CONFIG",
		"MACHINARCHY_CLI_CONTEXT",
		"MACHINARCHY_CLI_ENV",
		"MACHINARCHY_CLI_OUTPUT",
		"MACHINARCHY_CLI_VERBOSE",
		"MACHINARCHY_CLI_SERVER",
		"MACHINARCHY_CLI_TOKEN",
		"MACHINARCHY_CLI_REQUEST_TIMEOUT",
		"MACHINARCHY_CLI_QUIET",
		"MACHINARCHY_CLI_NO_HEADERS",
		"MACHINARCHY_CLI_NO_INPUT",
		"MACHINARCHY_CLI_YES",
	} {
		t.Setenv(name, "")
	}
}

func newFlags(t *testing.T, args ...string) *pflag.FlagSet {
	t.Helper()

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("context", "", "")
	flags.String("server", "", "")
	flags.String("env", "", "")
	flags.String("output", "", "")
	flags.String("request-timeout", "", "")
	flags.Bool("verbose", false, "")
	flags.Bool("quiet", false, "")
	flags.Bool("no-headers", false, "")
	flags.Bool("no-input", false, "")
	flags.Bool("yes", false, "")

	if err := flags.Parse(args); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	return flags
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return path
}

func loadSample(t *testing.T, args ...string) *Store {
	t.Helper()

	store, err := Load(writeConfig(t, sampleConfig), newFlags(t, args...))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	return store
}

func TestLoadMissingFileYieldsEmptyDocument(t *testing.T) {
	isolateEnvironment(t)

	store, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), newFlags(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if names := store.ContextNames(); len(names) != 0 {
		t.Fatalf("ContextNames = %v, want none", names)
	}
	if store.CurrentContext() != "" {
		t.Fatalf("CurrentContext = %q, want empty", store.CurrentContext())
	}
}

func TestNamesArePreservedExactly(t *testing.T) {
	isolateEnvironment(t)

	path := filepath.Join(t.TempDir(), "config.yaml")
	store, err := Load(path, newFlags(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Mixed case and dots are exactly what Viper's key handling would mangle.
	if err := store.SetCluster("Prod.EU", "https://api.example.com"); err != nil {
		t.Fatalf("SetCluster: %v", err)
	}
	if err := store.SetUser("Ops.User", "token-value"); err != nil {
		t.Fatalf("SetUser: %v", err)
	}
	if err := store.SetContext("Prod.EU", "Prod.EU", "Ops.User", "Env.One"); err != nil {
		t.Fatalf("SetContext: %v", err)
	}
	if err := store.UseContext("Prod.EU"); err != nil {
		t.Fatalf("UseContext: %v", err)
	}

	reloaded, err := Load(path, newFlags(t))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	names := reloaded.ContextNames()
	if len(names) != 1 || names[0] != "Prod.EU" {
		t.Fatalf("ContextNames = %v, want [Prod.EU]", names)
	}
	if reloaded.CurrentContext() != "Prod.EU" {
		t.Fatalf("CurrentContext = %q", reloaded.CurrentContext())
	}
	if _, ok := reloaded.Cluster("Prod.EU"); !ok {
		t.Fatal("cluster Prod.EU was not preserved")
	}
	if user, ok := reloaded.User("Ops.User"); !ok || user.Token != "token-value" {
		t.Fatalf("user Ops.User = %+v, %v", user, ok)
	}

	target, err := reloaded.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if target.Server != "https://api.example.com" || target.Environment != "Env.One" {
		t.Fatalf("target = %+v", target)
	}
}

func TestResolvePrecedence(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		args       []string
		wantServer string
		wantToken  string
		wantEnv    string
	}{
		{
			name:       "config file only",
			wantServer: "http://file.example:8080",
			wantToken:  "file-token",
			wantEnv:    "staging",
		},
		{
			name:       "environment beats file",
			env:        map[string]string{"MACHINARCHY_CLI_SERVER": "http://env.example", "MACHINARCHY_CLI_TOKEN": "env-token", "MACHINARCHY_CLI_ENV": "prod"},
			wantServer: "http://env.example",
			wantToken:  "env-token",
			wantEnv:    "prod",
		},
		{
			name:       "flag beats environment",
			env:        map[string]string{"MACHINARCHY_CLI_SERVER": "http://env.example", "MACHINARCHY_CLI_ENV": "prod"},
			args:       []string{"--server", "http://flag.example/", "--env", "dev"},
			wantServer: "http://flag.example",
			wantToken:  "file-token",
			wantEnv:    "dev",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			isolateEnvironment(t)
			for key, value := range test.env {
				t.Setenv(key, value)
			}

			target, err := loadSample(t, test.args...).Resolve()
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}

			if target.Server != test.wantServer {
				t.Errorf("server = %q, want %q", target.Server, test.wantServer)
			}
			if target.Token != test.wantToken {
				t.Errorf("token = %q, want %q", target.Token, test.wantToken)
			}
			if target.Environment != test.wantEnv {
				t.Errorf("environment = %q, want %q", target.Environment, test.wantEnv)
			}
			if target.ContextName != "local" {
				t.Errorf("context = %q, want local", target.ContextName)
			}
			if target.RequestTimeout != DefaultRequestTimeout {
				t.Errorf("timeout = %s, want default", target.RequestTimeout)
			}
		})
	}
}

func TestResolveErrorsAreClassifiedAndHelpful(t *testing.T) {
	t.Run("no context", func(t *testing.T) {
		isolateEnvironment(t)

		store, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), newFlags(t))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}

		_, err = store.Resolve()
		if err == nil {
			t.Fatal("Resolve succeeded without a context")
		}
		if clierr.ExitCode(err) != clierr.ExitUsage {
			t.Errorf("exit code = %d, want %d", clierr.ExitCode(err), clierr.ExitUsage)
		}
		if !strings.Contains(err.Error(), "no active context") {
			t.Errorf("message = %q", err.Error())
		}
		if !strings.Contains(clierr.Hint(err), "config set-cluster") {
			t.Errorf("hint = %q, want setup instructions", clierr.Hint(err))
		}
	})

	t.Run("unknown context", func(t *testing.T) {
		isolateEnvironment(t)

		_, err := loadSample(t, "--context", "nope").Resolve()
		if err == nil || !strings.Contains(err.Error(), `context "nope" does not exist`) {
			t.Fatalf("error = %v", err)
		}
		if clierr.ExitCode(err) != clierr.ExitUsage {
			t.Errorf("exit code = %d", clierr.ExitCode(err))
		}
	})

	t.Run("missing token", func(t *testing.T) {
		isolateEnvironment(t)

		path := writeConfig(t, strings.Replace(sampleConfig, "token: file-token", `token: ""`, 1))
		store, err := Load(path, newFlags(t))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}

		_, err = store.Resolve()
		if err == nil || !strings.Contains(err.Error(), "no API token configured") {
			t.Fatalf("error = %v", err)
		}
		if clierr.ExitCode(err) != clierr.ExitUsage {
			t.Errorf("exit code = %d", clierr.ExitCode(err))
		}
	})

	t.Run("invalid token is never echoed", func(t *testing.T) {
		isolateEnvironment(t)
		t.Setenv("MACHINARCHY_CLI_TOKEN", "secret part two")

		_, err := loadSample(t).Resolve()
		if err == nil {
			t.Fatal("Resolve accepted a token containing whitespace")
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(clierr.Hint(err), "secret") {
			t.Fatalf("error leaks the token: %q / %q", err.Error(), clierr.Hint(err))
		}
	})

	t.Run("unknown cluster", func(t *testing.T) {
		isolateEnvironment(t)

		path := writeConfig(t, strings.Replace(sampleConfig, "cluster: local", "cluster: gone", 1))
		store, err := Load(path, newFlags(t))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if _, err := store.Resolve(); err == nil || !strings.Contains(err.Error(), `unknown cluster "gone"`) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestRequestTimeout(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		args    []string
		want    time.Duration
		wantErr bool
	}{
		{name: "default", want: DefaultRequestTimeout},
		{name: "environment", env: "5s", want: 5 * time.Second},
		{name: "flag beats environment", env: "5s", args: []string{"--request-timeout", "2m"}, want: 2 * time.Minute},
		{name: "not a duration", args: []string{"--request-timeout", "banana"}, wantErr: true},
		{name: "zero", args: []string{"--request-timeout", "0s"}, wantErr: true},
		{name: "negative", args: []string{"--request-timeout", "-1s"}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			isolateEnvironment(t)
			if test.env != "" {
				t.Setenv("MACHINARCHY_CLI_REQUEST_TIMEOUT", test.env)
			}

			got, err := loadSample(t, test.args...).RequestTimeout()
			if test.wantErr {
				if err == nil {
					t.Fatalf("RequestTimeout = %s, want error", got)
				}
				if clierr.ExitCode(err) != clierr.ExitUsage {
					t.Fatalf("exit code = %d, want %d", clierr.ExitCode(err), clierr.ExitUsage)
				}
				return
			}
			if err != nil {
				t.Fatalf("RequestTimeout: %v", err)
			}
			if got != test.want {
				t.Fatalf("RequestTimeout = %s, want %s", got, test.want)
			}
		})
	}
}

func TestOutputAndBooleanSettings(t *testing.T) {
	isolateEnvironment(t)

	store := loadSample(t)
	if got := store.OutputExplicit(); got != "" {
		t.Fatalf("OutputExplicit = %q, want empty so commands apply their own default", got)
	}
	if got := store.OutputFormat(); got != "table" {
		t.Fatalf("OutputFormat = %q, want table", got)
	}
	if store.Quiet() || store.NoHeaders() || store.NoInput() || store.Verbose() {
		t.Fatal("boolean settings should default to false")
	}

	t.Setenv("MACHINARCHY_CLI_OUTPUT", "JSON")
	t.Setenv("MACHINARCHY_CLI_QUIET", "true")
	t.Setenv("MACHINARCHY_CLI_NO_INPUT", "true")

	store = loadSample(t, "--no-headers")
	if got := store.OutputExplicit(); got != "json" {
		t.Fatalf("OutputExplicit = %q, want json (normalised)", got)
	}
	if !store.Quiet() || !store.NoHeaders() || !store.NoInput() {
		t.Fatalf("quiet=%v noHeaders=%v noInput=%v, want all true", store.Quiet(), store.NoHeaders(), store.NoInput())
	}

	var nilStore *Store
	if nilStore.Quiet() || nilStore.NoHeaders() || nilStore.NoInput() || nilStore.OutputExplicit() != "" {
		t.Fatal("a nil store must report defaults")
	}
}

func TestLoadRejectsDamagedFiles(t *testing.T) {
	isolateEnvironment(t)

	tests := []struct {
		name string
		path func(t *testing.T) string
	}{
		{"malformed yaml", func(t *testing.T) string { return writeConfig(t, "contexts: [unterminated") }},
		{"wrong shape", func(t *testing.T) string { return writeConfig(t, "- a\n- list\n") }},
		{"oversized", func(t *testing.T) string { return writeConfig(t, strings.Repeat("#", maxConfigSize+16)) }},
		{"directory", func(t *testing.T) string { return t.TempDir() }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(test.path(t), newFlags(t))
			if err == nil {
				t.Fatal("Load succeeded for a damaged config")
			}
			if clierr.ExitCode(err) != clierr.ExitUsage {
				t.Fatalf("exit code = %d, want %d", clierr.ExitCode(err), clierr.ExitUsage)
			}
			if clierr.Hint(err) == "" {
				t.Fatal("a damaged config should come with a hint")
			}
		})
	}
}

func TestLoadSettingsIgnoresDamagedFile(t *testing.T) {
	isolateEnvironment(t)
	t.Setenv("MACHINARCHY_CONFIG", writeConfig(t, "contexts: [unterminated"))
	t.Setenv("MACHINARCHY_CLI_OUTPUT", "yaml")

	store, err := LoadSettings(newFlags(t))
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if got := store.OutputExplicit(); got != "yaml" {
		t.Fatalf("OutputExplicit = %q, want yaml", got)
	}
	if err := store.SetCluster("x", "http://x"); err == nil {
		t.Fatal("a settings-only store must refuse to write a config file")
	}
}

func TestSavedFilesAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	isolateEnvironment(t)

	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	store, err := Load(path, newFlags(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := store.SetCluster("local", "http://localhost:8080"); err != nil {
		t.Fatalf("SetCluster: %v", err)
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if mode := fileInfo.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode = %04o, want 0600", mode)
	}

	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Errorf("directory mode = %04o, want 0700", mode)
	}
}

func TestPermissionWarnings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	isolateEnvironment(t)

	t.Run("loose file with token warns without leaking it", func(t *testing.T) {
		path := writeConfig(t, sampleConfig)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}

		store, err := Load(path, newFlags(t))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}

		warnings := store.Warnings()
		if len(warnings) != 1 || !strings.Contains(warnings[0], "chmod 600") {
			t.Fatalf("warnings = %v", warnings)
		}
		if strings.Contains(warnings[0], "file-token") {
			t.Fatal("warning leaks the token")
		}
	})

	t.Run("owner-only file does not warn", func(t *testing.T) {
		store, err := Load(writeConfig(t, sampleConfig), newFlags(t))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if warnings := store.Warnings(); len(warnings) != 0 {
			t.Fatalf("warnings = %v, want none", warnings)
		}
	})

	t.Run("loose file without tokens does not warn", func(t *testing.T) {
		path := writeConfig(t, "apiVersion: machinarchy.io/v1\n")
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}

		store, err := Load(path, newFlags(t))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if warnings := store.Warnings(); len(warnings) != 0 {
			t.Fatalf("warnings = %v, want none", warnings)
		}
	})
}

func TestValidateServer(t *testing.T) {
	valid := []string{"http://localhost:8080", "https://api.example.com", "https://api.example.com/prefix", "  http://x  "}
	for _, server := range valid {
		if err := ValidateServer(server); err != nil {
			t.Errorf("ValidateServer(%q) = %v, want nil", server, err)
		}
	}

	invalid := []string{"", "localhost:8080", "ftp://example.com", "https://", "https://user:pass@example.com", "https://example.com?x=1", "https://example.com#frag"}
	for _, server := range invalid {
		err := ValidateServer(server)
		if err == nil {
			t.Errorf("ValidateServer(%q) = nil, want error", server)
			continue
		}
		if clierr.ExitCode(err) != clierr.ExitUsage {
			t.Errorf("ValidateServer(%q) exit code = %d", server, clierr.ExitCode(err))
		}
	}
}

func TestValidateToken(t *testing.T) {
	valid := []string{"abc", "mch_pat_0123456789abcdef", "eyJhbGciOi.eyJzdWIi.sig-_"}
	for _, token := range valid {
		if err := ValidateToken(token); err != nil {
			t.Errorf("ValidateToken(%q) = %v, want nil", token, err)
		}
	}

	invalid := []string{"", "has space", "tab\there", "line\nbreak", "carriage\rreturn", "nul\x00byte", "del\x7fbyte", strings.Repeat("a", MaxTokenBytes+1)}
	for _, token := range invalid {
		if err := ValidateToken(token); err == nil {
			t.Errorf("ValidateToken(%q) = nil, want error", token)
		}
	}
}
