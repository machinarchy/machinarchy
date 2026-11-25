package group

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/ui"
)

func TestClassOf(t *testing.T) {
	t.Parallel()

	tests := map[string]Class{
		"Succeeded": ClassSucceeded, "Failed": ClassFailed, "TimedOut": ClassTimedOut, "Cancelled": ClassCancelled,
		"Interrupted": ClassUnknown, "Lost": ClassUnknown, "Running": ClassRunning, "Pending": ClassPending,
		"SomethingNew": ClassUnknown, "": ClassUnknown,
	}
	for state, want := range tests {
		if got := ClassOf(state); got != want {
			t.Errorf("ClassOf(%q) = %v, want %v", state, got, want)
		}
	}
}

// G-01
func TestAllSuccessIsOneGroup(t *testing.T) {
	t.Parallel()

	s := summarise(fleetAllSuccess())
	if s.Total != 14 || len(s.Classes) != 1 || s.GroupCount() != 1 {
		t.Fatalf("want one class and one group for 14 hosts, got total=%d classes=%d groups=%d", s.Total, len(s.Classes), s.GroupCount())
	}
	g := s.Classes[0].Groups[0]
	if g.Count != 14 || g.Hosts[0] != "web-01" || g.Hosts[13] != "web-14" || g.Stdout != "active" {
		t.Fatalf("unexpected group %+v", g)
	}
	if got := ExitFor(s); got != 0 {
		t.Fatalf("ExitFor = %d, want 0", got)
	}
	if s.Drift.Kind != DriftNone {
		t.Fatalf("one group is not drift: %+v", s.Drift)
	}
}

// G-02
func TestIdenticalFailuresAreOneGroupAndExitNine(t *testing.T) {
	t.Parallel()

	s := summarise(fleetIdenticalFailures())
	if s.GroupCount() != 1 || s.Classes[0].Class != ClassFailed || s.Classes[0].Groups[0].Count != 14 {
		t.Fatalf("unexpected summary %+v", s)
	}
	if got := ExitFor(s); got != 9 {
		t.Fatalf("ExitFor = %d, want 9", got)
	}
}

// G-03
func TestDistinctFailuresAreOrderedByWhatToActOn(t *testing.T) {
	t.Parallel()

	s := summarise(fleetDistinctFailures())
	var order []Class
	for _, view := range s.Classes {
		order = append(order, view.Class)
	}
	want := []Class{ClassFailed, ClassTimedOut, ClassUnknown, ClassSucceeded}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("class order = %v, want %v", order, want)
	}
	if s.Count(ClassFailed) != 2 || s.Count(ClassTimedOut) != 1 || s.Count(ClassUnknown) != 2 || s.Count(ClassSucceeded) != 9 {
		t.Fatalf("counts wrong: %+v", s.Document().Counts)
	}
	if got := ExitFor(s); got != 8 {
		t.Fatalf("ExitFor = %d, want 8", got)
	}
	// Lost hosts are "unknown", never counted as failed or succeeded.
	if hosts := s.Classes[2].Groups[0].Hosts; strings.Join(hosts, ",") != "web-05,web-11" {
		t.Fatalf("unknown hosts = %v", hosts)
	}
}

// G-07
func TestDriftIsDetectedAgainstTheDominantGroup(t *testing.T) {
	t.Parallel()

	s := summarise(fleetDrift())
	if s.Drift.Kind != DriftDominant || s.Drift.Deviant != 3 || s.Drift.Majority != 11 {
		t.Fatalf("drift = %+v, want dominant 3 of 14 against 11", s.Drift)
	}
	groups := s.Classes[0].Groups
	if len(groups) != 3 || groups[0].Differs || !groups[1].Differs || !groups[2].Differs {
		t.Fatalf("differs flags wrong: %+v", groups)
	}
	if got := ExitFor(s); got != 0 {
		t.Fatalf("drift is information, not failure: ExitFor = %d", got)
	}
}

// G-08
func TestTimeDependentOutputIsNotDrift(t *testing.T) {
	t.Parallel()

	s := summarise(fleetTimeDependent())
	if s.Drift.Kind != DriftAllDifferent {
		t.Fatalf("drift = %+v, want all different", s.Drift)
	}
	for _, g := range s.Classes[0].Groups {
		if g.Differs {
			t.Fatal("no group may be flagged when every host differs")
		}
	}
}

