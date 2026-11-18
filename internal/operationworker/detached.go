package operationworker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/hostaccess"
	"github.com/machinarchy/machinarchy/internal/hostaccess/runner"
)

// Detached execution (AUDIT/10b §1.3). The launcher starts each target with
// one short SSH session and records a launch handle; the poller (poller.go)
// follows the command to its end. A worker slot is held only while a
// target launches.

// RunnerConnector runs detached-runner commands; the SSH connector
// implements it.
type RunnerConnector interface {
	RunRunner(ctx context.Context, host *resource.Host, profile *resource.AccessProfileRevision, operationID, command string) (string, error)
}

const launchCallTimeout = 60 * time.Second

// launchDetached launches every Pending target of a Running detached
// Operation, RunSpec.Concurrency at a time. It also resumes an Operation
// whose launcher stopped part-way: a target whose launch reached the host
// but was never recorded is adopted by the runner's mkdir lock, not started
// twice.
func (w *Worker) launchDetached(ctx context.Context, key resource.OperationKey, op *resource.Operation) {
	persist := context.WithoutCancel(ctx)
	connector, ok := w.deps.Connector.(RunnerConnector)
	if !ok || w.detached == nil {
		for _, target := range op.Status.Targets {
			if target.State == resource.TargetPending {
				w.completeTarget(persist, key, target.Spec.HostID, resource.TargetFailed, &resource.TargetResult{ErrorClass: "detached_unsupported"})
			}
		}
		w.finishDetachedIfDone(persist, key)
		return
	}
	slots := make(chan struct{}, op.Spec.Run.Concurrency)
	var wg sync.WaitGroup
	for _, target := range op.Status.Targets {
		if target.State != resource.TargetPending {
			continue
		}
		state, err := w.deps.Operations.GetOperationState(ctx, key.WorkspaceID, key.ID)
		if err != nil || ctx.Err() != nil || state == resource.OperationCancelRequested {
			break // the rest stay Pending and are settled below
		}
		if op.Spec.Deadline != nil && !time.Now().Before(*op.Spec.Deadline) {
			w.completeTarget(persist, key, target.Spec.HostID, resource.TargetTimedOut, &resource.TargetResult{ErrorClass: "operation_deadline_exceeded"})
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(spec resource.OperationTargetSpec) {
			defer wg.Done()
			defer func() { <-slots }()
			w.launchTarget(ctx, connector, key, op, spec, 1)
		}(target.Spec)
	}
	wg.Wait()
	// Targets still Pending: the Operation was cancelled (they never start),
	// or this worker is stopping (another resume launches them).
	if state, err := w.deps.Operations.GetOperationState(persist, key.WorkspaceID, key.ID); err == nil && state == resource.OperationCancelRequested {
		latest, err := w.deps.Operations.GetOperation(persist, key.WorkspaceID, key.ID)
		if err == nil {
			for _, target := range latest.Status.Targets {
				if target.State == resource.TargetPending {
					w.completeTarget(persist, key, target.Spec.HostID, resource.TargetCancelled, &resource.TargetResult{ErrorClass: "cancelled"})
				}
			}
		}
	}
	w.finishDetachedIfDone(persist, key)
}

func (w *Worker) launchTarget(ctx context.Context, connector RunnerConnector, key resource.OperationKey, op *resource.Operation, spec resource.OperationTargetSpec, attempt int) {
	persist := context.WithoutCancel(ctx)
	fail := func(class string) {
		if ctx.Err() != nil {
			return // this worker is stopping: the target stays Pending for a resume
		}
		w.metrics.launches.WithLabelValues("failed").Inc()
		w.completeTarget(persist, key, spec.HostID, resource.TargetFailed, &resource.TargetResult{ErrorClass: class})
	}
	host, err := w.deps.Hosts.GetHost(ctx, key.WorkspaceID, spec.HostID)
	if err != nil || host.Metadata.Generation != spec.HostGeneration || host.Spec.Presence != resource.PresenceManaged {
		fail("host_snapshot_stale")
		return
	}
	profile, err := w.deps.Profiles.GetAccessProfile(ctx, key.WorkspaceID, spec.AccessProfileID)
	if err != nil || profile.DeletedAt != nil {
		fail("access_profile_unavailable")
		return
	}
	revision, err := w.deps.Profiles.GetAccessProfileRevision(ctx, key.WorkspaceID, spec.AccessProfileID, spec.AccessProfileRevision)
	if err != nil || !revision.Spec.Enabled {
		fail("access_profile_unavailable")
		return
	}
	settings := resource.ExecutionSettings{}
	if revision.Spec.Execution != nil {
		settings = *revision.Spec.Execution
	}
	mode := runner.ModeAuto
	switch settings.Supervisor {
	case resource.SupervisorAttached:
		fail("detached_disabled_by_profile")
		return
	case resource.SupervisorSystemd:
		mode = runner.ModeSystemd
	case resource.SupervisorSetsid:
		mode = runner.ModeSetsid
	}
	now := time.Now().UTC()
	deadline := now.Add(time.Duration(op.Spec.Run.TimeoutSeconds) * time.Second)
	if op.Spec.Deadline != nil && op.Spec.Deadline.Before(deadline) {
		deadline = op.Spec.Deadline.UTC()
	}
	timeout := int(deadline.Sub(now).Seconds() + 0.999)
	if timeout < 1 {
		w.completeTarget(persist, key, spec.HostID, resource.TargetTimedOut, &resource.TargetResult{ErrorClass: "operation_deadline_exceeded"})
		return
	}
	command, err := runner.LaunchCommand(runner.LaunchRequest{Root: settings.StateRoot, Ref: runner.Ref{OperationID: string(op.ID), TargetID: string(spec.HostID)},
		TimeoutSeconds: timeout, MaxFileBytes: settings.MaxOutputFileBytes, Mode: mode, Argv: op.Spec.Run.Argv})
	if err != nil {
		fail("launch_invalid")
		return
	}
	handle := resource.LaunchHandle{Root: settings.StateRoot, Endpoint: host.Spec.AccessTarget.Endpoint, LaunchedAt: now, Deadline: deadline, Attempt: attempt}
	// A launch already sent is not cut off when this worker stops or the
	// Operation is cancelled: killing it half-way leaves its outcome unknown.
	// It finishes (normally in about a second), and the poller then cancels
	// the command if the Operation was cancelled.
	callCtx, cancel := context.WithTimeout(persist, launchCallTimeout)
	started := time.Now()
	output, err := connector.RunRunner(callCtx, host, revision, string(op.ID), command)
	cancel()
	w.metrics.duration.Observe(time.Since(started).Seconds())
	if err != nil {
		if class, definite := notLaunched(err); definite {
			fail(class)
			return
		}
		// The connection failed somewhere during the launch: the command may
		// have started. Record an unconfirmed handle; the poller finds out.
		w.metrics.launches.WithLabelValues("unconfirmed").Inc()
		w.recordLaunch(persist, key, spec.HostID, handle)
		return
	}
	launched, err := runner.ParseLaunch(output)
	var refused *runner.RefusedError
	switch {
	case errors.As(err, &refused) && refused.Code != "busy":
		fail("runner_" + refused.Code)
		return
	case err != nil:
		w.metrics.launches.WithLabelValues("unconfirmed").Inc()
		w.recordLaunch(persist, key, spec.HostID, handle)
		return
	}
	handle.Mode, handle.Unit, handle.PID = string(launched.Mode), launched.Unit, launched.PID
	w.metrics.launches.WithLabelValues("confirmed").Inc()
	w.recordLaunch(persist, key, spec.HostID, handle)
}

// notLaunched reports whether err proves the command never reached the
// host (the session failed before it could start anything).
func notLaunched(err error) (string, bool) {
	switch {
	case errors.Is(err, hostaccess.ErrHostKey):
		return "launch_host_key_rejected", true
	case errors.Is(err, hostaccess.ErrAuth):
		return "launch_authentication_failed", true
	case errors.Is(err, hostaccess.ErrRefused):
		return "launch_connection_refused", true
	case errors.Is(err, hostaccess.ErrUnavailable):
		return "launch_access_unavailable", true
	case errors.Is(err, hostaccess.ErrCapacity):
		return "launch_connector_at_capacity", true
	case errors.Is(err, domain.ErrTargetNotAllowed):
		return "launch_target_not_allowed", true
	}
	return "", false
}

// recordLaunch persists a launch handle, retrying transient errors. If it
// still fails the target stays Pending and a resume adopts the command.
func (w *Worker) recordLaunch(ctx context.Context, key resource.OperationKey, hostID resource.ID, handle resource.LaunchHandle) {
	delay := 100 * time.Millisecond
	for attempt := 1; ; attempt++ {
		err := w.detached.RecordTargetLaunch(ctx, key.WorkspaceID, key.ID, hostID, handle)
		if err == nil || errors.Is(err, resource.ErrOperationFinished) || errors.Is(err, resource.ErrOperationNotFound) {
			return
		}
		if attempt == 6 {
			w.deps.Logger.Error("record detached launch failed; a resume will adopt it", "operation_id", key.ID, "host_id", hostID, "error", err)
			return
		}
		time.Sleep(delay)
		delay *= 2
	}
}

// finishDetachedIfDone finishes the Operation once every target is terminal.
func (w *Worker) finishDetachedIfDone(ctx context.Context, key resource.OperationKey) {
	op, err := w.deps.Operations.GetOperation(ctx, key.WorkspaceID, key.ID)
	if err != nil || op.IsTerminal() {
		return
	}
	for _, target := range op.Status.Targets {
		if resource.TargetProgress(target.State) < 2 {
			return
		}
	}
	if err := w.finishResults(ctx, key); err != nil {
		w.deps.Logger.Warn("finish detached Operation failed", "operation_id", key.ID, "error", err)
		return
	}
	w.mu.Lock()
	delete(w.settled, key)
	w.mu.Unlock()
}

// detachedResumeNeeded reports whether a Running detached Operation still
// has targets to launch.
func detachedResumeNeeded(op *resource.Operation) bool {
	if op.Spec.Run == nil || !op.Spec.Run.Detach {
		return false
	}
	for _, target := range op.Status.Targets {
		if target.State == resource.TargetPending {
			return true
		}
	}
	return false
}
