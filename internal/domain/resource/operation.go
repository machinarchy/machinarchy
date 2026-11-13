package resource

import (
	"fmt"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

type OperationKind string

const OperationKindRun OperationKind = "Run"

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
	Run      *RunSpec              `json:"run,omitempty"`
}

type TargetResult struct {
	ExitCode   *int   `json:"exitCode,omitempty"`
	Output     string `json:"output,omitempty"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	ErrorClass string `json:"errorClass,omitempty"`
}

type OperationTarget struct {
	Spec   OperationTargetSpec `json:"spec"`
	State  TargetState         `json:"state"`
	Result *TargetResult       `json:"result,omitempty"`
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

func (o *Operation) Validate() error {
	if o == nil || o.ID.Validate() != nil || o.WorkspaceID <= 0 || o.Revision == 0 || o.Kind != OperationKindRun {
		return fmt.Errorf("%w: operation identity, workspace, revision, and supported kind are required", ErrInvalidResource)
	}
	if o.Spec.Run == nil || len(o.Spec.Targets) == 0 || len(o.Spec.Targets) > MaxOperationTargets {
		return fmt.Errorf("%w: Run operations require a bounded target set and Run spec", ErrInvalidResource)
	}
	if err := domain.ValidateArgv(o.Spec.Run.Argv); err != nil {
		return err
	}
	if len(strings.Join(o.Spec.Run.Argv, "\x00")) > MaxOperationArgvBytes {
		return fmt.Errorf("%w: operation arguments exceed %d bytes", ErrInvalidResource, MaxOperationArgvBytes)
	}
	if o.Spec.Run.TimeoutSeconds < 1 || o.Spec.Run.TimeoutSeconds > 900 || o.Spec.Run.Concurrency < 1 || o.Spec.Run.Concurrency > 20 {
		return fmt.Errorf("%w: Run timeout or concurrency is outside supported bounds", ErrInvalidResource)
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
			copyResult := *result
			copyResult.Output = sanitizeMessage(copyResult.Output, MaxTargetOutputBytes)
			remaining := MaxTargetOutputBytes - len(copyResult.Output)
			copyResult.Stdout = sanitizeMessage(copyResult.Stdout, remaining)
			remaining -= len(copyResult.Stdout)
			if remaining < 0 {
				remaining = 0
			}
			copyResult.Stderr = sanitizeMessage(copyResult.Stderr, remaining)
			copyResult.ErrorClass = sanitizeMessage(copyResult.ErrorClass, 128)
			target.Result = &copyResult
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
	case TargetPending, TargetRunning, TargetSucceeded, TargetFailed, TargetTimedOut, TargetCancelled, TargetInterrupted:
		return true
	default:
		return false
	}
}

func isFinalTargetState(state TargetState) bool {
	return state != TargetPending && state != TargetRunning
}
