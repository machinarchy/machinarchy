package cli_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/deprecate"
)

func withAliases(options cli.Options, aliases ...deprecate.Alias) cli.Options {
	options.Aliases = aliases

	return options
}

func legacyCommands() []cli.CommandBuilder {
	return []cli.CommandBuilder{
		command("old", func(c *cobra.Command, _ *cli.Factory) error {
			_, err := fmt.Fprintln(c.OutOrStdout(), "old output")

			return err
		}),
		command("old-fails", func(*cobra.Command, *cli.Factory) error {
			return clierr.Newf(clierr.ExitConflict, "retry", "stale revision")
		}),
		command("new", func(c *cobra.Command, _ *cli.Factory) error {
			_, err := fmt.Fprintln(c.OutOrStdout(), "new output")

			return err
		}),
	}
}

func TestDeprecatedCommandWorksAsBeforeAndNamesItsReplacement(t *testing.T) {
	none := filepath.Join(t.TempDir(), "absent.yaml")
	options := withAliases(testOptions(legacyCommands()...),
		deprecate.Alias{Old: "old", New: "machinarchy new", RemovedIn: "0.2.0"},
		deprecate.Alias{Old: "old-fails", New: "machinarchy new", RemovedIn: "0.2.0"})

	got := runWithEnv(t, nil, options, "--config", none, "old")
	if got.code != 0 || got.stdout != "old output\n" {
		t.Fatalf("a deprecated command must behave as before: %+v", got)
	}
	want := `WARN Deprecated: "machinarchy old" is now "machinarchy new". It will be removed in 0.2.0.` + "\n"
	if got.stderr != want {
		t.Fatalf("stderr = %q, want %q", got.stderr, want)
	}

	// Same exit code and error text as before, with the notice first.
	got = runWithEnv(t, nil, options, "--config", none, "old-fails")
	wantFails := `WARN Deprecated: "machinarchy old-fails" is now "machinarchy new". It will be removed in 0.2.0.` + "\n"
	if got.code != clierr.ExitConflict || !strings.HasPrefix(got.stderr, wantFails) || !strings.Contains(got.stderr, "Error: stale revision\nNext:  retry\n") {
		t.Fatalf("a failing deprecated command keeps its exit code: %+v", got)
	}
	if strings.Count(got.stderr, "Deprecated:") != 1 {
		t.Fatalf("the notice must appear once per invocation: %q", got.stderr)
	}

	// The replacement prints nothing extra.
	got = runWithEnv(t, nil, options, "--config", none, "new")
	if got.code != 0 || got.stderr != "" || got.stdout != "new output\n" {
		t.Fatalf("the replacement must be silent: %+v", got)
	}
}

func TestDeprecationNoticeCanBeSilenced(t *testing.T) {
	none := filepath.Join(t.TempDir(), "absent.yaml")
	options := withAliases(testOptions(legacyCommands()...), deprecate.Alias{Old: "old", New: "machinarchy new"})

	got := runWithEnv(t, map[string]string{"MACHINARCHY_NO_DEPRECATION_WARNINGS": "1"}, options, "--config", none, "old")
	if got.code != 0 || got.stderr != "" || got.stdout != "old output\n" {
		t.Fatalf("result = %+v", got)
	}
}

func TestDeprecatedCommandsAreHiddenFromHelpAndCompletion(t *testing.T) {
	none := filepath.Join(t.TempDir(), "absent.yaml")
	options := withAliases(testOptions(legacyCommands()...), deprecate.Alias{Old: "old", New: "machinarchy new"})

	help := runWithEnv(t, nil, options, "--config", none, "--help")
	if strings.Contains(help.stdout, "\n  old ") || !strings.Contains(help.stdout, "  new ") {
		t.Fatalf("help must list the replacement and not the old command:\n%s", help.stdout)
	}

	completion := runWithEnv(t, nil, options, "__complete", "")
	if strings.Contains(completion.stdout, "old\t") || strings.Contains(completion.stdout, "old\n") || !strings.Contains(completion.stdout, "new") {
		t.Fatalf("completion must offer the replacement and not the old command:\n%s", completion.stdout)
	}
}

func TestAliasForAMissingCommandIsAProgrammingError(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("an alias for a command that does not exist must panic, like a duplicate name")
		}
	}()
	cli.NewRootCommand(withAliases(testOptions(), deprecate.Alias{Old: "no-such-command", New: "machinarchy ok"}))
}
