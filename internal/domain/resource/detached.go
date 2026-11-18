package resource

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Detached execution (AUDIT/10b §1): a short SSH session starts the command
// under a supervisor on the host; later short sessions read its state and
// output. These types are what the controller keeps per target.

const (
	// MaxAttachedTimeoutSeconds bounds a run that keeps its SSH session open.
	MaxAttachedTimeoutSeconds = 900
	// MaxDetachedTimeoutSeconds bounds a detached run (24 h).
	MaxDetachedTimeoutSeconds = 24 * 60 * 60
	// CaptureHeadBytes and CaptureTailBytes are kept of each output stream:
	// the first and the last bytes. Both streams together stay within
	// MaxTargetOutputBytes.
	CaptureHeadBytes = 15 << 10
	CaptureTailBytes = 15 << 10
)

// TargetLost is terminal: the command's outcome is unknown (the supervisor
// vanished, or the host stayed unreachable past the deadline). It is never
// retried automatically.
const TargetLost TargetState = "Lost"

// ExecutionSupervisor chooses how a profile's detached runs are supervised.
type ExecutionSupervisor string

const (
	SupervisorAuto     ExecutionSupervisor = "auto"
	SupervisorSystemd  ExecutionSupervisor = "systemd"
	SupervisorSetsid   ExecutionSupervisor = "setsid"
	SupervisorAttached ExecutionSupervisor = "attached" // no detached runs
)

// ExecutionSettings are an Access Profile's detached-execution options.
type ExecutionSettings struct {
	// Supervisor: auto (default; systemd-run when usable, else setsid),
	// systemd, setsid, or attached (detached runs are refused).
	Supervisor ExecutionSupervisor `json:"supervisor,omitempty"`
	// StateRoot holds per-target directories on the host; the default is
	// $HOME/.local/state/machinarchy of the login user.
	StateRoot string `json:"stateRoot,omitempty"`
	// MaxOutputFileBytes caps each output file on the host (ulimit -f);
	// 0 means 100 MiB.
	MaxOutputFileBytes int64 `json:"maxOutputFileBytes,omitempty"`
}

var stateRootPattern = regexp.MustCompile(`^/[A-Za-z0-9._/-]{0,255}$`)

func (e *ExecutionSettings) validate() error {
	if e == nil {
		return nil
	}
	switch e.Supervisor {
	case "", SupervisorAuto, SupervisorSystemd, SupervisorSetsid, SupervisorAttached:
	default:
		return fmt.Errorf("%w: execution supervisor must be auto, systemd, setsid or attached", ErrInvalidResource)
	}
	if e.StateRoot != "" && (!stateRootPattern.MatchString(e.StateRoot) || strings.Contains(e.StateRoot, "..")) {
		return fmt.Errorf("%w: execution state root must be an absolute path of [A-Za-z0-9._/-] without \"..\"", ErrInvalidResource)
	}
	if e.MaxOutputFileBytes != 0 && (e.MaxOutputFileBytes < 1<<20 || e.MaxOutputFileBytes > 100<<30) {
		return fmt.Errorf("%w: execution output file cap must be 1 MiB..100 GiB", ErrInvalidResource)
	}
	return nil
}

// LaunchHandle is how the controller finds a detached target again.
type LaunchHandle struct {
	// Mode is systemd or setsid; empty means the launch reply never arrived
	// (the connection failed mid-launch), so the poller must find out
	// whether the command started.
	Mode string `json:"mode,omitempty"`
	Unit string `json:"unit,omitempty"`
	PID  int    `json:"pid,omitempty"`
	// Root is the state root used at launch ("" = the default).
	Root string `json:"root,omitempty"`
	// Endpoint is the Host endpoint at launch; a poll of a moved Host is
	// treated as unreachable rather than sent to another machine.
	Endpoint   string    `json:"endpoint"`
	LaunchedAt time.Time `json:"launchedAt"`
	// Attempt counts launches of this target (1 for the first). A launch
	// proven never to have started is retried up to MaxLaunchAttempts.
	Attempt int `json:"attempt,omitempty"`
	// Deadline is when the command must have finished: launch time plus
	// the run's timeout, or the Operation deadline if earlier.
	Deadline time.Time `json:"deadline"`
}

