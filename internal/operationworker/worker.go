// Package operationworker executes immutable, persisted Operations independently
// from Host reconciliation.
package operationworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/eventing"
	"github.com/machinarchy/machinarchy/internal/hostaccess"
)

type Dependencies struct {
	Operations resource.OperationStore
	Queue      resource.OperationQueueRepository
	Hosts      resource.HostRepository
	Profiles   interface {
		GetAccessProfile(context.Context, int, resource.ID) (*resource.AccessProfile, error)
		GetAccessProfileRevision(context.Context, int, resource.ID, uint64) (*resource.AccessProfileRevision, error)
	}
	Outbox          eventing.OutboxStore
	Connector       hostaccess.HostConnector
	ExecutionPolicy func() domain.ExecutionPolicy
	Leases          resource.LeaseStore
	Recovery        resource.OperationRecoveryRepository
	Logger          *slog.Logger
}

type Worker struct {
	deps    Dependencies
	workers int
	queue   chan resource.OperationKey
	mu      sync.Mutex
	pending map[resource.OperationKey]struct{}
	active  map[resource.OperationKey]context.CancelFunc
	owner   string
}

func New(deps Dependencies, workers int) (*Worker, error) {
	if deps.Operations == nil || deps.Queue == nil || deps.Hosts == nil || deps.Profiles == nil || deps.Outbox == nil || deps.Connector == nil || deps.ExecutionPolicy == nil || workers < 1 || workers > 128 {
		return nil, errors.New("operation worker requires persistence, outbox, connector, policy, and bounded workers")
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	id, err := resource.NewResourceID()
	if err != nil {
		return nil, fmt.Errorf("generate Operation worker identity: %w", err)
	}
	return &Worker{deps: deps, workers: workers, queue: make(chan resource.OperationKey, workers*16), pending: make(map[resource.OperationKey]struct{}), active: make(map[resource.OperationKey]context.CancelFunc), owner: string(id)}, nil
}

// Start runs a bounded pool and a durable level-triggered scan. Outbox events
// lower scheduling latency; the database scan recovers missed notifications.
func (w *Worker) Start(ctx context.Context) error {
	if w == nil || ctx == nil {
		return errors.New("operation worker and context are required")
	}
	if w.deps.Recovery != nil {
		if err := w.recoverRunning(ctx); err != nil {
			return fmt.Errorf("recover interrupted Operations: %w", err)
		}
	}
	if err := w.schedule(ctx); err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("initial Operation scheduling: %w", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < w.workers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); w.runWorker(ctx) }()
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case <-ticker.C:
			if err := w.schedule(ctx); err != nil && !errors.Is(err, context.Canceled) {
				w.deps.Logger.Warn("Operation scheduling scan failed", "error", err)
			}
		}
	}
}

// recoverRunning makes uncertain executions terminal. A remote command may
// have completed before a worker crashed, so recovery must never replay it.
func (w *Worker) recoverRunning(ctx context.Context) error {
	keys, err := w.deps.Recovery.ListRunningOperations(ctx, 500)
	if err != nil {
		return err
	}
	for _, key := range keys {
		w.mu.Lock()
		_, active := w.active[key]
		w.mu.Unlock()
		if active {
			continue
		}
		fenced := ctx
		var lease resource.Lease
		if w.deps.Leases != nil {
			var acquired bool
			lease, acquired, err = w.deps.Leases.AcquireResourceLease(ctx, resource.Lease{Kind: "operation", WorkspaceID: key.WorkspaceID, ID: string(key.ID), Owner: w.owner}, time.Now(), 2*time.Minute)
			if err != nil {
				return err
			}
			if !acquired {
				continue // A live worker owns this Operation.
			}
			fenced = resource.WithLeaseFence(ctx, resource.LeaseFence{Kind: "operation", WorkspaceID: key.WorkspaceID, ID: string(key.ID), Token: lease.Token})
		}
		op, loadErr := w.deps.Operations.GetOperation(ctx, key.WorkspaceID, key.ID)
		if loadErr == nil && (op.Status.State == resource.OperationRunning || op.Status.State == resource.OperationCancelRequested) {
			state, reason := resource.OperationInterrupted, "worker_restarted"
			if op.Status.State == resource.OperationCancelRequested {
				state, reason = resource.OperationCancelled, "cancelled_during_worker_restart"
			}
			if updateErr := w.finishWithoutExecution(fenced, op, state, reason); updateErr != nil {
				if w.deps.Leases != nil {
					if err := w.deps.Leases.ReleaseResourceLease(ctx, lease); err != nil {
						slog.Warn("failed to release resource lease",
							"lease_id", lease.ID,
							"error", err,
						)
					}
				}
				return updateErr
			}
		}
		if loadErr != nil && !errors.Is(loadErr, resource.ErrOperationNotFound) {
			return loadErr
		}
		if w.deps.Leases != nil {
			if releaseErr := w.deps.Leases.ReleaseResourceLease(ctx, lease); releaseErr != nil {
				return releaseErr
			}
		}
	}
	return nil
}

