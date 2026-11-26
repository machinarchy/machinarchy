package cli

import (
	"errors"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

var unknownCommand = regexp.MustCompile(`^unknown command "([^"]*)" for "([^"]*)"`)

// suggestCommand turns Cobra's "unknown command" failure into an error that
// names the closest command and the whole corrected command line, at any
// depth (docs/cli/spec/03-ux.md section 10). Any other error, and an unknown
// word with no close match, is returned unchanged.
func suggestCommand(root *cobra.Command, args []string, err error) error {
	if err == nil {
		return nil
	}
	match := unknownCommand.FindStringSubmatch(err.Error())
	if match == nil {
		return err
	}
	word, path := match[1], match[2]

	parent := root
	for _, name := range strings.Fields(path)[1:] {
		next, _, findErr := parent.Find([]string{name})
		if findErr != nil || next == nil || next == parent {
			return err
		}
		parent = next
	}

	// Cobra only defaults the distance on the command it searches itself.
	parent.SuggestionsMinimumDistance = 2
	candidates := parent.SuggestionsFor(word)
	if len(candidates) == 0 {
		return err
	}
	closest := candidates[0]

	corrected := []string{root.Name()}
	replaced := false
	for _, arg := range args {
		if !replaced && arg == word {
			arg, replaced = closest, true
		}
		corrected = append(corrected, shellQuote(arg))
	}
	if !replaced {
		return err
	}

	return &clierr.Error{Code: clierr.ExitUsage, Err: errors.New(match[0]),
		Why: `"` + closest + `" is the closest command.`, Hint: strings.Join(corrected, " ")}
}

// shellQuote quotes an argument only when it holds characters a shell would
// treat specially, so the corrected command can be pasted as it is.
func shellQuote(arg string) string {
	if arg != "" && !strings.ContainsAny(arg, " \t\n'\"\\$`&|;<>()*?[]{}!#~") {
		return arg
	}

	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}
