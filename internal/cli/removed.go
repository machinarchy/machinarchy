package cli

import (
	"errors"
	"slices"
	"strings"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

// removedForm is a command (or flag) of the old grammar. There are no aliases
// (owner decision, no legacy support): the old form is refused and the new one
// is named, so nobody is left with "unknown command" or a wrong suggestion.
type removedForm struct {
	words []string // the command words, in order
	flag  string   // when set, the refused form is this flag on that command
	use   string   // the replacement, as a command line
}

var removedForms = []removedForm{
	{words: []string{"get", "hosts"}, use: "machinarchy hosts"},
	{words: []string{"get", "profiles"}, use: "machinarchy access"},
	{words: []string{"get", "operations"}, use: "machinarchy run"},
	{words: []string{"get", "operation"}, use: "machinarchy run"},
	{words: []string{"get", "ops"}, use: "machinarchy run"},
	{words: []string{"get", "runs"}, use: "machinarchy run"},
	{words: []string{"get", "tokens"}, use: "machinarchy tokens"},
	{words: []string{"create", "host"}, use: "machinarchy hosts add NAME ADDRESS --access ENTRY"},
	{words: []string{"create", "profile"}, use: "machinarchy access add NAME"},
	{words: []string{"create", "token"}, use: "machinarchy tokens create NAME --role ROLE"},
	{words: []string{"create", "agent-token"}, use: "machinarchy hosts enroll HOST"},
	{words: []string{"delete", "host"}, use: "machinarchy hosts remove NAME"},
	{words: []string{"delete", "profile"}, use: "machinarchy access remove NAME"},
	{words: []string{"delete", "token"}, use: "machinarchy tokens revoke NAME"},
	{words: []string{"delete", "agent-token"}, use: "machinarchy hosts revoke-agent"},
	{words: []string{"label", "host"}, use: "machinarchy hosts tag NAME KEY=VALUE"},
	{words: []string{"trust", "host"}, use: "machinarchy hosts trust NAME"},
	{words: []string{"cancel", "operation"}, use: "machinarchy run ID --cancel"},
	{words: []string{"logs", "operation"}, use: "machinarchy run ID --follow"},
	{words: []string{"config", "set-cluster"}, use: "machinarchy login URL"},
	{words: []string{"config", "set-context"}, use: "machinarchy login URL, then machinarchy envs use NAME"},
	{words: []string{"config", "use-context"}, use: "machinarchy switch NAME"},
	{words: []string{"config", "get-contexts"}, use: "machinarchy switch"},
	{words: []string{"config", "current-context"}, use: "machinarchy whoami"},
	{words: []string{"config", "set-credentials"}, flag: "--token", use: "machinarchy login URL --with-token"},
	{words: []string{"login"}, flag: "--context", use: "machinarchy login URL --name NAME"},
	{words: []string{"run"}, flag: "-l", use: "machinarchy run --on TAGS -- PROGRAM"},
	{words: []string{"run"}, flag: "--all", use: "machinarchy run --on all -- PROGRAM"},
	{words: []string{"run"}, flag: "--exclude", use: "machinarchy run --on TARGET --except HOST -- PROGRAM"},
	{words: []string{"run"}, flag: "--selector", use: "machinarchy run --on TAGS -- PROGRAM"},
}

// removedCommand turns an "unknown command" or "unknown flag" failure into a
// refusal that names the replacement. It returns nil for anything else.
func removedCommand(args []string, err error) *clierr.Error {
	if err == nil {
		return nil
	}
	text := err.Error()
	unknownFlag := strings.HasPrefix(text, "unknown flag: ") || strings.HasPrefix(text, "unknown shorthand flag: ")
	if !unknownFlag && !unknownCommand.MatchString(text) {
		return nil
	}

	var words []string
	var flags []string
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, strings.SplitN(arg, "=", 2)[0])
		} else {
			words = append(words, arg)
		}
	}

	for _, form := range removedForms {
		if !containsRun(words, form.words) {
			continue
		}
		name := strings.Join(form.words, " ")
		if form.flag != "" {
			if !unknownFlag || !hasFlag(flags, form.flag) {
				continue
			}
			name += " " + form.flag
		} else if unknownFlag {
			continue
		}

		return &clierr.Error{Code: clierr.ExitUsage, Err: errors.New(`"` + name + `" was removed`),
			Why: "the command set was redesigned and the old form has no alias", Hint: form.use}
	}

	return nil
}

// containsRun tells whether run appears in words as consecutive entries
// (global flags with values may come before it).
func containsRun(words, run []string) bool {
	for start := 0; start+len(run) <= len(words); start++ {
		if slices.Equal(words[start:start+len(run)], run) {
			return true
		}
	}

	return false
}

func hasFlag(flags []string, want string) bool {
	for _, flag := range flags {
		if flag == want {
			return true
		}
	}

	return false
}
