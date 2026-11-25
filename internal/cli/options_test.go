package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

// runWithEnv runs the CLI with the given environment variables set on top of
// an isolated environment.
func runWithEnv(t *testing.T, env map[string]string, options cli.Options, args ...string) result {
	t.Helper()
	isolate(t)
	for name, value := range env {
		t.Setenv(name, value)
	}

	var out, errOut bytes.Buffer
	options.Streams = cli.IOStreams{In: strings.NewReader(""), Out: &out, ErrOut: &errOut}
	code := cli.Run(context.Background(), options, args)

	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

func writeConfigWith(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// optionsCommand prints the effective global options and where they came from.
func optionsCommand() cli.CommandBuilder {
	return command("options", func(c *cobra.Command, f *cli.Factory) error {
		store := f.Config()
		_, err := fmt.Fprintf(c.OutOrStdout(), "output=%s output-source=%s plain=%t quiet=%t dry-run=%t confirm=%q confirm-env=%q\n",
			store.OutputExplicit(), store.Source("output"), f.Plain(), store.Quiet(), f.DryRun(), f.ConfirmName(), f.ConfirmEnv())

		return err
	})
}

func TestOptionPrecedence(t *testing.T) {
	saved := writeConfigWith(t, "defaults:\n  output: yaml\n  quiet: true\n")
	none := filepath.Join(t.TempDir(), "absent.yaml")

	tests := []struct {
		name       string
		env        map[string]string
		args       []string
		wantOut    string
		wantStderr string
	}{
		{"default", nil, []string{"--config", none, "options"},
			"output= output-source=default plain=false quiet=false dry-run=false confirm=\"\" confirm-env=\"\"\n", ""},
		{"saved default", nil, []string{"--config", saved, "options"},
			"output=yaml output-source=config file plain=false quiet=true dry-run=false confirm=\"\" confirm-env=\"\"\n", ""},
		{"environment beats saved default", map[string]string{"MACHINARCHY_OUTPUT": "json"}, []string{"--config", saved, "options"},
			"output=json output-source=environment plain=false quiet=true dry-run=false confirm=\"\" confirm-env=\"\"\n", ""},
		{"flag beats environment", map[string]string{"MACHINARCHY_OUTPUT": "json"}, []string{"--config", saved, "-o", "table", "options"},
			"output=table output-source=flag plain=false quiet=true dry-run=false confirm=\"\" confirm-env=\"\"\n", ""},
		{"deprecated name still works and warns", map[string]string{"MACHINARCHY_CLI_OUTPUT": "json"}, []string{"--config", none, "options"},
			"output=json output-source=environment (deprecated name) plain=false quiet=false dry-run=false confirm=\"\" confirm-env=\"\"\n",
			"WARN Deprecated: the environment variable MACHINARCHY_CLI_OUTPUT is now MACHINARCHY_OUTPUT. The old name will stop working in the next release.\n"},
		{"new name beats deprecated name without a warning", map[string]string{"MACHINARCHY_CLI_OUTPUT": "json", "MACHINARCHY_OUTPUT": "yaml"},
			[]string{"--config", none, "options"},
			"output=yaml output-source=environment plain=false quiet=false dry-run=false confirm=\"\" confirm-env=\"\"\n", ""},
		{"deprecated name beats saved default", map[string]string{"MACHINARCHY_CLI_OUTPUT": "json"}, []string{"--config", saved, "options"},
			"output=json output-source=environment (deprecated name) plain=false quiet=true dry-run=false confirm=\"\" confirm-env=\"\"\n",
			"WARN Deprecated: the environment variable MACHINARCHY_CLI_OUTPUT is now MACHINARCHY_OUTPUT. The old name will stop working in the next release.\n"},
		{"warning can be silenced", map[string]string{"MACHINARCHY_CLI_OUTPUT": "json", "MACHINARCHY_NO_DEPRECATION_WARNINGS": "1"},
			[]string{"--config", none, "options"},
			"output=json output-source=environment (deprecated name) plain=false quiet=false dry-run=false confirm=\"\" confirm-env=\"\"\n", ""},
		{"context keeps its old name without a warning", map[string]string{"MACHINARCHY_CLI_CONTEXT": "x"}, []string{"--config", none, "options"},
			"output= output-source=default plain=false quiet=false dry-run=false confirm=\"\" confirm-env=\"\"\n", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := runWithEnv(t, tc.env, testOptions(optionsCommand()), tc.args...)
			if got.code != 0 || got.stdout != tc.wantOut || got.stderr != tc.wantStderr {
				t.Fatalf("result = %+v\nwant stdout %q\nwant stderr %q", got, tc.wantOut, tc.wantStderr)
			}
		})
	}
}

func TestPlainFromFlagEnvironmentAndSavedDefault(t *testing.T) {
	saved := writeConfigWith(t, "defaults:\n  plain: true\n")
	none := filepath.Join(t.TempDir(), "absent.yaml")

	for name, tc := range map[string]struct {
		env  map[string]string
		args []string
		want string
	}{
		"flag":            {nil, []string{"--config", none, "--plain", "options"}, "plain=true"},
		"environment":     {map[string]string{"MACHINARCHY_PLAIN": "1"}, []string{"--config", none, "options"}, "plain=true"},
		"saved default":   {nil, []string{"--config", saved, "options"}, "plain=true"},
		"flag overrides":  {nil, []string{"--config", saved, "--plain=false", "options"}, "plain=false"},
		"off by default":  {nil, []string{"--config", none, "options"}, "plain=false"},
		"environment off": {map[string]string{"MACHINARCHY_PLAIN": "0"}, []string{"--config", saved, "options"}, "plain=false"},
	} {
		t.Run(name, func(t *testing.T) {
			got := runWithEnv(t, tc.env, testOptions(optionsCommand()), tc.args...)
			if got.code != 0 || !strings.Contains(got.stdout, tc.want) {
				t.Fatalf("result = %+v, want %q", got, tc.want)
			}
		})
	}
}

// --yes, --confirm, --confirm-env and --dry-run answer one invocation. They
// can never come from the environment or from saved defaults.
func TestFlagOnlyOptionsIgnoreEnvironmentAndConfig(t *testing.T) {
	none := filepath.Join(t.TempDir(), "absent.yaml")

	got := runWithEnv(t, map[string]string{
		"MACHINARCHY_YES": "1", "MACHINARCHY_CLI_YES": "1", "MACHINARCHY_DRY_RUN": "1", "MACHINARCHY_CLI_DRY_RUN": "1",
		"MACHINARCHY_CONFIRM": "web-01", "MACHINARCHY_CONFIRM_ENV": "production",
	}, testOptions(optionsCommand()), "--config", none, "options")
	want := "output= output-source=default plain=false quiet=false dry-run=false confirm=\"\" confirm-env=\"\"\n"
	if got.code != 0 || got.stdout != want || got.stderr != "" {
		t.Fatalf("environment leaked into a flag-only option: %+v", got)
	}

	for _, key := range []string{"yes", "confirm", "confirm-env", "dry-run"} {
		path := writeConfigWith(t, "defaults:\n  "+key+": \"true\"\n")
		got := runWithEnv(t, nil, testOptions(optionsCommand()), "--config", path, "options")
		if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "defaults."+key+" cannot be saved") {
			t.Errorf("defaults.%s must be refused: %+v", key, got)
		}
	}

	got = runWithEnv(t, nil, testOptions(optionsCommand()), "--config", none, "--confirm", "web-01", "--confirm-env", "production", "options")
	if got.code != 0 || !strings.Contains(got.stdout, `confirm="web-01" confirm-env="production"`) {
		t.Fatalf("the confirmation flags must be readable: %+v", got)
	}
}

