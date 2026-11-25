package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzParseSelector: the parser never panics, a parsed selector reports its
// source, and matching never panics on any field values.
func FuzzParseSelector(f *testing.F) {
	for _, seed := range []string{"tier=web", "role in (web,db),!canary", "name=web-*", "a!=b", "x notin (a)", "", "((", "a=b,,c", "k=" + strings.Repeat("v", 300)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, expr string) {
		selector, err := ParseSelector(expr)
		if err != nil {
			return
		}
		if selector.String() != strings.TrimSpace(expr) {
			t.Fatalf("String() = %q, want %q", selector.String(), strings.TrimSpace(expr))
		}
		fields := func(key string) (string, bool, bool) { return expr, len(key)%2 == 0, len(key)%3 == 0 }
		_ = selector.MatchesFields(fields)
	})
}

// FuzzGlobMatch: matching is total and agrees with the trivial cases.
func FuzzGlobMatch(f *testing.F) {
	f.Add("web-*", "web-01")
	f.Add("?", "")
	f.Add(strings.Repeat("*a", 20), strings.Repeat("a", 40)+"b")
	f.Fuzz(func(t *testing.T, pattern, value string) {
		got := globMatch(pattern, value)
		if !strings.ContainsAny(pattern, "*?") && got != (pattern == value) {
			t.Fatalf("globMatch(%q, %q) = %v without wildcards", pattern, value, got)
		}
		if pattern == "*" && !got {
			t.Fatal("* must match everything")
		}
	})
}

// FuzzQuoteArgv: any argv ValidateArgv accepts survives POSIX quoting intact,
// so the remote shell sees exactly the arguments that were approved.
func FuzzQuoteArgv(f *testing.F) {
	f.Add("uptime", "-p", "$(reboot)")
	f.Add("printf", "%s\n", "a'b\"c`d")
	f.Add("ls", "; rm -rf /", "*")
	f.Fuzz(func(t *testing.T, program, first, second string) {
		argv := []string{program, first, second}
		if ValidateArgv(argv) != nil {
			return
		}
		got := unquote(t, QuoteArgv(argv))
		if len(got) != len(argv) {
			t.Fatalf("quoted %q round-tripped to %q", argv, got)
		}
		for i := range argv {
			if got[i] != argv[i] {
				t.Fatalf("argument %d changed: %q -> %q", i, argv[i], got[i])
			}
		}
	})
}

// FuzzSanitizeOutput: sanitised output is valid UTF-8, bounded, and has no
// control characters other than newline and tab.
func FuzzSanitizeOutput(f *testing.F) {
	f.Add("\x1b[31mred\x1b[0m\n", 64)
	f.Add("\xff\xfe broken \x00", 8)
	f.Fuzz(func(t *testing.T, input string, limit int) {
		if limit < 1 || limit > 1<<16 {
			return
		}
		out := SanitizeOutput(input, limit)
		if !utf8.ValidString(out) {
			t.Fatalf("invalid UTF-8: %q", out)
		}
		if len(out) > limit+len(outputTruncationMarker) {
			t.Fatalf("len %d exceeds limit %d", len(out), limit)
		}
		for _, r := range strings.TrimSuffix(out, outputTruncationMarker) {
			if r < 0x20 && r != '\n' && r != '\t' {
				t.Fatalf("control character %U in %q", r, out)
			}
		}
	})
}
