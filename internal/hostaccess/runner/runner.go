// Package runner builds the remote commands of the detached-execution runner
// (AUDIT/10b §1.2) and parses its replies. The runner is a POSIX sh script,
// embedded here and sent with every SSH session, so nothing has to be
// installed on a host.
package runner

import (
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/machinarchy/machinarchy/internal/domain"
)

//go:embed runner.sh
var script string

// Version is the runner protocol. A reply from another version is refused.
const Version = 1

const (
	// ChunkBytes is the most each stream returns per status poll.
	ChunkBytes = 32 << 10
	// HeadBytes is the prefix of each stream that is always read in full;
	// past it, a poll that has fallen behind skips to the last ChunkBytes.
	HeadBytes = 15 << 10 // = resource.CaptureHeadBytes
	// MaxTimeoutSeconds bounds a detached run (24 h).
	MaxTimeoutSeconds = 24 * 60 * 60
	// DefaultMaxFileBytes caps each output file on the host (ulimit -f).
	DefaultMaxFileBytes = 100 << 20
	// DefaultRetentionDays is how long a leftover directory stays on a host.
	DefaultRetentionDays = 7
	// MaxRefsPerCall bounds one status, cancel or cleanup session, so a
	// reply (2 × ChunkBytes of base64 per target) stays below the SSH
	// client's 512 KiB per-stream cap.
	MaxRefsPerCall = 4
	protocolPrefix = "@@MR1 "
	argv0          = "machinarchy-runner"
)

// Mode chooses the host-side supervisor.
type Mode string

const (
	ModeAuto    Mode = "auto"
	ModeSystemd Mode = "systemd"
	ModeSetsid  Mode = "setsid"
)

var (
	idPattern   = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
	rootPattern = regexp.MustCompile(`^/[A-Za-z0-9._/-]{0,255}$`)
	// ErrProtocol reports a reply the runner protocol does not allow.
	ErrProtocol = errors.New("runner: malformed reply")
)

// Ref names one target directory on the host.
type Ref struct {
	OperationID string
	TargetID    string
}

func (r Ref) String() string { return r.OperationID + "/" + r.TargetID }

func (r Ref) validate() error {
	if !idPattern.MatchString(r.OperationID) || !idPattern.MatchString(r.TargetID) {
		return fmt.Errorf("%w: runner identifiers must match [a-z0-9-]{1,64}", domain.ErrInvalidInput)
	}
	return nil
}

// ValidateRoot accepts "" (the default under the login user's home) or an
// absolute path without "..".
func ValidateRoot(root string) error {
	if root != "" && (!rootPattern.MatchString(root) || strings.Contains(root, "..")) {
		return fmt.Errorf("%w: execution state root must be an absolute path of [A-Za-z0-9._/-] without \"..\"", domain.ErrInvalidInput)
	}
	return nil
}

func command(args ...string) string {
	return domain.QuoteArgv(append([]string{"sh", "-c", script, argv0}, args...))
}

// LaunchRequest starts one target.
type LaunchRequest struct {
	Root           string
	Ref            Ref
	TimeoutSeconds int
	MaxFileBytes   int64
	RetentionDays  int
	Mode           Mode
	// Argv is the command; it reaches the host only through domain.QuoteArgv.
	Argv []string
}

// LaunchCommand returns the remote command for req.
func LaunchCommand(req LaunchRequest) (string, error) {
	if err := req.Ref.validate(); err != nil {
		return "", err
	}
	if err := ValidateRoot(req.Root); err != nil {
		return "", err
	}
	if err := domain.ValidateArgv(req.Argv); err != nil {
		return "", err
	}
	if req.TimeoutSeconds < 1 || req.TimeoutSeconds > MaxTimeoutSeconds {
		return "", fmt.Errorf("%w: detached timeout must be 1..%d seconds", domain.ErrInvalidInput, MaxTimeoutSeconds)
	}
	if req.MaxFileBytes == 0 {
		req.MaxFileBytes = DefaultMaxFileBytes
	}
	if req.MaxFileBytes < 1<<20 || req.MaxFileBytes > 100<<30 {
		return "", fmt.Errorf("%w: output file cap must be 1 MiB..100 GiB", domain.ErrInvalidInput)
	}
	if req.RetentionDays == 0 {
		req.RetentionDays = DefaultRetentionDays
	}
	if req.RetentionDays < 1 || req.RetentionDays > 365 {
		return "", fmt.Errorf("%w: retention must be 1..365 days", domain.ErrInvalidInput)
	}
	switch req.Mode {
	case "":
		req.Mode = ModeAuto
	case ModeAuto, ModeSystemd, ModeSetsid:
	default:
		return "", fmt.Errorf("%w: unknown supervisor mode %q", domain.ErrInvalidInput, req.Mode)
	}
	return command("launch", req.Root, req.Ref.OperationID, req.Ref.TargetID, strconv.Itoa(req.TimeoutSeconds),
		strconv.FormatInt(req.MaxFileBytes, 10), string(req.Mode), strconv.Itoa(req.RetentionDays), domain.QuoteArgv(req.Argv)), nil
}

