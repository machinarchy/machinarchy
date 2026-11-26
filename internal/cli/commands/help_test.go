package commands_test

import (
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

// "Did you mean": the closest command and the whole corrected command line,
// at any depth (03-ux.md section 10).
func TestUnknownCommandSuggestsTheCorrectedCommandLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"top level", []string{"host", "add", "web-01"}, "Next:  machinarchy hosts add web-01\n"},
		{"second level", []string{"hosts", "shwo", "web-01"}, "Next:  machinarchy hosts show web-01\n"},
		{"after a flag", []string{"-o", "json", "hots"}, "Next:  machinarchy -o json hosts\n"},
		{"nothing close", []string{"zzzzzz"}, "Next:  machinarchy --help\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := execute(t, nil, "", tc.args...)
			if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, tc.want) || !strings.Contains(got.stderr, "unknown command") {
				t.Fatalf("%v: code %d, stderr %q, want %q", tc.args, got.code, got.stderr, tc.want)
			}
		})
	}
}

// The root help shows the four groups in order, points at details and at tab
// completion, and ends with Examples; so does a command's help.
func TestHelpLayout(t *testing.T) {
	root := execute(t, nil, "", "--help")
	last := -1
	for _, title := range []string{"Get started:", "Everyday:", "Account:", "Advanced:", "Flags:",
		"Run 'machinarchy COMMAND --help' for details.", "Tab completion: machinarchy completion --help", "Examples:"} {
		at := strings.Index(root.stdout, title)
		if at <= last {
			t.Fatalf("%q is missing or out of order in the root help:\n%s", title, root.stdout)
		}
		last = at
	}

	for _, args := range [][]string{{"hosts", "show", "--help"}, {"run", "--help"}, {"secret", "list", "--help"}} {
		got := execute(t, nil, "", args...)
		_, examples, found := strings.Cut(got.stdout, "\nExamples:\n")
		if !found || strings.Contains(examples, "\n\n") {
			t.Errorf("%v: Examples is not the last block:\n%s", args, got.stdout)
		}
	}
}