// MaxLaunchAttempts bounds relaunches of a target whose launch was cut off
// before it started anything.
const MaxLaunchAttempts = 3

// Confirmed reports whether the runner acknowledged the launch.
func (h LaunchHandle) Confirmed() bool { return h.Mode != "" }

// StreamCapture keeps the head and the tail of one output stream as the
// poller reads it in chunks.
type StreamCapture struct {
	Head []byte `json:"head,omitempty"`
	Tail []byte `json:"tail,omitempty"`
	// Next is the offset of the next byte to read; Tail ends there.
	Next int64 `json:"next"`
	// Size is the stream's size on the host at the last poll.
	Size int64 `json:"size"`
}

// Apply adds the bytes [from, from+len(data)) of a stream whose current
// size is size. A chunk past Next means bytes were skipped.
func (c *StreamCapture) Apply(from, size int64, data []byte) {
	if size > c.Size {
		c.Size = size
	}
	end := from + int64(len(data))
	if end <= c.Next {
		return // already seen
	}
	if from < c.Next {
		data = data[c.Next-from:]
		from = c.Next
	}
	if from == c.Next && int64(len(c.Head)) == from && len(c.Head) < CaptureHeadBytes {
		take := min(CaptureHeadBytes-len(c.Head), len(data))
		c.Head = append(c.Head, data[:take]...)
	}
	if from > c.Next {
		c.Tail = nil // a gap: the old tail is no longer contiguous
	}
	c.Tail = append(c.Tail, data...)
	if len(c.Tail) > CaptureTailBytes {
		c.Tail = append([]byte(nil), c.Tail[len(c.Tail)-CaptureTailBytes:]...)
	}
	c.Next = end
}

// NextFrom is the offset to ask for next. The runner itself skips ahead
// when a poll past the head has fallen far behind.
func (c *StreamCapture) NextFrom() int64 { return c.Next }

// CaughtUp reports whether every byte up to the last known size was read.
func (c *StreamCapture) CaughtUp() bool { return c.Next >= c.Size }

// Render returns the captured output: everything if nothing was skipped,
// otherwise head, an omission marker and tail. omitted counts bytes that
// were never read.
func (c *StreamCapture) Render() (text string, omitted int64) {
	tailStart := c.Next - int64(len(c.Tail))
	head := int64(len(c.Head))
	if tailStart <= head {
		joined := append(append([]byte(nil), c.Head...), c.Tail[head-tailStart:]...)
		return string(joined) + c.unread(), max(c.Size-c.Next, 0)
	}
	omitted = tailStart - head
	return string(c.Head) + fmt.Sprintf("\n[… %d bytes omitted …]\n", omitted) + string(c.Tail) + c.unread(), omitted + max(c.Size-c.Next, 0)
}

func (c *StreamCapture) unread() string {
	if c.Size > c.Next {
		return fmt.Sprintf("\n[… %d bytes not read …]", c.Size-c.Next)
	}
	return ""
}

// LiveOutput is a running detached target's progress.
type LiveOutput struct {
	Stdout StreamCapture `json:"stdout"`
	Stderr StreamCapture `json:"stderr"`
	// LastContactAt is the last successful poll; nil before the first.
	LastContactAt *time.Time `json:"lastContactAt,omitempty"`
	// CancelSentAt is when the poller asked the host to stop the command
	// (an Operation cancel or a timeout).
	CancelSentAt *time.Time `json:"cancelSentAt,omitempty"`
	// CancelReason is "cancelled" or "timeout".
	CancelReason string `json:"cancelReason,omitempty"`
	Polls        int    `json:"polls"`
}

