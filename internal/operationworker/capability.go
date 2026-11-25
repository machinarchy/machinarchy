package operationworker

import (
	"context"
	"errors"
	"time"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"

	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/store/resources"
)

// Capability Operations (ADR-057): agent work goes through the same
// Operation model as SSH runs. Dispatch writes one durable command per
// target; a relay publishes it to the agent (PubAck-gated, Nats-Msg-Id =
// command ID); results are stored before the gateway acknowledges them; a
// target with no result by its due time becomes Lost. Like detached runs,
// capability Operations survive a controller restart.

// AgentTransport publishes signed commands to agents (the agent gateway).
type AgentTransport interface {
	PublishCommand(ctx context.Context, cmd agentv1.Command) error
	CommandLifetime() time.Duration
}

// AgentBindings finds the agent bound to a managed host.
type AgentBindings interface {
	HostAgent(ctx context.Context, workspaceID int, hostID string) (*agentgateway.Agent, error)
}

const (
	// defaultCapabilityResultGrace is added to the command lifetime and the
	// call's timeout before a target without a result becomes Lost.
	defaultCapabilityResultGrace = 2 * time.Minute
	capabilityRelayEvery         = 250 * time.Millisecond
	capabilityRelayBatch         = 100
	maxPublishBackoff            = 30 * time.Second
)

// capabilityReady reports whether this process can run capability work.
func (w *Worker) capabilityReady() bool {
	return w.capabilities != nil && w.deps.Agents != nil && w.deps.AgentBindings != nil
}

// executeCapability starts a queued capability Operation and dispatches its
// pending targets; on a running one (resumed after a restart) it dispatches
// what is still pending.
func (w *Worker) executeCapability(ctx context.Context, key resource.OperationKey, op *resource.Operation) {
	if op.Status.State == resource.OperationQueued {
		if w.expireIfPastDeadline(ctx, op) {
			return
		}
		expected := op.Revision
		if err := op.Start(time.Now()); err != nil {
			return
		}
		if err := w.deps.Operations.UpdateOperation(ctx, op, expected); err != nil {
			return
		}
	}
	if op.Status.State != resource.OperationRunning && op.Status.State != resource.OperationCancelRequested {
		return
	}
	w.dispatchCapability(ctx, key, op)
	w.markSettled(key)
	w.finishDetachedIfDone(context.WithoutCancel(ctx), key)
}

func (w *Worker) dispatchCapability(ctx context.Context, key resource.OperationKey, op *resource.Operation) {
	fail := func(hostID resource.ID, class string) {
		w.completeTarget(ctx, key, hostID, resource.TargetFailed, &resource.TargetResult{ErrorClass: class})
	}
	for _, target := range op.Status.Targets {
		if target.State != resource.TargetPending {
			continue
		}
		hostID := target.Spec.HostID
		if ctx.Err() != nil {
			return // resumed by the next worker; nothing was sent for this target
		}
		current, err := w.deps.Operations.GetOperationState(ctx, key.WorkspaceID, key.ID)
		if err != nil {
			return
		}
		if current == resource.OperationCancelRequested {
			w.completeTarget(ctx, key, hostID, resource.TargetCancelled, &resource.TargetResult{ErrorClass: "cancelled"})
			continue
		}
		if current != resource.OperationRunning {
			return
		}
		if !w.capabilityReady() {
			fail(hostID, "agent_gateway_unavailable")
			continue
		}
		host, err := w.deps.Hosts.GetHost(ctx, key.WorkspaceID, hostID)
		if err != nil || host.Metadata.Generation != target.Spec.HostGeneration || host.Spec.Presence != resource.PresenceManaged {
			fail(hostID, "host_snapshot_stale")
			continue
		}
		revision, err := w.deps.Profiles.GetAccessProfileRevision(ctx, key.WorkspaceID, target.Spec.AccessProfileID, target.Spec.AccessProfileRevision)
		if err != nil || !revision.Spec.Enabled {
			fail(hostID, "access_profile_unavailable")
			continue
		}
		if revision.Spec.Connector != resource.ConnectorAgent {
			fail(hostID, "not_an_agent_host")
			continue
		}
		agent, err := w.deps.AgentBindings.HostAgent(ctx, key.WorkspaceID, string(hostID))
		if errors.Is(err, agentgateway.ErrHostNotBound) {
			fail(hostID, "agent_not_bound")
			continue
		}
		if err != nil {
			w.deps.Logger.Warn("read host agent binding failed", "operation_id", key.ID, "host_id", hostID, "error", err)
			return
		}
		commandID, err := resources.NewAgentCommandID()
		if err != nil {
			return
		}
		agentOperationID, err := resources.NewAgentOperationID()
		if err != nil {
			return
		}
		now := time.Now().UTC()
		lifetime := w.deps.Agents.CommandLifetime()
		timeout := time.Duration(op.Spec.Capability.TimeoutSeconds) * time.Second
		err = w.capabilities.DispatchCapabilityTarget(ctx, resource.AgentCommand{
			CommandID: commandID, OperationID: key.ID, HostID: hostID, WorkspaceID: key.WorkspaceID, AgentID: agent.ID,
			AgentOperationID: agentOperationID, Type: resource.AgentCommandRequest, CreatedAt: now,
			ExpiresAt: now.Add(lifetime), ResultDueAt: now.Add(lifetime + timeout + w.deps.CapabilityResultGrace),
		})
		if errors.Is(err, resource.ErrOperationFinished) {
			return
		}
		if err != nil {
			w.deps.Logger.Warn("dispatch capability target failed", "operation_id", key.ID, "host_id", hostID, "error", err)
			return
		}
		w.nudgeRelay()
	}
}

