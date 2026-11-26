package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/group"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
)

// pollInterval is how often a watched run is asked for its state.
var pollInterval = time.Second

// Limits of a run (docs/cli/spec/02-commands.md section 3.10).
const (
	// maxAttachedTimeout is the longest run that keeps its SSH session open
	// (the controller's attached limit), in seconds.
	maxAttachedTimeout = 900
	// maxRunTimeout is the longest per-host timeout, in seconds.
	maxRunTimeout = 24 * 60 * 60
	// defaultRunTimeout is the per-host timeout when --timeout is not given.
	defaultRunTimeout = 60
	// maxConcurrency is the most hosts a run works on at once.
	maxConcurrency = 20
	// defaultAttachedConcurrency is the default fan-out of a run that is watched.
	defaultAttachedConcurrency = 5
	// maxRunTargets is the most hosts one run can target (the controller's
	// MaxOperationTargets; C-05 decided: final).
	maxRunTargets = 200
	// runIDPrefixMin is the shortest run ID prefix the CLI resolves.
	runIDPrefixMin = 6
	// runIDSearch is how many recent runs a prefix is looked up in.
	runIDSearch = 500
)

// terminalOperationStates are the run states from which a run never moves.
var terminalOperationStates = map[string]bool{"Succeeded": true, "PartiallySucceeded": true, "Failed": true,
	"Cancelled": true, "TimedOut": true, "Interrupted": true}

// terminalTargetStates are the host states from which a target never moves.
var terminalTargetStates = map[string]bool{group.StateSucceeded: true, group.StateFailed: true, group.StateTimedOut: true,
	group.StateCancelled: true, group.StateInterrupted: true, group.StateLost: true}

// summarize lists up to limit names, then "and N more". The names are
// sanitised.
func summarize(rawNames []string, limit int) string {
	names := make([]string, len(rawNames))
	for i, name := range rawNames {
		names[i] = sanitize.Line(name)
	}
	if len(names) <= limit {
		return strings.Join(names, ", ")
	}

	return strings.Join(names[:limit], ", ") + fmt.Sprintf(" and %d more", len(names)-limit)
}

// shortRunID is how a run is shown: r- and the first 8 characters of its ID.
func shortRunID(id string) string {
	id = strings.ToLower(id)
	if len(id) > 8 {
		id = id[:8]
	}

	return "r-" + sanitize.Line(id)
}

// runIDChars reports whether text could be (a prefix of) a run ID.
func runIDChars(text string) bool {
	for _, r := range text {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}

	return text != ""
}

// resolveRunID turns what the user typed (a full ID, an ID prefix of at least
// 6 characters, or either written r-PREFIX) into the controller's run ID. A
// prefix is looked up among the 500 most recent runs.
func resolveRunID(ctx context.Context, client api.Client, arg string) (string, error) {
	text := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(arg)), "r-")
	if !runIDChars(text) || len(text) < runIDPrefixMin {
		return "", clierr.Newf(clierr.ExitUsage, "machinarchy run   lists recent runs with their IDs",
			"%q is not a run ID; use r- and at least %d characters of the ID, for example r-4k2p9x1a", sanitize.Line(arg), runIDPrefixMin)
	}
	if len(text) == 36 { // a whole ID needs no lookup
		return text, nil
	}

	page, err := client.ListOperations(ctx, api.OperationQuery{Limit: runIDSearch})
	if err != nil {
		return "", fmt.Errorf("look up the run: %w", err)
	}
	var matches []string
	for _, item := range page.Items {
		if strings.HasPrefix(strings.ToLower(item.ID), text) {
			matches = append(matches, item.ID)
		}
	}

	switch len(matches) {
	case 0:
		return "", clierr.Newf(clierr.ExitNotFound, "machinarchy run", "no run starts with %s among the %d most recent runs", shortRunText(text), runIDSearch)
	case 1:
		return matches[0], nil
	}
	shown := make([]string, 0, len(matches))
	for _, match := range matches[:min(len(matches), 5)] {
		shown = append(shown, shortRunID(match))
	}

	return "", clierr.Newf(clierr.ExitUsage, "type more of the ID", "%s matches %d runs (%s)", shortRunText(text), len(matches), strings.Join(shown, ", "))
}

