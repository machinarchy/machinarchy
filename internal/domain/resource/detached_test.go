package resource

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// stream returns n bytes of distinguishable text.
func stream(n int) []byte {
	var b bytes.Buffer
	for i := 0; b.Len() < n; i++ {
		b.WriteString(strings.Repeat(string(rune('a'+i%26)), 99) + "\n")
	}
	return b.Bytes()[:n]
}

func TestStreamCaptureKeepsEverythingWhenItFits(t *testing.T) {
	data := stream(CaptureHeadBytes + CaptureTailBytes - 10)
	var c StreamCapture
	// Read in uneven chunks, with one chunk repeated (a retried poll).
	for from := 0; from < len(data); {
		end := min(from+7000, len(data))
		c.Apply(int64(from), int64(len(data)), data[from:end])
		c.Apply(int64(from), int64(len(data)), data[from:end])
		from = end
	}
	text, omitted := c.Render()
	if text != string(data) || omitted != 0 || !c.CaughtUp() {
		t.Fatalf("render: %d bytes, omitted %d, want all %d", len(text), omitted, len(data))
	}
}

func TestStreamCaptureHeadAndTailAcrossAGap(t *testing.T) {
	data := stream(200_000)
	var c StreamCapture
	c.Apply(0, int64(len(data)), data[:32<<10])
	// The runner skipped ahead to the last 32 KiB.
	from := len(data) - 32<<10
	c.Apply(int64(from), int64(len(data)), data[from:])
	text, omitted := c.Render()
	head, tail := string(data[:CaptureHeadBytes]), string(data[len(data)-CaptureTailBytes:])
	if !strings.HasPrefix(text, head) || !strings.HasSuffix(text, tail) {
		t.Fatal("render does not start with the head and end with the tail")
	}
	if want := int64(len(data) - CaptureHeadBytes - CaptureTailBytes); omitted != want || !strings.Contains(text, "bytes omitted") {
		t.Fatalf("omitted = %d, want %d", omitted, want)
	}
	if len(text) > CaptureHeadBytes+CaptureTailBytes+64 {
		t.Fatalf("render is %d bytes", len(text))
	}
}

func TestStreamCaptureReportsUnreadBytes(t *testing.T) {
	var c StreamCapture
	c.Apply(0, 100, []byte("0123456789"))
	if c.CaughtUp() {
		t.Fatal("90 bytes are still unread")
	}
	text, omitted := c.Render()
	if omitted != 90 || !strings.Contains(text, "90 bytes not read") {
		t.Fatalf("render = %q, omitted %d", text, omitted)
	}
}

func TestLiveResultFitsTheOutputBudget(t *testing.T) {
	var live LiveOutput
	big := stream(500_000)
	live.Stdout.Apply(0, int64(len(big)), big[:32<<10])
	live.Stdout.Apply(int64(len(big)-32<<10), int64(len(big)), big[len(big)-32<<10:])
	live.Stderr.Apply(0, int64(len(big)), big[:32<<10])
	live.Stderr.Apply(int64(len(big)-32<<10), int64(len(big)), big[len(big)-32<<10:])
	code := 0
	result := NormalizeTargetResult(live.Result(&code, ""))
	if !result.Truncated || result.StdoutBytes != int64(len(big)) {
		t.Fatalf("result = truncated %v, %d bytes", result.Truncated, result.StdoutBytes)
	}
	if !strings.HasSuffix(result.Stderr, string(big[len(big)-CaptureTailBytes:])) {
		t.Fatal("stderr's tail was cut by the output budget")
	}
}

func TestSanitizeOutputKeepsLinesAndDropsEscapes(t *testing.T) {
	got := sanitizeOutput("line1\n\tline2\x1b[31mred\x00\n", 1000)
	if got != "line1\n\tline2 [31mred \n" {
		t.Fatalf("sanitized = %q", got)
	}
	cut := sanitizeOutput("ab€", 3) // the euro sign is 3 bytes
	if !utf8.ValidString(cut) || cut != "ab" {
		t.Fatalf("cut = %q", cut)
	}
	if got := sanitizeOutput("bad\xffbyte", 100); !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8 kept: %q", got)
	}
}

func detachedOperation(t *testing.T, timeout int, detach bool) error {
	t.Helper()
	host, _ := NewResourceID()
	_, err := NewRunOperation(1, ActorRef{UserID: 1}, OperationSpec{
		Targets: []OperationTargetSpec{{HostID: host, HostGeneration: 1, DisplayName: "web"}},
		Run:     &RunSpec{Argv: []string{"true"}, TimeoutSeconds: timeout, Concurrency: 1, Detach: detach},
	}, time.Now())
	return err
}