// nudgeRelay wakes the relay so a new command goes out at once.
func (w *Worker) nudgeRelay() {
	select {
	case w.relayWake <- struct{}{}:
	default:
	}
}

// runCapabilityRelay publishes due commands and settles overdue targets
// until ctx ends. Every replica may run it: JetStream drops a duplicate
// publish of the same command ID, and the agent's replay ledger drops a
// duplicate command.
func (w *Worker) runCapabilityRelay(ctx context.Context) {
	ticker := time.NewTicker(capabilityRelayEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.relayWake:
		}
		w.relayOnce(ctx)
		w.settleOverdue(ctx)
	}
}

func (w *Worker) relayOnce(ctx context.Context) {
	now := time.Now().UTC()
	commands, err := w.capabilities.ListDueAgentCommands(ctx, now, capabilityRelayBatch)
	if err != nil {
		if ctx.Err() == nil {
			w.deps.Logger.Warn("list agent commands failed", "error", err)
		}
		return
	}
	specs := map[resource.OperationKey]*resource.CapabilitySpec{}
	for _, c := range commands {
		key := resource.OperationKey{WorkspaceID: c.WorkspaceID, ID: c.OperationID}
		if !now.Before(c.ExpiresAt) {
			// Never published while the agent would still accept it.
			if err := w.capabilities.ExpireAgentCommand(ctx, c.CommandID); err == nil && c.Type == resource.AgentCommandRequest {
				w.completeTarget(ctx, key, c.HostID, resource.TargetFailed, &resource.TargetResult{ErrorClass: "agent_command_undelivered"})
				w.finishDetachedIfDone(ctx, key)
			}
			continue
		}
		cmd := agentv1.Command{Type: agentv1.CommandType(c.Type), CommandID: c.CommandID, AgentID: c.AgentID,
			OperationID: c.AgentOperationID, IssuedAt: c.CreatedAt, ExpiresAt: c.ExpiresAt}
		if c.Type == resource.AgentCommandRequest {
			spec, ok := specs[key]
			if !ok {
				op, err := w.deps.Operations.GetOperation(ctx, c.WorkspaceID, c.OperationID)
				if err != nil || op.Spec.Capability == nil {
					continue // retried on the next tick
				}
				spec = op.Spec.Capability
				specs[key] = spec
			}
			cmd.Capability, cmd.Input, cmd.TimeoutMillis = spec.Name, spec.Input, int64(spec.TimeoutSeconds)*1000
		}
		if err := w.deps.Agents.PublishCommand(ctx, cmd); err != nil {
			backoff := min(time.Duration(1<<min(c.Attempts, 5))*time.Second, maxPublishBackoff)
			if retryErr := w.capabilities.RetryAgentCommand(ctx, c.CommandID, now.Add(backoff)); retryErr != nil && ctx.Err() == nil {
				w.deps.Logger.Warn("record agent command retry failed", "command_id", c.CommandID, "error", retryErr)
			}
			continue
		}
		if err := w.capabilities.MarkAgentCommandPublished(ctx, c.CommandID, now); err != nil && !errors.Is(err, resource.ErrAgentCommandNotFound) {
			// Published but not recorded: the next tick publishes it again,
			// which JetStream deduplicates by command ID.
			w.deps.Logger.Warn("record agent command publish failed", "command_id", c.CommandID, "error", err)
		}
	}
}

