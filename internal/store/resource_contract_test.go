package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/eventing"
)

func TestManagedResourceContract(t *testing.T) {
	for _, engine := range availableEngines(t) {
		t.Run(engine, func(t *testing.T) {
			t.Run("host revision and status", func(t *testing.T) { testResourceHost(t, newFixture(t, engine)) })
			t.Run("operation persistence and CAS", func(t *testing.T) { testResourceOperation(t, newFixture(t, engine)) })
			t.Run("transaction rollback", func(t *testing.T) { testResourceRollback(t, newFixture(t, engine)) })
			t.Run("outbox atomicity", func(t *testing.T) { testOutboxAtomicity(t, newFixture(t, engine)) })
			t.Run("outbox retry and crash recovery", func(t *testing.T) { testOutboxRecovery(t, newFixture(t, engine)) })
			t.Run("resource lease fencing", func(t *testing.T) { testResourceLease(t, newFixture(t, engine)) })
		})
	}
}

func newManagedHost(t *testing.T, f *fixture, name string) *resource.Host {
	t.Helper()
	id, err := resource.NewHost(f.newWorkspace(t, "managed-"+name), 0, name, map[string]string{"tier": "test"}, resource.HostSpec{}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func testResourceHost(t *testing.T, f *fixture) {
	ctx := context.Background()
	host := newManagedHost(t, f, "managed-edge")
	if err := f.stores.ResourceHosts.CreateHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	got, err := f.stores.ResourceHosts.GetHost(ctx, host.Metadata.WorkspaceID, host.Metadata.ID)
	if err != nil || got.Metadata.Revision != 1 || got.Spec.Presence != resource.PresenceManaged {
		t.Fatalf("GetHost = %+v, %v", got, err)
	}
	if err := f.stores.ResourceHosts.UpdateHostMetadata(ctx, host.Metadata.WorkspaceID, host.Metadata.ID, 1, "managed-edge-renamed", 0, got.Metadata.Labels, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.stores.ResourceHosts.UpdateHostSpec(ctx, host.Metadata.WorkspaceID, host.Metadata.ID, 2, 1, resource.HostSpec{Presence: resource.PresenceAbsent}, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err = f.stores.ResourceHosts.GetHost(ctx, host.Metadata.WorkspaceID, host.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata.Generation != 2 || got.Metadata.Revision != 3 {
		t.Fatalf("updated versions = generation %d revision %d", got.Metadata.Generation, got.Metadata.Revision)
	}
	observeCtx := ctx
	if f.engine == "postgres" {
		lease, ok, err := f.stores.ResourceLeases.AcquireResourceLease(ctx, resource.ResourceLease{Kind: "host", WorkspaceID: host.Metadata.WorkspaceID,
			ID: string(host.Metadata.ID), Owner: "contract-test"}, time.Now(), time.Minute)
		if err != nil || !ok {
			t.Fatalf("acquire observation lease = %v, %v", ok, err)
		}
		observeCtx = resource.WithLeaseFence(ctx, resource.LeaseFence{Kind: lease.Kind, WorkspaceID: lease.WorkspaceID, ID: lease.ID, Token: lease.Token})
	}
	if err := f.stores.ResourceHosts.ObserveHost(observeCtx, host.Metadata.WorkspaceID, host.Metadata.ID, 2, resource.HostDeleting, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err = f.stores.ResourceHosts.GetHost(ctx, host.Metadata.WorkspaceID, host.Metadata.ID)
	if err != nil || got.Status.ObservedGeneration != 2 || got.Status.Phase != resource.HostDeleting || got.Metadata.Revision != 4 {
		t.Fatalf("observed Host = %+v, %v", got, err)
	}
	if err := f.stores.ResourceHosts.UpdateHostMetadata(ctx, host.Metadata.WorkspaceID, host.Metadata.ID, 3, "stale", 0, nil, time.Now()); !errors.Is(err, resource.ErrConflict) {
		t.Fatalf("stale update error = %v", err)
	}
	listed, err := f.stores.ResourceHosts.ListHosts(ctx, host.Metadata.WorkspaceID, 10)
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListHosts = %d, %v", len(listed), err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"concurrent-a", "concurrent-b"} {
		wg.Add(1)
		go func(nextName string) {
			defer wg.Done()
			results <- f.stores.ResourceHosts.UpdateHostMetadata(ctx, host.Metadata.WorkspaceID, host.Metadata.ID, 4, nextName, 0, got.Metadata.Labels, time.Now())
		}(name)
	}
	wg.Wait()
	close(results)
	wins := 0
	for result := range results {
		if result == nil {
			wins++
		} else if !errors.Is(result, resource.ErrConflict) {
			t.Fatalf("concurrent update error = %v", result)
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent updates succeeded %d times, want exactly one", wins)
	}
	if err := f.stores.ResourceHosts.DeleteHost(ctx, host.Metadata.WorkspaceID, host.Metadata.ID, 3); !errors.Is(err, resource.ErrConflict) {
		t.Fatalf("stale delete error = %v", err)
	}
	if err := f.stores.ResourceHosts.DeleteHost(ctx, host.Metadata.WorkspaceID, host.Metadata.ID, 5); err != nil {
		t.Fatal(err)
	}
}

func testResourceOperation(t *testing.T, f *fixture) {
	ctx := context.Background()
	workspaceID := f.newWorkspace(t, "operation-workspace")
	hostID, err := resource.NewResourceID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	op, err := resource.NewRunOperation(workspaceID, resource.ActorRef{UserID: 7}, resource.OperationSpec{
		Targets: []resource.OperationTargetSpec{{HostID: hostID, HostGeneration: 1, DisplayName: "target"}},
		Run:     &resource.RunSpec{Argv: []string{"/usr/bin/printf", "hello"}, TimeoutSeconds: 30, Concurrency: 1},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stores.Operations.CreateOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	var encrypted string
	if err := f.stores.DB.QueryRowContext(ctx, f.pick("SELECT spec_encrypted FROM operations WHERE id = ?", "SELECT spec_encrypted FROM operations WHERE id = $1"), string(op.ID)).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encrypted, "printf") {
		t.Fatal("Operation spec was persisted without encryption")
	}
	loaded, err := f.stores.Operations.GetOperation(ctx, workspaceID, op.ID)
	if err != nil || loaded.Spec.Run.Argv[0] != "/usr/bin/printf" {
		t.Fatalf("GetOperation = %+v, %v", loaded, err)
	}
	if err := loaded.Start(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := loaded.SetTargetState(hostID, resource.TargetRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := loaded.SetTargetState(hostID, resource.TargetSucceeded, &resource.TargetResult{ExitCode: intPtr(0), Output: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Finish(resource.OperationSucceeded, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := f.stores.Operations.UpdateOperation(ctx, loaded, 1); err != nil {
		t.Fatal(err)
	}
	loaded, err = f.stores.Operations.GetOperation(ctx, workspaceID, op.ID)
	if err != nil || loaded.Status.State != resource.OperationSucceeded || loaded.Status.Targets[0].Result.Output != "hello" {
		t.Fatalf("persisted Operation = %+v, %v", loaded, err)
	}
	var encryptedOutput string
	if err := f.stores.DB.QueryRowContext(ctx, f.pick("SELECT output FROM operation_targets WHERE operation_id = ?", "SELECT output FROM operation_targets WHERE operation_id = $1"), string(op.ID)).Scan(&encryptedOutput); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encryptedOutput, "hello") {
		t.Fatal("Operation output was persisted without encryption")
	}
	stale, err := f.stores.Operations.GetOperation(ctx, workspaceID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.stores.Operations.GetOperation(ctx, workspaceID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.RequestCancel(now.Add(3 * time.Second)); !errors.Is(err, resource.ErrOperationFinished) {
		t.Fatalf("finished cancel error = %v", err)
	}
	if err := f.stores.Operations.UpdateOperation(ctx, stale, 1); !errors.Is(err, resource.ErrConflict) {
		t.Fatalf("stale Operation update error = %v", err)
	}
}

func testResourceRollback(t *testing.T, f *fixture) {
	ctx := context.Background()
	workspaceID := f.newWorkspace(t, "rollback-workspace")
	installFailingAuditTrigger(t, f)
	host, err := resource.NewHost(workspaceID, 0, "rollback-host", nil, resource.HostSpec{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	err = f.stores.ResourceHosts.CreateHost(ctx, host)
	if err == nil {
		t.Fatal("CreateHost unexpectedly passed failing audit trigger")
	}
	if _, err := f.stores.ResourceHosts.GetHost(ctx, workspaceID, host.Metadata.ID); !errors.Is(err, resource.ErrHostNotFound) {
		t.Fatalf("Host after rolled-back transaction: %v", err)
	}
	var outboxCount int
	if err := f.stores.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox_events`).Scan(&outboxCount); err != nil || outboxCount != 0 {
		t.Fatalf("outbox rows after rollback = %d, %v", outboxCount, err)
	}
}

func testOutboxAtomicity(t *testing.T, f *fixture) {
	ctx := context.Background()
	workspaceID := f.newWorkspace(t, "outbox-atomicity")
	if f.engine == "sqlite" {
		_, err := f.stores.DB.Exec(`CREATE TRIGGER fail_host_outbox BEFORE INSERT ON outbox_events WHEN NEW.event_type = 'machinarchy.host.created' BEGIN SELECT RAISE(ABORT, 'forced outbox failure'); END`)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		_, err := f.stores.DB.Exec(`CREATE FUNCTION fail_host_outbox_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type = 'machinarchy.host.created' THEN RAISE EXCEPTION 'forced outbox failure'; END IF; RETURN NEW; END $$`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.stores.DB.Exec(`CREATE TRIGGER fail_host_outbox BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION fail_host_outbox_fn()`)
		if err != nil {
			t.Fatal(err)
		}
	}
	host, err := resource.NewHost(workspaceID, 0, "atomic-host", nil, resource.HostSpec{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stores.ResourceHosts.CreateHost(ctx, host); err == nil {
		t.Fatal("Host creation unexpectedly committed despite outbox failure")
	}
	if _, err := f.stores.ResourceHosts.GetHost(ctx, workspaceID, host.Metadata.ID); !errors.Is(err, resource.ErrHostNotFound) {
		t.Fatalf("Host remained after outbox transaction failure: %v", err)
	}
	var events, audits int
	if err := f.stores.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := f.stores.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE target_type = 'resource_host'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if events != 0 || audits != 0 {
		t.Fatalf("transaction left events=%d audit rows=%d after failure", events, audits)
	}
}

func testOutboxRecovery(t *testing.T, f *fixture) {
	ctx := context.Background()
	workspaceID := f.newWorkspace(t, "outbox-workspace")
	now := time.Now().UTC()
	host, err := resource.NewHost(workspaceID, 0, "outbox-host", nil, resource.HostSpec{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stores.ResourceHosts.CreateHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	if err := f.stores.ResourceHosts.UpdateHostSpec(ctx, workspaceID, host.Metadata.ID, 1, 1,
		resource.HostSpec{Presence: resource.PresenceAbsent}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// The next change intentionally restores desired presence. The queue consumer
	// must treat the older deletion notice as only a key wake-up.
	if err := f.stores.ResourceHosts.UpdateHostSpec(ctx, workspaceID, host.Metadata.ID, 2, 2,
		resource.HostSpec{Presence: resource.PresenceManaged}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	keyPage, err := resource.HostKeyLister(f.stores.ResourceHosts).ListHostKeys(ctx, resource.HostKeyCursor{Limit: 20})
	if err != nil || len(keyPage) != 1 || keyPage[0].WorkspaceID != workspaceID || keyPage[0].ID != host.Metadata.ID {
		t.Fatalf("startup resync page = %+v, %v", keyPage, err)
	}
	first, err := f.stores.Outbox.Claim(ctx, eventing.ConsumerControlManager, "manager-a", now.Add(3*time.Second), 10*time.Second, 3)
	if err != nil || len(first) != 3 {
		t.Fatalf("first outbox claim = %d, %v", len(first), err)
	}
	if first[0].Attempt != 1 {
		t.Fatalf("first delivery attempt = %d, want 1", first[0].Attempt)
	}
	if first[0].Envelope.Type != eventing.HostCreated || first[0].Envelope.Subject.Generation != 1 {
		t.Fatalf("first envelope = %+v", first[0].Envelope)
	}
	if first[1].Envelope.Type != eventing.HostDeletionRequested || first[2].Envelope.Type != eventing.HostSpecChanged ||
		first[2].Envelope.Subject.Generation != 3 {
		t.Fatalf("ordered Host event facts = %+v", first)
	}
	// Simulate a relay crash after queue admission but before ACK. The lease
	// prevents immediate duplicate claim, then expiry makes delivery retryable.
	if claimed, err := f.stores.Outbox.Claim(ctx, eventing.ConsumerControlManager, "manager-b", now.Add(4*time.Second), 10*time.Second, 1); err != nil || len(claimed) != 0 {
		t.Fatalf("claim before lease expiry = %d, %v", len(claimed), err)
	}
	retried, err := f.stores.Outbox.Claim(ctx, eventing.ConsumerControlManager, "manager-b", now.Add(14*time.Second), 10*time.Second, 3)
	if err != nil || len(retried) != 3 || retried[0].Envelope.ID != first[0].Envelope.ID {
		t.Fatalf("recovered outbox claim = %+v, %v", retried, err)
	}
	if retried[0].Attempt != 2 {
		t.Fatalf("recovered delivery attempt = %d, want 2", retried[0].Attempt)
	}
	if err := f.stores.Outbox.Retry(ctx, eventing.ConsumerControlManager, "manager-b", retried[0].Envelope.ID, now.Add(30*time.Second), "temporary"); err != nil {
		t.Fatal(err)
	}
	for _, event := range retried[1:] {
		if err := f.stores.Outbox.Acknowledge(ctx, eventing.ConsumerControlManager, "manager-b", event.Envelope.ID, now.Add(15*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if claimed, err := f.stores.Outbox.Claim(ctx, eventing.ConsumerControlManager, "manager-c", now.Add(20*time.Second), 10*time.Second, 1); err != nil || len(claimed) != 0 {
		t.Fatalf("claim before retry time = %d, %v", len(claimed), err)
	}
	ready, err := f.stores.Outbox.Claim(ctx, eventing.ConsumerControlManager, "manager-c", now.Add(31*time.Second), 10*time.Second, 10)
	if err != nil || len(ready) != 1 {
		t.Fatalf("claim due events = %d, %v", len(ready), err)
	}
	for _, event := range ready {
		if err := f.stores.Outbox.Acknowledge(ctx, eventing.ConsumerControlManager, "manager-c", event.Envelope.ID, now.Add(32*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if claimed, err := f.stores.Outbox.Claim(ctx, eventing.ConsumerControlManager, "manager-d", now.Add(time.Minute), 10*time.Second, 10); err != nil || len(claimed) != 0 {
		t.Fatalf("claim after ACK = %d, %v", len(claimed), err)
	}
	pruned, err := f.stores.Outbox.Prune(ctx, now.Add(8*24*time.Hour))
	if err != nil || pruned != 3 {
		t.Fatalf("pruned delivered events = %d, %v", pruned, err)
	}
}

func testResourceLease(t *testing.T, f *fixture) {
	if f.engine != "postgres" {
		if _, ok, err := f.stores.ResourceLeases.AcquireResourceLease(context.Background(), resource.ResourceLease{}, time.Now(), time.Second); !errors.Is(err, resource.ErrLeaseUnsupported) || ok {
			t.Fatalf("SQLite lease acquisition = %v, %v", ok, err)
		}
		return
	}
	ctx := context.Background()
	workspaceID := f.newWorkspace(t, "lease-workspace")
	id, err := resource.NewResourceID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	first, ok, err := f.stores.ResourceLeases.AcquireResourceLease(ctx, resource.ResourceLease{Kind: "host", WorkspaceID: workspaceID, ID: string(id), Owner: "manager-a"}, now, time.Second)
	if err != nil || !ok || first.Token != 1 {
		t.Fatalf("first lease = %+v, %v, %v", first, ok, err)
	}
	if _, ok, err := f.stores.ResourceLeases.AcquireResourceLease(ctx, resource.ResourceLease{Kind: "host", WorkspaceID: workspaceID, ID: string(id), Owner: "manager-b"}, now.Add(time.Millisecond), time.Second); err != nil || ok {
		t.Fatalf("contended lease acquired = %v, %v", ok, err)
	}
	renewed, ok, err := f.stores.ResourceLeases.RenewResourceLease(ctx, first, now.Add(100*time.Millisecond), time.Second)
	if err != nil || !ok || renewed.Token != first.Token {
		t.Fatalf("renew lease = %+v, %v, %v", renewed, ok, err)
	}
	second, ok, err := f.stores.ResourceLeases.AcquireResourceLease(ctx, resource.ResourceLease{Kind: "host", WorkspaceID: workspaceID, ID: string(id), Owner: "manager-b"}, now.Add(2*time.Second), time.Second)
	if err != nil || !ok || second.Token != first.Token+1 {
		t.Fatalf("expired lease takeover = %+v, %v, %v", second, ok, err)
	}
	if _, ok, err := f.stores.ResourceLeases.RenewResourceLease(ctx, first, now.Add(2*time.Second), time.Second); err != nil || ok {
		t.Fatalf("stale lease renewal = %v, %v", ok, err)
	}
	if err := f.stores.ResourceLeases.ReleaseResourceLease(ctx, second); err != nil {
		t.Fatal(err)
	}
}

func installFailingAuditTrigger(t *testing.T, f *fixture) {
	t.Helper()
	if f.engine == "sqlite" {
		_, err := f.stores.DB.Exec(`CREATE TRIGGER fail_resource_audit BEFORE INSERT ON audit_events WHEN NEW.target_type = 'resource_host' BEGIN SELECT RAISE(ABORT, 'forced audit failure'); END`)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	_, err := f.stores.DB.Exec(`CREATE FUNCTION fail_resource_audit_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type = 'resource_host' THEN RAISE EXCEPTION 'forced audit failure'; END IF; RETURN NEW; END $$`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.stores.DB.Exec(`CREATE TRIGGER fail_resource_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION fail_resource_audit_fn()`)
	if err != nil {
		t.Fatal(err)
	}
}

func intPtr(value int) *int { return &value }
