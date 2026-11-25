package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/config"
	"github.com/machinarchy/machinarchy/internal/cli/ui"
)

func clearEnvironment(t *testing.T) {
	t.Helper()

	t.Setenv("MACHINARCHY_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	for _, name := range []string{
		"MACHINARCHY_CONTEXT", "MACHINARCHY_ENV", "MACHINARCHY_OUTPUT",
		"MACHINARCHY_VERBOSE", "MACHINARCHY_SERVER", "MACHINARCHY_TOKEN",
		"MACHINARCHY_REQUEST_TIMEOUT", "MACHINARCHY_QUIET", "MACHINARCHY_NO_HEADERS",
		"MACHINARCHY_NO_INPUT", "MACHINARCHY_YES",
	} {
		t.Setenv(name, "")
	}
}

func globalFlags(t *testing.T, args ...string) *pflag.FlagSet {
	t.Helper()

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("output", "", "")
	flags.String("request-timeout", "", "")
	flags.Bool("quiet", false, "")
	flags.Bool("no-headers", false, "")
	flags.Bool("no-input", false, "")
	flags.Bool("yes", false, "")
	flags.String("confirm", "", "")
	flags.String("confirm-env", "", "")
	flags.Bool("dry-run", false, "")
	flags.Bool("plain", false, "")
	if err := flags.Parse(args); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	return flags
}

func newFactory(t *testing.T, input string, terminal bool, args ...string) (*Factory, *bytes.Buffer) {
	t.Helper()
	clearEnvironment(t)

	var errOut bytes.Buffer
	factory := &Factory{
		Streams:    IOStreams{In: strings.NewReader(input), Out: &bytes.Buffer{}, ErrOut: &errOut},
		isTerminal: func(io.Reader) bool { return terminal },
	}
	if err := factory.LoadSettings(globalFlags(t, args...)); err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}

	return factory, &errOut
}

func TestConfirm(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		terminal   bool
		args       []string
		want       bool
		wantCode   int
		wantPrompt bool
	}{
		{name: "--yes skips the prompt", terminal: true, args: []string{"--yes"}, want: true},
		{name: "--yes works without a terminal", terminal: false, args: []string{"--yes"}, want: true},
		{name: "y", input: "y\n", terminal: true, want: true, wantPrompt: true},
		{name: "yes", input: "yes\n", terminal: true, want: true, wantPrompt: true},
		{name: "case and spaces are forgiven", input: "  YES \n", terminal: true, want: true, wantPrompt: true},
		{name: "answer without a newline", input: "y", terminal: true, want: true, wantPrompt: true},
		{name: "n", input: "n\n", terminal: true, want: false, wantPrompt: true},
		{name: "empty answer defaults to no", input: "\n", terminal: true, want: false, wantPrompt: true},
		{name: "anything else is no", input: "maybe\n", terminal: true, want: false, wantPrompt: true},
		{name: "end of input is not consent", input: "", terminal: true, wantCode: clierr.ExitUsage, wantPrompt: true},
		{name: "no terminal fails safely", input: "y\n", terminal: false, wantCode: clierr.ExitUsage},
		{name: "--no-input fails even on a terminal", input: "y\n", terminal: true, args: []string{"--no-input"}, wantCode: clierr.ExitUsage},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			factory, errOut := newFactory(t, test.input, test.terminal, test.args...)

			got, err := factory.Confirm("Delete host web-01?")

			if test.wantCode != 0 {
				if err == nil {
					t.Fatalf("Confirm = %v, nil; want an error", got)
				}
				if code := clierr.ExitCode(err); code != test.wantCode {
					t.Fatalf("exit code = %d, want %d", code, test.wantCode)
				}
				if !strings.Contains(clierr.Hint(err), "--yes") {
					t.Errorf("hint = %q, want it to mention --yes", clierr.Hint(err))
				}
				if got {
					t.Error("an error must never come with consent")
				}
			} else {
				if err != nil {
					t.Fatalf("Confirm: %v", err)
				}
				if got != test.want {
					t.Fatalf("Confirm = %v, want %v", got, test.want)
				}
			}

			prompted := strings.Contains(errOut.String(), "Delete host web-01? [y/N]: ")
			if prompted != test.wantPrompt {
				t.Errorf("prompt shown = %v, want %v (stderr %q)", prompted, test.wantPrompt, errOut.String())
			}
		})
	}
}