// settleOverdue marks targets whose agent never answered as Lost.
func (w *Worker) settleOverdue(ctx context.Context) {
	overdue, err := w.capabilities.ListOverdueCapabilityTargets(ctx, time.Now().UTC(), capabilityRelayBatch)
	if err != nil {
		return
	}
	for _, c := range overdue {
		key := resource.OperationKey{WorkspaceID: c.WorkspaceID, ID: c.OperationID}
		w.completeTarget(ctx, key, c.HostID, resource.TargetLost, &resource.TargetResult{ErrorClass: "no_result"})
		w.finishDetachedIfDone(ctx, key)
	}
}

// HandleAgentResult stores an agent's result for its capability target and
// finishes the Operation when every target is final. The gateway acknowledges
// a terminal result to the agent only after this returns nil (ADR-057).
func (w *Worker) HandleAgentResult(ctx context.Context, result agentv1.Result) error {
	if w.capabilities == nil {
		return errors.New("capability results are not stored by this process")
	}
	c, err := w.capabilities.GetAgentRequest(ctx, result.OperationID)
	if errors.Is(err, resource.ErrAgentCommandNotFound) {
		// Not an Operation of ours (or deleted): acknowledge so the agent stops.
		w.deps.Logger.Info("result for an unknown agent operation acknowledged", "agent_id", result.AgentID, "operation_id", result.OperationID)
		return nil
	}
	if err != nil {
		return err
	}
	if c.AgentID != result.AgentID {
		return errors.New("result from an agent the command was not sent to")
	}
	state, stored, terminal := targetOutcome(result)
	if !terminal {
		return nil // accepted: the target is already Running
	}
	key := resource.OperationKey{WorkspaceID: c.WorkspaceID, ID: c.OperationID}
	err = w.deps.Operations.UpdateOperationTarget(ctx, c.WorkspaceID, c.OperationID, c.HostID, state, stored)
	if err != nil && !errors.Is(err, resource.ErrOperationFinished) {
		return err // not stored: no ack, the agent republishes
	}
	w.finishDetachedIfDone(ctx, key)
	return nil
}

// targetOutcome maps an agent result to a target state (ADR-057 §5).
func targetOutcome(result agentv1.Result) (resource.TargetState, *resource.TargetResult, bool) {
	stored := &resource.TargetResult{Output: string(result.Output), ErrorClass: result.Code}
	switch result.Status {
	case agentv1.StatusSucceeded:
		stored.ErrorClass = ""
		return resource.TargetSucceeded, stored, true
	case agentv1.StatusFailed, agentv1.StatusRejected:
		if stored.ErrorClass == "" {
			stored.ErrorClass = string(result.Status)
		}
		return resource.TargetFailed, stored, true
	case agentv1.StatusTimedOut:
		return resource.TargetTimedOut, &resource.TargetResult{ErrorClass: "timeout"}, true
	case agentv1.StatusCancelled:
		return resource.TargetCancelled, &resource.TargetResult{ErrorClass: "cancelled"}, true
	case agentv1.StatusUnknown:
		// The agent cannot tell whether it ran; never retried.
		return resource.TargetInterrupted, &resource.TargetResult{ErrorClass: "outcome_unknown"}, true
	default:
		return "", nil, false
	}
}