func TestSavedDefaultsAreValidated(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"server is not a global default": {"defaults:\n  server: https://x\n", "defaults.server cannot be saved"},
		"token is not a global default":  {"defaults:\n  token: abc\n", "defaults.token cannot be saved"},
		"bad boolean":                    {"defaults:\n  plain: maybe\n", `defaults.plain must be true or false, not "maybe"`},
		"lists what is allowed":          {"defaults:\n  env: x\n", "Saved defaults can set: no-headers, no-input, output, plain, quiet, request-timeout, verbose."},
	} {
		t.Run(name, func(t *testing.T) {
			got := runWithEnv(t, nil, testOptions(optionsCommand()), "--config", writeConfigWith(t, tc.body), "options")
			if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, tc.want) {
				t.Fatalf("result = %+v, want stderr containing %q", got, tc.want)
			}
		})
	}
}

// An unsupported --dry-run is refused: a command that ignores it would make
// the change for real.
func TestDryRunIsRefusedWhereItIsNotSupported(t *testing.T) {
	none := filepath.Join(t.TempDir(), "absent.yaml")

	supported := func(*cli.Factory) *cobra.Command {
		return &cobra.Command{
			Use: "preview", Short: "supports dry run", Args: cobra.NoArgs,
			Annotations: map[string]string{cli.AnnotationDryRun: "true"},
			RunE: func(c *cobra.Command, _ []string) error {
				_, err := fmt.Fprintln(c.OutOrStdout(), "previewed")

				return err
			},
		}
	}

	got := runWithEnv(t, nil, testOptions(supported), "--config", none, "--dry-run", "ok")
	if got.code != clierr.ExitUsage || got.stdout != "" ||
		!strings.Contains(got.stderr, `"machinarchy ok" does not support --dry-run yet, so it would make the change for real`) {
		t.Fatalf("an unsupported --dry-run must stop the command: %+v", got)
	}

	got = runWithEnv(t, nil, testOptions(supported), "--config", none, "--dry-run", "preview")
	if got.code != 0 || got.stdout != "previewed\n" {
		t.Fatalf("a command that supports --dry-run must run: %+v", got)
	}

	got = runWithEnv(t, nil, testOptions(supported), "--config", none, "ok")
	if got.code != 0 || got.stdout != "fine\n" {
		t.Fatalf("without --dry-run nothing changes: %+v", got)
	}
}