// Launched is the runner's launch reply.
type Launched struct {
	Mode     Mode
	PID      int
	Unit     string
	Existing bool // the target was already launched; this call adopted it
}

// ParseLaunch reads a launch reply.
func ParseLaunch(output string) (Launched, error) {
	lines, err := protocolLines(output)
	if err != nil {
		return Launched{}, err
	}
	for _, fields := range lines {
		if fields[0] != "launched" {
			continue
		}
		if len(fields) != 5 {
			return Launched{}, fmt.Errorf("%w: launch reply", ErrProtocol)
		}
		launched := Launched{Mode: Mode(fields[1]), Unit: dash(fields[3]), Existing: fields[4] == "existing"}
		if launched.Mode != ModeSystemd && launched.Mode != ModeSetsid {
			return Launched{}, fmt.Errorf("%w: launch mode %q", ErrProtocol, fields[1])
		}
		if fields[2] != "-" {
			if launched.PID, err = strconv.Atoi(fields[2]); err != nil || launched.PID < 1 {
				return Launched{}, fmt.Errorf("%w: launch pid", ErrProtocol)
			}
		}
		return launched, nil
	}
	return Launched{}, fmt.Errorf("%w: no launch reply", ErrProtocol)
}

// Query asks for one target's state and the output from the given offsets.
type Query struct {
	Ref        Ref
	StdoutFrom int64
	StderrFrom int64
}

// StatusCommand returns the remote command for up to MaxRefsPerCall queries.
func StatusCommand(root string, queries []Query) (string, error) {
	if err := ValidateRoot(root); err != nil {
		return "", err
	}
	if len(queries) == 0 || len(queries) > MaxRefsPerCall {
		return "", fmt.Errorf("%w: 1..%d status queries per call", domain.ErrInvalidInput, MaxRefsPerCall)
	}
	args := []string{"status", root, strconv.Itoa(ChunkBytes), strconv.Itoa(HeadBytes)}
	for _, q := range queries {
		if err := q.Ref.validate(); err != nil {
			return "", err
		}
		if q.StdoutFrom < 0 || q.StderrFrom < 0 {
			return "", fmt.Errorf("%w: negative output offset", domain.ErrInvalidInput)
		}
		args = append(args, fmt.Sprintf("%s:%d:%d", q.Ref, q.StdoutFrom, q.StderrFrom))
	}
	return command(args...), nil
}

// State is a target's state on the host.
type State string

const (
	StateRunning State = "running"
	StateExited  State = "exited"
	// StateLost: no exit status and the supervisor is gone (a reboot, the
	// OOM killer). The outcome is unknown.
	StateLost State = "lost"
	// StateMissing: the directory does not exist (never launched, or
	// already cleaned up).
	StateMissing State = "missing"
	// StateUnsupported: written by a runner version this build does not know.
	StateUnsupported State = "unsupported"
)

// Chunk is a slice of one output stream: Data are the bytes [From, From+len).
type Chunk struct {
	Size int64
	From int64
	Data []byte
}

// Status is the runner's reply for one target.
type Status struct {
	Ref       Ref
	State     State
	ExitCode  *int
	TimedOut  bool
	Cancelled bool
	Stdout    Chunk
	Stderr    Chunk
}

