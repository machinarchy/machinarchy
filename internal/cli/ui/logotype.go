package ui

import "strings"

// tagline is the line printed with the logotype (07-brand.md section 4).
const tagline = "one authority. distributed execution."

// Logotype returns the mark, the wordmark and the tagline for style s, ending
// with a newline: a small tree of hosts under one point. With Unicode off the
// diamonds are asterisks. In plain mode it is empty: 07-brand.md section 6
// says plain output has no logotype.
//
// It does not decide whether to show the logotype: callers use
// ShowLogotype so it appears only on a terminal.
func (s Style) Logotype() string {
	if s.Plain {
		return ""
	}

	mark := "◆"
	if !s.Unicode {
		mark = "*"
	}

	paint := func(text string) string { return s.Paint(RoleAccent, text) }

	var b strings.Builder
	b.WriteString("   " + paint(mark) + "\n")
	b.WriteString("  " + paint(mark+" "+mark) + "     " + s.Paint(RoleBold, "machinarchy") + "\n")
	b.WriteString(" " + paint(mark+" "+mark+" "+mark) + "    " + s.Paint(RoleDim, tagline) + "\n")

	return b.String()
}