func (w *Worker) schedule(ctx context.Context) error {
	if w.deps.Recovery != nil {
		if err := w.recoverRunning(ctx); err != nil {
			return err
		}
	}
	claimed, err := w.deps.Outbox.Claim(ctx, eventing.ConsumerOperationWorker, w.owner, time.Now().UTC(), 30*time.Second, 64)
	if err != nil {
		return err
	}
	for _, delivery := range claimed {
		e := delivery.Envelope
		if err = e.Validate(); err != nil || e.Type != eventing.OperationCreated {
			if quarantineErr := w.deps.Outbox.Quarantine(ctx, eventing.ConsumerOperationWorker, w.owner, e.ID, time.Now(), "invalid_operation_notification"); quarantineErr != nil {
				return quarantineErr
			}
			continue
		}
		key := resource.OperationKey{WorkspaceID: e.WorkspaceID, ID: e.Subject.ID}
		if !w.enqueue(ctx, key) {
			if err = w.deps.Outbox.Retry(ctx, eventing.ConsumerOperationWorker, w.owner, e.ID, time.Now().Add(time.Second), "operation_queue_full"); err != nil {
				return err
			}
			continue
		}
		if err = w.deps.Outbox.Acknowledge(ctx, eventing.ConsumerOperationWorker, w.owner, e.ID, time.Now()); err != nil {
			return err
		}
	}
	keys, err := w.deps.Queue.ListQueuedOperations(ctx, 200)
	if err != nil {
		return err
	}
	for _, key := range keys {
		w.enqueue(ctx, key)
	}
	return nil
}

func (w *Worker) enqueue(ctx context.Context, key resource.OperationKey) bool {
	w.mu.Lock()
	if _, ok := w.pending[key]; ok {
		w.mu.Unlock()
		return true
	}
	w.pending[key] = struct{}{}
	w.mu.Unlock()
	select {
	case w.queue <- key:
		return true
	case <-ctx.Done():
		w.mu.Lock()
		delete(w.pending, key)
		w.mu.Unlock()
		return false
	default:
		w.mu.Lock()
		delete(w.pending, key)
		w.mu.Unlock()
		return false
	}
}

func (w *Worker) runWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case key := <-w.queue:
			w.safeExecute(ctx, key)
			w.mu.Lock()
			delete(w.pending, key)
			w.mu.Unlock()
		}
	}
}

func (w *Worker) Cancel(ctx context.Context, workspace int, id resource.ID) (*resource.Operation, error) {
	var op *resource.Operation
	for attempt := 0; attempt < 8; attempt++ {
		var err error
		op, err = w.deps.Operations.GetOperation(ctx, workspace, id)
		if err != nil {
			return nil, err
		}
		if op.Status.CancelAsked {
			break
		}
		expected := op.Revision
		if err = op.RequestCancel(time.Now()); err != nil {
			return nil, err
		}
		if err = w.deps.Operations.UpdateOperation(ctx, op, expected); err == nil {
			break
		}
		if !errors.Is(err, resource.ErrConflict) {
			return nil, err
		}
		op = nil
	}
	if op == nil {
		return nil, resource.ErrConflict
	}
	key := resource.OperationKey{WorkspaceID: workspace, ID: id}
	w.mu.Lock()
	cancel := w.active[key]
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return op, nil
}

