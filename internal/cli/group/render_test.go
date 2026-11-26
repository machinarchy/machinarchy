package group

import (
	"bytes"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/ui"
)

// --no-groups: one block per host, in natural order, sanitised, with the same
// first line and legend as the grouped result.
func TestRenderPerHost(t *testing.T) {
	t.Parallel()

	three, zero := 3, 0
	results := []Result{
		{Host: "web-10", State: StateSucceeded, ExitCode: &zero, Stdout: "ok\n"},
		{Host: "web-02", State: StateFailed, ExitCode: &three, Stdout: "inactive \x1b[31mred\x1b[0m\n", Stderr: "boom\n"},
		{Host: "web-01", State: StateSucceeded, ExitCode: &zero, Stdout: "ok\n"},
		{Host: "web-03", State: StateTimedOut},
		{Host: "web-02", State: StateSucceeded}, // a repeat is ignored
	}

	var out bytes.Buffer
	if err := RenderPerHost(&out, results, RenderOptions{Style: ui.Style{Plain: true}, Width: 80, RunID: "r-4c2d9e10", TimeoutLabel: "60 s"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()

	if !strings.Contains(text, "2 of 4 hosts succeeded, 2 did not") {
		t.Errorf("first line:\n%s", text)
	}
	order := []string{"web-01", "web-02", "web-03", "web-10"}
	last := -1
	for _, host := range order {
		at := strings.Index(text, host+"  ")
		if at < 0 || at < last {
			t.Fatalf("hosts are not one block each in natural order (%s):\n%s", host, text)
		}
		last = at
	}
	for _, want := range []string{"exit 3", "inactive red", "stderr: boom", "timed out after 60 s", "may still have run", "machinarchy run r-4c2d9e10 --retry-failed"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\x1b") {
		t.Errorf("control bytes reached the output:\n%q", text)
	}

	out.Reset()
	if err := RenderPerHost(&out, nil, RenderOptions{}); err != nil || out.String() != "No hosts.\n" {
		t.Errorf("empty = %q, %v", out.String(), err)
	}
}
