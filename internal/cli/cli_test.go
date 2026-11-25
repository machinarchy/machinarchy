package cli_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/config"
)

type result struct {
	code   int
	stdout string
	stderr string
}

// isolate clears every variable the CLI reads so the developer's shell cannot
// influence a test.
func isolate(t *testing.T) {
	t.Helper()

	// Point the default config path at a file that does not exist so a test
	// that omits --config never reads the developer's real configuration.
	t.Setenv("MACHINARCHY_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))

	for _, name := range []string{
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
		"MACHINARCHY_ENV",
		"MACHINARCHY_OUTPUT",
		"MACHINARCHY_VERBOSE",
		"MACHINARCHY_SERVER",
		"MACHINARCHY_TOKEN",
		"MACHINARCHY_REQUEST_TIMEOUT",
		"MACHINARCHY_QUIET",
		"MACHINARCHY_NO_HEADERS",
		"MACHINARCHY_NO_INPUT",
		"MACHINARCHY_PLAIN",
		"MACHINARCHY_NO_DEPRECATION_WARNINGS",
	} {
		t.Setenv(name, "")
	}
}

func execute(ctx context.Context, t *testing.T, options cli.Options, args ...string) result {
	t.Helper()
	isolate(t)

	var out, errOut bytes.Buffer
	options.Streams.Out = &out
	options.Streams.ErrOut = &errOut
	if options.Streams.In == nil {
		options.Streams.In = strings.NewReader("")
	}
	if args == nil {
		args = []string{}
	}

	code := cli.Run(ctx, options, args)

	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

func run(t *testing.T, options cli.Options, args ...string) result {
	t.Helper()

	return execute(context.Background(), t, options, args...)
}

func command(name string, body func(*cobra.Command, *cli.Factory) error) cli.CommandBuilder {
	return func(factory *cli.Factory) *cobra.Command {
		return &cobra.Command{
			Use:   name,
			Short: name + " test command",
			Args:  cobra.NoArgs,
			RunE: func(command *cobra.Command, _ []string) error {
				return body(command, factory)
			},
		}
	}
}

// stubClient satisfies api.Client; tests that use it never call the API.
type stubClient struct{ api.Client }

func writeConfig(t *testing.T, server, token string) string {
	t.Helper()

	content := fmt.Sprintf(`apiVersion: machinarchy.io/v1
current-context: test
clusters:
  test:
    server: %s
users:
  test:
    token: %s
contexts:
  test:
    cluster: test
    user: test
`, server, token)

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func writeBrokenConfig(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "broken.yaml")
	if err := os.WriteFile(path, []byte("contexts: [unterminated"), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func testOptions(extra ...cli.CommandBuilder) cli.Options {
	builders := []cli.CommandBuilder{
		command("ok", func(c *cobra.Command, _ *cli.Factory) error {
			_, err := fmt.Fprintln(c.OutOrStdout(), "fine")
			return err
		}),
		command("fail-api", func(*cobra.Command, *cli.Factory) error {
			return fmt.Errorf("list hosts: %w", &api.Error{Status: 401, Message: "nope", Method: "GET", Path: "/api/v1/hosts"})
		}),
		command("fail-plain", func(*cobra.Command, *cli.Factory) error { return errors.New("boom") }),
		command("fail-conflict", func(*cobra.Command, *cli.Factory) error {
			return clierr.Newf(clierr.ExitConflict, "retry with a newer revision", "stale revision")
		}),
		command("fail-canceled", func(*cobra.Command, *cli.Factory) error {
			return fmt.Errorf("wait: %w", context.Canceled)
		}),
		command("fail-deadline", func(*cobra.Command, *cli.Factory) error {
			return fmt.Errorf("wait: %w", context.DeadlineExceeded)
		}),
		func(*cli.Factory) *cobra.Command {
			return &cobra.Command{Use: "needs-arg NAME", Short: "requires one argument", Args: cobra.ExactArgs(1), RunE: func(*cobra.Command, []string) error { return nil }}
		},
		command("settings", func(c *cobra.Command, f *cli.Factory) error {
			store := f.Config()
			timeout, err := store.RequestTimeout()
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(c.OutOrStdout(), "output=%s quiet=%t no-headers=%t no-input=%t yes=%t timeout=%s env=%s\n",
				store.OutputExplicit(), store.Quiet(), store.NoHeaders(), store.NoInput(), f.Yes(), timeout, store.Environment())
			return err
		}),
		command("confirm", func(_ *cobra.Command, f *cli.Factory) error {
			return f.RequireConfirmation("Proceed?")
		}),
	}

	return cli.Options{Groups: []cli.CommandGroup{
		{ID: "test", Title: "Test Commands", Commands: append(builders, extra...)},
	}}
}

func TestSuccessExitsZeroWithCleanStderr(t *testing.T) {
	got := run(t, testOptions(), "ok")

	if got.code != 0 || got.stdout != "fine\n" || got.stderr != "" {
		t.Fatalf("result = %+v", got)
	}
}

func TestHelpShowsGroupsAndEveryGlobalFlag(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {}} {
		got := run(t, testOptions(), args...)

		if got.code != 0 {
			t.Fatalf("%v: exit code = %d, stderr = %q", args, got.code, got.stderr)
		}
		for _, want := range []string{
			"Usage:", "Test Commands", "--config", "--context", "--env", "--output", "--verbose",
			"--server", "--request-timeout", "--quiet", "--no-headers", "--no-input", "--yes",
		} {
			if !strings.Contains(got.stdout, want) {
				t.Errorf("%v: help lacks %q:\n%s", args, want, got.stdout)
			}
		}
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		message string
	}{
		{"unknown command", []string{"nope"}, "unknown command"},
		{"unknown flag", []string{"ok", "--bogus"}, "unknown flag"},
		{"unknown shorthand", []string{"ok", "-Z"}, "unknown shorthand flag"},
		{"missing argument", []string{"needs-arg"}, "accepts 1 arg"},
		{"too many arguments", []string{"needs-arg", "a", "b"}, "accepts 1 arg"},
		{"unexpected argument", []string{"ok", "extra"}, "unknown command"},
		{"flag without value", []string{"ok", "--output"}, "flag needs an argument"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := run(t, testOptions(), test.args...)

			if got.code != clierr.ExitUsage {
				t.Fatalf("exit code = %d, want %d (stderr %q)", got.code, clierr.ExitUsage, got.stderr)
			}
			if !strings.HasPrefix(got.stderr, "Error: ") || !strings.Contains(got.stderr, test.message) {
				t.Errorf("stderr = %q, want an error mentioning %q", got.stderr, test.message)
			}
			// The hint points at the help of the command that failed.
			if !strings.Contains(got.stderr, "Hint: Run 'machinarchy") || !strings.Contains(got.stderr, " --help'") {
				t.Errorf("stderr = %q, want a help hint", got.stderr)
			}
			if got.stdout != "" {
				t.Errorf("stdout = %q, want nothing", got.stdout)
			}
		})
	}
}

func TestRuntimeFailuresMapToExitCodes(t *testing.T) {
	tests := []struct {
		command      string
		wantCode     int
		wantStderr   []string
		wantNoStderr []string
	}{
		{"fail-api", clierr.ExitAuth, []string{"Error: list hosts: API returned HTTP 401: nope", "Hint: ", "set-credentials"}, nil},
		{"fail-plain", clierr.ExitFailure, []string{"Error: boom"}, []string{"Hint:", "--help"}},
		{"fail-conflict", clierr.ExitConflict, []string{"Error: stale revision", "Hint: retry with a newer revision"}, nil},
		{"fail-deadline", clierr.ExitUnavailable, []string{"Error: wait: context deadline exceeded"}, nil},
		{"fail-canceled", clierr.ExitInterrupted, []string{"Interrupted."}, []string{"Error:"}},
	}

	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			got := run(t, testOptions(), test.command)

			if got.code != test.wantCode {
				t.Fatalf("exit code = %d, want %d (stderr %q)", got.code, test.wantCode, got.stderr)
			}
			for _, want := range test.wantStderr {
				if !strings.Contains(got.stderr, want) {
					t.Errorf("stderr lacks %q:\n%s", want, got.stderr)
				}
			}
			for _, unwanted := range test.wantNoStderr {
				if strings.Contains(got.stderr, unwanted) {
					t.Errorf("stderr contains %q:\n%s", unwanted, got.stderr)
				}
			}
			if got.stdout != "" {
				t.Errorf("stdout = %q, want nothing", got.stdout)
			}
		})
	}
}

