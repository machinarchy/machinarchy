package sanitize

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		in          string
		want        string
		wantRemoved int
	}{
		{"plain text is untouched", "hello world\nnext\tline", "hello world\nnext\tline", 0},
		{"unicode text is untouched", "café ✔ 日本", "café ✔ 日本", 0},
		{"CSI colour", "a\x1b[31mred\x1b[0mb", "aredb", 2},
		{"CSI clear screen", "x\x1b[2J\x1b[Hy", "xy", 2},
		{"CSI with intermediates", "a\x1b[?25lb", "ab", 1},
		{"OSC title ended by BEL", "a\x1b]0;evil title\x07b", "ab", 1},
		{"OSC 52 clipboard ended by ST", "a\x1b]52;c;Zm9v\x1b\\b", "ab", 1},
		{"OSC unterminated is removed to the end", "a\x1b]52;c;Zm9v", "a", 1},
		{"DCS", "a\x1bPq#0;2;0;0;0\x1b\\b", "ab", 1},
		{"APC", "a\x1b_payload\x1b\\b", "ab", 1},
		{"two-byte escape", "a\x1b=b\x1b>c", "abc", 2},
		{"charset escape with intermediate", "a\x1b(Bb", "ab", 1},
		{"lone ESC at the end", "a\x1b", "a", 1},
		{"ESC then multi-byte rune consumes the rune whole", "a\x1béb", "ab", 1},
		{"stray ESC inside a string starts a new sequence", "a\x1b]0;t\x1b[2Jb", "ab", 2},
		{"C1 CSI sequence", "a\u009b31mb", "ab", 1},
		{"C1 OSC sequence", "a\u009d0;title\x07b", "ab", 1},
		{"other C1 control", "a\u0085b", "ab", 1},
		{"DEL", "a\x7fb", "ab", 1},
		{"bare carriage return is replaced", "line\roverwrite", "line\ufffdoverwrite", 1},
		{"CRLF keeps the line break visible as replacement plus newline", "a\r\nb", "a\ufffd\nb", 1},
		{"BEL and backspace are replaced", "a\x07b\x08c", "a\ufffdb\ufffdc", 2},
		{"NUL is replaced", "a\x00b", "a\ufffdb", 1},
		{"bidi override is removed", "abc\u202edef\u202c", "abcdef", 2},
		{"bidi isolates are removed", "a\u2066b\u2069c", "abc", 2},
		{"zero-width characters are removed", "a\u200bb\u200dc\ufeffd\u2060e", "abcde", 4},
		{"LRM and RLM are removed", "a\u200eb\u200fc", "abc", 2},
		{"invalid UTF-8", "a\xffb\xc3", "a\ufffdb\ufffd", 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, removed := Clean(tc.in)
			if got != tc.want {
				t.Fatalf("Clean(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if removed != tc.wantRemoved {
				t.Fatalf("Clean(%q) removed = %d, want %d", tc.in, removed, tc.wantRemoved)
			}
		})
	}
}

func TestCleanReturnsSameStringWhenClean(t *testing.T) {
	t.Parallel()

	in := strings.Repeat("web-01 ready\n", 100)
	got, removed := Clean(in)
	if got != in || removed != 0 {
		t.Fatalf("clean text changed or counted: removed=%d", removed)
	}
}

func TestLine(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{"one\ntwo", "one two"},
		{"a\tb", "a    b"},
		{"web-01\x1b[2J", "web-01"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := Line(tc.in); got != tc.want {
			t.Errorf("Line(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBounded(t *testing.T) {
	t.Parallel()

	if got := Bounded("abcdef", 3); got != "abc..." {
		t.Errorf("Bounded = %q", got)
	}
	if got := Bounded("abc", 3); got != "abc" {
		t.Errorf("Bounded short = %q", got)
	}
	if got := Bounded("日本語文", 2); got != "日本..." {
		t.Errorf("Bounded counts runes, got %q", got)
	}
	if got := Bounded("abc", 0); got != "abc" {
		t.Errorf("Bounded with no limit = %q", got)
	}
}

// TestHostileCorpus is spec test S-13 at the unit level: names and output a
// hostile host could produce never reach the terminal with a control byte.
func TestHostileCorpus(t *testing.T) {
	t.Parallel()

	corpus := []string{
		"web-01\x1b[2J\x1b[H",
		"$(touch /tmp/x)\nweb-02",
		"web\u202e-03",
		"\x1b]52;c;ZXZpbA==\x07web-04",
		"web-05\r\x1b[1Aweb-FAKE",
		"web\u200b-06",
		"\u009b2Jweb-07",
	}
	for _, in := range corpus {
		got := Line(in)
		assertTerminalSafe(t, got)
	}
}

func assertTerminalSafe(t *testing.T, s string) {
	t.Helper()

	if !utf8.ValidString(s) {
		t.Fatalf("output is not valid UTF-8: %q", s)
	}
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f, hidden(r):
			t.Fatalf("output %q contains unsafe rune %U", s, r)
		}
	}
}

func FuzzClean(f *testing.F) {
	seeds := []string{
		"", "plain", "a\x1b[31mb", "\x1b]52;c;Zm9v\x07", "\u009b2J", "a\u202eb", "\xff\xfe", "\x1b", "\x1b[", "\x1bP", "a\r\nb",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		out, removed := Clean(in)
		assertTerminalSafe(t, out)
		if removed < 0 {
			t.Fatalf("negative removal count %d", removed)
		}
		if out2, removed2 := Clean(out); out2 != out || removed2 != 0 {
			t.Fatalf("Clean is not idempotent: %q -> %q (removed %d)", out, out2, removed2)
		}
		if line := Line(in); strings.ContainsAny(line, "\n\t\r") {
			t.Fatalf("Line kept a line break or tab: %q", line)
		}
	})
}

func BenchmarkCleanClean15KiB(b *testing.B) {
	in := strings.Repeat("0123456789abcdef\n", 960)
	b.SetBytes(int64(len(in)))
	for range b.N {
		Clean(in)
	}
}
