package ui

import (
	"strings"

	"github.com/machinarchy/machinarchy/internal/cli/msg"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
)

// Render formats a message for output in style s. The result ends with a
// newline.
//
// Errors use the four-part form of docs/cli/spec/02-commands.md section 2.3:
//
//	Error: what happened
//	Why:   the cause, when known
//	Next:  the exact command to run
//
// Other kinds start with a marker (✔, ⚠, ✖ or its ASCII word) and carry their
// explanation and next step on indented lines. Lore is printed only for
// success and info messages, and never in plain mode. Every text field passes
// through the sanitiser, so a host name or server message inside a message can
// never drive the terminal.
func Render(m msg.Message, s Style) string {
	var b strings.Builder

	primary := sanitize.Line(m.Primary)
	why := sanitize.Line(m.Why)
	next := sanitize.Text(strings.TrimRight(m.Next, "\n"))

	switch m.Kind {
	case msg.KindError:
		b.WriteString(s.Paint(RoleError, "Error:") + " " + primary + "\n")
		if why != "" {
			b.WriteString(s.Paint(RoleDim, "Why:  ") + " " + why + "\n")
		}
		writeNext(&b, s, next, "")
	case msg.KindSuccess:
		b.WriteString(s.Marker(SymOK) + " " + primary + "\n")
		writeDetail(&b, s, why, m, next)
	case msg.KindWarning, msg.KindSecurity:
		b.WriteString(s.Marker(SymWarn) + " " + primary + "\n")
		writeDetail(&b, s, why, m, next)
	case msg.KindFailure:
		b.WriteString(s.Marker(SymFail) + " " + primary + "\n")
		writeDetail(&b, s, why, m, next)
	case msg.KindInfo, msg.KindPrompt:
		b.WriteString(primary + "\n")
		writeDetail(&b, s, why, m, next)
	}

	return b.String()
}

// writeDetail prints the explanation, the lore line and the next step of a
// message that is not an error.
func writeDetail(b *strings.Builder, s Style, why string, m msg.Message, next string) {
	if why != "" {
		b.WriteString("  " + s.Paint(RoleDim, why) + "\n")
	}
	if m.Lore != "" && !s.Plain && (m.Kind == msg.KindSuccess || m.Kind == msg.KindInfo) {
		b.WriteString("  " + s.Paint(RoleDim, sanitize.Line(m.Lore)) + "\n")
	}
	writeNext(b, s, next, "  ")
}

// writeNext prints the Next line. A multi-line next step keeps its lines
// aligned under the first.
func writeNext(b *strings.Builder, s Style, next, indent string) {
	if strings.TrimSpace(next) == "" {
		return
	}

	label := indent + s.Paint(RoleDim, "Next:") + "  "
	continuation := indent + strings.Repeat(" ", len("Next:")+2)

	lines := strings.Split(next, "\n")
	for i, line := range lines {
		prefix := continuation
		if i == 0 {
			prefix = label
		}
		b.WriteString(prefix + s.Paint(RoleAccent, line) + "\n")
	}
}