func TestInterruptedContextExitsOneThirty(t *testing.T) {
	options := testOptions(command("await-cancel", func(c *cobra.Command, _ *cli.Factory) error {
		<-c.Context().Done()
		return c.Context().Err()
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := execute(ctx, t, options, "await-cancel")
	if got.code != clierr.ExitInterrupted {
		t.Fatalf("exit code = %d, want %d (stderr %q)", got.code, clierr.ExitInterrupted, got.stderr)
	}
	if strings.TrimSpace(got.stderr) != "Interrupted." {
		t.Fatalf("stderr = %q", got.stderr)
	}
}

func TestInvalidGlobalSettingsAreUsageErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		message string
	}{
		{"unsupported output", []string{"--output", "xml", "ok"}, `unsupported output format "xml"`},
		{"bad timeout", []string{"--request-timeout", "banana", "ok"}, `invalid request timeout "banana"`},
		{"negative timeout", []string{"--request-timeout", "-5s", "ok"}, "invalid request timeout"},
		{"damaged config", []string{"--config", "BROKEN", "ok"}, "parse CLI config"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string(nil), test.args...)
			for index, arg := range args {
				if arg == "BROKEN" {
					args[index] = writeBrokenConfig(t)
				}
			}

			got := run(t, testOptions(), args...)
			if got.code != clierr.ExitUsage {
				t.Fatalf("exit code = %d, want %d (stderr %q)", got.code, clierr.ExitUsage, got.stderr)
			}
			if !strings.Contains(got.stderr, test.message) {
				t.Errorf("stderr = %q, want %q", got.stderr, test.message)
			}
			if got.stdout != "" {
				t.Errorf("the command must not run: stdout = %q", got.stdout)
			}
		})
	}
}

