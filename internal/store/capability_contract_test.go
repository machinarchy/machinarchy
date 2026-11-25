package store_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

// ADR-057: per-host enrolment binds agents to hosts;
// capability Operations persist their kind; dispatch, relay states, result
// lookup, overdue detection and cancel commands work on both engines.
func testCapabilityOperations(t *testing.T, f *fixture) {
	ctx := context.Background()
	ws := domain.DefaultWorkspaceID // agents and enrolment tokens live in the default workspace
	owner := f.member(t, "capability-owner", domain.RoleOwner)
	now := time.Now().UTC()

	profile, revision, err := resource.NewAccessProfile(ws, "agents", "", resource.AccessProfileSpec{Connector: resource.ConnectorAgent, Enabled: true}, owner.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stores.ResourceProfiles.CreateAccessProfile(ctx, profile, revision); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resource.NewAccessProfile(ws, "bad", "", resource.AccessProfileSpec{Connector: resource.ConnectorAgent, Enabled: true,
		Username: "root"}, owner.ID, now); err == nil {
		t.Fatal("an agent profile with a username was accepted")
	}
	var hosts []*resource.Host
	for i := range 2 {
		host, err := resource.NewHost(ws, 0, fmt.Sprintf("agent-host-%d", i), nil, resource.HostSpec{AccessProfileID: profile.ID}, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.stores.ResourceHosts.CreateHost(ctx, host); err != nil {
			t.Fatal(err)
		}
		hosts = append(hosts, host)
	}

	// Enrolment with a per-host token binds the agent to that host.
	enrol := func(host *resource.Host, seed byte, request string) agentgateway.Agent {
		t.Helper()
		secret, token, err := agentgateway.NewEnrollmentToken("", 0, owner.ID, now)
		if err != nil {
			t.Fatal(err)
		}
		token.HostID = string(host.Metadata.ID)
		if err := f.stores.Agents.CreateEnrollmentToken(ctx, token); err != nil {
			t.Fatal(err)
		}
		pub := ed25519.NewKeyFromSeed(append(make([]byte, ed25519.SeedSize-1), seed)).Public().(ed25519.PublicKey)
		agent, err := f.stores.Agents.RegisterAgent(ctx, agentgateway.Agent{ID: fmt.Sprintf("agent-%d", seed), Hostname: "h", PublicKey: pub,
			LeaseID: "lease", LeaseExpiresAt: now.Add(time.Minute), RegisteredAt: now, LastSeenAt: now}, request,
			agentgateway.Enrollment{TokenHash: agentgateway.HashEnrollmentToken(secret), Hostname: "h"})
		if err != nil {
			t.Fatal(err)
		}
		return agent
	}
	if _, err := f.stores.Agents.HostAgent(ctx, ws, string(hosts[0].Metadata.ID)); !errors.Is(err, agentgateway.ErrHostNotBound) {
		t.Fatalf("unbound host = %v", err)
	}
	// Binding and revocation queue the host for reconciliation, so its Ready
	// condition follows at once instead of at the next resync (F17-04).
	hostEvents := func(host *resource.Host) int {
		t.Helper()
		var n int
		if err := f.raw(t).QueryRowContext(ctx, f.pick(`SELECT count(*) FROM outbox_events e JOIN outbox_deliveries d ON d.event_id = e.event_id
			WHERE e.subject_id = ? AND d.consumer_group = 'control-manager'`, `SELECT count(*) FROM outbox_events e JOIN outbox_deliveries d ON d.event_id = e.event_id
			WHERE e.subject_id = $1 AND d.consumer_group = 'control-manager'`), string(host.Metadata.ID)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	beforeBind := hostEvents(hosts[1])
	first := enrol(hosts[0], 1, "req-1")
	second := enrol(hosts[1], 2, "req-2")
	if got := hostEvents(hosts[1]); got != beforeBind+1 {
		t.Fatalf("binding queued %d reconcile events for the host, want 1", got-beforeBind)
	}
	if bound, err := f.stores.Agents.HostAgent(ctx, ws, string(hosts[0].Metadata.ID)); err != nil || bound.ID != first.ID {
		t.Fatalf("host 0 bound to %+v, %v", bound, err)
	}
	// Replacing the machine: a new agent enrolled for host 0 takes it over.
	replacement := enrol(hosts[0], 3, "req-3")
	if bound, err := f.stores.Agents.HostAgent(ctx, ws, string(hosts[0].Metadata.ID)); err != nil || bound.ID != replacement.ID {
		t.Fatalf("after re-enrolment host 0 bound to %+v, %v", bound, err)
	}
	// A revoked agent no longer serves its host.
	beforeRevoke := hostEvents(hosts[1])
	if err := f.stores.Agents.RevokeAgent(ctx, second.ID, now); err != nil {
		t.Fatal(err)
	}
	if got := hostEvents(hosts[1]); got != beforeRevoke+1 {
		t.Fatalf("revoking the agent queued %d reconcile events for its host, want 1", got-beforeRevoke)
	}
	if _, err := f.stores.Agents.HostAgent(ctx, ws, string(hosts[1].Metadata.ID)); !errors.Is(err, agentgateway.ErrHostNotBound) {
		t.Fatalf("host of a revoked agent = %v", err)
	}
	// A token for a host that does not exist is refused.
	_, ghost, _ := agentgateway.NewEnrollmentToken("", 0, owner.ID, now)
	ghost.HostID = "00000000-0000-4000-8000-000000000000"
	if err := f.stores.Agents.CreateEnrollmentToken(ctx, ghost); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("token for an unknown host = %v", err)
	}

	// A capability Operation round-trips with its kind and spec.
	var targets []resource.OperationTargetSpec
	for _, host := range hosts {
		targets = append(targets, resource.OperationTargetSpec{HostID: host.Metadata.ID, HostGeneration: host.Metadata.Generation,
			DisplayName: host.Metadata.Name, AccessProfileID: profile.ID, AccessProfileRevision: revision.Revision})
	}
	op, err := resource.NewCapabilityOperation(ws, resource.ActorRef{UserID: owner.ID}, resource.OperationSpec{Targets: targets,
		Capability: &resource.CapabilitySpec{Name: "service.status", Input: json.RawMessage(`{"service":"nginx.service"}`), TimeoutSeconds: 30}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stores.Operations.CreateOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	capabilities := f.stores.Operations.(resource.CapabilityRepository)
	command := resource.AgentCommand{CommandID: "cmd-aaaa", OperationID: op.ID, HostID: hosts[0].Metadata.ID, WorkspaceID: ws,
		AgentID: replacement.ID, AgentOperationID: "op-aaaa", Type: resource.AgentCommandRequest, CreatedAt: now,
		ExpiresAt: now.Add(5 * time.Minute), ResultDueAt: now.Add(10 * time.Minute)}
	if err := capabilities.DispatchCapabilityTarget(ctx, command); !errors.Is(err, resource.ErrOperationFinished) {
		t.Fatalf("dispatch on a queued Operation = %v, want ErrOperationFinished", err)
	}
	expected := op.Revision
	if err := op.Start(now); err != nil {
		t.Fatal(err)
	}
	if err := f.stores.Operations.UpdateOperation(ctx, op, expected); err != nil {
		t.Fatal(err)
	}
	if err := capabilities.DispatchCapabilityTarget(ctx, command); err != nil {
		t.Fatal(err)
	}
	if err := capabilities.DispatchCapabilityTarget(ctx, command); err == nil {
		t.Fatal("a target was dispatched twice")
	}
	stored, err := f.stores.Operations.GetOperation(ctx, ws, op.ID)
	if err != nil || stored.Kind != resource.OperationKindCapability || stored.Spec.Capability == nil ||
		stored.Spec.Capability.Name != "service.status" || string(stored.Spec.Capability.Input) != `{"service":"nginx.service"}` ||
		stored.Status.Targets[0].State != resource.TargetRunning || stored.Status.Targets[1].State != resource.TargetPending {
		t.Fatalf("stored capability Operation = %+v, %v", stored, err)
	}

	// The relay: due, retried with backoff, published once.
	due, err := capabilities.ListDueAgentCommands(ctx, now, 10)
	if err != nil || len(due) != 1 || due[0].CommandID != "cmd-aaaa" || due[0].State != resource.AgentCommandPending {
		t.Fatalf("due commands = %+v, %v", due, err)
	}
	if err := capabilities.RetryAgentCommand(ctx, "cmd-aaaa", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if due, _ := capabilities.ListDueAgentCommands(ctx, now, 10); len(due) != 0 {
		t.Fatalf("a command due later was listed now: %+v", due)
	}
	if err := capabilities.MarkAgentCommandPublished(ctx, "cmd-aaaa", now); err != nil {
		t.Fatal(err)
	}
	if err := capabilities.MarkAgentCommandPublished(ctx, "cmd-aaaa", now); !errors.Is(err, resource.ErrAgentCommandNotFound) {
		t.Fatalf("second publish mark = %v", err)
	}
	if due, _ := capabilities.ListDueAgentCommands(ctx, now.Add(time.Hour), 10); len(due) != 0 {
		t.Fatalf("a published command is still due: %+v", due)
	}

	// Results find their command; overdue targets are listed.
	request, err := capabilities.GetAgentRequest(ctx, "op-aaaa")
	if err != nil || request.AgentID != replacement.ID || request.HostID != hosts[0].Metadata.ID || request.Attempts != 2 {
		t.Fatalf("agent request = %+v, %v", request, err)
	}
	if _, err := capabilities.GetAgentRequest(ctx, "op-unknown"); !errors.Is(err, resource.ErrAgentCommandNotFound) {
		t.Fatalf("unknown agent operation = %v", err)
	}
	if overdue, _ := capabilities.ListOverdueCapabilityTargets(ctx, now.Add(5*time.Minute), 10); len(overdue) != 0 {
		t.Fatalf("overdue before the due time: %+v", overdue)
	}
	if overdue, err := capabilities.ListOverdueCapabilityTargets(ctx, now.Add(11*time.Minute), 10); err != nil || len(overdue) != 1 {
		t.Fatalf("overdue after the due time: %+v, %v", overdue, err)
	}

	// Cancel adds one cancel command per running dispatched target, once.
	if n, err := capabilities.AddCancelCommands(ctx, ws, op.ID, now, 5*time.Minute); err != nil || n != 1 {
		t.Fatalf("cancel commands = %d, %v", n, err)
	}
	if n, _ := capabilities.AddCancelCommands(ctx, ws, op.ID, now, 5*time.Minute); n != 0 {
		t.Fatalf("cancel commands added again: %d", n)
	}
	due, _ = capabilities.ListDueAgentCommands(ctx, now, 10)
	if len(due) != 1 || due[0].Type != resource.AgentCommandCancel || due[0].AgentOperationID != "op-aaaa" {
		t.Fatalf("cancel command = %+v", due)
	}
	if err := capabilities.ExpireAgentCommand(ctx, due[0].CommandID); err != nil {
		t.Fatal(err)
	}

	// The target's result, once stored, ends it: no longer overdue.
	if err := f.stores.Operations.UpdateOperationTarget(ctx, ws, op.ID, hosts[0].Metadata.ID, resource.TargetSucceeded,
		&resource.TargetResult{Output: `{"active_state":"active"}`}); err != nil {
		t.Fatal(err)
	}
	if overdue, _ := capabilities.ListOverdueCapabilityTargets(ctx, now.Add(11*time.Minute), 10); len(overdue) != 0 {
		t.Fatalf("a finished target is overdue: %+v", overdue)
	}
}

// AUDIT G-13C-01: RequestHostRecheck queues one reconcile event for the Host
// without changing the Host, and ignores a Host that no longer exists.
func testHostRecheck(t *testing.T, f *fixture) {
	ctx := context.Background()
	ws := domain.DefaultWorkspaceID
	owner := f.member(t, "recheck-owner", domain.RoleOwner)
	now := time.Now().UTC()
	profile, revision, err := resource.NewAccessProfile(ws, "recheck", "", resource.AccessProfileSpec{Connector: resource.ConnectorAgent, Enabled: true}, owner.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stores.ResourceProfiles.CreateAccessProfile(ctx, profile, revision); err != nil {
		t.Fatal(err)
	}
	host, err := resource.NewHost(ws, 0, "recheck-host", nil, resource.HostSpec{AccessProfileID: profile.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stores.ResourceHosts.CreateHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	events := func() int {
		t.Helper()
		var n int
		if err := f.raw(t).QueryRowContext(ctx, f.pick(`SELECT count(*) FROM outbox_events e JOIN outbox_deliveries d ON d.event_id = e.event_id
			WHERE e.subject_id = ? AND d.consumer_group = 'control-manager'`, `SELECT count(*) FROM outbox_events e JOIN outbox_deliveries d ON d.event_id = e.event_id
			WHERE e.subject_id = $1 AND d.consumer_group = 'control-manager'`), string(host.Metadata.ID)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	rechecker, ok := f.stores.ResourceHosts.(interface {
		RequestHostRecheck(context.Context, int, resource.ID, string) error
	})
	if !ok {
		t.Fatal("the Host store does not implement RequestHostRecheck")
	}
	before := events()
	if err := rechecker.RequestHostRecheck(ctx, ws, host.Metadata.ID, "host-key-rejected"); err != nil {
		t.Fatal(err)
	}
	if got := events(); got != before+1 {
		t.Fatalf("a recheck queued %d events, want 1", got-before)
	}
	after, err := f.stores.ResourceHosts.GetHost(ctx, ws, host.Metadata.ID)
	if err != nil || after.Metadata.Revision != host.Metadata.Revision || after.Metadata.Generation != host.Metadata.Generation {
		t.Fatalf("the Host changed: %+v, %v", after, err)
	}
	missing, _ := resource.NewResourceID()
	if err := rechecker.RequestHostRecheck(ctx, ws, missing, "host-key-rejected"); err != nil {
		t.Fatalf("recheck of a missing Host = %v, want nil", err)
	}
}
