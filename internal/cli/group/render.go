package group

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/machinarchy/machinarchy/internal/cli/msg"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
	"github.com/machinarchy/machinarchy/internal/cli/ui"
)

// DefaultMaxGroups is how many groups are shown before the rest is summarised.
const DefaultMaxGroups = 10

// RenderOptions control the text rendering of a Summary.
type RenderOptions struct {
	// Style selects symbols, colour and plain mode.
	Style ui.Style
	// Width is the line width; 0 means 80.
	Width int
	// MaxGroups is the number of groups shown: 0 means DefaultMaxGroups and a
	// negative value shows all of them.
	MaxGroups int
	// RunID is the run's ID, used in the next-step commands. When empty no
	// command that needs it is printed.
	RunID string
	// TimeoutLabel is the per-host limit, for example "60 s", shown as
	// "timed out after 60 s".
	TimeoutLabel string
}

// Render writes the grouped result of a finished run as text:
//
//	⚠ 9 of 14 hosts succeeded, 5 did not
//	  ✖ web-09   exit 3
//	      inactive
//	  ...
//
// Failed and unknown outcomes come first, then succeeded hosts. After the
// groups come the legends and the drift line, then the next step. Nothing a
// server or host sent reaches the output unsanitised, and no line is longer
// than the width except an unbreakable name or command.
func Render(w io.Writer, s Summary, o RenderOptions) error {
	_, err := io.WriteString(w, RenderString(s, o))

	return err
}

// RenderString is Render into a string.
func RenderString(s Summary, o RenderOptions) string {
	var b strings.Builder
	r := renderer{b: &b, o: o, width: o.Width}
	if r.width <= 0 {
		r.width = 80
	}
	r.width = max(r.width, 40)
	r.render(s)

	return b.String()
}

type renderer struct {
	b     *strings.Builder
	o     RenderOptions
	width int
}

func (r *renderer) render(s Summary) {
	if s.Total == 0 {
		r.b.WriteString("No hosts.\n")

		return
	}

	header, tail := r.header(s)
	r.b.WriteString(header + "\n")

	limit := r.o.MaxGroups
	if limit == 0 {
		limit = DefaultMaxGroups
	}

	multi := s.GroupCount() > 1
	shown, hiddenGroups, hiddenHosts := 0, 0, 0
	for _, view := range s.Classes {
		groups, extra := view.Groups, ""
		if collapsible(view, s.Drift) {
			groups, extra = []GroupView{merge(view)}, "  each printed different output"
		}

		for _, g := range groups {
			if limit > 0 && shown >= limit {
				hiddenGroups++
				hiddenHosts += g.Count

				continue
			}
			shown++
			note := extra
			if g.Differs {
				note = "  different output"
			}
			r.group(view.Class, g, multi, note)
		}
	}

	var notes []msg.Message
	if s.Count(ClassTimedOut) > 0 || s.Count(ClassUnknown) > 0 {
		notes = append(notes, msg.Get("run.unknown"))
	}
	switch s.Drift.Kind {
	case DriftDominant:
		notes = append(notes, msg.Get("run.drift").With(msg.Vars{
			"deviant": strconv.Itoa(s.Drift.Deviant), "majority": strconv.Itoa(s.Drift.Majority)}))
	case DriftAllDifferent:
		notes = append(notes, msg.Get("run.drift_none"))
	case DriftMixed:
		notes = append(notes, msg.Get("run.drift_mixed").With(msg.Vars{
			"groups": strconv.Itoa(s.Drift.Groups), "hosts": strconv.Itoa(s.Count(ClassSucceeded))}))
	case DriftNone:
	}
	if hiddenGroups > 0 {
		more := msg.Get("run.more_groups").With(msg.Vars{
			"groups": strconv.Itoa(hiddenGroups), "hosts": strconv.Itoa(hiddenHosts)})
		if r.o.RunID != "" {
			more = more.WithNext("machinarchy run " + r.o.RunID + " --groups all")
		}
		notes = append(notes, more)
	}

	if len(notes) == 0 && len(tail) == 0 {
		return
	}
	r.b.WriteString("\n")
	for _, note := range notes {
		for _, line := range lines(ui.Render(note, r.o.Style)) {
			if strings.HasPrefix(strings.TrimSpace(stripANSI(line)), "Next:") {
				r.b.WriteString(line + "\n") // a command is never broken
			} else {
				r.wrap("  " + line)
			}
		}
	}
	for _, line := range tail {
		r.b.WriteString(line + "\n")
	}
}