func TestHelpAndCompletionDoNotNeedAHealthyConfig(t *testing.T) {
	broken := writeBrokenConfig(t)

	for _, args := range [][]string{
		{"--config", broken, "help"},
		{"--config", broken, "help", "ok"},
		{"--config", broken, "completion", "bash"},
		{"--config", broken, "--help"},
	} {
		got := run(t, testOptions(), args...)

		if got.code != 0 {
			t.Errorf("%v: exit code = %d, stderr = %q", args, got.code, got.stderr)
		}
		if strings.TrimSpace(got.stdout) == "" {
			t.Errorf("%v: no output", args)
		}
	}
}

func TestGlobalFlagsAndEnvironmentReachCommands(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.yaml")

	t.Run("defaults", func(t *testing.T) {
		got := run(t, testOptions(), "--config", missing, "settings")

		want := "output= quiet=false no-headers=false no-input=false yes=false timeout=30s env=\n"
		if got.code != 0 || got.stdout != want {
			t.Fatalf("result = %+v, want stdout %q", got, want)
		}
	})

	t.Run("flags", func(t *testing.T) {
		got := run(t, testOptions(), "--config", missing, "-o", "json", "-q", "--no-headers", "--no-input", "-y",
			"--request-timeout", "7s", "-n", "prod", "settings")

		want := "output=json quiet=true no-headers=true no-input=true yes=true timeout=7s env=prod\n"
		if got.code != 0 || got.stdout != want {
			t.Fatalf("result = %+v, want stdout %q", got, want)
		}
	})

	t.Run("environment variables", func(t *testing.T) {
		isolate(t)
		t.Setenv("MACHINARCHY_OUTPUT", "yaml")
		t.Setenv("MACHINARCHY_QUIET", "true")
		t.Setenv("MACHINARCHY_REQUEST_TIMEOUT", "2m")
		t.Setenv("MACHINARCHY_ENV", "staging")

		var out, errOut bytes.Buffer
		options := testOptions()
		options.Streams = cli.IOStreams{In: strings.NewReader(""), Out: &out, ErrOut: &errOut}
		code := cli.Run(context.Background(), options, []string{"--config", missing, "settings"})

		want := "output=yaml quiet=true no-headers=false no-input=false yes=false timeout=2m0s env=staging\n"
		if code != 0 || out.String() != want {
			t.Fatalf("code = %d, stdout = %q, stderr = %q, want stdout %q", code, out.String(), errOut.String(), want)
		}
	})

	t.Run("flag beats environment", func(t *testing.T) {
		isolate(t)
		t.Setenv("MACHINARCHY_OUTPUT", "yaml")

		var out, errOut bytes.Buffer
		options := testOptions()
		options.Streams = cli.IOStreams{In: strings.NewReader(""), Out: &out, ErrOut: &errOut}
		code := cli.Run(context.Background(), options, []string{"--config", missing, "-o", "json", "settings"})

		if code != 0 || !strings.HasPrefix(out.String(), "output=json ") {
			t.Fatalf("code = %d, stdout = %q", code, out.String())
		}
	})
}

