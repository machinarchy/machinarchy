package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// RunStatus is the lifecycle state of a run.
type RunStatus string

// Const defines the possible statuses for a run.
const (
	RunQueued             RunStatus = "queued"
	RunRunning            RunStatus = "running"
	RunSucceeded          RunStatus = "succeeded"
	RunPartiallySucceeded RunStatus = "partially_succeeded"
	RunFailed             RunStatus = "failed"
	RunCancelled          RunStatus = "cancelled"
	// RunInterrupted means the process executing the run stopped (shutdown or
	// crash) before every host finished. Whether a command ran on a host that was
	// mid-flight is unknown.
	RunInterrupted RunStatus = "interrupted"
	// RunPlanned is only ever returned for dry runs; it is never stored.
	RunPlanned RunStatus = "planned"
)

// Final reports whether the run can no longer change.
func (s RunStatus) Final() bool {
	switch s {
	case RunSucceeded, RunPartiallySucceeded, RunFailed, RunCancelled, RunInterrupted:
		return true
	}
	return false
}

// RunHostStatus is the outcome of a run on one host.
type RunHostStatus string

// Const defines the possible statuses for a run on a host.
const (
	RunHostPending     RunHostStatus = "pending"
	RunHostRunning     RunHostStatus = "running"
	RunHostSucceeded   RunHostStatus = "succeeded"
	RunHostFailed      RunHostStatus = "failed"
	RunHostTimedOut    RunHostStatus = "timed_out"
	RunHostCancelled   RunHostStatus = "cancelled"
	RunHostInterrupted RunHostStatus = "interrupted"
)

// Final reports whether the host result can no longer change.
func (s RunHostStatus) Final() bool { return s != RunHostPending && s != RunHostRunning }

// Limits that keep a run bounded.
const (
	MaxRunHosts            = 200
	DefaultRunTimeout      = 60 // seconds, per host
	MaxRunTimeout          = 900
	DefaultRunConcurrency  = 5
	MaxRunConcurrency      = 20
	MaxRunArgs             = 64
	MaxRunArgBytes         = 4096
	MaxRunArgvBytes        = 16384
	MaxRunHostOutputBytes  = 64 << 10
	DefaultRunPageSize     = 50
	MaxRunPageSize         = 200
	maxProgramNameLength   = 255
	outputTruncationMarker = "\n[output truncated]"
)

// Errors returned by the run service.
var (
	ErrRunNotFound          = errors.New("run not found")
	ErrRunFinished          = errors.New("the run has already finished")
	ErrExecutionNotAllowed  = errors.New("command execution is not permitted")
	ErrTargetsChanged       = errors.New("the targeted hosts changed since the preview")
	programNamePattern      = regexp.MustCompile(`^[A-Za-z0-9_./+][A-Za-z0-9_./+-]*$`)
	terminalEscapeSequences = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)
)

// TargetsChangedError carries the current target set when it differs from the
// one the caller approved.
type TargetsChangedError struct {
	TargetsHash string
	Total       int
}

// Error implements the error interface.
func (e *TargetsChangedError) Error() string { return ErrTargetsChanged.Error() }

// Is implements errors.Is so callers can check for ErrTargetsChanged.
func (e *TargetsChangedError) Is(target error) bool { return target == ErrTargetsChanged }

// RunRequest asks for a command to be run on a set of hosts.
type RunRequest struct {
	// Argv is the program and its arguments. There is no shell: each element is
	// passed to the program as exactly one argument.
	Argv []string
	Rule TargetRule
	// TimeoutSeconds bounds the command on each host (default 60, maximum 900).
	TimeoutSeconds int
	// Concurrency bounds hosts processed at once (default 5, maximum 20).
	Concurrency int
	// ExpectTargetsHash, from a preview, makes the run fail with
	// ErrTargetsChanged unless it targets exactly the previewed hosts.
	ExpectTargetsHash string
	// DryRun validates and plans without storing or contacting anything.
	DryRun bool
}

