// Package sanitize makes text that came from a server or a remote host safe to
// print on a terminal (docs/cli/spec/04-security.md, threat T8 and section 7).
//
// Remote output, host names, tags and error messages are untrusted. They may
// contain terminal escape sequences (cursor movement, screen clearing, title
// changes, clipboard writes through OSC 52), C1 control characters, bidirectional
// controls that reorder text visually, or zero-width characters that disguise
// it. Everything the CLI prints in table or text form passes through this
// package first. JSON and YAML output do not: their encoders escape control
// characters.
//
// The package has no dependencies on the rest of the CLI.
package sanitize

import (
	"strings"
	"unicode/utf8"
)

const (
	esc         = 0x1b
	bel         = 0x07
	replacement = '�'
)

// Clean returns s without anything that could drive a terminal and the number
// of characters or sequences that were removed or replaced. It keeps newline
// and tab; see Line for single-line text.
//
// The rules (spec section 7):
//  1. invalid UTF-8 becomes U+FFFD;
//  2. complete escape sequences (CSI, OSC, DCS, SOS, PM, APC and two-byte ESC
//     forms) are removed; an unterminated sequence is removed to the end;
//  3. C1 controls (U+0080-U+009F) and DEL are removed, and the 8-bit forms of
//     CSI, OSC and the string introducers start a sequence like ESC does;
//  4. other C0 controls, including a bare carriage return, become U+FFFD;
//  5. bidirectional controls and zero-width characters are removed.
func Clean(s string) (string, int) {
	if clean(s) {
		return s, 0
	}

	var b strings.Builder
	b.Grow(len(s))
	removed := 0

	for i := 0; i < len(s); {
		c := s[i]
		if c == esc {
			i = skipEscape(s, i)
			removed++
			continue
		}

		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(replacement)
			removed++
			i++
			continue
		}
		i += size

		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20:
			b.WriteRune(replacement)
			removed++
		case r == 0x7f:
			removed++
		case r >= 0x80 && r <= 0x9f:
			i = skipC1(s, i, r)
			removed++
		case hidden(r):
			removed++
		default:
			b.WriteRune(r)
		}
	}

	return b.String(), removed
}

// Text is Clean without the count.
func Text(s string) string {
	out, _ := Clean(s)

	return out
}

// Line returns s as one line of text for a table cell or a message: it is
// cleaned, newlines become one space and tabs become four spaces.
func Line(s string) string {
	out := Text(s)
	if !strings.ContainsAny(out, "\n\t") {
		return out
	}

	return strings.NewReplacer("\n", " ", "\t", "    ").Replace(out)
}

// Bounded is Line cut to at most maxRunes runes, with "..." marking the cut.
func Bounded(s string, maxRunes int) string {
	out := Line(s)
	if maxRunes <= 0 || utf8.RuneCountInString(out) <= maxRunes {
		return out
	}

	runes := []rune(out)

	return string(runes[:maxRunes]) + "..."
}

// clean reports whether s needs no change, without allocating. Printable
// ASCII, newline and tab are clean; so is any valid multi-byte text without
// the runes dropped below.
func clean(s string) bool {
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c == '\n' || c == '\t' || (c >= 0x20 && c != 0x7f) {
				i++
				continue
			}

			return false
		}

		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return false
		}
		if (r >= 0x80 && r <= 0x9f) || hidden(r) {
			return false
		}
		i += size
	}

	return true
}

// hidden reports bidirectional controls and zero-width characters, which can
// reorder or disguise text without being visible.
func hidden(r rune) bool {
	switch {
	case r >= 0x200b && r <= 0x200f: // zero-width space/joiners, LRM, RLM
		return true
	case r >= 0x202a && r <= 0x202e: // embeddings and overrides
		return true
	case r >= 0x2066 && r <= 0x2069: // isolates
		return true
	case r == 0x2060 || r == 0xfeff: // word joiner, byte order mark
		return true
	}

	return false
}

