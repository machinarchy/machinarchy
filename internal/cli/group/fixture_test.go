package group

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

func intp(n int) *int { return &n }

func okRes(host, out string) Result {
	return Result{Host: host, State: StateSucceeded, ExitCode: intp(0), Stdout: out}
}

func failRes(host string, exit int, out string) Result {
	return Result{Host: host, State: StateFailed, ExitCode: intp(exit), Stdout: out}
}

func classRes(host, state, errClass string) Result {
	return Result{Host: host, State: state, ErrorClass: errClass}
}

// hostName builds names like web-01 with the given zero padding.
func hostName(prefix string, i, width int) string {
	return fmt.Sprintf("%s-%0*d", prefix, width, i)
}

func summarise(results []Result) Summary {
	a := New(Options{})
	for _, r := range results {
		a.Add(r)
	}

	return a.Summary()
}

// golden compares got with testdata/NAME.golden. Run the tests with -update to
// rewrite the files, then read the diff before committing it.
func golden(t *testing.T, name, got string) {
	t.Helper()

	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(path) // #nosec G304 -- fixed test fixture path
	if err != nil {
		t.Fatalf("missing golden file %s (run with -update): %v", path, err)
	}
	if string(want) != got {
		t.Fatalf("output differs from %s (run with -update to accept):\n--- got ---\n%s--- want ---\n%s", path, got, string(want))
	}
}

// ---- scenario fleets (docs/cli/spec/09-acceptance-tests.md group G) ----

// fleetAllSuccess is G-01: 14 hosts, same output.
func fleetAllSuccess() []Result {
	var rs []Result
	for i := 1; i <= 14; i++ {
		rs = append(rs, okRes(hostName("web", i, 2), "active\n"))
	}

	return rs
}

// fleetIdenticalFailures is G-02: 14 hosts fail the same way.
func fleetIdenticalFailures() []Result {
	var rs []Result
	for i := 1; i <= 14; i++ {
		rs = append(rs, failRes(hostName("web", i, 2), 3, "inactive\n"))
	}

	return rs
}

// fleetDistinctFailures is G-03: 9 ok, one failed, one unreachable, one timed
// out, two with an unknown outcome.
func fleetDistinctFailures() []Result {
	var rs []Result
	for _, i := range []int{1, 2, 3, 4, 6, 7, 8, 10, 12} {
		rs = append(rs, okRes(hostName("web", i, 2), "active\n"))
	}
	rs = append(rs,
		failRes("web-09", 3, "inactive\n"),
		classRes("web-13", StateFailed, "unreachable"),
		classRes("web-14", StateTimedOut, ""),
		classRes("web-05", StateLost, ""),
		classRes("web-11", StateLost, ""),
	)

	return rs
}

// fleetTimeouts is G-06: three hosts exceed the limit.
func fleetTimeouts() []Result {
	var rs []Result
	for i := 1; i <= 14; i++ {
		host := hostName("web", i, 2)
		if i == 3 || i == 7 || i == 8 {
			rs = append(rs, classRes(host, StateTimedOut, ""))

			continue
		}
		rs = append(rs, okRes(host, "done\n"))
	}

	return rs
}

// fleetDrift is G-07: 11 hosts agree, three print something else.
func fleetDrift() []Result {
	var rs []Result
	for i := 1; i <= 14; i++ {
		host := hostName("web", i, 2)
		switch i {
		case 10, 13:
			rs = append(rs, okRes(host, "nginx version: nginx/1.22.1\n"))
		case 3:
			rs = append(rs, okRes(host, "nginx version: nginx/1.24.0 (Debian)\n"))
		default:
			rs = append(rs, okRes(host, "nginx version: nginx/1.24.0\n"))
		}
	}

	return rs
}

// fleetTimeDependent is G-08: every host prints a different load figure.
func fleetTimeDependent() []Result {
	var rs []Result
	for i := 1; i <= 14; i++ {
		out := fmt.Sprintf("14:02:11 up 41 days,  3:12,  0 users,  load average: 0.%02d, 0.10, 0.09\n", i)
		rs = append(rs, okRes(hostName("web", i, 2), out))
	}

	return rs
}

// fleet500 is G-04: 500 hosts, 480 identical, 12 failures in three kinds, and
// 8 unreachable.
func fleet500() []Result {
	failures := map[int]Result{}
	for _, i := range []int{17, 18, 100, 101, 102} {
		failures[i] = failRes(hostName("app", i, 3), 1, "write error: disk full\n")
	}
	for _, i := range []int{300, 301, 302, 303} {
		failures[i] = failRes(hostName("app", i, 3), 1, "permission denied\n")
	}
	for _, i := range []int{480, 481, 482} {
		failures[i] = failRes(hostName("app", i, 3), 2, "no such file or directory\n")
	}
	for _, i := range []int{50, 51, 52, 200, 201, 202, 350, 351} {
		failures[i] = classRes(hostName("app", i, 3), StateFailed, "unreachable")
	}

	var rs []Result
	for i := 1; i <= 500; i++ {
		if r, bad := failures[i]; bad {
			rs = append(rs, r)

			continue
		}
		rs = append(rs, okRes(hostName("app", i, 3), "ok\n"))
	}

	return rs
}

// variantText builds an output that differs per variant. With repeat 214 it is
// 15 KiB, the size of the controller's window for a chatty command.
func variantText(v, repeat int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "build %03d started\n", v)
	b.WriteString(strings.Repeat("compiling module with a fairly long log line to fill the window 12345\n", repeat))

	return b.String()
}

// fleet5000 is G-05: 5,000 hosts with 15 KiB of output each, 200 different
// outputs among the successes, and four kinds of trouble.
func fleet5000(variants []string) []Result {
	rs := make([]Result, 0, 5000)
	for i := 1; i <= 5000; i++ {
		host := hostName("n", i, 4)
		switch {
		case i%50 == 0:
			rs = append(rs, Result{Host: host, State: StateFailed, ExitCode: intp(1), Stdout: variants[0]})
		case i%97 == 0:
			rs = append(rs, classRes(host, StateTimedOut, ""))
		case i%131 == 0:
			rs = append(rs, classRes(host, StateLost, "supervisor_lost"))
		case i%211 == 0:
			rs = append(rs, classRes(host, StateFailed, "unreachable"))
		default:
			rs = append(rs, okRes(host, variants[i%len(variants)]))
		}
	}

	return rs
}
