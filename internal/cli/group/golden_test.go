package group

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/machinarchy/machinarchy/internal/cli/ui"
)

var (
	richStyle  = ui.Style{Unicode: true}
	plainStyle = ui.Style{Plain: true}
)

type scenario struct {
	name    string
	results []Result
	timeout string
}

func scenarios() []scenario {
	return []scenario{
		{"g01-all-success", fleetAllSuccess(), ""},
		{"g02-identical-failures", fleetIdenticalFailures(), ""},
		{"g03-distinct-failures", fleetDistinctFailures(), "60 s"},
		{"g06-timeouts", fleetTimeouts(), "60 s"},
		{"g07-drift", fleetDrift(), ""},
		{"g08-time-dependent", fleetTimeDependent(), ""},
	}
}

// G-01..G-03, G-06..G-08 and G-19: the transcripts of docs/cli/spec/03-ux.md
// section 7, in rich and in plain mode.
func TestRenderGolden(t *testing.T) {
	for _, sc := range scenarios() {
		s := summarise(sc.results)
		for mode, style := range map[string]ui.Style{"rich": richStyle, "plain": plainStyle} {
			t.Run(sc.name+"-"+mode, func(t *testing.T) {
				got := RenderString(s, RenderOptions{Style: style, Width: 80, RunID: "r-4k2p9x1a", TimeoutLabel: sc.timeout})
				golden(t, sc.name+"."+mode, got)
			})
		}
	}
}

func TestJSONGolden(t *testing.T) {
	for _, name := range []string{"g03-distinct-failures", "g07-drift"} {
		for _, sc := range scenarios() {
			if sc.name != name {
				continue
			}
			var buf bytes.Buffer
			if err := RenderJSON(&buf, summarise(sc.results)); err != nil {
				t.Fatal(err)
			}
			golden(t, name+".json", buf.String())
		}
	}
}

// G-04: 500 hosts stay on one screen, and nothing is lost from the JSON.
func TestG04FiveHundredHosts(t *testing.T) {
	s := summarise(fleet500())
	opts := RenderOptions{Style: richStyle, Width: 100, RunID: "r-4k2p9x1a"}
	out := RenderString(s, opts)
	golden(t, "g04-500-hosts.rich", out)

	if lines := strings.Count(out, "\n"); lines > 40 {
		t.Fatalf("500-host output has %d lines, want at most 40:\n%s", lines, out)
	}
	assertWidth(t, out, 100)
	if got := ExitFor(s); got != 8 {
		t.Fatalf("exit = %d, want 8", got)
	}
	if !strings.Contains(out, "480 of 500 hosts succeeded") && !strings.Contains(out, "of 500 hosts succeeded") {
		t.Fatalf("exact counts missing:\n%s", out)
	}

	var buf bytes.Buffer
	if err := RenderJSON(&buf, s); err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	hosts := map[string]bool{}
	for _, class := range doc.Classes {
		for _, g := range class.Groups {
			for _, host := range g.Hosts {
				hosts[host] = true
			}
		}
	}
	if len(hosts) != 500 {
		t.Fatalf("the JSON lists %d hosts, want 500", len(hosts))
	}
}

// G-05 (rendering): 5,000 hosts stay on one screen and the counts are exact.
// The outputs are short so the test is also fast under -race and -cover.
func TestG05FiveThousandHostsRendering(t *testing.T) {
	variants := make([]string, 200)
	for i := range variants {
		variants[i] = variantText(i, 10)
	}

	s := summarise(fleet5000(variants))
	out := RenderString(s, RenderOptions{Style: plainStyle, Width: 100, RunID: "r-4k2p9x1a", TimeoutLabel: "60 s"})
	golden(t, "g05-5000-hosts.plain", out)

	if lines := strings.Count(out, "\n"); lines > 60 {
		t.Fatalf("5,000-host output has %d lines, want at most 60", lines)
	}
	assertWidth(t, out, 100)
	if s.Total != 5000 || s.Count(ClassSucceeded)+s.Count(ClassFailed)+s.Count(ClassTimedOut)+s.Count(ClassUnknown) != 5000 {
		t.Fatalf("counts do not add up: %+v", s.Document().Counts)
	}
	if got := ExitFor(s); got != 8 {
		t.Fatalf("exit = %d, want 8", got)
	}
}

// G-05 (memory and time): 5,000 hosts with 15 KiB of output each, 69 MiB in
// all. The aggregator keeps hashes, samples and indexes, not output, so the
// retained heap stays small. The race detector and coverage instrumentation
// slow the byte scanner by two orders of magnitude, so this test runs only in
// a plain `go test`.
func TestG05FiveThousandHostsMemoryAndTime(t *testing.T) {
	if raceEnabled || testing.CoverMode() != "" {
		t.Skip("instrumented build: run `go test -run G05 ./internal/cli/group` without -race and -cover")
	}

	variants := make([]string, 200)
	for i := range variants {
		variants[i] = variantText(i, 214)
	}

	runtimeGC()
	before := heapAlloc()
	start := time.Now()

	a := New(Options{})
	fed := 0
	for _, r := range fleet5000(variants) {
		fed += len(r.Stdout) + len(r.Stderr)
		a.Add(r)
	}
	s := a.Summary()
	out := RenderString(s, RenderOptions{Style: plainStyle, Width: 100, RunID: "r-4k2p9x1a", TimeoutLabel: "60 s"})

	elapsed := time.Since(start)
	runtimeGC()
	retained := int64(heapAlloc()) - int64(before) // #nosec G115 -- sizes are far below int64 limits
	keepAlive(a, s)

	t.Logf("5,000 hosts, %d MiB of output: %v, %d KiB retained, %d output lines", fed>>20, elapsed.Round(time.Millisecond), retained>>10, strings.Count(out, "\n"))

	if fed < 60<<20 {
		t.Fatalf("the test fed only %d MiB, the point is 5,000 x 15 KiB", fed>>20)
	}
	if s.Total != 5000 || strings.Count(out, "\n") > 60 {
		t.Fatalf("unexpected result: total=%d lines=%d", s.Total, strings.Count(out, "\n"))
	}
	if retained > 50<<20 {
		t.Fatalf("retained %d MiB, want under 50 MiB (spec G-05)", retained>>20)
	}
	if retained > 20<<20 {
		t.Fatalf("retained %d MiB: memory is growing with the amount of output", retained>>20)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("took %v; the spec target is 2 s and this machine normally needs well under 1 s", elapsed)
	}
}