// safeExecute isolates a panic in one Operation (AUDIT Q-04): it is logged
// with its stack, the Operation is finished as Interrupted with reason
// worker_panic (like a crash during execution: a command may already have
// run, so it is never retried), and the worker keeps serving other work.
// Without this, one panic would terminate the whole process, including the
// API in monolith mode.
func (w *Worker) safeExecute(ctx context.Context, key resource.OperationKey) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		w.deps.Logger.Error("Operation execution panicked", "operation_id", key.ID, "panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		op, err := w.deps.Operations.GetOperation(persistCtx, key.WorkspaceID, key.ID)
		if err != nil {
			w.deps.Logger.Error("cannot load Operation after panic", "operation_id", key.ID, "error", err)
			return
		}
		if err := w.finishWithoutExecution(persistCtx, op, resource.OperationInterrupted, "worker_panic"); err != nil {
			w.deps.Logger.Error("cannot record Operation failure after panic", "operation_id", key.ID, "error", err)
		}
	}()
	w.execute(ctx, key)
}

func (w *Worker) execute(parent context.Context, key resource.OperationKey) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	w.mu.Lock()
	w.active[key] = cancel
	w.mu.Unlock()
	defer func() { w.mu.Lock(); delete(w.active, key); w.mu.Unlock() }()
	var lease resource.Lease
	if w.deps.Leases != nil {
		var acquired bool
		var err error
		lease, acquired, err = w.deps.Leases.AcquireResourceLease(ctx, resource.Lease{Kind: "operation", WorkspaceID: key.WorkspaceID, ID: string(key.ID), Owner: w.owner}, time.Now(), 2*time.Minute)
		if err != nil || !acquired {
			return
		}
		ctx = resource.WithLeaseFence(ctx, resource.LeaseFence{Kind: "operation", WorkspaceID: key.WorkspaceID, ID: string(key.ID), Token: lease.Token})
		leaseStop := make(chan struct{})
		leaseDone := make(chan struct{})
		go w.renewLease(ctx, lease, cancel, leaseStop, leaseDone)
		defer func() {
			close(leaseStop)
			<-leaseDone
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer releaseCancel()
			if err := w.deps.Leases.ReleaseResourceLease(releaseCtx, lease); err != nil {
				w.deps.Logger.Warn("release Operation lease failed", "error", err)
			}
		}()
	}
	op, err := w.deps.Operations.GetOperation(ctx, key.WorkspaceID, key.ID)
	if err != nil || op.Status.State != resource.OperationQueued {
		return
	}
	if op.Spec.Deadline != nil && !time.Now().Before(*op.Spec.Deadline) {
		expected := op.Revision
		if op.Finish(resource.OperationTimedOut, time.Now()) == nil {
			if err := w.deps.Operations.UpdateOperation(ctx, op, expected); err != nil {
				w.deps.Logger.Warn("persist expired Operation failed", "error", err)
			}
		}
		return
	}
	expected := op.Revision
	if err = op.Start(time.Now()); err != nil {
		return
	}
	if err = w.deps.Operations.UpdateOperation(ctx, op, expected); err != nil {
		return
	}
	policy := w.deps.ExecutionPolicy()
	if err = policy.Check(op.Spec.Run.Argv[0]); err != nil {
		for _, target := range op.Spec.Targets {
			w.completeTarget(ctx, key, target.HostID, resource.TargetFailed, &resource.TargetResult{ErrorClass: "execution_denied"})
		}
		latest, loadErr := w.deps.Operations.GetOperation(ctx, key.WorkspaceID, key.ID)
		if loadErr == nil {
			if err := w.finishWithoutExecution(context.WithoutCancel(ctx), latest, resource.OperationFailed, "execution_denied"); err != nil {
				slog.Error("failed to finish failed operation", "error", err)
			}
		}
		return
	}
	for _, targetSpec := range op.Spec.Targets {
		if op.Spec.Deadline != nil && !time.Now().Before(*op.Spec.Deadline) {
			latest, loadErr := w.deps.Operations.GetOperation(context.WithoutCancel(ctx), key.WorkspaceID, key.ID)
			if loadErr == nil {
				if err := w.finishWithoutExecution(context.WithoutCancel(ctx), latest, resource.OperationTimedOut, "operation_deadline_exceeded"); err != nil {
					slog.Error("failed to finish timeout operation", "error", err)
				}
			}
			return
		}
		if ctx.Err() != nil {
			latest, loadErr := w.deps.Operations.GetOperation(context.WithoutCancel(ctx), key.WorkspaceID, key.ID)
			if loadErr == nil {
				if err := w.finishWithoutExecution(context.WithoutCancel(ctx), latest, resource.OperationInterrupted, "worker_stopped"); err != nil {
					slog.Error("failed to finish interrupted operation", "error", err)
				}
			}
			return
		}
		current, stateErr := w.deps.Operations.GetOperationState(ctx, key.WorkspaceID, key.ID)
		if stateErr != nil {
			w.deps.Logger.Warn("read Operation state failed", "operation_id", key.ID, "error", stateErr)
			return
		}
		if current == resource.OperationCancelRequested {
			if err := w.finishWithoutExecution(ctx, op, resource.OperationCancelled, "cancelled"); err != nil {
				w.deps.Logger.Error("failed to finish cancelled operation", "operation_id", key.ID, "error", err)
			}
			return
		}
		if current != resource.OperationRunning {
			return
		}
		host, hostErr := w.deps.Hosts.GetHost(ctx, key.WorkspaceID, targetSpec.HostID)
		if hostErr != nil || host.Metadata.Generation != targetSpec.HostGeneration || host.Spec.Presence != resource.PresenceManaged {
			w.completeTarget(ctx, key, targetSpec.HostID, resource.TargetFailed, &resource.TargetResult{ErrorClass: "host_snapshot_stale"})
			continue
		}
		profile, profileErr := w.deps.Profiles.GetAccessProfile(ctx, key.WorkspaceID, targetSpec.AccessProfileID)
		if profileErr != nil || profile.DeletedAt != nil {
			w.completeTarget(ctx, key, targetSpec.HostID, resource.TargetFailed, &resource.TargetResult{ErrorClass: "access_profile_unavailable"})
			continue
		}
		revision, revisionErr := w.deps.Profiles.GetAccessProfileRevision(ctx, key.WorkspaceID, targetSpec.AccessProfileID, targetSpec.AccessProfileRevision)
		if revisionErr != nil || !revision.Spec.Enabled {
			w.completeTarget(ctx, key, targetSpec.HostID, resource.TargetFailed, &resource.TargetResult{ErrorClass: "access_profile_unavailable"})
			continue
		}
		if err = w.deps.Operations.UpdateOperationTarget(ctx, key.WorkspaceID, key.ID, targetSpec.HostID, resource.TargetRunning, nil); err != nil {
			if !errors.Is(err, resource.ErrOperationFinished) {
				w.deps.Logger.Warn("start Operation target failed", "operation_id", key.ID, "host_id", targetSpec.HostID, "error", err)
			}
			return
		}
		commandCtx, commandCancel := context.WithTimeout(ctx, time.Duration(op.Spec.Run.TimeoutSeconds)*time.Second)
		watchCtx, stopWatching := context.WithCancel(context.Background())
		go w.watchCancellation(watchCtx, key, commandCancel)
		result, runErr := w.deps.Connector.Execute(commandCtx, host, revision, string(op.ID), op.Spec.Run.Argv)
		timedOut := errors.Is(commandCtx.Err(), context.DeadlineExceeded)
		commandCancel()
		stopWatching()
		if ctx.Err() != nil {
			latest, loadErr := w.deps.Operations.GetOperation(context.WithoutCancel(ctx), key.WorkspaceID, key.ID)
			if loadErr == nil {
				if err := w.finishWithoutExecution(context.WithoutCancel(ctx), latest, resource.OperationInterrupted, "worker_stopped"); err != nil {
					slog.Error("failed to finish interrupted operation", "error", err)
				}
			}
			return
		}
		state := resource.TargetSucceeded
		stored := &resource.TargetResult{Stdout: result.Stdout, Stderr: result.Stderr}
		code := result.ExitCode
		stored.ExitCode = &code
		switch {
		case runErr != nil && timedOut:
			state = resource.TargetTimedOut
			stored = &resource.TargetResult{ErrorClass: "timeout"}
		case runErr != nil:
			state = resource.TargetFailed
			stored = &resource.TargetResult{ErrorClass: "remote_execution_failed"}
		}
		if runErr == nil && result.ExitCode != 0 {
			state = resource.TargetFailed
		}
		if current, err := w.deps.Operations.GetOperationState(context.WithoutCancel(ctx), key.WorkspaceID, key.ID); err == nil && current == resource.OperationCancelRequested {
			state = resource.TargetCancelled
			stored = &resource.TargetResult{ErrorClass: "cancelled"}
		}
		w.completeTarget(context.WithoutCancel(ctx), key, targetSpec.HostID, state, stored)
	}
	if err := w.finishResults(context.WithoutCancel(ctx), key); err != nil {
		w.deps.Logger.Warn("persist Operation completion failed", "error", err)
	}
}