func TestPromptGoesToStderrNotStdout(t *testing.T) {
	factory, errOut := newFactory(t, "y\n", true)
	stdout := factory.Streams.Out.(*bytes.Buffer)

	if _, err := factory.Confirm("Proceed?"); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q; prompts must not pollute piped output", stdout.String())
	}
	if !strings.Contains(errOut.String(), "Proceed?") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestRequireConfirmation(t *testing.T) {
	t.Run("confirmed", func(t *testing.T) {
		factory, _ := newFactory(t, "y\n", true)
		if err := factory.RequireConfirmation("Proceed?"); err != nil {
			t.Fatalf("RequireConfirmation: %v", err)
		}
	})

	t.Run("declined aborts with a failure", func(t *testing.T) {
		factory, _ := newFactory(t, "n\n", true)

		err := factory.RequireConfirmation("Proceed?")
		if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("error = %v", err)
		}
		if code := clierr.ExitCode(err); code != clierr.ExitFailure {
			t.Fatalf("exit code = %d, want %d", code, clierr.ExitFailure)
		}
	})
}

func TestEnvironmentCannotSupplyConfirmation(t *testing.T) {
	clearEnvironment(t)
	t.Setenv("MACHINARCHY_YES", "true")

	factory := &Factory{
		Streams:    IOStreams{In: strings.NewReader(""), Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}},
		isTerminal: func(io.Reader) bool { return false },
	}
	if err := factory.LoadSettings(globalFlags(t)); err != nil {
		t.Fatal(err)
	}

	if factory.Yes() {
		t.Fatal("MACHINARCHY_YES must not enable --yes")
	}
	if _, err := factory.Confirm("Proceed?"); err == nil {
		t.Fatal("Confirm succeeded without an explicit --yes")
	}
}

func TestYesOnNilFactory(t *testing.T) {
	var factory *Factory
	if factory.Yes() {
		t.Fatal("a nil factory must not report --yes")
	}
	if factory.Config() != nil {
		t.Fatal("a nil factory has no config")
	}
}

