// Package ui is the CLI's presentation layer: it decides how a message,
// a symbol or a progress line looks on a given output (rich terminal, plain
// text, pipe) and nothing else. It does not decide what is said (package msg)
// and it never talks to the API.
//
// Style follows docs/cli/spec/07-brand.md: the terminal's own 16 colours, a
// small set of symbols that always come with words, and a plain mode
// (--plain, MACHINARCHY_PLAIN=1) with no lore, colour, symbols or redraw.
package ui

import (
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// Style describes what an output can show.
type Style struct {
	// Plain removes lore, colour, symbols and live redraw (--plain).
	Plain bool
	// Color allows ANSI colour and bold or dim text.
	Color bool
	// Unicode allows the symbols ✔ ✖ ⚠ and the block characters of the
	// progress bar. When false the ASCII markers (OK, FAILED, WARN) are used.
	Unicode bool
}

// DetectStyle works out the style for output written to w. plain is the
// --plain flag or MACHINARCHY_PLAIN. getenv is os.Getenv in production.
//
//   - Plain mode turns everything off.
//   - NO_COLOR (any non-empty value), TERM=dumb or a writer that is not a
//     terminal turn colour off.
//   - A locale that is not UTF-8, or TERM=dumb, turns Unicode off. A pipe with
//     a UTF-8 locale keeps the symbols: they are text, not escape codes.
func DetectStyle(getenv func(string) string, w io.Writer, plain bool) Style {
	if plain {
		return Style{Plain: true}
	}

	dumb := getenv("TERM") == "dumb"

	return Style{
		Color:   getenv("NO_COLOR") == "" && !dumb && IsTerminal(w),
		Unicode: !dumb && utf8Locale(getenv),
	}
}

// IsTerminal reports whether w is an interactive terminal.
func IsTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}

	return term.IsTerminal(int(file.Fd())) // #nosec G115 -- a file descriptor fits in int
}

// Width returns the usable line width for w: the terminal's width, else the
// COLUMNS variable, else 80. It is never below 40.
func Width(getenv func(string) string, w io.Writer) int {
	width := 0
	if file, ok := w.(*os.File); ok {
		if columns, _, err := term.GetSize(int(file.Fd())); err == nil { // #nosec G115 -- a file descriptor fits in int
			width = columns
		}
	}
	if width <= 0 {
		if columns, err := strconv.Atoi(strings.TrimSpace(getenv("COLUMNS"))); err == nil {
			width = columns
		}
	}
	if width <= 0 {
		width = 80
	}

	return max(width, 40)
}

// utf8Locale reports whether the first locale variable that is set names a
// UTF-8 character set. The order is the POSIX one: LC_ALL, LC_CTYPE, LANG.
func utf8Locale(getenv func(string) string) bool {
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if value := strings.ToLower(getenv(name)); value != "" {
			return strings.Contains(value, "utf-8") || strings.Contains(value, "utf8")
		}
	}

	return false
}

// Symbol is a status marker.
type Symbol int

// Symbols. Each is always printed together with words, never alone.
const (
	SymOK Symbol = iota
	SymFail
	SymWarn
	SymRunning
	SymPending
	SymUnknown
	SymCancelled
)

// Sym returns the marker for sym: a symbol when Unicode is allowed, otherwise
// an ASCII word (07-brand.md section 5).
func (s Style) Sym(sym Symbol) string {
	if s.Unicode && !s.Plain {
		switch sym {
		case SymOK:
			return "✔"
		case SymFail:
			return "✖"
		case SymWarn:
			return "⚠"
		case SymRunning:
			return "●"
		case SymPending:
			return "○"
		case SymUnknown:
			return "?"
		case SymCancelled:
			return "⏹"
		}
	}

	switch sym {
	case SymOK:
		return "OK"
	case SymFail:
		return "FAILED"
	case SymWarn:
		return "WARN"
	case SymRunning:
		return "RUN"
	case SymPending:
		return "WAIT"
	case SymUnknown:
		return "UNKNOWN"
	case SymCancelled:
		return "CANCELLED"
	}

	return ""
}

// Role is the meaning of a piece of coloured text.
type Role int

// Colour roles (07-brand.md section 5).
const (
	RoleNone Role = iota
	RoleAccent
	RoleOK
	RoleError
	RoleWarn
	RoleUnknown
	RoleDim
	RoleBold
)

// Paint wraps text in the ANSI codes for role when colour is allowed, and
// returns it unchanged otherwise. Meaning never depends on it: every coloured
// text also carries a word or symbol.
func (s Style) Paint(role Role, text string) string {
	if !s.Color || s.Plain || text == "" {
		return text
	}

	code := ""
	switch role {
	case RoleAccent:
		code = "36"
	case RoleOK:
		code = "32"
	case RoleError:
		code = "31"
	case RoleWarn:
		code = "33"
	case RoleUnknown:
		code = "35"
	case RoleDim:
		code = "2"
	case RoleBold:
		code = "1"
	case RoleNone:
		return text
	}

	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// SymRole returns the colour role that goes with a symbol.
func SymRole(sym Symbol) Role {
	switch sym {
	case SymOK:
		return RoleOK
	case SymFail:
		return RoleError
	case SymWarn:
		return RoleWarn
	case SymUnknown:
		return RoleUnknown
	case SymRunning, SymPending, SymCancelled:
		return RoleDim
	}

	return RoleNone
}

// Marker returns the marker for sym, painted in its role.
func (s Style) Marker(sym Symbol) string {
	return s.Paint(SymRole(sym), s.Sym(sym))
}