// ParseStatus reads a status reply.
func ParseStatus(output string) ([]Status, error) {
	lines, err := protocolLines(output)
	if err != nil {
		return nil, err
	}
	var statuses []Status
	for _, fields := range lines {
		if fields[0] != "status" {
			continue
		}
		if len(fields) != 11 {
			return nil, fmt.Errorf("%w: status reply has %d fields", ErrProtocol, len(fields))
		}
		op, target, ok := strings.Cut(fields[1], "/")
		status := Status{Ref: Ref{OperationID: op, TargetID: target}, State: State(fields[2])}
		if !ok || status.Ref.validate() != nil {
			return nil, fmt.Errorf("%w: status reference", ErrProtocol)
		}
		switch status.State {
		case StateRunning, StateExited, StateLost, StateMissing, StateUnsupported:
		default:
			return nil, fmt.Errorf("%w: status state %q", ErrProtocol, fields[2])
		}
		if fields[3] != "-" {
			code, err := strconv.Atoi(fields[3])
			if err != nil || code < 0 || code > 255 {
				return nil, fmt.Errorf("%w: exit status", ErrProtocol)
			}
			status.ExitCode = &code
		}
		if status.State == StateExited && status.ExitCode == nil {
			return nil, fmt.Errorf("%w: exited without a status", ErrProtocol)
		}
		for _, flag := range strings.Split(dash(fields[4]), ",") {
			switch flag {
			case "timedout":
				status.TimedOut = true
			case "cancelled":
				status.Cancelled = true
			case "":
			default:
				return nil, fmt.Errorf("%w: flag %q", ErrProtocol, flag)
			}
		}
		if status.Stdout, err = parseChunk(fields[5:8]); err != nil {
			return nil, err
		}
		if status.Stderr, err = parseChunk(fields[8:11]); err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func parseChunk(fields []string) (Chunk, error) {
	size, err1 := strconv.ParseInt(fields[0], 10, 64)
	from, err2 := strconv.ParseInt(fields[1], 10, 64)
	if err1 != nil || err2 != nil || size < 0 || from < 0 || from > size {
		return Chunk{}, fmt.Errorf("%w: output offsets", ErrProtocol)
	}
	chunk := Chunk{Size: size, From: from}
	if fields[2] != "-" {
		data, err := base64.StdEncoding.DecodeString(fields[2])
		if err != nil || len(data) > ChunkBytes || from+int64(len(data)) > size {
			return Chunk{}, fmt.Errorf("%w: output data", ErrProtocol)
		}
		chunk.Data = data
	}
	return chunk, nil
}

// CancelCommand returns the remote command that stops targets: TERM to the
// command's process group at once and KILL after 10 seconds.
func CancelCommand(root string, refs []Ref) (string, error) {
	return refsCommand("cancel", root, refs)
}

// CleanupCommand returns the remote command that removes finished or lost
// target directories; a running target is left alone ("busy").
func CleanupCommand(root string, refs []Ref) (string, error) {
	return refsCommand("cleanup", root, refs)
}

func refsCommand(action, root string, refs []Ref) (string, error) {
	if err := ValidateRoot(root); err != nil {
		return "", err
	}
	if len(refs) == 0 || len(refs) > MaxRefsPerCall {
		return "", fmt.Errorf("%w: 1..%d targets per call", domain.ErrInvalidInput, MaxRefsPerCall)
	}
	args := []string{action, root}
	for _, ref := range refs {
		if err := ref.validate(); err != nil {
			return "", err
		}
		args = append(args, ref.String())
	}
	return command(args...), nil
}

// ParseAcks reads cancel or cleanup replies: ref -> "sent", "done" or "busy".
func ParseAcks(output, action string) (map[Ref]string, error) {
	lines, err := protocolLines(output)
	if err != nil {
		return nil, err
	}
	acks := map[Ref]string{}
	for _, fields := range lines {
		if fields[0] != action {
			continue
		}
		op, target, ok := strings.Cut(fields[1], "/")
		if len(fields) != 3 || !ok {
			return nil, fmt.Errorf("%w: %s reply", ErrProtocol, action)
		}
		acks[Ref{OperationID: op, TargetID: target}] = fields[2]
	}
	return acks, nil
}

// RefusedError is an error the runner reported ("@@MR1 error CODE").
type RefusedError struct{ Code string }

func (e *RefusedError) Error() string { return "runner refused the request: " + e.Code }

func protocolLines(output string) ([][]string, error) {
	var lines [][]string
	for _, line := range strings.Split(output, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), protocolPrefix)
		if !ok {
			continue // output of the login shell's start-up files
		}
		fields := strings.Fields(rest)
		if len(fields) < 2 {
			return nil, fmt.Errorf("%w: short line", ErrProtocol)
		}
		if fields[0] == "error" {
			return nil, &RefusedError{Code: fields[1]}
		}
		lines = append(lines, fields)
	}
	return lines, nil
}

func dash(value string) string {
	if value == "-" {
		return ""
	}
	return value
}