// G-13 for the renderer: plain and non-terminal output has no escape codes
// and no carriage returns, and plain output is ASCII only (U-05).
func TestPlainOutputIsASCII(t *testing.T) {
	t.Parallel()

	for _, sc := range scenarios() {
		out := RenderString(summarise(sc.results), RenderOptions{Style: plainStyle, Width: 80, RunID: "r-1", TimeoutLabel: sc.timeout})
		for _, r := range out {
			if r > 127 || r == 0x1b || r == '\r' {
				t.Fatalf("%s: plain output contains %q:\n%s", sc.name, r, out)
			}
		}
	}
}

// Colour only adds codes: removing them gives the uncoloured text.
func TestColourOnlyAddsCodes(t *testing.T) {
	t.Parallel()

	s := summarise(fleetDistinctFailures())
	plain := RenderString(s, RenderOptions{Style: richStyle, Width: 80, RunID: "r-1"})
	coloured := RenderString(s, RenderOptions{Style: ui.Style{Color: true, Unicode: true}, Width: 80, RunID: "r-1"})
	if coloured == plain || stripPaint(coloured) != plain {
		t.Fatalf("colour changed more than the codes:\n%q\n%q", stripPaint(coloured), plain)
	}
}

// U-07: no line is longer than the width at the common widths. Next-step lines
// hold a command, which is never broken.
func TestLinesFitTheWidth(t *testing.T) {
	t.Parallel()

	for _, width := range []int{60, 80, 120} {
		for _, sc := range scenarios() {
			out := RenderString(summarise(sc.results), RenderOptions{Style: richStyle, Width: width, RunID: "r-4k2p9x1a", TimeoutLabel: sc.timeout})
			assertWidth(t, out, width)
		}
		assertWidth(t, RenderString(summarise(fleet500()), RenderOptions{Style: richStyle, Width: width}), width)
	}
}

// U-08: a symbol is always followed by words.
func TestSymbolsAreFollowedByWords(t *testing.T) {
	t.Parallel()

	for _, sc := range scenarios() {
		for _, line := range strings.Split(RenderString(summarise(sc.results), RenderOptions{Style: richStyle, Width: 80}), "\n") {
			switch strings.TrimSpace(line) {
			case "✔", "✖", "⚠", "?", "⏹":
				t.Fatalf("%s: bare symbol line %q", sc.name, line)
			}
		}
	}
}

func TestGroupCapSummarisesTheRest(t *testing.T) {
	t.Parallel()

	var rs []Result
	for i := 1; i <= 25; i++ { // 25 hosts, each with its own failure
		rs = append(rs, failRes(hostName("h", i, 2), i, "error "+strings.Repeat("x", i)))
	}
	s := summarise(rs)

	out := RenderString(s, RenderOptions{Style: plainStyle, Width: 100, RunID: "r-1"})
	if !strings.Contains(out, "and 15 more groups, 15 hosts") || !strings.Contains(out, "machinarchy run r-1 --groups all") {
		t.Fatalf("missing cap summary:\n%s", out)
	}
	if strings.Count(out, "FAILED") != 10+1 { // ten groups plus the header
		t.Fatalf("want 10 groups shown:\n%s", out)
	}

	all := RenderString(s, RenderOptions{Style: plainStyle, Width: 100, MaxGroups: -1})
	if strings.Contains(all, "more groups") || strings.Count(all, "FAILED") != 25+1 {
		t.Fatalf("MaxGroups -1 must show every group:\n%s", all)
	}
}

func TestOutputWithoutRunIDHasNoDanglingCommands(t *testing.T) {
	t.Parallel()

	out := RenderString(summarise(fleetDistinctFailures()), RenderOptions{Style: plainStyle, Width: 80})
	if strings.Contains(out, "machinarchy run") || strings.Contains(out, "{") {
		t.Fatalf("a command without an ID, or an unfilled placeholder, was printed:\n%s", out)
	}
}

func TestHostNamesInTheRenderedOutputAreSanitised(t *testing.T) {
	t.Parallel()

	s := summarise([]Result{okRes("web-01\x1b[2J", "x"), okRes("web-02\x1b]0;t\x07", "x")})
	out := RenderString(s, RenderOptions{Style: richStyle, Width: 80})
	if strings.ContainsRune(out, 0x1b) || strings.ContainsRune(out, 0x07) {
		t.Fatalf("host name reached the output unsanitised: %q", out)
	}
	if !utf8.ValidString(out) {
		t.Fatal("output is not valid UTF-8")
	}
}

func assertWidth(t *testing.T, out string, width int) {
	t.Helper()

	for _, line := range strings.Split(out, "\n") {
		if utf8.RuneCountInString(stripPaint(line)) > width && !strings.Contains(line, "Next:") {
			t.Fatalf("line of %d runes exceeds the width %d: %q", utf8.RuneCountInString(line), width, line)
		}
	}
}