// RunCounts summarizes per-host outcomes.
type RunCounts struct {
	Pending     int `json:"pending"`
	Running     int `json:"running"`
	Succeeded   int `json:"succeeded"`
	Failed      int `json:"failed"`
	TimedOut    int `json:"timedOut"`
	Cancelled   int `json:"cancelled"`
	Interrupted int `json:"interrupted"`
}

// Total is the number of hosts counted.
func (c RunCounts) Total() int {
	return c.Pending + c.Running + c.Succeeded + c.Failed + c.TimedOut + c.Cancelled + c.Interrupted
}

// RunHost is the result of a run on one host. Name, address, and port are
// snapshots taken when the run was submitted.
type RunHost struct {
	HostID     string        `json:"hostId"`
	Name       string        `json:"name"`
	Address    string        `json:"address"`
	Port       int           `json:"port"`
	Status     RunHostStatus `json:"status"`
	ExitCode   *int          `json:"exitCode,omitempty"`
	Output     string        `json:"output,omitempty"`
	Error      string        `json:"error,omitempty"`
	StartedAt  *time.Time    `json:"startedAt,omitempty"`
	FinishedAt *time.Time    `json:"finishedAt,omitempty"`
}

// Run is a command executed against a pinned set of hosts.
type Run struct {
	ID                int64      `json:"id"`
	WorkspaceID       int        `json:"-"`
	Status            RunStatus  `json:"status"`
	CreatedByUserID   int        `json:"createdByUserId,omitempty"`
	CreatedByUsername string     `json:"createdByUsername,omitempty"`
	CreatedByTokenID  int        `json:"createdByTokenId,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	StartedAt         *time.Time `json:"startedAt,omitempty"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty"`
	Program           string     `json:"program"`
	ArgCount          int        `json:"argCount"`
	// Argv is only returned to callers allowed to execute; it may contain
	// sensitive values and is stored encrypted.
	Argv           []string   `json:"argv,omitempty"`
	ArgvSHA256     string     `json:"-"`
	TimeoutSeconds int        `json:"timeoutSeconds"`
	Concurrency    int        `json:"concurrency"`
	TargetsHash    string     `json:"targetsHash"`
	Rule           TargetRule `json:"rule"`
	// CancelRequested is set when cancellation was asked for.
	CancelRequested bool      `json:"cancelRequested"`
	Counts          RunCounts `json:"counts"`
	// Hosts is omitted from listings. Output is only included for callers
	// allowed to execute.
	Hosts       []RunHost `json:"hosts,omitempty"`
	HeartbeatAt time.Time `json:"-"`
}