func TestDetachedTimeoutBounds(t *testing.T) {
	if err := detachedOperation(t, MaxAttachedTimeoutSeconds+1, false); !errors.Is(err, ErrInvalidResource) {
		t.Fatalf("attached run over 15 minutes = %v", err)
	}
	if err := detachedOperation(t, MaxDetachedTimeoutSeconds, true); err != nil {
		t.Fatalf("detached 24 h run = %v", err)
	}
	if err := detachedOperation(t, MaxDetachedTimeoutSeconds+1, true); !errors.Is(err, ErrInvalidResource) {
		t.Fatalf("detached run over 24 h = %v", err)
	}
}

func TestLiveOutputOnlyWhileRunning(t *testing.T) {
	host, _ := NewResourceID()
	op, err := NewRunOperation(1, ActorRef{UserID: 1}, OperationSpec{
		Targets: []OperationTargetSpec{{HostID: host, HostGeneration: 1, DisplayName: "web"}},
		Run:     &RunSpec{Argv: []string{"true"}, TimeoutSeconds: 60, Concurrency: 1, Detach: true},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	op.Status.Targets[0].Live = &LiveOutput{}
	if err := op.Validate(); !errors.Is(err, ErrInvalidResource) {
		t.Fatalf("pending target with live output = %v", err)
	}
	op.Status.Targets[0].Live = nil
	op.Status.Targets[0].Launch = &LaunchHandle{Mode: "setsid"}
	if err := op.Validate(); !errors.Is(err, ErrInvalidResource) {
		t.Fatalf("pending target with a launch = %v", err)
	}
}

func TestExecutionSettingsValidation(t *testing.T) {
	for _, bad := range []ExecutionSettings{
		{Supervisor: "tmux"},
		{StateRoot: "relative"},
		{StateRoot: "/a/../b"},
		{StateRoot: "/a b"},
		{MaxOutputFileBytes: 10},
	} {
		if err := bad.validate(); !errors.Is(err, ErrInvalidResource) {
			t.Errorf("%+v accepted", bad)
		}
	}
	good := ExecutionSettings{Supervisor: SupervisorSystemd, StateRoot: "/var/lib/machinarchy", MaxOutputFileBytes: 1 << 30}
	if err := good.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOutputOfByOffset(t *testing.T) {
	op, _ := NewResourceID()
	target := OperationTarget{State: TargetRunning, Launch: &LaunchHandle{Mode: "setsid"}, Live: &LiveOutput{}}
	target.Live.Stdout.Apply(0, 6, []byte("hello\n"))
	target.Live.Stderr.Apply(0, 4, []byte("\x1b[1m"))
	out := target.OutputOf(op, StreamStdout, 0)
	if !out.Live || out.Text != "hello\n" || out.Next != 6 || out.Bytes != 6 || out.Truncated {
		t.Fatalf("first read = %+v", out)
	}
	if errOut := target.OutputOf(op, StreamStderr, 0); strings.Contains(errOut.Text, "\x1b") {
		t.Fatalf("an escape sequence reached the output view: %q", errOut.Text)
	}
	// Following: only what is new since the last offset.
	target.Live.Stdout.Apply(6, 12, []byte("world\n"))
	if out := target.OutputOf(op, StreamStdout, out.Next); out.Text != "world\n" || out.From != 6 || out.Next != 12 {
		t.Fatalf("follow read = %+v", out)
	}
	// A follower that fell behind a skipped middle part gets one marker line.
	big := stream(200_000)
	long := OperationTarget{State: TargetSucceeded, Launch: &LaunchHandle{Mode: "setsid"}, Live: &LiveOutput{}}
	long.Live.Stdout.Apply(0, int64(len(big)), big[:20_000])
	long.Live.Stdout.Apply(int64(len(big)-32<<10), int64(len(big)), big[len(big)-32<<10:])
	out = long.OutputOf(op, StreamStdout, 20_000)
	tail := string(big[len(big)-CaptureTailBytes:])
	if out.Live || !strings.HasPrefix(out.Text, "\n[… ") || !strings.HasSuffix(out.Text, tail) || out.Next != int64(len(big)) || !out.Truncated {
		t.Fatalf("read across the gap: next %d, truncated %v, starts %q", out.Next, out.Truncated, out.Text[:min(40, len(out.Text))])
	}
	if out := long.OutputOf(op, StreamStdout, out.Next); out.Text != "" || out.Next != int64(len(big)) {
		t.Fatalf("read at the end = %+v", out)
	}
	// An attached run's result is served by offset into its text.
	code := 0
	attached := OperationTarget{State: TargetSucceeded, Result: &TargetResult{ExitCode: &code, Stdout: "abc\n", Stderr: "e\n"}}
	if out := attached.OutputOf(op, StreamStdout, 2); out.Text != "c\n" || out.Next != 4 || out.Bytes != 4 {
		t.Fatalf("attached read = %+v", out)
	}
	legacy := OperationTarget{State: TargetSucceeded, Result: &TargetResult{Output: "combined"}}
	if out := legacy.OutputOf(op, StreamStdout, 0); out.Text != "combined" || out.Bytes != 8 {
		t.Fatalf("legacy output = %+v", out)
	}
}