// skipEscape returns the index after the escape sequence that starts at s[i],
// which is ESC.
func skipEscape(s string, i int) int {
	j := i + 1
	if j >= len(s) {
		return len(s)
	}

	switch s[j] {
	case '[':
		return skipCSI(s, j+1)
	case ']', 'P', 'X', '^', '_':
		return skipString(s, j+1)
	}

	// Two-byte and nF forms: intermediates 0x20-0x2F, then one final byte.
	for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
		j++
	}
	if j >= len(s) {
		return len(s)
	}
	_, size := utf8.DecodeRuneInString(s[j:])

	return j + size
}

// skipC1 handles a C1 control that was just decoded; i is the index after it.
func skipC1(s string, i int, r rune) int {
	switch r {
	case 0x9b: // CSI
		return skipCSI(s, i)
	case 0x9d, 0x90, 0x98, 0x9e, 0x9f: // OSC, DCS, SOS, PM, APC
		return skipString(s, i)
	}

	return i
}

// skipCSI skips the parameter, intermediate and final bytes of a control
// sequence that starts at s[k].
func skipCSI(s string, k int) int {
	for k < len(s) && s[k] >= 0x20 && s[k] <= 0x3f {
		k++
	}
	if k < len(s) && s[k] >= 0x40 && s[k] <= 0x7e {
		k++
	}

	return k
}

// skipString skips a string-type sequence (OSC, DCS, SOS, PM, APC) that
// starts at s[k]. It ends at BEL, at ST (ESC \ or the C1 form), or at the end
// of the text. A stray ESC that is not followed by a backslash ends it and is
// handled as the start of a new sequence.
func skipString(s string, k int) int {
	for k < len(s) {
		switch {
		case s[k] == bel:
			return k + 1
		case s[k] == esc:
			if k+1 < len(s) && s[k+1] == '\\' {
				return k + 2
			}

			return k
		case s[k] == 0xc2 && k+1 < len(s) && s[k+1] == 0x9c: // C1 ST
			return k + 2
		}
		k++
	}

	return len(s)
}

// maxPending bounds how much of an unfinished escape sequence a Stream holds
// back; a longer one is treated as finished and removed.
const maxPending = 4096

// Stream cleans text that arrives in pieces, such as the output of a command
// that is followed live. A piece may end in the middle of an escape sequence
// or of a multi-byte character; the unfinished tail is held back and joined to
// the next piece, so a sequence split across two pieces is still removed
// whole instead of leaking its second half.
//
// The zero value is ready to use. It is not safe for concurrent use.
type Stream struct{ pending string }

// Clean returns the safe text of the piece, without the unfinished tail.
func (s *Stream) Clean(piece string) string {
	text := s.pending + piece
	cut := holdBack(text)
	s.pending = text[cut:]

	return Text(text[:cut])
}

// Flush returns the safe text of whatever is held back. Call it when the
// stream has ended.
func (s *Stream) Flush() string {
	text := s.pending
	s.pending = ""

	return Text(text)
}

// holdBack returns the index where an unfinished escape sequence or character
// begins at the end of text, or len(text) when the text ends cleanly.
func holdBack(text string) int {
	cut := len(text)

	if i := strings.LastIndexByte(text, esc); i >= 0 && len(text)-i <= maxPending && !escapeFinished(text[i+1:]) {
		cut = i
	}

	// An incomplete multi-byte character at the end.
	for back := 1; back <= utf8.UTFMax-1 && back <= cut; back++ {
		c := text[cut-back]
		if c < utf8.RuneSelf {
			break
		}
		if c >= 0xc0 { // a lead byte
			if !utf8.FullRuneInString(text[cut-back : cut]) {
				cut -= back
			}

			break
		}
	}

	return cut
}

// escapeFinished reports whether the bytes after an ESC hold a complete
// sequence.
func escapeFinished(rest string) bool {
	if rest == "" {
		return false
	}

	switch rest[0] {
	case '[': // CSI: parameters, then a final byte
		j := 1
		for j < len(rest) && rest[j] >= 0x20 && rest[j] <= 0x3f {
			j++
		}

		return j < len(rest)
	case ']', 'P', 'X', '^', '_': // strings end at BEL or ST
		body := rest[1:]

		return strings.IndexByte(body, bel) >= 0 || strings.Contains(body, "\x1b\\") || strings.Contains(body, "\xc2\x9c")
	}

	j := 0
	for j < len(rest) && rest[j] >= 0x20 && rest[j] <= 0x2f {
		j++
	}

	return j < len(rest)
}