// header returns the first line of the result and the lines that go after the
// groups (lore and the next step).
func (r *renderer) header(s Summary) (string, []string) {
	ok := s.Count(ClassSucceeded)
	vars := msg.Vars{
		"ok": strconv.Itoa(ok), "total": strconv.Itoa(s.Total), "failed": strconv.Itoa(s.Total - ok),
		"id": r.o.RunID, "host": sanitize.Line(s.FirstHost()),
	}

	var m msg.Message
	switch {
	case ok == s.Total:
		m = msg.Get("run.all_ok")
	case ok == 0:
		m = msg.Get("run.all_failed")
	default:
		m = msg.Get("run.partial")
	}
	if r.o.RunID == "" {
		m = m.WithNext("")
	}

	if s.Drift.Kind == DriftDominant {
		m.Lore = "" // no flavour next to a warning
	}
	all := lines(ui.Render(m.With(vars), r.o.Style))
	if len(all) == 0 {
		return "", nil
	}

	return all[0], all[1:]
}

// collapseSingletons is the number of one-host groups above which succeeded
// hosts that all printed something different are shown as one line instead of
// one group each (time-dependent output such as uptime).
const collapseSingletons = 3

// collapsible reports whether a class is shown as one line: succeeded hosts
// that all printed something different, and more than a few of them.
func collapsible(view ClassView, d Drift) bool {
	return view.Class == ClassSucceeded && d.Kind == DriftAllDifferent && len(view.Groups) > collapseSingletons
}

// merge joins the groups of a class into one, without samples.
func merge(view ClassView) GroupView {
	hosts := make([]string, 0, view.Hosts)
	for _, g := range view.Groups {
		hosts = append(hosts, g.Hosts...)
	}

	return GroupView{Hosts: SortNatural(hosts), Count: view.Hosts, ExitCode: view.Groups[0].ExitCode}
}

func (r *renderer) group(class Class, g GroupView, multi bool, extra string) {
	sym := symbolFor(class)
	if g.Differs {
		sym = ui.SymWarn
	}
	marker := r.o.Style.Marker(sym)
	markerWidth := len([]rune(r.o.Style.Sym(sym)))

	desc := describe(class, g, r.o.TimeoutLabel) + extra
	count := ""
	if multi && g.Count > 1 {
		count = fmt.Sprintf("  (%d hosts)", g.Count)
	}

	names := make([]string, len(g.Hosts))
	for i, host := range g.Hosts {
		names[i] = sanitize.Line(host)
	}
	lead := 2 + markerWidth + 1 // two spaces, the marker, a space
	fixed := lead + 2 + len([]rune(desc)) + len([]rune(count))
	if avail := r.width - fixed; avail >= 24 {
		r.b.WriteString("  " + marker + " " + CompressHosts(names, avail) + "  " + desc + count + "\n")
	} else {
		// Not enough room for the hosts and the description on one line:
		// put the description under the hosts.
		r.b.WriteString("  " + marker + " " + CompressHosts(names, max(r.width-lead-len([]rune(count)), 24)) + count + "\n")
		r.wrap(strings.Repeat(" ", lead+2) + desc)
	}
	r.sample(g.Stdout, g.StdoutMore, "")
	r.sample(g.Stderr, g.StderrMore, "stderr: ")
}