// RunHostUpdate records a host's progress or result.
type RunHostUpdate struct {
	Status     RunHostStatus
	ExitCode   *int
	Output     string
	Error      string
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// RunFilter selects and pages runs, newest first.
type RunFilter struct {
	Status   string
	BeforeID int64
	Limit    int
}

// NormalizeRunFilter applies defaults and bounds.
func NormalizeRunFilter(filter RunFilter) (RunFilter, error) {
	switch {
	case filter.Limit < 0 || filter.BeforeID < 0:
		return filter, fmt.Errorf("%w: limit and before_id must not be negative", ErrInvalidInput)
	case filter.Limit == 0:
		filter.Limit = DefaultRunPageSize
	case filter.Limit > MaxRunPageSize:
		filter.Limit = MaxRunPageSize
	}
	filter.Status = strings.TrimSpace(filter.Status)
	if filter.Status != "" && !RunStatus(filter.Status).valid() {
		return filter, fmt.Errorf("%w: unknown run status %q", ErrInvalidInput, truncateForError(filter.Status))
	}
	return filter, nil
}

func (s RunStatus) valid() bool {
	switch s {
	case RunQueued, RunRunning, RunSucceeded, RunPartiallySucceeded, RunFailed, RunCancelled, RunInterrupted:
		return true
	}
	return false
}

// RunStore persists runs. Every method that changes a run records its audit
// event in the same transaction.
type RunStore interface {
	// CreateRun stores a queued run and its pending hosts (argv is stored
	// encrypted) and sets run.ID, CreatedAt, and HeartbeatAt.
	CreateRun(ctx context.Context, run *Run) error
	// GetRun returns the run with its hosts and decrypted argv, or ErrRunNotFound.
	GetRun(ctx context.Context, workspaceID int, id int64) (*Run, error)
	// ListRuns returns runs without hosts or argv, newest first, with counts.
	ListRuns(ctx context.Context, workspaceID int, filter RunFilter) ([]Run, error)
	// StartRun moves a queued run to running and claims it for owner. It returns
	// ErrRunFinished when the run is no longer queued.
	StartRun(ctx context.Context, id int64, owner string, now time.Time) error
	// Heartbeat renews the run's liveness and reports whether cancellation was requested.
	Heartbeat(ctx context.Context, id int64, now time.Time) (cancelRequested bool, err error)
	// UpdateRunHost records a host's progress or result. A host that already has
	// a final result is left unchanged.
	UpdateRunHost(ctx context.Context, runID int64, hostID string, update RunHostUpdate) error
	// RequestCancel asks the executor to stop. It returns ErrRunNotFound or
	// ErrRunFinished.
	RequestCancel(ctx context.Context, workspaceID int, id int64) error
	// FinishRun sets the final status of a queued or running run. A run that is
	// already final is left unchanged.
	FinishRun(ctx context.Context, id int64, status RunStatus, now time.Time) error
	// InterruptStaleRuns marks queued or running runs whose heartbeat is older
	// than cutoff, and their unfinished hosts, as interrupted.
	InterruptStaleRuns(ctx context.Context, cutoff, now time.Time) (int, error)
}

// RunService submits, inspects, and cancels runs for the caller's workspace.
type RunService interface {
	// Submit validates and starts a run (or plans one when DryRun is set).
	Submit(ctx context.Context, principal Principal, request RunRequest) (*Run, error)
	Get(ctx context.Context, principal Principal, id int64) (*Run, error)
	List(ctx context.Context, principal Principal, filter RunFilter) ([]Run, error)
	// Cancel asks a run to stop. It stops new hosts from starting and closes
	// running SSH sessions; a command already sent may still have run, or keep
	// running, on the host.
	Cancel(ctx context.Context, principal Principal, id int64) (*Run, error)
}

// ExecutionPolicy decides whether commands may run at all and which programs
// are allowed. The zero value allows nothing.
type ExecutionPolicy struct {
	Enabled  bool
	AllowAny bool
	Programs map[string]struct{}
}

// NewExecutionPolicy builds a policy from a program list; "*" allows any
// program. Enabling execution with an empty list is an error so that allowing
// everything is always an explicit choice.
func NewExecutionPolicy(enabled bool, programs []string) (ExecutionPolicy, error) {
	policy := ExecutionPolicy{Enabled: enabled, Programs: make(map[string]struct{})}
	for _, program := range programs {
		program = strings.TrimSpace(program)
		switch {
		case program == "":
			continue
		case program == "*":
			policy.AllowAny = true
		case programNamePattern.MatchString(program) && len(program) <= maxProgramNameLength:
			policy.Programs[program] = struct{}{}
		default:
			return ExecutionPolicy{}, fmt.Errorf("%w: %q is not a valid program name", ErrInvalidInput, truncateForError(program))
		}
	}
	if enabled && !policy.AllowAny && len(policy.Programs) == 0 {
		return ExecutionPolicy{}, fmt.Errorf("%w: execution is enabled but no programs are allowed; list the allowed programs or use * to allow any", ErrInvalidInput)
	}
	return policy, nil
}

// Check reports whether program may run.
func (p ExecutionPolicy) Check(program string) error {
	if !p.Enabled {
		return fmt.Errorf("%w: execution is disabled on this server", ErrExecutionNotAllowed)
	}
	if p.AllowAny {
		return nil
	}
	if _, ok := p.Programs[program]; !ok {
		return fmt.Errorf("%w: program %q is not on the allowed list", ErrExecutionNotAllowed, truncateForError(program))
	}
	return nil
}

// ValidateArgv checks a program and its arguments. The program must be a plain
// name or path, so it can never be shell syntax; arguments may hold any text
// except control characters.
func ValidateArgv(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("%w: a command needs at least a program name", ErrInvalidInput)
	}
	if len(argv) > MaxRunArgs {
		return fmt.Errorf("%w: a command may have at most %d arguments", ErrInvalidInput, MaxRunArgs)
	}
	if len(argv[0]) > maxProgramNameLength || !programNamePattern.MatchString(argv[0]) {
		return fmt.Errorf("%w: the program must be a plain name or path of letters, digits, '.', '_', '+', '-', or '/', not starting with '-'", ErrInvalidInput)
	}
	total := 0
	for _, arg := range argv {
		if len(arg) > MaxRunArgBytes {
			return fmt.Errorf("%w: an argument is longer than %d bytes", ErrInvalidInput, MaxRunArgBytes)
		}
		if !utf8.ValidString(arg) {
			return fmt.Errorf("%w: arguments must be valid UTF-8", ErrInvalidInput)
		}
		for _, r := range arg {
			if unicode.IsControl(r) {
				return fmt.Errorf("%w: arguments must not contain control characters", ErrInvalidInput)
			}
		}
		total += len(arg)
	}
	if total > MaxRunArgvBytes {
		return fmt.Errorf("%w: the command is longer than %d bytes in total", ErrInvalidInput, MaxRunArgvBytes)
	}
	return nil
}

