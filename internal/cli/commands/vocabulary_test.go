package commands_test

import (
	"regexp"
	"strings"
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

// splitExample splits one example line the way a shell would for plain words,
// single quotes and double quotes.
func splitExample(line string) []string {
	var words []string
	var current strings.Builder
	var quote rune
	started := false
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, started = r, true
		case r == ' ' || r == '\t':
			if started || current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteRune(r)
		}
	}
	if started || current.Len() > 0 {
		words = append(words, current.String())
	}

	return words
}

// U-02: every visible command has examples, and each example line is accepted
// by the command's own parser (command path, flags and argument count).
func TestEveryCommandHasParsableExamples(t *testing.T) {
	t.Parallel()

	root := cli.NewRootCommand(cli.Options{Groups: commands.DefaultGroups()})

	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		if command.Hidden || command.Name() == "help" || command.Name() == "completion" {
			return
		}
		if command.Example == "" {
			t.Errorf("%q has no Examples", command.CommandPath())
		}
		for _, line := range strings.Split(command.Example, "\n") {
			line, _, _ = strings.Cut(line, " #") // a trailing comment
			if _, after, piped := strings.Cut(line, " | "); piped {
				line = after // the input of a piped example is not part of the command
			}
			words := splitExample(strings.TrimSpace(line))
			if len(words) == 0 {
				continue
			}
			if words[0] != "machinarchy" {
				t.Errorf("%q: example %q does not start with machinarchy", command.CommandPath(), line)

				continue
			}
			target, rest, err := root.Find(words[1:])
			if err != nil {
				t.Errorf("%q: example %q: %v", command.CommandPath(), line, err)

				continue
			}
			if err := target.ParseFlags(rest); err != nil {
				t.Errorf("%q: example %q: %v", command.CommandPath(), line, err)

				continue
			}
			if target.Args != nil {
				if err := target.Args(target, target.Flags().Args()); err != nil {
					t.Errorf("%q: example %q: %v", command.CommandPath(), line, err)
				}
			}
		}
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(root)
}

// U-05: with --plain, the help of every visible command is ASCII only, with
// no ANSI sequences and no carriage returns.
func TestPlainHelpIsASCIIForEveryCommand(t *testing.T) {
	t.Parallel()

	probe := cli.NewRootCommand(cli.Options{Groups: commands.DefaultGroups()})
	var paths [][]string
	var walk func(*cobra.Command, []string)
	walk = func(command *cobra.Command, path []string) {
		if command.Hidden {
			return
		}
		paths = append(paths, path)
		for _, child := range command.Commands() {
			walk(child, append(append([]string(nil), path...), child.Name()))
		}
	}
	walk(probe, nil)

	for _, path := range paths {
		root := cli.NewRootCommand(cli.Options{Groups: commands.DefaultGroups()})
		var out strings.Builder
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(append(append([]string(nil), path...), "--help", "--plain"))
		if err := root.Execute(); err != nil {
			t.Errorf("%v --help: %v", path, err)

			continue
		}
		for _, r := range out.String() {
			if r > 127 || r == '\r' || r == 0x1b {
				t.Errorf("%v --plain help contains %q", path, r)

				break
			}
		}
	}
}