// sample prints a group's output sample, four spaces in, cut to the width.
func (r *renderer) sample(text string, more int, label string) {
	if text == "" {
		return
	}
	for _, line := range strings.Split(text, "\n") {
		r.b.WriteString(r.cut("    "+label+line) + "\n")
	}
	if more > 0 {
		ellipsis := "..."
		if r.o.Style.Unicode && !r.o.Style.Plain {
			ellipsis = "…"
		}
		r.b.WriteString(r.cut(fmt.Sprintf("    [%s %d more lines]", ellipsis, more)) + "\n")
	}
}

// wrap writes line broken at spaces so it fits the width. Continuation lines
// are indented two spaces deeper than the first. A word longer than the line
// (a name or a command) is never split.
func (r *renderer) wrap(line string) {
	indent := len(line) - len(strings.TrimLeft(line, " "))
	words := strings.Fields(line)
	prefix := strings.Repeat(" ", indent)
	current := prefix
	empty := true

	for _, word := range words {
		if !empty && visibleLen(current)+1+visibleLen(word) > r.width {
			r.b.WriteString(current + "\n")
			current = prefix + "  "
			empty = true
		}
		if !empty {
			current += " "
		}
		current += word
		empty = false
	}
	r.b.WriteString(current + "\n")
}

func (r *renderer) cut(line string) string {
	runes := []rune(line)
	if len(runes) <= r.width {
		return line
	}

	return string(runes[:r.width-3]) + "..."
}

// stripANSI removes ANSI colour codes.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j

			continue
		}
		b.WriteByte(s[i])
	}

	return b.String()
}

// visibleLen is the number of runes of s that appear on screen: ANSI colour
// codes take no room.
func visibleLen(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j + 1

			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n++
	}

	return n
}

func lines(text string) []string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}

	return strings.Split(text, "\n")
}

func symbolFor(c Class) ui.Symbol {
	switch c {
	case ClassFailed, ClassTimedOut:
		return ui.SymFail
	case ClassUnknown:
		return ui.SymUnknown
	case ClassCancelled:
		return ui.SymCancelled
	case ClassRunning:
		return ui.SymRunning
	case ClassPending:
		return ui.SymPending
	case ClassSucceeded:
		return ui.SymOK
	}

	return ui.SymUnknown
}

// describe is the short statement of what happened to a group.
func describe(c Class, g GroupView, timeout string) string {
	exit := ""
	if g.ExitCode != nil {
		exit = "exit " + strconv.Itoa(*g.ExitCode)
	}
	class := sanitize.Line(g.ErrorClass)

	switch c {
	case ClassSucceeded:
		if exit == "" {
			return "exit 0"
		}

		return exit
	case ClassFailed:
		switch g.ErrorClass {
		case "":
			if exit == "" {
				return "failed"
			}

			return exit
		case "unreachable":
			return "could not connect (unreachable)"
		case "connection_failed":
			return "could not connect"
		default:
			if exit != "" {
				return "failed (" + class + "), " + exit
			}

			return "failed (" + class + ")"
		}
	case ClassTimedOut:
		if timeout != "" {
			return "timed out after " + timeout
		}

		return "timed out"
	case ClassUnknown:
		return "outcome unknown (" + unknownReason(g.ErrorClass) + ")"
	case ClassCancelled:
		return "cancelled"
	case ClassRunning:
		return "still running"
	case ClassPending:
		return "not started"
	}

	return "unknown"
}

// unknownReason explains why the controller could not tell what happened,
// using the error classes of docs/operations.md.
func unknownReason(class string) string {
	switch class {
	case "supervisor_lost":
		return "the command's supervisor stopped without recording an exit"
	case "state_missing":
		return "the host lost the command's state"
	case "unreachable":
		return "the host stayed unreachable"
	case "unreachable_during_cancel":
		return "the host stayed unreachable after the cancel"
	case "outcome_unknown":
		return "the agent cannot tell whether it ran"
	}

	return "the controller lost contact before it finished"
}