// With no majority there is no odd one out: nothing is flagged.
func TestNoMajorityIsMixedNotDrift(t *testing.T) {
	t.Parallel()

	rs := []Result{okRes("a-1", "x"), okRes("a-2", "x"), okRes("b-1", "y"), okRes("b-2", "y")}
	s := summarise(rs)
	if s.Drift.Kind != DriftMixed || s.Drift.Groups != 2 || s.Drift.Majority != 2 {
		t.Fatalf("drift = %+v", s.Drift)
	}
	for _, g := range s.Classes[0].Groups {
		if g.Differs {
			t.Fatalf("no group may be flagged without a majority: %+v", s.Classes[0].Groups)
		}
	}
	if out := RenderString(s, RenderOptions{Style: ui.Style{Plain: true}}); !strings.Contains(out, "2 different outputs among 4 hosts; none is the majority.") {
		t.Fatalf("mixed note missing:\n%s", out)
	}
}

// Exactly half is not a majority; one more host is.
func TestMajorityNeedsMoreThanHalf(t *testing.T) {
	t.Parallel()

	half := summarise([]Result{okRes("a-1", "x"), okRes("a-2", "x"), okRes("b-1", "y"), okRes("c-1", "z")})
	if half.Drift.Kind != DriftMixed {
		t.Fatalf("2 of 4 is not a majority: %+v", half.Drift)
	}
	more := summarise([]Result{okRes("a-1", "x"), okRes("a-2", "x"), okRes("a-3", "x"), okRes("b-1", "y"), okRes("c-1", "z")})
	if more.Drift.Kind != DriftDominant || more.Drift.Deviant != 2 || more.Drift.Majority != 3 {
		t.Fatalf("3 of 5 is a majority: %+v", more.Drift)
	}
}

// G-09: output that differs only in volatile data is one group.
func TestVolatileDataDoesNotSplitGroups(t *testing.T) {
	t.Parallel()

	rs := []Result{
		okRes("h-1", "2026-11-26T09:14:03Z pid=101 worker 123e4567-e89b-12d3-a456-426614174000 at 0xdeadbeef01\n"),
		okRes("h-2", "2026-11-26T09:15:44Z pid=202 worker 223e4567-e89b-12d3-a456-426614174111 at 0xcafebabe02\n"),
		okRes("h-3", "2026-11-26T09:16:10Z pid=303 worker 323e4567-e89b-12d3-a456-426614174222 at 0x1234567803   \n"),
	}
	if s := summarise(rs); s.GroupCount() != 1 {
		t.Fatalf("want one group, got %d: %+v", s.GroupCount(), s.Classes)
	}
}

// G-10: numbers that are not volatile data keep results apart.
func TestVersionNumbersSplitGroups(t *testing.T) {
	t.Parallel()

	rs := []Result{okRes("h-1", "nginx/1.24.0\n"), okRes("h-2", "nginx/1.22.1\n")}
	if s := summarise(rs); s.GroupCount() != 2 {
		t.Fatalf("want two groups, got %d", s.GroupCount())
	}
}

func TestExitCodeAndErrorClassSplitGroups(t *testing.T) {
	t.Parallel()

	rs := []Result{
		failRes("a-1", 1, "boom"), failRes("a-2", 2, "boom"),
		classRes("a-3", StateFailed, "unreachable"), classRes("a-4", StateFailed, "connection_failed"),
		classRes("a-5", StateLost, "supervisor_lost"), classRes("a-6", StateLost, "unreachable"),
	}
	if s := summarise(rs); s.GroupCount() != 6 {
		t.Fatalf("want six groups, got %d", s.GroupCount())
	}
}

func TestDuplicateHostIsIgnored(t *testing.T) {
	t.Parallel()

	a := New(Options{})
	a.Add(okRes("web-01", "x"))
	a.Add(failRes("web-01", 1, "y"))
	if a.Len() != 1 || a.Summary().Count(ClassSucceeded) != 1 || a.Summary().Count(ClassFailed) != 0 {
		t.Fatalf("a second result for a host must be ignored: %+v", a.Summary())
	}
}

// The sample of a group is the output of its naturally first host, whatever
// the order in which hosts finished.
func TestSampleComesFromTheNaturallyFirstHost(t *testing.T) {
	t.Parallel()

	for _, order := range [][]string{{"web-2", "web-10", "web-1"}, {"web-1", "web-2", "web-10"}, {"web-10", "web-1", "web-2"}} {
		a := New(Options{})
		for _, host := range order {
			a.Add(okRes(host, "started at 14:02:11 by "+host+"\n"))
		}
		got := a.Summary().Classes[0].Groups[0].Stdout
		if got != "started at 14:02:11 by web-1" {
			t.Fatalf("order %v: sample %q", order, got)
		}
	}
}