func TestIsTerminal(t *testing.T) {
	if isTerminal(strings.NewReader("")) {
		t.Error("a strings.Reader is not a terminal")
	}
	if isTerminal(nil) {
		t.Error("a nil reader is not a terminal")
	}

	regular := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(regular, []byte("y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handle, err := os.Open(regular)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()

	if isTerminal(handle) {
		t.Error("a redirected regular file is not a terminal")
	}
}

func TestOutputFormatAndPrinterDefaults(t *testing.T) {
	t.Run("command default applies when the user chose nothing", func(t *testing.T) {
		factory, _ := newFactory(t, "", false)

		format, err := factory.OutputFormat("json")
		if err != nil || format != "json" {
			t.Fatalf("OutputFormat = %q, %v; want the command's default", format, err)
		}
	})

	t.Run("the user's choice beats the command default", func(t *testing.T) {
		factory, _ := newFactory(t, "", false, "--output", "yaml")

		format, err := factory.OutputFormat("json")
		if err != nil || format != "yaml" {
			t.Fatalf("OutputFormat = %q, %v; want yaml", format, err)
		}
	})

	t.Run("a nil store falls back to the default", func(t *testing.T) {
		factory := &Factory{}

		format, err := factory.OutputFormat("table")
		if err != nil || format != "table" {
			t.Fatalf("OutputFormat = %q, %v", format, err)
		}
	})

	t.Run("printer honours quiet and no-headers", func(t *testing.T) {
		factory, _ := newFactory(t, "", false, "--quiet", "--no-headers")

		var buffer bytes.Buffer
		printer, err := factory.Printer(&buffer, "table")
		if err != nil {
			t.Fatal(err)
		}
		if err := printer.IDs([]string{"a", "b"}); err != nil {
			t.Fatal(err)
		}
		if buffer.String() != "a\nb\n" {
			t.Fatalf("output = %q", buffer.String())
		}
	})
}

// A credentials file that other users can read is refused, not tolerated: the
// command stops and says how to fix it, without printing the token.
func TestLooseCredentialPermissionsStopTheCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	clearEnvironment(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	credentials := config.CredentialsPath(path)
	if err := os.WriteFile(path, []byte("version: 2\nactive: me\nservers:\n  me:\n    url: https://machinarchy.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentials, []byte("version: 2\nservers:\n  me:\n    token:\n      kind: token\n      secret: super-secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(credentials, 0o644); err != nil {
		t.Fatal(err)
	}

	var errOut bytes.Buffer
	factory := &Factory{Streams: IOStreams{In: strings.NewReader(""), Out: &bytes.Buffer{}, ErrOut: &errOut}}
	if err := factory.LoadConfig(path, globalFlags(t)); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	_, err := factory.NewAPIClient(context.Background())
	if err == nil {
		t.Fatal("a credentials file readable by other users was used")
	}
	if !strings.Contains(clierr.Hint(err), "chmod 600 "+credentials) || strings.Contains(err.Error()+clierr.Why(err)+clierr.Hint(err), "super-secret-token") {
		t.Fatalf("err = %v, next = %q", err, clierr.Hint(err))
	}
}

func TestNewAPIClientRequiresInitialisedConfiguration(t *testing.T) {
	var uninitialised *Factory
	if _, err := uninitialised.NewAPIClient(context.Background()); err == nil {
		t.Error("a nil factory produced a client")
	}

	factory := &Factory{}
	if _, err := factory.NewAPIClient(context.Background()); err == nil {
		t.Error("a factory without config produced a client")
	}

	loaded, _ := newFactory(t, "", false)

	var noContext context.Context
	if _, err := loaded.NewAPIClient(noContext); err == nil {
		t.Error("a nil context was accepted")
	}
}

func TestReportError(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		started     bool
		path        string
		wantCode    int
		wantStderr  string
		wantAbsent  []string
		wantPresent []string
	}{
		{name: "success prints nothing", wantCode: clierr.ExitOK, wantStderr: ""},
		{
			name: "classified usage error keeps its own hint", err: clierr.Newf(clierr.ExitUsage, "custom hint", "bad value"),
			started: true, wantCode: clierr.ExitUsage,
			wantPresent: []string{"Error: bad value\n", "Next:  custom hint\n"}, wantAbsent: []string{"--help"},
		},
		{
			name: "usage error without a hint points at help", err: clierr.Usagef("bad value"),
			started: true, wantCode: clierr.ExitUsage,
			wantPresent: []string{"Error: bad value\n", "Next:  machinarchy --help\n"},
		},
		{
			name: "usage error in a subcommand points at that command's help", err: clierr.Usagef("pick one with --host"),
			started: true, path: "machinarchy logs operation", wantCode: clierr.ExitUsage,
			wantPresent: []string{"Next:  machinarchy logs operation --help\n"}, wantAbsent: []string{"available commands"},
		},
		{
			name: "unclassified error before the command ran is a usage error", err: errors.New("unknown flag: --x"),
			started: false, wantCode: clierr.ExitUsage,
			wantPresent: []string{"Error: unknown flag: --x\n", "--help"},
		},
		{
			name: "unclassified error while running is a plain failure", err: errors.New("boom"),
			started: true, wantCode: clierr.ExitFailure,
			wantPresent: []string{"Error: boom\n"}, wantAbsent: []string{"Next:", "--help"},
		},
		{
			name: "interrupt", err: context.Canceled, started: true, wantCode: clierr.ExitInterrupted,
			wantPresent: []string{"Interrupted.\n"}, wantAbsent: []string{"Error:"},
		},
		{
			name: "interrupt before start is still an interrupt", err: context.Canceled, started: false, wantCode: clierr.ExitInterrupted,
			wantPresent: []string{"Interrupted.\n"},
		},
		{
			name: "multi-line hints are preserved", err: clierr.Newf(clierr.ExitAuth, "line one\nline two", "denied"),
			started: true, wantCode: clierr.ExitAuth,
			wantPresent: []string{"Next:  line one\n       line two\n"},
		},
		{
			name: "percent signs in messages are not treated as formats", err: clierr.Newf(clierr.ExitFailure, "use 100%s", "%s", "50%d done"),
			started: true, wantCode: clierr.ExitFailure,
			wantPresent: []string{"Error: 50%d done\n"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var buffer bytes.Buffer

			code := reportError(&buffer, test.err, test.started, test.path, presentation{})

			if code != test.wantCode {
				t.Fatalf("exit code = %d, want %d", code, test.wantCode)
			}
			if test.err == nil && buffer.Len() != 0 {
				t.Fatalf("output = %q, want nothing", buffer.String())
			}
			for _, want := range test.wantPresent {
				if !strings.Contains(buffer.String(), want) {
					t.Errorf("output lacks %q:\n%s", want, buffer.String())
				}
			}
			for _, unwanted := range test.wantAbsent {
				if strings.Contains(buffer.String(), unwanted) {
					t.Errorf("output contains %q:\n%s", unwanted, buffer.String())
				}
			}
		})
	}
}

func TestConfirmTyped(t *testing.T) {
	t.Run("--confirm with the right name", func(t *testing.T) {
		factory, _ := newFactory(t, "", false, "--confirm", "ci")
		if err := factory.ConfirmTyped("Revoke ci?", "ci"); err != nil {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("--confirm with another name", func(t *testing.T) {
		factory, _ := newFactory(t, "", false, "--confirm", "other")
		err := factory.ConfirmTyped("Revoke ci?", "ci")
		if err == nil || !strings.Contains(err.Error(), `--confirm is "other", but this is about "ci"`) || clierr.Hint(err) != "--confirm ci" {
			t.Fatalf("err = %v / %q", err, clierr.Hint(err))
		}
	})

	t.Run("--yes is not enough, with or without a terminal", func(t *testing.T) {
		for _, terminal := range []bool{false, true} {
			args := []string{"--yes"}
			if terminal {
				args = append(args, "--no-input")
			}
			factory, _ := newFactory(t, "ci\n", terminal, args...)
			err := factory.ConfirmTyped("Revoke ci?", "ci")
			if err == nil || !strings.Contains(err.Error(), "this needs confirmation, and --yes is not enough here") || clierr.Hint(err) != "--confirm ci" {
				t.Fatalf("terminal %v: err = %v", terminal, err)
			}
		}
	})

	t.Run("typing the name on a terminal", func(t *testing.T) {
		factory, errOut := newFactory(t, "ci\n", true)
		if err := factory.ConfirmTyped("Revoke ci?", "ci"); err != nil {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(errOut.String(), "Revoke ci?\nType \"ci\" to confirm: ") {
			t.Fatalf("prompt = %q", errOut.String())
		}
	})

	t.Run("a wrong answer, and the end of input, abort", func(t *testing.T) {
		for _, input := range []string{"nope\n", ""} {
			factory, _ := newFactory(t, input, true)
			err := factory.ConfirmTyped("Revoke ci?", "ci")
			if err == nil || !strings.Contains(err.Error(), "aborted; nothing was changed") {
				t.Fatalf("input %q: err = %v", input, err)
			}
		}
	})
}

func TestReadSecretAndPrompter(t *testing.T) {
	t.Run("nothing is asked without a terminal", func(t *testing.T) {
		factory, _ := newFactory(t, "x\n", false)
		if _, err := factory.ReadSecret("Password: "); !errors.Is(err, ErrNoTerminal) {
			t.Fatalf("ReadSecret err = %v", err)
		}
		if factory.Prompter().Interactive {
			t.Fatal("the prompter must not be interactive")
		}
	})

	t.Run("--no-input stops the prompts on a terminal", func(t *testing.T) {
		factory, _ := newFactory(t, "x\n", true, "--no-input")
		if _, err := factory.ReadSecret("Password: "); !errors.Is(err, ErrNoTerminal) {
			t.Fatalf("ReadSecret err = %v", err)
		}
		if factory.Prompter().Interactive {
			t.Fatal("the prompter must not be interactive")
		}
	})

	t.Run("a terminal gets a prompter that shares one reader", func(t *testing.T) {
		factory, _ := newFactory(t, "first\nsecond\n", true)
		prompter := factory.Prompter()
		if !prompter.Interactive || prompter.ReadSecret == nil {
			t.Fatalf("prompter = %+v", prompter)
		}
		one, err := prompter.Line("One", "")
		two, err2 := factory.Prompter().Line("Two", "")
		if err != nil || err2 != nil || one != "first" || two != "second" {
			t.Fatalf("answers = %q, %q (%v, %v): a second prompter must not lose buffered input", one, two, err, err2)
		}
	})

	t.Run("an injected hidden reader is used on a terminal", func(t *testing.T) {
		factory, _ := newFactory(t, "", true)
		factory.readSecret = func(prompt string) (string, error) { return "typed:" + prompt, nil }
		if got, err := factory.ReadSecret("Password: "); err != nil || got != "typed:Password: " {
			t.Fatalf("ReadSecret = %q, %v", got, err)
		}
	})

	t.Run("a terminal that is not a file cannot hide typing", func(t *testing.T) {
		factory, _ := newFactory(t, "", true)
		if _, err := factory.ReadSecret("Password: "); !errors.Is(err, ErrNoTerminal) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestClockHostnameAndCredentialsAccessors(t *testing.T) {
	factory, _ := newFactory(t, "", false)

	before := time.Now()
	if got := factory.Now(); got.Before(before) {
		t.Fatalf("Now = %v", got)
	}
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	factory.now = func() time.Time { return fixed }
	if !factory.Now().Equal(fixed) {
		t.Fatal("the injected clock was ignored")
	}

	if name, err := factory.Hostname(); err != nil || name == "" {
		t.Fatalf("Hostname = %q, %v", name, err)
	}
	factory.hostname = func() (string, error) { return "box", nil }
	if name, _ := factory.Hostname(); name != "box" {
		t.Fatalf("Hostname = %q", name)
	}

	if factory.Credentials() != nil {
		t.Fatal("commands that skip the config have no credential store")
	}
	var nilFactory *Factory
	if nilFactory.Style() != (ui.Style{}) {
		t.Fatal("a nil factory has the zero style")
	}
}

func TestNewManagementClientNeedsAManagementClient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("version: 2\nactive: lab\nservers:\n  lab:\n    url: https://machinarchy.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.CredentialsPath(path), []byte("version: 2\nservers:\n  lab:\n    session:\n      kind: session\n      secret: s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	clearEnvironment(t)

	factory := &Factory{
		Streams:       IOStreams{In: strings.NewReader(""), Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}},
		ClientBuilder: func(config.Target) (api.Client, error) { return nil, nil },
	}
	if err := factory.LoadConfig(path, globalFlags(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := factory.NewManagementClient(context.Background()); err == nil {
		t.Fatal("a nil client was accepted")
	}

	factory.ClientBuilder = func(config.Target) (api.Client, error) { return struct{ api.Client }{}, nil }
	if _, err := factory.NewManagementClient(context.Background()); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("err = %v", err)
	}
}