func shortRunText(text string) string { return "r-" + sanitize.Line(text) }

// parseSeconds reads a duration such as 90s, 15m or 2h; a bare number is
// seconds. It returns whole seconds, rounded up.
func parseSeconds(flag, text string, lowest, highest int) (int, error) {
	text = strings.TrimSpace(text)
	var duration time.Duration
	if n, err := strconv.Atoi(text); err == nil {
		duration = time.Duration(n) * time.Second
	} else if parsed, err := time.ParseDuration(text); err == nil {
		duration = parsed
	} else {
		return 0, clierr.Usagef("%s %q is not a duration; write it like 90s, 15m or 2h", flag, sanitize.Line(text))
	}
	seconds := int((duration + time.Second - 1) / time.Second)
	if seconds < lowest || seconds > highest {
		return 0, clierr.Usagef("%s must be between %s and %s", flag, humanSeconds(lowest), humanSeconds(highest))
	}

	return seconds, nil
}

// humanSeconds writes a number of seconds the way the preview says it: 60 s,
// 15 min, 24 h.
func humanSeconds(seconds int) string {
	switch {
	case seconds%3600 == 0 && seconds >= 3600:
		return fmt.Sprintf("%d h", seconds/3600)
	case seconds%60 == 0 && seconds >= 120:
		return fmt.Sprintf("%d min", seconds/60)
	default:
		return fmt.Sprintf("%d s", seconds)
	}
}

// shellQuote writes one argument of a program for display: as it is when that
// is unambiguous, otherwise in single quotes, and as a Go string when it holds
// anything a terminal could act on. The program is never run through a shell;
// the quoting only makes the argument list readable.
func shellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	if sanitize.Line(arg) != arg {
		return strconv.Quote(arg)
	}
	safe := true
	for _, r := range arg {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("_@%+=:,./-", r) {
			safe = false

			break
		}
	}
	if safe {
		return arg
	}

	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// commandText is the program of a run as the preview shows it.
func commandText(argv []string) string {
	parts := make([]string, len(argv))
	for i, arg := range argv {
		parts[i] = shellQuote(arg)
	}

	return strings.Join(parts, " ")
}

// specText is what a run did, for a list: the program or the capability.
func specText(spec api.OperationSpec) string {
	switch {
	case spec.Run != nil && len(spec.Run.Argv) > 0:
		return commandText(spec.Run.Argv)
	case spec.Capability != nil && spec.Capability.Name != "":
		return "--do " + sanitize.Line(spec.Capability.Name)
	}

	return ""
}

// timeoutLabel is the per-host limit of a run, for "timed out after 60 s".
func timeoutLabel(spec api.OperationSpec) string {
	switch {
	case spec.Run != nil && spec.Run.TimeoutSeconds > 0:
		return humanSeconds(spec.Run.TimeoutSeconds)
	case spec.Capability != nil && spec.Capability.TimeoutSeconds > 0:
		return humanSeconds(spec.Capability.TimeoutSeconds)
	}

	return ""
}

// checkOperation refuses an answer that is not a run, so a server (or a proxy)
// that does not speak the API is named rather than half-shown.
func checkOperation(op *api.Operation, endpoint string) error {
	if op == nil || op.ID == "" || op.Status.State == "" {
		return clierr.Newf(clierr.ExitUnavailable, "check the server address with: machinarchy whoami",
			"the server did not answer like a Machinarchy server on %s", endpoint)
	}

	return nil
}

// isTransportFailure reports whether err means the request may or may not
// have reached the server: no HTTP answer was read.
func isTransportFailure(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *api.Error

	return !errors.As(err, &apiErr)
}

// transientPollError reports whether a failed poll is worth trying again:
// the network failed, or the server was busy or briefly broken.
func transientPollError(err error) bool {
	var apiErr *api.Error
	if !errors.As(err, &apiErr) {
		return true
	}

	return apiErr.Status >= 500 || apiErr.Status == 408 || apiErr.Status == 429
}
