package ui

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/cli/msg"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestDetectStyle(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer // not a terminal
	tests := []struct {
		name  string
		env   map[string]string
		plain bool
		want  Style
	}{
		{"plain flag turns everything off", map[string]string{"LANG": "en_US.UTF-8"}, true, Style{Plain: true}},
		{"utf-8 pipe keeps symbols, no colour", map[string]string{"LANG": "en_US.UTF-8"}, false, Style{Unicode: true}},
		{"C locale is ASCII", map[string]string{"LANG": "C"}, false, Style{}},
		{"no locale at all is ASCII", map[string]string{}, false, Style{}},
		{"LC_ALL wins over LANG", map[string]string{"LC_ALL": "C", "LANG": "en_US.UTF-8"}, false, Style{}},
		{"LC_CTYPE wins over LANG", map[string]string{"LC_CTYPE": "en_US.UTF-8", "LANG": "C"}, false, Style{Unicode: true}},
		{"utf8 spelling", map[string]string{"LANG": "en_US.utf8"}, false, Style{Unicode: true}},
		{"TERM=dumb is ASCII", map[string]string{"LANG": "en_US.UTF-8", "TERM": "dumb"}, false, Style{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := DetectStyle(env(tc.env), &buffer, tc.plain); got != tc.want {
				t.Fatalf("DetectStyle = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestIsTerminalFalseForPipesAndBuffers(t *testing.T) {
	t.Parallel()

	if IsTerminal(&bytes.Buffer{}) {
		t.Fatal("a buffer is not a terminal")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	if IsTerminal(writer) {
		t.Fatal("a pipe is not a terminal")
	}
}

func TestWidth(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	tests := []struct {
		columns string
		want    int
	}{{"", 80}, {"120", 120}, {"10", 40}, {"abc", 80}, {"0", 80}}
	for _, tc := range tests {
		if got := Width(env(map[string]string{"COLUMNS": tc.columns}), &buffer); got != tc.want {
			t.Errorf("Width(COLUMNS=%q) = %d, want %d", tc.columns, got, tc.want)
		}
	}
}

func TestSymbolsAlwaysComeWithWords(t *testing.T) {
	t.Parallel()

	rich := Style{Unicode: true}
	ascii := Style{}
	for sym := SymOK; sym <= SymCancelled; sym++ {
		if rich.Sym(sym) == "" || ascii.Sym(sym) == "" {
			t.Errorf("symbol %d has no marker", sym)
		}
		for _, r := range ascii.Sym(sym) {
			if r > 127 {
				t.Errorf("ASCII marker %q for %d is not ASCII", ascii.Sym(sym), sym)
			}
		}
	}
	if got := rich.Sym(SymOK); got != "✔" {
		t.Errorf("rich OK marker = %q", got)
	}
	if got := ascii.Sym(SymFail); got != "FAILED" {
		t.Errorf("ASCII failure marker = %q", got)
	}
	if got := (Style{Unicode: true, Plain: true}).Sym(SymOK); got != "OK" {
		t.Errorf("plain overrides unicode, got %q", got)
	}
}

func TestPaint(t *testing.T) {
	t.Parallel()

	colour := Style{Color: true}
	if got := colour.Paint(RoleError, "x"); got != "\x1b[31mx\x1b[0m" {
		t.Errorf("Paint = %q", got)
	}
	if got := (Style{}).Paint(RoleError, "x"); got != "x" {
		t.Errorf("no colour must leave text alone, got %q", got)
	}
	if got := (Style{Color: true, Plain: true}).Paint(RoleError, "x"); got != "x" {
		t.Errorf("plain must leave text alone, got %q", got)
	}
	if got := colour.Paint(RoleError, ""); got != "" {
		t.Errorf("empty text must stay empty, got %q", got)
	}
}

func TestRenderError(t *testing.T) {
	t.Parallel()

	m := msg.Message{Kind: msg.KindError, Primary: "web-09 is not ready (cannot connect).",
		Why: "the controller could not reach 10.0.0.9:22.", Next: "machinarchy hosts show web-09"}
	want := "Error: web-09 is not ready (cannot connect).\n" +
		"Why:   the controller could not reach 10.0.0.9:22.\n" +
		"Next:  machinarchy hosts show web-09\n"
	if got := Render(m, Style{}); got != want {
		t.Fatalf("Render error:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderErrorWithoutWhyAndMultilineNext(t *testing.T) {
	t.Parallel()

	m := msg.Message{Kind: msg.KindError, Primary: "boom", Next: "line one\nline two"}
	want := "Error: boom\nNext:  line one\n       line two\n"
	if got := Render(m, Style{}); got != want {
		t.Fatalf("Render = %q, want %q", got, want)
	}
	if got := Render(msg.Message{Kind: msg.KindError, Primary: "boom"}, Style{}); got != "Error: boom\n" {
		t.Fatalf("Render without next = %q", got)
	}
}

func TestRenderSuccessWithLore(t *testing.T) {
	t.Parallel()

	m := msg.Get("run.all_ok").With(msg.Vars{"ok": "14", "total": "14", "id": "r-4k2p9x1a", "host": "web-01"})

	rich := Render(m, Style{Unicode: true})
	wantRich := "✔ 14 of 14 hosts succeeded\n  The machines complied.\n  Next:  machinarchy run r-4k2p9x1a --host web-01\n"
	if rich != wantRich {
		t.Fatalf("rich:\n%s\nwant:\n%s", rich, wantRich)
	}

	plain := Render(m, Style{Plain: true})
	wantPlain := "OK 14 of 14 hosts succeeded\n  Next:  machinarchy run r-4k2p9x1a --host web-01\n"
	if plain != wantPlain {
		t.Fatalf("plain:\n%s\nwant:\n%s", plain, wantPlain)
	}
}

func TestLoreNeverInErrorsOrWarnings(t *testing.T) {
	t.Parallel()

	for _, kind := range []msg.Kind{msg.KindError, msg.KindWarning, msg.KindFailure, msg.KindSecurity} {
		got := Render(msg.Message{Kind: kind, Primary: "x", Lore: "SECRET LORE", Next: "y"}, Style{Unicode: true})
		if strings.Contains(got, "SECRET LORE") {
			t.Errorf("lore printed for kind %s: %q", kind, got)
		}
	}
}

// U-05: plain output is ASCII only, with no ANSI sequences and no carriage
// returns, for every message in the catalogue.
func TestPlainRenderIsASCII(t *testing.T) {
	t.Parallel()

	for _, m := range msg.All() {
		got := Render(m, Style{Plain: true, Color: true, Unicode: true})
		for _, r := range got {
			if r > 127 || r == '\r' || r == 0x1b {
				t.Fatalf("plain rendering of %q contains %q: %q", m.ID, r, got)
			}
		}
	}
}

// S-13 at message level: a hostile value inside a message cannot drive the
// terminal.
func TestRenderSanitisesVariables(t *testing.T) {
	t.Parallel()

	m := msg.Message{Kind: msg.KindError, Primary: "{host} is down", Next: "machinarchy hosts show {host}"}.
		With(msg.Vars{"host": "web-01\x1b[2J\x1b]52;c;ZXZpbA==\x07"})
	got := Render(m, Style{Color: true, Unicode: true})
	if strings.Contains(got, "52;c") || strings.Contains(got, "[2J") {
		t.Fatalf("escape sequence reached the output: %q", got)
	}
}

func TestMarkersAreFollowedByWords(t *testing.T) {
	t.Parallel()

	for _, m := range msg.All() {
		for _, style := range []Style{{Unicode: true}, {Plain: true}} {
			for _, line := range strings.Split(strings.TrimRight(Render(m, style), "\n"), "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == "✔" || trimmed == "✖" || trimmed == "⚠" || trimmed == "OK" || trimmed == "FAILED" || trimmed == "WARN" {
					t.Fatalf("bare marker on a line of %q: %q", m.ID, line)
				}
			}
		}
	}
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// G-13: not a terminal, so no redraw; one line every 10 s and at each 10 %.
func TestProgressPlainLines(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Unix(1000, 0)}
	var out bytes.Buffer
	p := NewProgress(&out, Style{Unicode: true}, false, clock.Now)

	p.Update(Counts{Total: 200})                                 // 0 %: nothing
	p.Update(Counts{Done: 5, Total: 200, OK: 5, Running: 195})   // 2.5 %: nothing
	p.Update(Counts{Done: 20, Total: 200, OK: 20, Running: 180}) // 10 %: line
	p.Update(Counts{Done: 25, Total: 200, OK: 25, Running: 175}) // same decile, not 10 s: nothing
	clock.Advance(11 * time.Second)                              //
	p.Update(Counts{Done: 26, Total: 200, OK: 26, Running: 174}) // 10 s since last line: line
	p.Update(Counts{Done: 60, Total: 200, OK: 55, Failed: 5})    // 30 %: line
	p.Finish(Counts{Done: 200, Total: 200, OK: 190, Failed: 10}) // final
	p.Finish(Counts{Done: 200, Total: 200, OK: 190, Failed: 10}) // second Finish: nothing

	want := "progress 20/200 ok=20 failed=0 running=180\n" +
		"progress 26/200 ok=26 failed=0 running=174\n" +
		"progress 60/200 ok=55 failed=5 running=0\n" +
		"progress 200/200 ok=190 failed=10 running=0\n"
	if out.String() != want {
		t.Fatalf("progress output:\n%s\nwant:\n%s", out.String(), want)
	}
	if strings.ContainsAny(out.String(), "\r\x1b") {
		t.Fatal("plain progress must not redraw")
	}
}

func TestProgressPlainModeIgnoresTTY(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Unix(0, 0)}
	var out bytes.Buffer
	p := NewProgress(&out, Style{Plain: true}, true, clock.Now)
	p.Update(Counts{Done: 10, Total: 10, OK: 10})
	p.Finish(Counts{Done: 10, Total: 10, OK: 10})

	if strings.ContainsAny(out.String(), "\r\x1b") || !strings.HasPrefix(out.String(), "progress ") {
		t.Fatalf("--plain on a terminal must still print plain lines: %q", out.String())
	}
}

func TestProgressLiveRedrawThrottled(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Unix(0, 0)}
	var out bytes.Buffer
	p := NewProgress(&out, Style{Unicode: true}, true, clock.Now)

	p.Update(Counts{Done: 100, Total: 200, OK: 98, Failed: 2, Running: 100})
	clock.Advance(100 * time.Millisecond)
	p.Update(Counts{Done: 101, Total: 200, OK: 99, Failed: 2, Running: 99}) // throttled
	clock.Advance(600 * time.Millisecond)
	p.Update(Counts{Done: 150, Total: 200, OK: 148, Failed: 2, Running: 50})
	p.Finish(Counts{Done: 200, Total: 200, OK: 198, Failed: 2})

	got := out.String()
	if strings.Count(got, "\r") != 3 {
		t.Fatalf("want 3 redraws (first, after 700 ms, final), got %q", got)
	}
	if !strings.Contains(got, "██████████░░░░░░░░░░ 100/200  ✔ 98  ✖ 2  ● 100 running") {
		t.Fatalf("live line wrong: %q", got)
	}
	if !strings.HasSuffix(got, "\n") || strings.Count(got, "\n") != 1 {
		t.Fatalf("exactly one newline at the end expected: %q", got)
	}
}

func TestProgressLiveASCIIBar(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Unix(0, 0)}
	var out bytes.Buffer
	p := NewProgress(&out, Style{}, true, clock.Now)
	p.Update(Counts{Done: 10, Total: 20, OK: 10})

	got := out.String()
	if !strings.Contains(got, "##########---------- 10/20  OK 10  FAILED 0  RUN 0 running") {
		t.Fatalf("ASCII live line wrong: %q", got)
	}
	for _, r := range got {
		if r > 127 {
			t.Fatalf("ASCII style printed %q", r)
		}
	}
}

func TestProgressZeroTotal(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Unix(0, 0)}
	var out bytes.Buffer
	p := NewProgress(&out, Style{}, false, clock.Now)
	p.Update(Counts{})
	p.Finish(Counts{})
	if out.String() != "progress 0/0 ok=0 failed=0 running=0\n" {
		t.Fatalf("zero-total progress = %q", out.String())
	}
}