func TestNewFlagsAreDocumentedOrHidden(t *testing.T) {
	none := filepath.Join(t.TempDir(), "absent.yaml")

	got := runWithEnv(t, nil, testOptions(), "--config", none, "--help")
	if !strings.Contains(got.stdout, "--plain") {
		t.Fatalf("--plain must be in the help:\n%s", got.stdout)
	}
	for _, hidden := range []string{"--dry-run", "--confirm-env", "--confirm "} {
		if strings.Contains(got.stdout, hidden) {
			t.Fatalf("%s is not usable by any command yet and must stay hidden:\n%s", hidden, got.stdout)
		}
	}
}

// How output looks follows the environment: the locale decides symbols, the
// terminal decides colour, COLUMNS decides the width, and --plain overrides
// all of it.
func TestOutputStyleFollowsTheEnvironmentAndPlain(t *testing.T) {
	none := filepath.Join(t.TempDir(), "absent.yaml")
	probe := command("style", func(c *cobra.Command, f *cli.Factory) error {
		var buffer bytes.Buffer // not a terminal: no colour in any case
		style := f.StyleFor(&buffer)
		_, err := fmt.Fprintf(c.OutOrStdout(), "unicode=%t color=%t plain=%t width=%d stderr-unicode=%t\n",
			style.Unicode, style.Color, style.Plain, f.Width(&buffer), f.Style().Unicode)

		return err
	})

	env := func(values map[string]string) cli.Options {
		options := testOptions(probe)
		options.Getenv = func(key string) string { return values[key] }

		return options
	}

	tests := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"utf-8 locale and COLUMNS", map[string]string{"LANG": "en_US.UTF-8", "COLUMNS": "100"}, nil,
			"unicode=true color=false plain=false width=100 stderr-unicode=true\n"},
		{"C locale is ASCII", map[string]string{"LANG": "C"}, nil,
			"unicode=false color=false plain=false width=80 stderr-unicode=false\n"},
		{"--plain wins over a UTF-8 locale", map[string]string{"LANG": "en_US.UTF-8"}, []string{"--plain"},
			"unicode=false color=false plain=true width=80 stderr-unicode=false\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := runWithEnv(t, nil, env(tc.env), append([]string{"--config", none}, append(tc.args, "style")...)...)
			if got.code != 0 || got.stdout != tc.want {
				t.Fatalf("result = %+v, want stdout %q", got, tc.want)
			}
		})
	}
}
