package commands_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/commands"
)

// Every `machinarchy ...` command line in a fenced block of the user docs is
// accepted by the command's own parser (the doc side of U-02).
func TestDocsExamplesParse(t *testing.T) {
	t.Parallel()

	files := []string{"../../../README.md"}
	more, err := filepath.Glob("../../../docs/cli/*.md")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, more...)

	root := cli.NewRootCommand(cli.Options{Groups: commands.DefaultGroups()})
	checked := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		fenced := false
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "```") {
				// Only shell blocks hold commands to type; text blocks hold
				// synopses and recorded output.
				fenced = !fenced && (line == "```sh" || line == "```bash")

				continue
			}
			if !fenced {
				continue
			}
			line = strings.TrimSpace(line)
			line, _, _ = strings.Cut(line, " #")
			line, _, _ = strings.Cut(line, " > ")
			if _, after, piped := strings.Cut(line, " | "); piped {
				line = after
			}
			words := splitExample(strings.TrimSpace(line))
			if len(words) == 0 || words[0] != "machinarchy" {
				continue
			}
			checked++
			target, rest, err := root.Find(words[1:])
			if err != nil {
				t.Errorf("%s: %q: %v", file, line, err)

				continue
			}
			if err := target.ParseFlags(rest); err != nil {
				t.Errorf("%s: %q: %v", file, line, err)

				continue
			}
			if target.Args != nil {
				if err := target.Args(target, target.Flags().Args()); err != nil {
					t.Errorf("%s: %q: %v", file, line, err)
				}
			}
		}
	}
	if checked < 20 {
		t.Errorf("only %d doc command lines checked; the extraction is broken", checked)
	}
}
