package ui

import (
	"strings"
	"testing"
)

// Golden logotypes (07-brand.md section 4).
func TestLogotypeGolden(t *testing.T) {
	t.Parallel()

	rich := "   ◆\n  ◆ ◆     machinarchy\n ◆ ◆ ◆    one authority. distributed execution.\n"
	ascii := "   *\n  * *     machinarchy\n * * *    one authority. distributed execution.\n"

	tests := []struct {
		name  string
		style Style
		want  string
	}{
		{"unicode", Style{Unicode: true}, rich},
		{"N-11: NO_COLOR keeps the symbols", Style{Unicode: true, Color: false}, rich},
		{"N-12: non-UTF-8 locale uses ASCII", Style{}, ascii},
		{"plain has no logotype", Style{Plain: true, Color: true, Unicode: true}, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.style.Logotype(); got != tc.want {
				t.Fatalf("Logotype() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLogotypeColourAndWidth(t *testing.T) {
	t.Parallel()

	coloured := Style{Color: true, Unicode: true}.Logotype()
	if !strings.Contains(coloured, "\x1b[36m") || !strings.Contains(coloured, "\x1b[1mmachinarchy") {
		t.Fatalf("coloured logotype lacks accent or bold: %q", coloured)
	}

	// U-07: no line is wider than the narrowest tested width, colour aside.
	plainText := Style{Unicode: true}.Logotype()
	for line := range strings.SplitSeq(strings.TrimRight(plainText, "\n"), "\n") {
		if n := len([]rune(line)); n > 60 {
			t.Fatalf("logotype line is %d columns wide: %q", n, line)
		}
	}

	// U-05 for the ASCII form: nothing but ASCII, no ANSI, no carriage return.
	for _, r := range (Style{}).Logotype() {
		if r > 127 || r == '\r' || r == 0x1b {
			t.Fatalf("ASCII logotype contains %q", r)
		}
	}
}
