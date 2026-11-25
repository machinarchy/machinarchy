package store_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

// AUDIT/20 F20-05 (owner decision): the audit trail records intents and
// outcomes, not system-driven progress. Every append serializes on the
// workspace chain lock, so auditing each reconcile and target transition
// made the chain the bottleneck under load.
func testAuditScope(t *testing.T, f *fixture) {
	ctx := context.Background()
	ws := f.newWorkspace(t, "audit-scope")
	now := time.Now().UTC()
	actions := func(targetID string) []string {
		t.Helper()
		rows, err := f.raw(t).QueryContext(ctx, f.pick(`SELECT action FROM audit_events WHERE target_id = ? ORDER BY id`,
			`SELECT action FROM audit_events WHERE target_id = $1 ORDER BY id`), targetID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var action string
			if err := rows.Scan(&action); err != nil {
				t.Fatal(err)
			}
			out = append(out, action)
		}
		return out
	}

	profile, revision, err := resource.NewAccessProfile(ws, "audit-scope", "", resource.AccessProfileSpec{Connector: resource.ConnectorAgent, Enabled: true}, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stores.ResourceProfiles.CreateAccessProfile(ctx, profile, revision); err != nil {
		t.Fatal(err)
	}
	host, err := resource.NewHost(ws, 0, "scoped", nil, resource.HostSpec{AccessProfileID: profile.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stores.ResourceHosts.CreateHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	observeCtx := ctx
	if f.engine == "postgres" {
		lease, ok, err := f.stores.ResourceLeases.AcquireResourceLease(ctx, resource.Lease{Kind: "host", WorkspaceID: ws,
			ID: string(host.Metadata.ID), Owner: "audit-scope"}, time.Now(), time.Minute)
		if err != nil || !ok {
			t.Fatalf("acquire observation lease = %v, %v", ok, err)
		}
		observeCtx = resource.WithLeaseFence(ctx, resource.LeaseFence{Kind: lease.Kind, WorkspaceID: lease.WorkspaceID, ID: lease.ID, Token: lease.Token})
	}
	// A reconcile's status update: not audited.
	ready := []resource.Condition{{Type: "Ready", Status: resource.ConditionTrue, Reason: "SSHVerified", ObservedGeneration: 1, LastTransitionTime: now}}
	if err := f.stores.ResourceHosts.ObserveHost(observeCtx, ws, host.Metadata.ID, 1, resource.HostActive, ready, nil, now); err != nil {
		t.Fatal(err)
	}
	if got := actions(string(host.Metadata.ID)); !slices.Equal(got, []string{domain.AuditResourceHostCreated}) {
		t.Fatalf("host audit after a reconcile = %v, want only the creation", got)
	}
	// A host starting to present another key: audited.
	changed := []resource.Condition{{Type: "Ready", Status: resource.ConditionFalse, Reason: "HostKeyChanged", ObservedGeneration: 1, LastTransitionTime: now}}
	if err := f.stores.ResourceHosts.ObserveHost(observeCtx, ws, host.Metadata.ID, 1, resource.HostActive, changed, nil, now); err != nil {
		t.Fatal(err)
	}
	if got := actions(string(host.Metadata.ID)); !slices.Contains(got, domain.AuditResourceHostKeyChanged) {
		t.Fatalf("host audit after a key change = %v, want %s", got, domain.AuditResourceHostKeyChanged)
	}

	// An Operation: creation and outcome audited; start and target progress not.
	targetHost, err := f.stores.ResourceHosts.GetHost(ctx, ws, host.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	op, err := resource.NewRunOperation(ws, resource.ActorRef{UserID: 1}, resource.OperationSpec{
		Targets: []resource.OperationTargetSpec{{HostID: targetHost.Metadata.ID, HostGeneration: targetHost.Metadata.Generation, DisplayName: "scoped",
			AccessProfileID: profile.ID, AccessProfileRevision: revision.Revision}},
		Run: &resource.RunSpec{Argv: []string{"uptime"}, TimeoutSeconds: 30, Concurrency: 1}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stores.Operations.CreateOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	expected := op.Revision
	if err := op.Start(now); err != nil {
		t.Fatal(err)
	}
	if err := f.stores.Operations.UpdateOperation(ctx, op, expected); err != nil {
		t.Fatal(err)
	}
	if err := f.stores.Operations.UpdateOperationTarget(ctx, ws, op.ID, targetHost.Metadata.ID, resource.TargetRunning, nil); err != nil {
		t.Fatal(err)
	}
	code := 0
	if err := f.stores.Operations.UpdateOperationTarget(ctx, ws, op.ID, targetHost.Metadata.ID, resource.TargetSucceeded, &resource.TargetResult{ExitCode: &code}); err != nil {
		t.Fatal(err)
	}
	if got := actions(string(op.ID)); !slices.Equal(got, []string{domain.AuditOperationCreated}) {
		t.Fatalf("Operation audit while running = %v, want only the creation", got)
	}
	loaded, err := f.stores.Operations.GetOperation(ctx, ws, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected = loaded.Revision
	if err := loaded.Finish(resource.OperationSucceeded, now); err != nil {
		t.Fatal(err)
	}
	if err := f.stores.Operations.UpdateOperation(ctx, loaded, expected); err != nil {
		t.Fatal(err)
	}
	if got := actions(string(op.ID)); !slices.Equal(got, []string{domain.AuditOperationCreated, domain.AuditOperationUpdated}) {
		t.Fatalf("Operation audit after finishing = %v, want creation and outcome", got)
	}
}
