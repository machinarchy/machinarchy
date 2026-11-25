package resource

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/machinarchy/machinarchy/internal/domain"
)

type OperationKind string

const (
	// OperationKindRun runs a program over SSH (spec.run).
	OperationKindRun OperationKind = "Run"
	// OperationKindCapability runs an agent capability (spec.capability) on
	// hosts whose Access Profile uses the agent connector (ADR-057).
	OperationKindCapability OperationKind = "Capability"
)

// Capability limits.
const (
	MaxCapabilityInputBytes      = 16 << 10
	MaxCapabilityTimeoutSeconds  = 3600 // agentv1.MaxOperationTimeout
	maxCapabilityNameLength      = 64
	DefaultCapabilityTimeoutSecs = 60
)

// CapabilitySpec is one agent capability call, the same for every target.
type CapabilitySpec struct {
	// Name is the capability, e.g. runtime.info or service.status.
	Name string `json:"name"`
	// Input is the capability's JSON input ({} when it takes none).
	Input json.RawMessage `json:"input,omitempty" swaggertype:"object"`
	// TimeoutSeconds bounds the call on each agent.
	TimeoutSeconds int `json:"timeoutSeconds"`
}

type OperationState string

const (
	OperationQueued             OperationState = "Queued"
	OperationRunning            OperationState = "Running"
	OperationCancelRequested    OperationState = "CancelRequested"
	OperationSucceeded          OperationState = "Succeeded"
	OperationPartiallySucceeded OperationState = "PartiallySucceeded"
	OperationFailed             OperationState = "Failed"
	OperationCancelled          OperationState = "Cancelled"
	OperationTimedOut           OperationState = "TimedOut"
	OperationInterrupted        OperationState = "Interrupted"
)

type TargetState string

const (
	TargetPending     TargetState = "Pending"
	TargetRunning     TargetState = "Running"
	TargetSucceeded   TargetState = "Succeeded"
	TargetFailed      TargetState = "Failed"
	TargetTimedOut    TargetState = "TimedOut"
	TargetCancelled   TargetState = "Cancelled"
	TargetInterrupted TargetState = "Interrupted"
)

const (
	MaxOperationTargets   = 200
	MaxOperationArgvBytes = 16 << 10
	MaxTargetOutputBytes  = 64 << 10
)

type ActorRef struct {
	UserID     int `json:"userId,omitempty"`
	APITokenID int `json:"apiTokenId,omitempty"`
}

type RunSpec struct {
	Argv           []string `json:"argv"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
	Concurrency    int      `json:"concurrency"`
	// Detach runs the command under a supervisor on each host and ends the
	// SSH session right after the launch (AUDIT/10b §1). The timeout may
	// then be up to MaxDetachedTimeoutSeconds; attached runs keep
	// MaxAttachedTimeoutSeconds.
	Detach bool `json:"detach,omitempty"`
}

type OperationTargetSpec struct {
	HostID                ID     `json:"hostId"`
	HostGeneration        uint64 `json:"hostGeneration"`
	DisplayName           string `json:"displayName"`
	AccessProfileID       ID     `json:"accessProfileId,omitempty"`
	AccessProfileRevision uint64 `json:"accessProfileRevision,omitempty"`
}

type OperationSpec struct {
	Targets  []OperationTargetSpec `json:"targets"`
	Deadline *time.Time            `json:"deadline,omitempty"`
	// Run (SSH) and Capability (agent) are exclusive; Kind says which.
	Run        *RunSpec        `json:"run,omitempty"`
	Capability *CapabilitySpec `json:"capability,omitempty"`
}

type TargetResult struct {
	ExitCode   *int   `json:"exitCode,omitempty"`
	Output     string `json:"output,omitempty"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	ErrorClass string `json:"errorClass,omitempty"`
	// StdoutBytes and StderrBytes are the streams' full sizes on the host;
	// Truncated means the stored text omits part of them (detached runs keep
	// a head and a tail of each stream).
	StdoutBytes int64 `json:"stdoutBytes,omitempty"`
	StderrBytes int64 `json:"stderrBytes,omitempty"`
	Truncated   bool  `json:"truncated,omitempty"`
}