// QuoteArgv renders argv as a command line for a POSIX shell in which every
// element is single-quoted, so the remote shell passes each one to the program
// as exactly one argument with no expansion, globbing, or command substitution.
// It assumes ValidateArgv succeeded and that the account's login shell is
// POSIX-compatible.
func QuoteArgv(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

func writeDigestField(digest hash.Hash, value string) {
	_, _ = digest.Write([]byte(strconv.Itoa(len(value)) + ":" + value + "|"))
}

// ArgvDigest returns a SHA-256 digest of argv that is unambiguous across
// argument boundaries. It is recorded instead of the arguments in audit events.
func ArgvDigest(argv []string) string {
	digest := sha256.New()

	for _, arg := range argv {
		writeDigestField(digest, arg)
	}

	return hex.EncodeToString(digest.Sum(nil))
}

// TargetsHash identifies a set of hosts by their IDs, independent of order. A
// preview returns it and a run can require it, binding the run to what was
// reviewed.
func TargetsHash(hosts []*Host) string {
	ids := make([]string, len(hosts))
	for i, h := range hosts {
		ids[i] = h.ID
	}
	sort.Strings(ids)
	digest := sha256.New()
	for _, id := range ids {
		writeDigestField(digest, id)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// AggregateRunStatus derives a run's final status from its host results.
func AggregateRunStatus(hosts []RunHostStatus, userCancelled, interrupted bool) RunStatus {
	switch {
	case interrupted:
		return RunInterrupted
	case userCancelled:
		return RunCancelled
	}
	succeeded := 0
	for _, status := range hosts {
		switch status {
		case RunHostSucceeded:
			succeeded++
		case RunHostPending, RunHostRunning:
			return RunRunning
		}
	}
	switch {
	case succeeded == len(hosts) && len(hosts) > 0:
		return RunSucceeded
	case succeeded == 0:
		return RunFailed
	default:
		return RunPartiallySucceeded
	}
}

// SanitizeOutput makes untrusted remote output safe to store and return:
// terminal escape sequences and control characters (other than newline and
// tab) are removed, invalid UTF-8 is replaced, and the result is cut at maxBytes
// on a character boundary with a marker.
func SanitizeOutput(output string, maxBytes int) string {
	output = terminalEscapeSequences.ReplaceAllString(output, "")
	output = strings.ToValidUTF8(output, "\uFFFD")
	output = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, output)
	if len(output) <= maxBytes {
		return output
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(output[cut]) {
		cut--
	}
	return output[:cut] + outputTruncationMarker
}
