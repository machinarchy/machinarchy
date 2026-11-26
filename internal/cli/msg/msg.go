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
	// Home screen (docs/cli/spec/03-ux.md section 1).
	register(Message{ID: "home.not_signed_in", Kind: KindInfo,
		Primary: "Not signed in.",
		Lore:    "Another machine will not manage itself.",
		Next:    "machinarchy login"})
	register(Message{ID: "home.expired", Kind: KindInfo,
		Primary: "Your session on {server} expired at {time}.",
		Next:    "machinarchy login"})
	register(Message{ID: "home.unreachable", Kind: KindFailure,
		Primary: "Cannot reach the server: {why}.",
		Next:    "check the address, or switch server: machinarchy switch"})
	register(Message{ID: "home.no_hosts", Kind: KindInfo,
		Primary: "You have no machines yet.",
		Next:    "machinarchy hosts add"})
	// Sign-in and credentials (docs/cli/spec/03-ux.md sections 3 and 4).
	register(Message{ID: "auth.signed_in", Kind: KindSuccess,
		Primary: "Signed in to {server} as {user}.",
		Why:     "{detail}",
		Lore:    "The authority acknowledges you.",
		Next:    "{next}"})
	register(Message{ID: "hint.completion", Kind: KindInfo,
		Primary: "Tab completion is available for commands, hosts and environments.",
		Next:    "machinarchy completion --help"})
	register(Message{ID: "auth.device_token_saved", Kind: KindSuccess,
		Primary: "Device token saved ({role}, {days} days).",
		Why:     "Owner actions still use your password session while it lasts."})
	register(Message{ID: "auth.device_token_failed", Kind: KindWarning,
		Primary: "Could not create a device token, so you stay signed in for this session only.",
		Why:     "{why}",
		Next:    "machinarchy login"})
	register(Message{ID: "auth.logged_out", Kind: KindSuccess,
		Primary: "Signed out of {server}.",
		Why:     "{detail}"})
	register(Message{ID: "auth.logged_out_token", Kind: KindWarning,
		Primary: "The saved token was removed from this computer. It still works until it expires or is revoked.",
		Next:    "{next}"})
	register(Message{ID: "auth.logout_unconfirmed", Kind: KindError,
		Primary: "Signed out locally, but the server could not be told: {why}.",
		Why:     "The session may still be active on the server.",
		Next:    "machinarchy login\nmachinarchy logout"})
	register(Message{ID: "server.switched", Kind: KindSuccess,
		Primary: "Active server: {server}.",
		Next:    "machinarchy whoami"})
	register(Message{ID: "env.created", Kind: KindSuccess,
		Primary: "Environment {env} created.",
		Next:    "machinarchy envs use {env}"})
	register(Message{ID: "env.created_protected", Kind: KindSuccess,
		Primary: "Environment {env} created (protected, local guard).",
		Why:     "Changes to hosts in {env} on this computer now ask you to type \"{env}\". The guard is not enforced by the server, so it does not stop other people, computers or tools.",
		Next:    "machinarchy envs use {env}"})
	register(Message{ID: "env.created_protected_server", Kind: KindSuccess,
		Primary: "Environment {env} created (protected).",
		Why:     "The server now refuses host changes and runs in {env} unless the caller confirms \"{env}\", for every client. Only the workspace owner can unprotect it.",
		Next:    "machinarchy envs use {env}"})
	register(Message{ID: "env.used", Kind: KindSuccess,
		Primary: "Active env: {env}.",
		Why:     "Commands on {server} now work on {env}. To undo: machinarchy envs use any."})
	register(Message{ID: "env.cleared", Kind: KindSuccess,
		Primary: "No active env: commands now cover every env.",
		Next:    "machinarchy envs use NAME"})
	register(Message{ID: "env.removed", Kind: KindSuccess,
		Primary: "Environment {env} removed.",
		Why:     "No host was deleted by this command.",
		Next:    "machinarchy envs"})
	register(Message{ID: "access.created", Kind: KindSuccess,
		Primary: "Access entry {name} added.",
		Why:     "The key or password is stored encrypted on the server and is never shown again.",
		Next:    "machinarchy access show {name}"})
	register(Message{ID: "access.removed", Kind: KindSuccess,
		Primary: "Access entry {name} removed.",
		Why:     "The stored key or password was not retired and still exists on the server.",
		Next:    "machinarchy secret list"})
	register(Message{ID: "host.tagged", Kind: KindSuccess,
		Primary: "Tags of {host}: {tags}.",
		Next:    "machinarchy hosts show {host}"})
	register(Message{ID: "host.added", Kind: KindSuccess,
		Primary: "{host} is ready ({seconds} s).",
		Lore:    "Another machine joins the hierarchy.",
		Next:    "machinarchy hosts show {host}"})
	register(Message{ID: "host.added_waiting_key", Kind: KindWarning,
		Primary: "{host} added. It is waiting for key approval: nothing was trusted.",
		Why:     "Trust the key only after you compared it with the one on the machine.",
		Next:    "{next}"})
	register(Message{ID: "host.added_no_connect", Kind: KindWarning,
		Primary: "{host} added, but the server cannot connect to it{reason}.",
		Why:     "Check that {endpoint} is reachable from the server and that the login works.",
		Next:    "machinarchy hosts show {host}"})
	register(Message{ID: "host.added_checking", Kind: KindInfo,
		Primary: "{host} added. It is not ready yet ({status}); stopped waiting after {seconds} s.",
		Next:    "machinarchy hosts show {host}"})
	register(Message{ID: "host.trusted", Kind: KindSuccess,
		Primary: "Key trusted. {host} will be reachable once it reports ready.",
		Next:    "machinarchy hosts show {host}"})
	register(Message{ID: "host.key_replaced", Kind: KindSuccess,
		Primary: "{host} now trusts the new key. Commands to it will be accepted again.",
		Next:    "machinarchy hosts show {host}"})
	register(Message{ID: "host.removed", Kind: KindSuccess,
		Primary: "Host {host} removed from the inventory.",
		Why:     "Only the inventory entry is gone. Not done: an installed agent was not uninstalled or revoked, the access entry and its credentials were kept, and the machine was not touched.",
		Next:    "machinarchy access"})
	register(Message{ID: "host.agent_revoked", Kind: KindSuccess,
		Primary: "Agent {agent} of host {host} revoked.",
		Why:     "It can no longer serve commands. Not done: the host is still in the inventory, the agent software is still on the machine, and the machine was not touched.",
		Next:    "machinarchy hosts show {host}"})
	register(Message{ID: "host.registered", Kind: KindInfo,
		Primary: "{host} registered."})
	register(Message{ID: "host.no_pending", Kind: KindInfo,
		Primary: "No hosts are waiting for key approval."})
	register(Message{ID: "host.key_trusted", Kind: KindInfo,
		Primary: "Key of {host} trusted."})
	register(Message{ID: "host.trust_summary", Kind: KindInfo,
		Primary: "{trusted} trusted, {skipped} skipped."})
	register(Message{ID: "host.waiting_key", Kind: KindInfo,
		Primary: "Waiting for the server to see {host}'s SSH key (up to {seconds} s) ..."})
	register(Message{ID: "host.waiting_tofu", Kind: KindInfo,
		Primary: "{host} trusts the first key it sees (lab setting). Waiting for it to become ready (up to {seconds} s) ..."})
	register(Message{ID: "host.waiting_ready", Kind: KindInfo,
		Primary: "Key trusted. Waiting for {host} to become ready ..."})
	register(Message{ID: "host.fingerprint_note", Kind: KindInfo,
		Primary: "A fingerprint identifies a machine. If it does not match what the machine itself reports, something else may be answering at {endpoint}.",
		Next:    "On the machine: ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub"})
	register(Message{ID: "list.no_hosts", Kind: KindInfo,
		Primary: "No hosts found."})
	register(Message{ID: "list.empty", Kind: KindInfo,
		Primary: "No resources found."})
	register(Message{ID: "secret.created", Kind: KindInfo,
		Primary: "Secret \"{name}\" created: {id} (version {version})."})
	register(Message{ID: "secret.retired", Kind: KindInfo,
		Primary: "Secret retired."})
	register(Message{ID: "auth.not_signed_in_server", Kind: KindInfo,
		Primary: "You are not signed in to {server}.",
		Next:    "machinarchy login"})
	register(Message{ID: "token.created", Kind: KindSuccess,
		Primary: "Token {name} created ({role}, expires {expires}). It is shown only once:"})
	register(Message{ID: "token.created_file", Kind: KindSuccess,
		Primary: "Token {name} created ({role}, expires {expires}) and written to {file}.",
		Why:     "It is shown only once; keep the file private."})
	register(Message{ID: "token.revoked", Kind: KindSuccess,
		Primary: "Token {name} revoked."})

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
	register(Message{ID: "run.drift_mixed", Kind: KindInfo,
		Primary: "{groups} different outputs among {hosts} hosts; none is the majority."})
	register(Message{ID: "run.more_groups", Kind: KindInfo,
		Primary: "and {groups} more groups, {hosts} hosts"})

	// Running, watching and stopping (docs/cli/spec/03-ux.md sections 7 and 9).
	register(Message{ID: "run.started", Kind: KindInfo,
		Primary: "Run {id}"})
	register(Message{ID: "run.detached", Kind: KindInfo,
		Primary: "Run {id} started on {hosts}. It keeps going without you.",
		Next:    "machinarchy run {id} --follow"})
	register(Message{ID: "run.detached_long", Kind: KindInfo,
		Primary: "Run {id} started on {hosts}. This run is long, so it runs detached.",
		Next:    "machinarchy run {id} --follow"})
	register(Message{ID: "run.stopped", Kind: KindInfo,
		Primary: "Stopped watching. The run keeps going on the hosts.",
		Next:    "machinarchy run {id} --follow   (watch again)\nmachinarchy run {id} --cancel   (cancel it)"})
	register(Message{ID: "run.wait_timeout", Kind: KindInfo,
		Primary: "Still running. This command stopped waiting after {wait}; the run keeps going.",
		Next:    "machinarchy run {id} --follow"})
	register(Message{ID: "run.lost_contact", Kind: KindWarning,
		Primary: "Lost contact with the server; the run keeps going.",
		Next:    "machinarchy run {id} --follow"})
	register(Message{ID: "run.submit_interrupted", Kind: KindWarning,
		Primary: "Interrupted while sending the run. It may or may not have been created.",
		Next:    "machinarchy run"})
	register(Message{ID: "run.submit_lost", Kind: KindWarning,
		Primary: "The connection ended while sending the run. It may or may not have been created.",
		Why:     "{why}",
		Next:    "machinarchy run"})
	register(Message{ID: "run.cancel_requested", Kind: KindInfo,
		Primary: "Cancellation requested. The hosts have not confirmed yet."})
	register(Message{ID: "run.cancel_sent", Kind: KindInfo,
		Primary: "Cancellation requested. The hosts have not confirmed yet; the run is {state}.",
		Next:    "machinarchy run {id} --follow"})
	register(Message{ID: "run.cancelled", Kind: KindInfo,
		Primary: "Run cancelled. A host that finished before the cancel arrived keeps its result."})
	register(Message{ID: "run.cancel_late", Kind: KindInfo,
		Primary: "The run ended as {state}: some hosts had finished before the cancel arrived."})
	register(Message{ID: "run.cancel_unreached", Kind: KindWarning,
		Primary: "{hosts} could not be reached while cancelling; the command may still be running there."})
	register(Message{ID: "run.dry_run", Kind: KindInfo,
		Primary: "Dry run: nothing was sent. The hosts above are what would run."})
}