// DetachedTarget is a running detached target, as the poller lists it.
type DetachedTarget struct {
	WorkspaceID           int
	OperationID           ID
	HostID                ID
	HostGeneration        uint64
	AccessProfileID       ID
	AccessProfileRevision uint64
	OperationState        OperationState
	Launch                LaunchHandle
	Live                  LiveOutput
}

// Result turns a finished detached target's capture into its stored result.
func (l *LiveOutput) Result(exitCode *int, errorClass string) *TargetResult {
	stdout, stdoutOmitted := l.Stdout.Render()
	stderr, stderrOmitted := l.Stderr.Render()
	return &TargetResult{ExitCode: exitCode, Stdout: stdout, Stderr: stderr, ErrorClass: errorClass,
		StdoutBytes: l.Stdout.Size, StderrBytes: l.Stderr.Size, Truncated: stdoutOmitted > 0 || stderrOmitted > 0}
}

// TargetProgressSummary is what any Operation reader may see of a running
// detached target: no output, only sizes and timing.
type TargetProgressSummary struct {
	StdoutBytes   int64      `json:"stdoutBytes"`
	StderrBytes   int64      `json:"stderrBytes"`
	LastContactAt *time.Time `json:"lastContactAt,omitempty"`
	Polls         int        `json:"polls"`
	CancelSentAt  *time.Time `json:"cancelSentAt,omitempty"`
	CancelReason  string     `json:"cancelReason,omitempty"`
}

// Summary returns the progress summary of l.
func (l *LiveOutput) Summary() *TargetProgressSummary {
	return &TargetProgressSummary{StdoutBytes: l.Stdout.Size, StderrBytes: l.Stderr.Size, LastContactAt: l.LastContactAt,
		Polls: l.Polls, CancelSentAt: l.CancelSentAt, CancelReason: l.CancelReason}
}

// OutputStream names a target output stream.
type OutputStream string

const (
	StreamStdout OutputStream = "stdout"
	StreamStderr OutputStream = "stderr"
)

// TargetOutput is one stream of one target's output, live while the target
// runs detached, final afterwards.
type TargetOutput struct {
	OperationID ID           `json:"operationId"`
	HostID      ID           `json:"hostId"`
	Stream      OutputStream `json:"stream"`
	State       TargetState  `json:"state"`
	// Live is true while the command runs and Text can still grow.
	Live bool   `json:"live"`
	Text string `json:"text"`
	// Bytes is the stream's full size; Truncated means Text omits part of it
	// (a head and a tail are kept).
	Bytes     int64 `json:"bytes"`
	Truncated bool  `json:"truncated"`
}

// OutputOf returns one stream of target's output.
func (target OperationTarget) OutputOf(operationID ID, stream OutputStream) TargetOutput {
	out := TargetOutput{OperationID: operationID, HostID: target.Spec.HostID, Stream: stream, State: target.State}
	switch {
	case target.Live != nil && target.State == TargetRunning:
		capture := &target.Live.Stdout
		if stream == StreamStderr {
			capture = &target.Live.Stderr
		}
		text, omitted := capture.Render()
		out.Live, out.Text, out.Bytes, out.Truncated = true, sanitizeOutput(text, MaxTargetOutputBytes), capture.Size, omitted > 0
	case target.Result != nil:
		result := target.Result
		out.Text, out.Bytes, out.Truncated = result.Stdout, result.StdoutBytes, result.Truncated
		if stream == StreamStderr {
			out.Text, out.Bytes = result.Stderr, result.StderrBytes
		} else if result.Stdout == "" && result.Output != "" {
			out.Text = result.Output // an older combined result
		}
		if out.Bytes == 0 {
			out.Bytes = int64(len(out.Text))
		}
	case target.State == TargetRunning:
		out.Live = true // attached, or not polled yet
	}
	return out
}