func TestConfirmationFailsSafelyWithoutATerminal(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.yaml")

	t.Run("no terminal and no --yes", func(t *testing.T) {
		got := run(t, testOptions(), "--config", missing, "confirm")

		if got.code != clierr.ExitUsage {
			t.Fatalf("exit code = %d, want %d", got.code, clierr.ExitUsage)
		}
		if !strings.Contains(got.stderr, "confirmation required") || !strings.Contains(got.stderr, "--yes") {
			t.Errorf("stderr = %q", got.stderr)
		}
		if strings.Contains(got.stderr, "[y/N]") {
			t.Errorf("a prompt was shown without a terminal: %q", got.stderr)
		}
	})

	t.Run("--no-input", func(t *testing.T) {
		got := run(t, testOptions(), "--config", missing, "--no-input", "confirm")
		if got.code != clierr.ExitUsage {
			t.Fatalf("exit code = %d, want %d", got.code, clierr.ExitUsage)
		}
	})

	t.Run("--yes proceeds", func(t *testing.T) {
		got := run(t, testOptions(), "--config", missing, "--yes", "confirm")
		if got.code != 0 || got.stderr != "" {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("the environment can never supply --yes", func(t *testing.T) {
		isolate(t)
		t.Setenv("MACHINARCHY_CLI_YES", "true")

		var out, errOut bytes.Buffer
		options := testOptions()
		options.Streams = cli.IOStreams{In: strings.NewReader("y\n"), Out: &out, ErrOut: &errOut}
		code := cli.Run(context.Background(), options, []string{"--config", missing, "confirm"})

		if code != clierr.ExitUsage {
			t.Fatalf("exit code = %d, want %d: MACHINARCHY_CLI_YES must not bypass confirmation", code, clierr.ExitUsage)
		}
	})
}

func TestRegistrationIsStrict(t *testing.T) {
	expectPanic := func(t *testing.T, want string, groups []cli.CommandGroup) {
		t.Helper()

		defer func() {
			recovered := recover()
			if recovered == nil {
				t.Fatal("registration did not panic")
			}
			if message := fmt.Sprint(recovered); !strings.Contains(message, want) {
				t.Fatalf("panic = %q, want it to mention %q", message, want)
			}
		}()
		cli.NewRootCommand(cli.Options{Groups: groups})
	}

	named := func(name string, aliases ...string) cli.CommandBuilder {
		return func(*cli.Factory) *cobra.Command {
			return &cobra.Command{Use: name, Aliases: aliases, Short: name, Run: func(*cobra.Command, []string) {}}
		}
	}

	t.Run("duplicate names", func(t *testing.T) {
		expectPanic(t, `"alpha" is registered twice`, []cli.CommandGroup{
			{ID: "a", Title: "A", Commands: []cli.CommandBuilder{named("alpha")}},
			{ID: "b", Title: "B", Commands: []cli.CommandBuilder{named("alpha")}},
		})
	})

	t.Run("alias collides with a name", func(t *testing.T) {
		expectPanic(t, `"beta" is registered twice`, []cli.CommandGroup{
			{ID: "a", Title: "A", Commands: []cli.CommandBuilder{named("alpha", "beta"), named("beta")}},
		})
	})

	t.Run("reserved names", func(t *testing.T) {
		expectPanic(t, `"help" is registered twice`, []cli.CommandGroup{
			{ID: "a", Title: "A", Commands: []cli.CommandBuilder{named("help")}},
		})
	})

	t.Run("nil builders and nil commands are skipped, untitled groups work", func(t *testing.T) {
		options := cli.Options{Groups: []cli.CommandGroup{
			{ID: "untitled", Commands: []cli.CommandBuilder{
				nil,
				func(*cli.Factory) *cobra.Command { return nil },
				named("alpha"),
			}},
			{Commands: []cli.CommandBuilder{named("ungrouped")}},
		}}

		got := run(t, options, "--help")
		if got.code != 0 {
			t.Fatalf("help failed: %+v", got)
		}
		for _, want := range []string{"alpha", "ungrouped", "untitled"} {
			if !strings.Contains(got.stdout, want) {
				t.Errorf("help lacks %q:\n%s", want, got.stdout)
			}
		}
	})

	t.Run("registration order does not change the command set", func(t *testing.T) {
		forward := cli.NewRootCommand(cli.Options{Groups: []cli.CommandGroup{
			{ID: "a", Title: "A", Commands: []cli.CommandBuilder{named("one"), named("two")}},
		}})
		reverse := cli.NewRootCommand(cli.Options{Groups: []cli.CommandGroup{
			{ID: "a", Title: "A", Commands: []cli.CommandBuilder{named("two"), named("one")}},
		}})

		names := func(root *cobra.Command) string {
			var found []string
			for _, child := range root.Commands() {
				found = append(found, child.Name())
			}
			return strings.Join(found, ",")
		}
		if names(forward) != names(reverse) {
			t.Fatalf("command order depends on registration order: %q vs %q", names(forward), names(reverse))
		}
	})
}

func TestFactoryBuildsTargetsFromResolvedConfiguration(t *testing.T) {
	var (
		mu  sync.Mutex
		got config.Target
	)
	builder := func(target config.Target) (api.Client, error) {
		mu.Lock()
		defer mu.Unlock()
		got = target
		return stubClient{}, nil
	}
	options := testOptions(command("client-target", func(c *cobra.Command, f *cli.Factory) error {
		_, err := f.NewAPIClient(c.Context())
		return err
	}))
	options.ClientBuilder = builder

	t.Run("from the config file", func(t *testing.T) {
		path := writeConfig(t, "https://file.example/", "file-token")

		result := run(t, options, "--config", path, "client-target")
		if result.code != 0 {
			t.Fatalf("result = %+v", result)
		}

		mu.Lock()
		defer mu.Unlock()
		if got.Server != "https://file.example" || got.Token != "file-token" || got.RequestTimeout != 30*time.Second || got.ContextName != "test" {
			t.Fatalf("target = %+v", got)
		}
	})

	t.Run("flags override the file", func(t *testing.T) {
		path := writeConfig(t, "https://file.example", "file-token")

		result := run(t, options, "--config", path, "--server", "https://flag.example", "--request-timeout", "5s", "client-target")
		if result.code != 0 {
			t.Fatalf("result = %+v", result)
		}

		mu.Lock()
		defer mu.Unlock()
		if got.Server != "https://flag.example" || got.RequestTimeout != 5*time.Second || got.Token != "file-token" {
			t.Fatalf("target = %+v", got)
		}
	})

	t.Run("no context is a usage error with setup help", func(t *testing.T) {
		result := run(t, options, "--config", filepath.Join(t.TempDir(), "absent.yaml"), "client-target")

		if result.code != clierr.ExitUsage {
			t.Fatalf("exit code = %d, want %d", result.code, clierr.ExitUsage)
		}
		if !strings.Contains(result.stderr, "no active context is configured") || !strings.Contains(result.stderr, "config set-cluster") {
			t.Errorf("stderr = %q", result.stderr)
		}
		if strings.Contains(result.stderr, "--help") {
			t.Errorf("a hint is already provided; the generic help hint must not replace it: %q", result.stderr)
		}
	})
}

// TestDefaultClientAuthenticatesEveryRequest exercises the production wiring:
// config resolution, the default client builder, and the HTTP client together.
func TestDefaultClientAuthenticatesEveryRequest(t *testing.T) {
	var (
		mu      sync.Mutex
		headers []string
		agents  []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Get("Authorization"))
		agents = append(agents, r.Header.Get("User-Agent"))
		mu.Unlock()

		switch r.URL.Path {
		case "/api/v1/managed/hosts":
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"id":"1","name":"a"}},{"metadata":{"id":"2","name":"b"}}]}`))
		case "/api/v1/environments":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such route"}`))
		}
	}))
	t.Cleanup(server.Close)

	options := testOptions(
		command("count-hosts", func(c *cobra.Command, f *cli.Factory) error {
			client, err := f.NewAPIClient(c.Context())
			if err != nil {
				return err
			}
			hosts, err := client.ListHosts(c.Context(), api.HostQuery{})
			if err != nil {
				return err
			}
			if _, err := client.ListEnvironments(c.Context()); err != nil {
				return err
			}
			_, err = fmt.Fprintln(c.OutOrStdout(), strconv.Itoa(len(hosts.Items)))
			return err
		}),
	)
	path := writeConfig(t, server.URL, "secret-token-value")

	got := run(t, options, "--config", path, "count-hosts")
	if got.code != 0 || got.stdout != "2\n" {
		t.Fatalf("result = %+v", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(headers) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(headers))
	}
	for index, header := range headers {
		if header != "Bearer secret-token-value" {
			t.Errorf("request %d Authorization = %q", index, header)
		}
		if !strings.HasPrefix(agents[index], "machinarchy-cli/") {
			t.Errorf("request %d User-Agent = %q", index, agents[index])
		}
	}
}

func TestStalledServerHonoursTheRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	t.Cleanup(server.Close)

	options := testOptions(command("list-hosts", func(c *cobra.Command, f *cli.Factory) error {
		client, err := f.NewAPIClient(c.Context())
		if err != nil {
			return err
		}
		_, err = client.ListHosts(c.Context(), api.HostQuery{})
		return err
	}))

	started := time.Now()
	got := run(t, options, "--config", writeConfig(t, server.URL, "tok"), "--request-timeout", "150ms", "list-hosts")

	if got.code != clierr.ExitUnavailable {
		t.Fatalf("exit code = %d, want %d (stderr %q)", got.code, clierr.ExitUnavailable, got.stderr)
	}
	if !strings.Contains(got.stderr, "timed out") || !strings.Contains(got.stderr, "--request-timeout") {
		t.Errorf("stderr = %q", got.stderr)
	}
	if elapsed := time.Since(started); elapsed > 2500*time.Millisecond {
		t.Errorf("the command took %s; the request timeout was not honoured", elapsed)
	}
}

// TestCLIStaysAnAPIClient enforces ADR-035: CLI code may reach Machinarchy only
// through its public HTTP API, so it must not import the database, services,
// transport, or any other application package, and only the api package may
// speak HTTP.
func TestCLIStaysAnAPIClient(t *testing.T) {
	const modulePrefix = "github.com/machinarchy/machinarchy/internal/"

	allowed := func(path string) bool {
		return strings.HasPrefix(path, modulePrefix+"cli") ||
			path == modulePrefix+"buildinfo"
	}

	roots := []string{".", filepath.Join("..", "..", "cmd", "machinarchy")}
	checked := 0
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			checked++

			inAPIPackage := filepath.Base(filepath.Dir(path)) == "api"
			for _, spec := range file.Imports {
				imported, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return err
				}

				switch {
				case strings.HasPrefix(imported, modulePrefix) && !allowed(imported):
					t.Errorf("%s imports %s; the CLI may only use the public HTTP API (ADR-035)", path, imported)
				case imported == "database/sql" || strings.HasPrefix(imported, "database/sql/"):
					t.Errorf("%s imports %s; the CLI must not access a database (ADR-035)", path, imported)
				case imported == "net/http" && !inAPIPackage:
					t.Errorf("%s imports net/http; HTTP communication belongs in internal/cli/api", path)
				}
			}

			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	if checked < 10 {
		t.Fatalf("only %d source files were inspected; the guard is not looking in the right place", checked)
	}
}