type OperationTarget struct {
	Spec   OperationTargetSpec `json:"spec"`
	State  TargetState         `json:"state"`
	Result *TargetResult       `json:"result,omitempty"`
	// Launch and Live are set for detached runs: how to reach the command,
	// and its captured output (kept after it ends, so output can be read by
	// byte offset). Live is stored encrypted on its own and never serialized
	// with the target; the output endpoint serves it to callers allowed to
	// see output.
	Launch *LaunchHandle `json:"launch,omitempty"`
	Live   *LiveOutput   `json:"-"`
	// Progress summarizes Live for API readers: sizes and timing, no output.
	Progress *TargetProgressSummary `json:"progress,omitempty"`
}

type OperationStatus struct {
	State       OperationState    `json:"state"`
	CancelAsked bool              `json:"cancelAsked,omitempty"`
	Conditions  []Condition       `json:"conditions,omitempty"`
	Targets     []OperationTarget `json:"targets"`
}

type Operation struct {
	ID          ID              `json:"id"`
	WorkspaceID int             `json:"workspaceId"`
	Kind        OperationKind   `json:"kind"`
	CreatedBy   ActorRef        `json:"createdBy"`
	Revision    uint64          `json:"revision"`
	CreatedAt   time.Time       `json:"createdAt"`
	StartedAt   *time.Time      `json:"startedAt,omitempty"`
	FinishedAt  *time.Time      `json:"finishedAt,omitempty"`
	Spec        OperationSpec   `json:"spec"`
	Status      OperationStatus `json:"status"`
}

func NewRunOperation(workspaceID int, actor ActorRef, spec OperationSpec, now time.Time) (*Operation, error) {
	id, err := NewResourceID()
	if err != nil {
		return nil, err
	}
	if spec.Run != nil {
		spec.Run.Argv = append([]string(nil), spec.Run.Argv...)
	}
	spec.Targets = append([]OperationTargetSpec(nil), spec.Targets...)
	op := &Operation{ID: id, WorkspaceID: workspaceID, Kind: OperationKindRun, CreatedBy: actor,
		Revision: 1, CreatedAt: now.UTC(), Spec: spec, Status: OperationStatus{State: OperationQueued}}
	for _, target := range spec.Targets {
		op.Status.Targets = append(op.Status.Targets, OperationTarget{Spec: target, State: TargetPending})
	}
	if err := op.Validate(); err != nil {
		return nil, err
	}
	return op, nil
}

// NewCapabilityOperation creates an agent capability Operation (ADR-057).
func NewCapabilityOperation(workspaceID int, actor ActorRef, spec OperationSpec, now time.Time) (*Operation, error) {
	id, err := NewResourceID()
	if err != nil {
		return nil, err
	}
	if spec.Capability != nil {
		capability := *spec.Capability
		capability.Input = append(json.RawMessage(nil), capability.Input...)
		if len(capability.Input) == 0 {
			capability.Input = json.RawMessage(`{}`)
		}
		spec.Capability = &capability
	}
	spec.Targets = append([]OperationTargetSpec(nil), spec.Targets...)
	op := &Operation{ID: id, WorkspaceID: workspaceID, Kind: OperationKindCapability, CreatedBy: actor,
		Revision: 1, CreatedAt: now.UTC(), Spec: spec, Status: OperationStatus{State: OperationQueued}}
	for _, target := range spec.Targets {
		op.Status.Targets = append(op.Status.Targets, OperationTarget{Spec: target, State: TargetPending})
	}
	if err := op.Validate(); err != nil {
		return nil, err
	}
	return op, nil
}

