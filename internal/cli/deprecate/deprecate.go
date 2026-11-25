// Package deprecate is the mechanism for retiring old commands without
// breaking anyone (docs/cli/spec/08-migration.md).
//
// A deprecated command keeps working exactly as before. It is hidden from the
// help and from tab completion, and each time it runs it prints one line on
// stderr that names the command to use instead:
//
//	Deprecated: "machinarchy get hosts" is now "machinarchy hosts". It will be removed in the next release.
//
// The notice goes to stderr so scripts that read stdout are not disturbed, and
// MACHINARCHY_NO_DEPRECATION_WARNINGS=1 silences it for scripts that cannot
// change yet. The table of aliases is data (a slice of Alias) so a test can
// check that every old command exists and every replacement exists.
//
// This package only marks commands and builds the message. The old command's
// code is not touched, which is what keeps its behaviour identical.
package deprecate

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli/msg"
)

// annotationKey is where a marked command records its replacement.
const annotationKey = "machinarchy.io/deprecated"

// DefaultRemovedIn is used when an Alias names no release.
const DefaultRemovedIn = "the next release"

// Alias says that an old command has been replaced.
type Alias struct {
	// Old is the old command's path below the root, for example "get hosts".
	Old string
	// New is the replacement as the user would type it, for example
	// "machinarchy hosts". It may include flags that replace old ones.
	New string
	// RemovedIn says when the old command goes, for example "0.2.0". Empty
	// means "the next release".
	RemovedIn string
}

// Notice is what a deprecated command says when it runs.
type Notice struct {
	Old, New, RemovedIn string
}

// Message renders the notice as a catalogue message.
func (n Notice) Message() msg.Message {
	return msg.Get("deprecate.alias").With(msg.Vars{"old": n.Old, "new": n.New, "version": n.RemovedIn})
}

// Mark hides cmd and records that it is replaced by alias.New. It does not
// change what cmd does.
func Mark(cmd *cobra.Command, alias Alias) {
	if alias.RemovedIn == "" {
		alias.RemovedIn = DefaultRemovedIn
	}
	cmd.Hidden = true
	if cmd.Annotations == nil {
		cmd.Annotations = make(map[string]string)
	}
	cmd.Annotations[annotationKey] = alias.New + "\x00" + alias.RemovedIn
}

// Apply marks the command at each alias's path. A path that does not name a
// command is an error: the table has drifted from the command tree.
func Apply(root *cobra.Command, aliases []Alias) error {
	var problems []string
	for _, alias := range aliases {
		cmd := find(root, alias.Old)
		if cmd == nil {
			problems = append(problems, fmt.Sprintf("%q is not a command", alias.Old))

			continue
		}
		Mark(cmd, alias)
	}

	if len(problems) > 0 {
		return fmt.Errorf("deprecate: %s", strings.Join(problems, "; "))
	}

	return nil
}

// Validate checks an alias table against the command tree: every old command
// must exist, and every replacement must start with the root's name and name a
// command that exists and is not itself deprecated. It returns one problem per
// finding.
func Validate(root *cobra.Command, aliases []Alias) []string {
	var problems []string
	seen := map[string]bool{}

	for _, alias := range aliases {
		if seen[alias.Old] {
			problems = append(problems, fmt.Sprintf("%q appears twice", alias.Old))
		}
		seen[alias.Old] = true

		if find(root, alias.Old) == nil {
			problems = append(problems, fmt.Sprintf("old command %q does not exist", alias.Old))
		}

		words := strings.Fields(alias.New)
		if len(words) == 0 || words[0] != root.Name() {
			problems = append(problems, fmt.Sprintf("replacement %q for %q must start with %q", alias.New, alias.Old, root.Name()))

			continue
		}

		target := find(root, strings.Join(commandWords(words[1:]), " "))
		switch {
		case target == nil:
			problems = append(problems, fmt.Sprintf("replacement %q for %q is not a command", alias.New, alias.Old))
		case isDeprecated(target):
			problems = append(problems, fmt.Sprintf("replacement %q for %q is itself deprecated", alias.New, alias.Old))
		}
	}

	return problems
}

// For returns the notice for the command that was selected: its own, or the
// nearest deprecated ancestor's. The second result is false when the command
// is not deprecated.
func For(cmd *cobra.Command) (Notice, bool) {
	for current := cmd; current != nil; current = current.Parent() {
		value, ok := current.Annotations[annotationKey]
		if !ok {
			continue
		}

		replacement, removedIn, _ := strings.Cut(value, "\x00")
		old := current.CommandPath()

		return Notice{Old: old, New: replacement, RemovedIn: removedIn}, true
	}

	return Notice{}, false
}

// commandWords drops the flags from a replacement so the command path can be
// looked up: "hosts --on tier=web" -> ["hosts"].
func commandWords(words []string) []string {
	var path []string
	for _, word := range words {
		if strings.HasPrefix(word, "-") || strings.Contains(word, "=") {
			break
		}
		path = append(path, word)
	}

	return path
}

func isDeprecated(cmd *cobra.Command) bool {
	_, ok := cmd.Annotations[annotationKey]

	return ok
}

// find returns the command at path below root ("get hosts"), or nil. An empty
// path is the root itself.
func find(root *cobra.Command, path string) *cobra.Command {
	current := root
	for _, name := range strings.Fields(path) {
		var next *cobra.Command
		for _, child := range current.Commands() {
			if child.Name() == name {
				next = child

				break
			}
		}
		if next == nil {
			return nil
		}
		current = next
	}

	return current
}
