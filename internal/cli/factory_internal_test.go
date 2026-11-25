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

	"github.com/spf13/pflag"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

func clearEnvironment(t *testing.T) {
	t.Helper()

	t.Setenv("MACHINARCHY_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	for _, name := range []string{
		"MACHINARCHY_CLI_CONTEXT", "MACHINARCHY_CLI_ENV", "MACHINARCHY_CLI_OUTPUT",
		"MACHINARCHY_CLI_VERBOSE", "MACHINARCHY_CLI_SERVER", "MACHINARCHY_CLI_TOKEN",
		"MACHINARCHY_CLI_REQUEST_TIMEOUT", "MACHINARCHY_CLI_QUIET", "MACHINARCHY_CLI_NO_HEADERS",
		"MACHINARCHY_CLI_NO_INPUT", "MACHINARCHY_CLI_YES",
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
	t.Setenv("MACHINARCHY_CLI_YES", "true")

	factory := &Factory{
		Streams:    IOStreams{In: strings.NewReader(""), Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}},
		isTerminal: func(io.Reader) bool { return false },
	}
	if err := factory.LoadSettings(globalFlags(t)); err != nil {
		t.Fatal(err)
	}

	if factory.Yes() {
		t.Fatal("MACHINARCHY_CLI_YES must not enable --yes")
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

func TestLoadingWarnsAboutLooseConfigPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	clearEnvironment(t)

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "users:\n  me:\n    token: super-secret-token\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	var errOut bytes.Buffer
	factory := &Factory{Streams: IOStreams{In: strings.NewReader(""), Out: &bytes.Buffer{}, ErrOut: &errOut}}
	if err := factory.LoadConfig(path, globalFlags(t)); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if !strings.Contains(errOut.String(), "Warning: ") || !strings.Contains(errOut.String(), "chmod 600") {
		t.Fatalf("stderr = %q, want a permissions warning", errOut.String())
	}
	if strings.Contains(errOut.String(), "super-secret-token") {
		t.Fatal("the warning leaks the token")
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
