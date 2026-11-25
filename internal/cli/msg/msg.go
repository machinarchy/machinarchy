// Package msg is the CLI's message catalogue: everything the CLI says to a
// person is a Message with a plain primary line, an optional explanation, an
// optional next command and, for a few success moments only, an optional lore
// line (docs/cli/spec/07-brand.md).
//
// A message is a value, not a string, so its rules can be tested: every error
// must carry a next step, lore is only allowed on an explicit list of success
// messages and never in errors, warnings or security text, and no message may
// contain anything shaped like a token. Rendering (symbols, colour, --plain)
// lives in the ui package; this package has no dependencies.
package msg

import (
	"fmt"
	"sort"
	"strings"
)

// Kind says how a message is presented and which rules it must follow.
type Kind int

// Message kinds.
const (
	// KindInfo is a neutral line.
	KindInfo Kind = iota
	// KindSuccess reports that something worked. Only success messages on the
	// lore allow-list may carry a Lore line.
	KindSuccess
	// KindWarning reports something the person should know but that did not
	// stop the command.
	KindWarning
	// KindFailure is a result line that reports failure, such as "14 of 14
	// hosts failed". It is not a command error and has no required next step.
	KindFailure
	// KindError ends the command. It must say what to do next.
	KindError
	// KindSecurity is text about trust and credentials. It is always plain.
	KindSecurity
	// KindPrompt is a question.
	KindPrompt
)

// String returns the lower-case name of the kind.
func (k Kind) String() string {
	switch k {
	case KindInfo:
		return "info"
	case KindSuccess:
		return "success"
	case KindWarning:
		return "warning"
	case KindFailure:
		return "failure"
	case KindError:
		return "error"
	case KindSecurity:
		return "security"
	case KindPrompt:
		return "prompt"
	}

	return "unknown"
}

// Message is one thing the CLI can say. Text fields may contain {name}
// placeholders that With replaces.
type Message struct {
	// ID is the stable, dotted identifier, for example "deprecate.alias".
	ID string
	// Kind selects the presentation.
	Kind Kind
	// Primary is the plain, searchable first line.
	Primary string
	// Why explains the cause, when it is known.
	Why string
	// Next is the exact command or action to take.
	Next string
	// Lore is an optional second line of flavour, dropped by --plain.
	Lore string
}

// Vars holds the values for {name} placeholders.
type Vars map[string]string

// With returns a copy of m with every {name} placeholder replaced by its value
// in v. A placeholder without a value is left in place, so a missing value is
// visible in output and caught by tests instead of being silently blanked.
func (m Message) With(v Vars) Message {
	m.Primary = expand(m.Primary, v)
	m.Why = expand(m.Why, v)
	m.Next = expand(m.Next, v)
	m.Lore = expand(m.Lore, v)

	return m
}

// WithNext returns a copy of m with its next step replaced.
func (m Message) WithNext(next string) Message {
	m.Next = next

	return m
}

// WithWhy returns a copy of m with its explanation replaced.
func (m Message) WithWhy(why string) Message {
	m.Why = why

	return m
}

func expand(text string, v Vars) string {
	if text == "" || len(v) == 0 || !strings.Contains(text, "{") {
		return text
	}

	pairs := make([]string, 0, len(v)*2)
	for name, value := range v {
		pairs = append(pairs, "{"+name+"}", value)
	}

	return strings.NewReplacer(pairs...).Replace(text)
}

var catalogue = map[string]Message{}

// register adds a message at package initialisation. A duplicate ID is a
// programming error and panics so it cannot ship.
func register(m Message) {
	if m.ID == "" {
		panic("msg: message without an ID")
	}
	if _, exists := catalogue[m.ID]; exists {
		panic(fmt.Sprintf("msg: message %q is registered twice", m.ID))
	}
	catalogue[m.ID] = m
}

// Get returns the catalogue message with the given ID. An unknown ID returns a
// visible placeholder rather than an empty line, so a typo is noticed.
func Get(id string) Message {
	if m, ok := catalogue[id]; ok {
		return m
	}

	return Message{ID: id, Kind: KindError, Primary: "internal error: unknown message " + id,
		Next: "report this to the Machinarchy maintainers"}
}

// All returns every catalogue message ordered by ID.
func All() []Message {
	out := make([]Message, 0, len(catalogue))
	for _, m := range catalogue {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	return out
}

func init() {
	// Deprecation (docs/cli/spec/08-migration.md).
	register(Message{ID: "deprecate.alias", Kind: KindWarning,
		Primary: `Deprecated: "{old}" is now "{new}". It will be removed in {version}.`})
	register(Message{ID: "deprecate.env_var", Kind: KindWarning,
		Primary: "Deprecated: the environment variable {old} is now {new}. The old name will stop working in {version}."})

	// Input and confirmation (docs/cli/spec/02-commands.md section 2).
	register(Message{ID: "input.required", Kind: KindError,
		Primary: "{flag} is required when nothing can be asked.",
		Why:     "{why}",
		Next:    "{next}"})
	register(Message{ID: "confirm.required", Kind: KindError,
		Primary: "This needs confirmation, and --yes is not enough here ({what}).",
		Next:    "{next}"})

	// Grouped run results (docs/cli/spec/03-ux.md section 7).
	register(Message{ID: "run.all_ok", Kind: KindSuccess,
		Primary: "{ok} of {total} hosts succeeded",
		Lore:    "The machines complied.",
		Next:    "machinarchy run {id} --host {host}"})
	register(Message{ID: "run.partial", Kind: KindWarning,
		Primary: "{ok} of {total} hosts succeeded, {failed} did not",
		Next:    "machinarchy run {id} --retry-failed"})
	register(Message{ID: "run.all_failed", Kind: KindFailure,
		Primary: "{failed} of {total} hosts failed",
		Next:    "machinarchy run {id} --host {host}"})
	register(Message{ID: "run.unknown", Kind: KindWarning,
		Primary: `A command on a "timed out" or "outcome unknown" host may still have run.`})
	register(Message{ID: "run.drift", Kind: KindWarning,
		Primary: "{deviant} hosts printed something different from the other {majority}."})
	register(Message{ID: "run.drift_none", Kind: KindInfo,
		Primary: "every host printed something different (normal for time-dependent output)"})
	register(Message{ID: "run.more_groups", Kind: KindInfo,
		Primary: "and {groups} more groups, {hosts} hosts"})
}
