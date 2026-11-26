package commands_test

import (
	"regexp"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/commands"
)

// forbiddenHelpWords are the words the spec keeps out of everything a person
// reads in help (docs/cli/spec/09-acceptance-tests.md U-03, 07-brand.md 2).
var forbiddenHelpWords = regexp.MustCompile(`(?i)\b(cluster|context|operation|selector|connector|supervisor|access profile|profile|label)s?\b`)

// U-03: no forbidden word in the name, help text, examples or flag usage of
// any command a person can see. Hidden commands are exempt.
func TestHelpUsesNoForbiddenWords(t *testing.T) {
	t.Parallel()

	root := cli.NewRootCommand(cli.Options{Groups: commands.DefaultGroups()})

	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		if command.Hidden {
			return
		}
		texts := []string{command.Use, command.Short, command.Long, command.Example}
		command.LocalFlags().VisitAll(func(flag *pflag.Flag) {
			if !flag.Hidden {
				texts = append(texts, flag.Name, flag.Usage)
			}
		})
		for _, text := range texts {
			if word := forbiddenHelpWords.FindString(text); word != "" {
				t.Errorf("%q: help uses the forbidden word %q in %q", command.CommandPath(), word, text)
			}
		}
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(root)
}