func (o *Operation) Validate() error {
	if o == nil || o.ID.Validate() != nil || o.WorkspaceID <= 0 || o.Revision == 0 {
		return fmt.Errorf("%w: operation identity, workspace, revision, and supported kind are required", ErrInvalidResource)
	}
	if len(o.Spec.Targets) == 0 || len(o.Spec.Targets) > MaxOperationTargets {
		return fmt.Errorf("%w: Operations require a bounded target set", ErrInvalidResource)
	}
	switch o.Kind {
	case OperationKindRun:
		if err := o.Spec.validateRun(); err != nil {
			return err
		}
	case OperationKindCapability:
		if err := o.Spec.validateCapability(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unsupported Operation kind %q", ErrInvalidResource, o.Kind)
	}
	seen := make(map[ID]struct{}, len(o.Spec.Targets))
	for _, target := range o.Spec.Targets {
		if err := target.HostID.Validate(); err != nil || target.HostGeneration == 0 || strings.TrimSpace(target.DisplayName) == "" {
			return fmt.Errorf("%w: operation target identity, generation, and display name are required", ErrInvalidResource)
		}
		if target.AccessProfileID != "" {
			if err := target.AccessProfileID.Validate(); err != nil {
				return fmt.Errorf("%w: operation target access profile ID is invalid", ErrInvalidResource)
			}
		}
		if _, exists := seen[target.HostID]; exists {
			return fmt.Errorf("%w: duplicate operation target %s", ErrInvalidResource, target.HostID)
		}
		seen[target.HostID] = struct{}{}
	}
	if !validOperationState(o.Status.State) || len(o.Status.Targets) != len(o.Spec.Targets) {
		return fmt.Errorf("%w: invalid operation status or target count", ErrInvalidResource)
	}
	if err := ValidateConditions(o.Status.Conditions, o.Revision); err != nil {
		return err
	}
	if o.Status.State == OperationQueued && (o.StartedAt != nil || o.FinishedAt != nil) {
		return fmt.Errorf("%w: queued Operation cannot have start or finish timestamps", ErrInvalidResource)
	}
	if (o.Status.State == OperationRunning || o.Status.State == OperationCancelRequested) && (o.StartedAt == nil || o.FinishedAt != nil) {
		return fmt.Errorf("%w: active Operation requires a start time and no finish time", ErrInvalidResource)
	}
	if validFinalOperationState(o.Status.State) && o.FinishedAt == nil {
		return fmt.Errorf("%w: terminal Operation requires a finish time", ErrInvalidResource)
	}
	for index, target := range o.Status.Targets {
		if target.Spec != o.Spec.Targets[index] || !validTargetState(target.State) {
			return fmt.Errorf("%w: operation target status does not match immutable target spec", ErrInvalidResource)
		}
		if validFinalOperationState(o.Status.State) && !isFinalTargetState(target.State) {
			return fmt.Errorf("%w: terminal Operation must have terminal targets", ErrInvalidResource)
		}
		if o.Status.State == OperationQueued && target.State != TargetPending {
			return fmt.Errorf("%w: queued Operation targets must be pending", ErrInvalidResource)
		}
		if (target.State == TargetRunning) && o.Status.State != OperationRunning && o.Status.State != OperationCancelRequested {
			return fmt.Errorf("%w: running target requires an active Operation", ErrInvalidResource)
		}
		if target.Live != nil && (target.Launch == nil || target.State == TargetPending) {
			return fmt.Errorf("%w: only a launched detached target has captured output", ErrInvalidResource)
		}
		if target.Launch != nil && target.State == TargetPending {
			return fmt.Errorf("%w: a pending target has no launch", ErrInvalidResource)
		}
		if target.Result != nil {
			if target.State == TargetPending || target.State == TargetRunning {
				return fmt.Errorf("%w: unfinished target cannot have a result", ErrInvalidResource)
			}
			if len(target.Result.Output)+len(target.Result.Stdout)+len(target.Result.Stderr) > MaxTargetOutputBytes || len(target.Result.ErrorClass) > 128 {
				return fmt.Errorf("%w: operation target result exceeds bounds", ErrInvalidResource)
			}
		}
	}
	return nil
}

func (s OperationSpec) validateRun() error {
	if s.Run == nil || s.Capability != nil {
		return fmt.Errorf("%w: Run operations require a Run spec and no capability", ErrInvalidResource)
	}
	if err := domain.ValidateArgv(s.Run.Argv); err != nil {
		return err
	}
	if len(strings.Join(s.Run.Argv, "\x00")) > MaxOperationArgvBytes {
		return fmt.Errorf("%w: operation arguments exceed %d bytes", ErrInvalidResource, MaxOperationArgvBytes)
	}
	maxTimeout := MaxAttachedTimeoutSeconds
	if s.Run.Detach {
		maxTimeout = MaxDetachedTimeoutSeconds
	}
	if s.Run.TimeoutSeconds < 1 || s.Run.TimeoutSeconds > maxTimeout || s.Run.Concurrency < 1 || s.Run.Concurrency > 20 {
		return fmt.Errorf("%w: Run timeout or concurrency is outside supported bounds", ErrInvalidResource)
	}
	return nil
}

func (s OperationSpec) validateCapability() error {
	c := s.Capability
	if c == nil || s.Run != nil {
		return fmt.Errorf("%w: Capability operations require a capability and no Run spec", ErrInvalidResource)
	}
	if !validCapabilityName(c.Name) {
		return fmt.Errorf("%w: capability name must be lowercase words joined by dots, e.g. service.status", ErrInvalidResource)
	}
	if len(c.Input) > MaxCapabilityInputBytes || !json.Valid(c.Input) || (len(c.Input) > 0 && c.Input[0] != '{') {
		return fmt.Errorf("%w: capability input must be a JSON object of at most %d bytes", ErrInvalidResource, MaxCapabilityInputBytes)
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > MaxCapabilityTimeoutSeconds {
		return fmt.Errorf("%w: capability timeout must be 1..%d seconds", ErrInvalidResource, MaxCapabilityTimeoutSeconds)
	}
	return nil
}

// validCapabilityName checks the form; the agent decides what exists.
func validCapabilityName(name string) bool {
	if name == "" || len(name) > maxCapabilityNameLength || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '_' {
			return false
		}
	}
	return true
}

func (o *Operation) Start(now time.Time) error {
	if o == nil || o.Status.State != OperationQueued {
		return ErrInvalidTransition
	}
	started := now.UTC()
	o.StartedAt = &started
	o.Status.State = OperationRunning
	o.Revision++
	return nil
}

func (o *Operation) RequestCancel(now time.Time) error {
	if o == nil {
		return ErrInvalidResource
	}
	if o.IsTerminal() {
		return ErrOperationFinished
	}
	if o.Status.CancelAsked {
		return nil
	}
	o.Status.CancelAsked = true
	if o.Status.State == OperationQueued {
		o.Status.State = OperationCancelled
		finished := now.UTC()
		o.FinishedAt = &finished
		for i := range o.Status.Targets {
			if o.Status.Targets[i].State == TargetPending {
				o.Status.Targets[i].State = TargetCancelled
			}
		}
	} else {
		o.Status.State = OperationCancelRequested
	}
	o.Revision++
	return nil
}

func (o *Operation) SetTargetState(hostID ID, state TargetState, result *TargetResult) error {
	if o == nil || o.IsTerminal() || (o.Status.State != OperationRunning && o.Status.State != OperationCancelRequested) {
		return ErrInvalidTransition
	}
	if !validTargetState(state) {
		return ErrInvalidTransition
	}
	for i := range o.Status.Targets {
		target := &o.Status.Targets[i]
		if target.Spec.HostID != hostID {
			continue
		}
		if target.State != TargetPending && target.State != TargetRunning {
			return ErrOperationFinished
		}
		if result != nil {
			target.Result = NormalizeTargetResult(result)
		}
		target.State = state
		o.Revision++
		return o.Validate()
	}
	return ErrOperationNotFound
}

func (o *Operation) Finish(state OperationState, now time.Time) error {
	if o == nil || !validFinalOperationState(state) {
		return fmt.Errorf("%w: final operation state is required", ErrInvalidResource)
	}
	if o.IsTerminal() {
		return ErrOperationFinished
	}
	if state == OperationSucceeded || state == OperationPartiallySucceeded || state == OperationFailed {
		for _, target := range o.Status.Targets {
			if !isFinalTargetState(target.State) {
				return fmt.Errorf("%w: all targets must be terminal before operation completion", ErrInvalidTransition)
			}
		}
	}
	if state == OperationCancelled || state == OperationTimedOut || state == OperationInterrupted {
		targetState := TargetCancelled
		if state == OperationTimedOut {
			targetState = TargetTimedOut
		}
		if state == OperationInterrupted {
			targetState = TargetInterrupted
		}
		for i := range o.Status.Targets {
			if !isFinalTargetState(o.Status.Targets[i].State) {
				o.Status.Targets[i].State = targetState
			}
		}
	}
	o.Status.State = state
	finished := now.UTC()
	o.FinishedAt = &finished
	o.Revision++
	return o.Validate()
}

func (o *Operation) IsTerminal() bool { return o == nil || validFinalOperationState(o.Status.State) }

func validOperationState(state OperationState) bool {
	switch state {
	case OperationQueued, OperationRunning, OperationCancelRequested, OperationSucceeded,
		OperationPartiallySucceeded, OperationFailed, OperationCancelled, OperationTimedOut, OperationInterrupted:
		return true
	default:
		return false
	}
}

func validFinalOperationState(state OperationState) bool {
	switch state {
	case OperationSucceeded, OperationPartiallySucceeded, OperationFailed, OperationCancelled, OperationTimedOut, OperationInterrupted:
		return true
	default:
		return false
	}
}

func validTargetState(state TargetState) bool {
	switch state {
	case TargetPending, TargetRunning, TargetSucceeded, TargetFailed, TargetTimedOut, TargetCancelled, TargetInterrupted, TargetLost:
		return true
	default:
		return false
	}
}

func isFinalTargetState(state TargetState) bool {
	return state != TargetPending && state != TargetRunning
}

// NormalizeTargetResult returns a sanitized copy of result bounded to
// MaxTargetOutputBytes, exactly as SetTargetState stores it. Repositories that
// write a single target use it so per-target writes keep the same invariants.
func NormalizeTargetResult(result *TargetResult) *TargetResult {
	if result == nil {
		return nil
	}
	copyResult := *result
	copyResult.Output = sanitizeOutput(copyResult.Output, MaxTargetOutputBytes)
	remaining := MaxTargetOutputBytes - len(copyResult.Output)
	copyResult.Stdout = sanitizeOutput(copyResult.Stdout, remaining)
	remaining -= len(copyResult.Stdout)
	if remaining < 0 {
		remaining = 0
	}
	copyResult.Stderr = sanitizeOutput(copyResult.Stderr, remaining)
	copyResult.ErrorClass = sanitizeMessage(copyResult.ErrorClass, 128)
	return &copyResult
}

// TargetProgress orders target states: pending < running < terminal. Target
// state only ever moves forward, which lets repositories reject writes based
// on a stale view instead of silently moving a target backwards.
func TargetProgress(state TargetState) int {
	switch state {
	case TargetPending:
		return 0
	case TargetRunning:
		return 1
	default:
		return 2
	}
}

// ValidTargetState reports whether state is a known target state.
func ValidTargetState(state TargetState) bool { return validTargetState(state) }

// sanitizeOutput bounds command output and removes control characters except
// newline and tab, so output keeps its lines but cannot carry terminal escape
// sequences. Invalid UTF-8 is replaced.
func sanitizeOutput(value string, limit int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	value = strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	if len(value) > limit {
		value = value[:limit]
		for len(value) > 0 && !utf8.ValidString(value) {
			value = value[:len(value)-1] // do not split a rune
		}
	}
	return strings.TrimRight(value, " ")
}