// G-15
func TestSampleIsBounded(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&out, "line %d\n", i)
	}
	g := summarise([]Result{okRes("h-1", out.String())}).Classes[0].Groups[0]
	if lines := strings.Split(g.Stdout, "\n"); len(lines) != 5 || lines[4] != "line 5" || g.StdoutMore != 295 {
		t.Fatalf("sample = %q, more = %d", g.Stdout, g.StdoutMore)
	}

	long := summarise([]Result{okRes("h-1", strings.Repeat("x", 1000))}).Classes[0].Groups[0]
	if len([]rune(long.Stdout)) != 303 || !strings.HasSuffix(long.Stdout, "...") {
		t.Fatalf("long line sample has %d runes", len([]rune(long.Stdout)))
	}
}

// G-14
func TestSampleIsSanitisedAndRenderingHasNoControlBytes(t *testing.T) {
	t.Parallel()

	hostile := "ok\x1b[2J\x1b]52;c;ZXZpbA==\x07 done\r\x1b[1Afake" + string(rune(0x202e))
	rs := []Result{okRes("web-01\x1b[31m", hostile), failRes("web-02", 1, hostile)}
	s := summarise(rs)

	for _, style := range []ui.Style{{Unicode: true}, {Plain: true}, {Color: true, Unicode: true}} {
		out := RenderString(s, RenderOptions{Style: style, Width: 100})
		stripped := out
		if style.Color {
			stripped = stripPaint(out)
		}
		for _, r := range stripped {
			if r == 0x1b || r == '\r' || r == 0x07 || (r >= 0x80 && r <= 0x9f) || r == 0x202e {
				t.Fatalf("control character %U reached the output:\n%q", r, out)
			}
		}
	}
}

func TestExitForEdges(t *testing.T) {
	t.Parallel()

	if got := ExitFor(summarise(nil)); got != 0 {
		t.Errorf("empty run exit = %d", got)
	}
	if got := ExitFor(summarise([]Result{classRes("a-1", StateCancelled, ""), classRes("a-2", StateCancelled, "")})); got != 9 {
		t.Errorf("all cancelled exit = %d, want 9", got)
	}
	if got := ExitFor(summarise([]Result{okRes("a-1", ""), classRes("a-2", StateLost, "")})); got != 8 {
		t.Errorf("one lost exit = %d, want 8", got)
	}
}

func TestEmptyRun(t *testing.T) {
	t.Parallel()

	if got := RenderString(summarise(nil), RenderOptions{}); got != "No hosts.\n" {
		t.Fatalf("got %q", got)
	}
}

// G-12: the same results in any order give the same summary, text and JSON.
func TestOrderIndependence(t *testing.T) {
	t.Parallel()

	results := fleet500()
	opts := RenderOptions{Style: ui.Style{Unicode: true}, Width: 100, RunID: "r-4k2p9x1a"}
	base := summarise(results)
	wantText := RenderString(base, opts)
	var wantJSON bytes.Buffer
	if err := RenderJSON(&wantJSON, base); err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(42)) // #nosec G404 -- deterministic test data
	for range 20 {
		rng.Shuffle(len(results), func(i, j int) { results[i], results[j] = results[j], results[i] })
		s := summarise(results)
		if got := RenderString(s, opts); got != wantText {
			t.Fatalf("text changed with the order of results:\n%s\nvs\n%s", got, wantText)
		}
		var got bytes.Buffer
		if err := RenderJSON(&got, s); err != nil {
			t.Fatal(err)
		}
		if got.String() != wantJSON.String() {
			t.Fatal("JSON changed with the order of results")
		}
	}
}

// G-17: the JSON lists every host and every group and is valid, stable JSON.
func TestJSONDocumentIsCompleteAndStable(t *testing.T) {
	t.Parallel()

	s := summarise(fleetDistinctFailures())
	var buf bytes.Buffer
	if err := RenderJSON(&buf, s); err != nil {
		t.Fatal(err)
	}

	var doc Document
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	hosts := 0
	for _, class := range doc.Classes {
		for _, g := range class.Groups {
			hosts += len(g.Hosts)
		}
	}
	if doc.Total != 14 || hosts != 14 || doc.Exit != 8 || doc.Counts.Succeeded != 9 || doc.Counts.Unknown != 2 {
		t.Fatalf("incomplete document: %+v (hosts=%d)", doc, hosts)
	}

	// Every key is present even when empty, and arrays are never null.
	var generic map[string]any
	if err := json.Unmarshal(buf.Bytes(), &generic); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"total", "exit", "counts", "drift", "classes"} {
		if _, ok := generic[key]; !ok {
			t.Errorf("key %q missing", key)
		}
	}
	empty, err := json.Marshal(summarise(nil).Document())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(empty), `"classes":[]`) {
		t.Fatalf("empty run must give an empty array, got %s", empty)
	}
}

// stripPaint removes the ANSI colour codes the style adds.
func stripPaint(s string) string {
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