func (w *Worker) renewLease(ctx context.Context, lease resource.Lease, cancel context.CancelFunc, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			renewed, ok, err := w.deps.Leases.RenewResourceLease(ctx, lease, now, 2*time.Minute)
			if err != nil || !ok {
				cancel() // Stop remote execution as soon as ownership is uncertain.
				return
			}
			lease = renewed
		}
	}
}

func (w *Worker) watchCancellation(ctx context.Context, key resource.OperationKey, cancel context.CancelFunc) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state, err := w.deps.Operations.GetOperationState(ctx, key.WorkspaceID, key.ID)
			if err == nil && state == resource.OperationCancelRequested {
				cancel()
				return
			}
		}
	}
}

// completeTarget records one target's outcome with a single-row write
// (AUDIT Q-02, Q-03). Because it no longer competes on the Operation
// revision, it cannot be starved by conflicts; transient database errors are
// retried with jittered backoff, and a result that still cannot be stored is
// logged with its real error. A terminal target or Operation is final.
func (w *Worker) completeTarget(ctx context.Context, key resource.OperationKey, hostID resource.ID, state resource.TargetState, result *resource.TargetResult) {
	delay := 50 * time.Millisecond
	for attempt := 1; ; attempt++ {
		err := w.deps.Operations.UpdateOperationTarget(ctx, key.WorkspaceID, key.ID, hostID, state, result)
		if err == nil || errors.Is(err, resource.ErrOperationFinished) || errors.Is(err, resource.ErrOperationNotFound) {
			return
		}
		if attempt == 6 || ctx.Err() != nil {
			w.deps.Logger.Error("persist Operation target result failed", "operation_id", key.ID, "host_id", hostID,
				"target_state", state, "attempts", attempt, "error", err)
			return
		}
		timer := time.NewTimer(delay/2 + time.Duration(rand.Int64N(int64(delay)))) // #nosec G404 -- retry jitter only
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		delay *= 2
	}
}
func (w *Worker) finishResults(ctx context.Context, key resource.OperationKey) error {
	for attempt := 0; attempt < 8; attempt++ {
		op, err := w.deps.Operations.GetOperation(ctx, key.WorkspaceID, key.ID)
		if err != nil {
			return err
		}
		if op.IsTerminal() {
			return nil
		}
		if op.Status.State == resource.OperationCancelRequested {
			return w.finishWithoutExecution(ctx, op, resource.OperationCancelled, "cancelled")
		}
		succeeded, failed := 0, 0
		for _, target := range op.Status.Targets {
			if target.State == resource.TargetSucceeded {
				succeeded++
			} else {
				failed++
			}
		}
		final := resource.OperationSucceeded
		if succeeded == 0 {
			final = resource.OperationFailed
		} else if failed > 0 {
			final = resource.OperationPartiallySucceeded
		}
		expected := op.Revision
		if err = op.Finish(final, time.Now()); err != nil {
			return err
		}
		if err = w.deps.Operations.UpdateOperation(ctx, op, expected); err == nil {
			return nil
		}
		if !errors.Is(err, resource.ErrConflict) {
			return err
		}
	}
	return resource.ErrConflict
}

func (w *Worker) finishWithoutExecution(ctx context.Context, op *resource.Operation, state resource.OperationState, reason string) error {
	for attempt := 0; attempt < 8; attempt++ {
		latest, err := w.deps.Operations.GetOperation(ctx, op.WorkspaceID, op.ID)
		if err != nil {
			return err
		}
		if latest.IsTerminal() {
			return nil
		}
		if latest.Status.State == resource.OperationCancelRequested {
			state, reason = resource.OperationCancelled, "cancelled"
		}
		expected := latest.Revision
		if err = latest.Finish(state, time.Now()); err != nil {
			return err
		}
		latest.Status.Conditions = []resource.Condition{{Type: "Execution", Status: resource.ConditionFalse, Reason: reason, ObservedGeneration: latest.Revision, LastTransitionTime: time.Now().UTC()}}
		if err = w.deps.Operations.UpdateOperation(ctx, latest, expected); err == nil {
			return nil
		}
		if !errors.Is(err, resource.ErrConflict) {
			w.deps.Logger.Warn("persist Operation terminal state failed", "error", err)
			return err
		}
	}
	return resource.ErrConflict
}