func TestCommandGroupsAreStrictAndNeedNoConfig(t *testing.T) {
	leaf := func(name string) *cobra.Command {
		return &cobra.Command{Use: name, Short: name, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(c.OutOrStdout(), "ran "+name)
			return err
		}}
	}
	// Both groups intentionally omit Args: the root must make them strict.
	options := cli.Options{Groups: []cli.CommandGroup{{ID: "g", Title: "Groups", Commands: []cli.CommandBuilder{
		func(*cli.Factory) *cobra.Command {
			group := &cobra.Command{Use: "group", Short: "a group"}
			group.AddCommand(leaf("child"))
			return group
		},
		func(*cli.Factory) *cobra.Command {
			group := &cobra.Command{Use: "outer", Short: "a nested group"}
			inner := &cobra.Command{Use: "inner", Short: "an inner group"}
			inner.AddCommand(leaf("deep"))
			group.AddCommand(inner)
			return group
		},
	}}}}
	broken := writeBrokenConfig(t)

	t.Run("a typo under a group is a usage error, not a silent success", func(t *testing.T) {
		for _, args := range [][]string{{"group", "chld"}, {"outer", "inner", "dep"}, {"outer", "nope"}} {
			got := run(t, options, args...)

			if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "unknown command") {
				t.Errorf("%v: result = %+v", args, got)
			}
			if got.stdout != "" {
				t.Errorf("%v: stdout = %q", args, got.stdout)
			}
		}
	})

	t.Run("a group without a subcommand prints its help", func(t *testing.T) {
		for _, args := range [][]string{{"group"}, {"outer"}, {"outer", "inner"}} {
			got := run(t, options, args...)

			if got.code != 0 || !strings.Contains(got.stdout, "Available Commands:") {
				t.Errorf("%v: result = %+v", args, got)
			}
		}
	})

	t.Run("group help does not read the config file", func(t *testing.T) {
		got := run(t, options, "--config", broken, "group")
		if got.code != 0 {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("subcommands still run and still read the config file", func(t *testing.T) {
		if got := run(t, options, "group", "child"); got.code != 0 || got.stdout != "ran child\n" {
			t.Errorf("result = %+v", got)
		}
		if got := run(t, options, "outer", "inner", "deep"); got.code != 0 || got.stdout != "ran deep\n" {
			t.Errorf("result = %+v", got)
		}
		if got := run(t, options, "--config", broken, "group", "child"); got.code != clierr.ExitUsage {
			t.Errorf("a leaf command must still validate the config file: %+v", got)
		}
	})

	t.Run("nested leaf commands validate config inherited from no group", func(t *testing.T) {
		for _, args := range [][]string{
			{"group", "child"},
			{"outer", "inner", "deep"},
		} {
			got := run(t, options, append(
				[]string{"--config", broken},
				args...,
			)...)

			if got.code != clierr.ExitUsage {
				t.Errorf("%v: expected config validation error, got %+v", args, got)
			}

			if strings.Contains(got.stdout, "ran ") {
				t.Errorf("%v: command executed despite invalid config: %q", args, got.stdout)
			}
		}
	})
}
