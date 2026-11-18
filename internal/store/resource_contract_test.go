package store_test

import (
	"context"
	"errors"
	"fmt"
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
			t.Run("host-key observations and trust", func(t *testing.T) { testHostKeyTrust(t, newFixture(t, engine)) })
			t.Run("Access Profile revision isolation and outbox", func(t *testing.T) { testAccessProfile(t, newFixture(t, engine)) })
			t.Run("operation persistence and CAS", func(t *testing.T) { testResourceOperation(t, newFixture(t, engine)) })
			t.Run("operation target writes", func(t *testing.T) { testOperationTargetWrites(t, newFixture(t, engine)) })
			t.Run("listing and keyset pagination", func(t *testing.T) { testResourceListing(t, newFixture(t, engine)) })
			t.Run("transaction rollback", func(t *testing.T) { testResourceRollback(t, newFixture(t, engine)) })
			t.Run("outbox atomicity", func(t *testing.T) { testOutboxAtomicity(t, newFixture(t, engine)) })
			t.Run("outbox retry and crash recovery", func(t *testing.T) { testOutboxRecovery(t, newFixture(t, engine)) })
			t.Run("resource lease fencing", func(t *testing.T) { testResourceLease(t, newFixture(t, engine)) })
		})
	}
}

func testAccessProfile(t *testing.T, f *fixture) {
	ctx := context.Background()
	workspace := f.newWorkspace(t, "profiles")
	profile, first, err := resource.NewAccessProfile(workspace, "deploy", "SSH access", resource.AccessProfileSpec{
		Connector: resource.ConnectorSSH, Username: "deploy", Authentication: resource.AuthenticationPassword,
		Credential: resource.SecretReference{ID: "secret-credential", Version: 4}, Enabled: true,
	}, 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err = f.stores.ResourceProfiles.CreateAccessProfile(ctx, profile, first); err != nil {
		t.Fatal(err)
	}
	got, err := f.stores.ResourceProfiles.GetAccessProfile(ctx, workspace, profile.ID)
	if err != nil || got.CurrentRevision != 1 {
		t.Fatalf("profile=%+v err=%v", got, err)
	}
	if _, err = f.stores.ResourceProfiles.UpdateAccessProfile(ctx, workspace, profile.ID, 1, first.Spec, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.stores.ResourceProfiles.UpdateAccessProfile(ctx, workspace, profile.ID, 1, first.Spec, 1, time.Now()); !errors.Is(err, resource.ErrConflict) {
		t.Fatalf("stale profile update=%v", err)
	}
	rev1, err := f.stores.ResourceProfiles.GetAccessProfileRevision(ctx, workspace, profile.ID, 1)
	if err != nil || rev1.Spec.Credential.Version != 4 {
		t.Fatalf("immutable revision=%+v err=%v", rev1, err)
	}
	host, err := resource.NewHost(workspace, 0, "profile-host", nil, resource.HostSpec{AccessProfileID: profile.ID,
		AccessTarget: resource.HostAccessTarget{Endpoint: "host.example:22", Trust: []string{"SHA256:host-key"}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = f.stores.ResourceHosts.CreateHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	storedHost, err := f.stores.ResourceHosts.GetHost(ctx, workspace, host.Metadata.ID)
	if err != nil || storedHost.Spec.AccessTarget.Endpoint != host.Spec.AccessTarget.Endpoint || len(storedHost.Spec.AccessTarget.Trust) != 1 {
		t.Fatalf("persisted Host access target=%+v err=%v", storedHost, err)
	}
	keys, err := f.stores.ResourceProfiles.ListHostsByAccessProfile(ctx, workspace, profile.ID)
	if err != nil || len(keys) != 1 {
		t.Fatalf("profile references=%v err=%v", keys, err)
	}
	if err = f.stores.ResourceProfiles.DeleteAccessProfile(ctx, workspace, profile.ID, 1, time.Now()); !errors.Is(err, resource.ErrConflict) {
		t.Fatalf("referenced profile deletion=%v", err)
	}
	if workspaceOther := f.newWorkspace(t, "other-profile-workspace"); workspaceOther == workspace {
		t.Fatal("workspace fixture reused identity")
	} else {
		foreignHost, createErr := resource.NewHost(workspaceOther, 0, "foreign-profile-host", nil, resource.HostSpec{AccessProfileID: profile.ID}, time.Now())
		if createErr != nil {
			t.Fatal(createErr)
		}
		if createErr = f.stores.ResourceHosts.CreateHost(ctx, foreignHost); !errors.Is(createErr, resource.ErrAccessProfileNotFound) {
			t.Fatalf("cross-workspace profile assignment=%v", createErr)
		}
	}
	var events int
	db := f.raw(t)
	if err = db.QueryRowContext(ctx, f.pick(`SELECT COUNT(*) FROM outbox_events WHERE event_type=?`, `SELECT COUNT(*) FROM outbox_events WHERE event_type=$1`), eventing.AccessProfileChanged).Scan(&events); err != nil || events != 2 {
		t.Fatalf("profile outbox events=%d err=%v", events, err)
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
		lease, ok, err := f.stores.ResourceLeases.AcquireResourceLease(ctx, resource.Lease{Kind: "host", WorkspaceID: host.Metadata.WorkspaceID,
			ID: string(host.Metadata.ID), Owner: "contract-test"}, time.Now(), time.Minute)
		if err != nil || !ok {
			t.Fatalf("acquire observation lease = %v, %v", ok, err)
		}
		observeCtx = resource.WithLeaseFence(ctx, resource.LeaseFence{Kind: lease.Kind, WorkspaceID: lease.WorkspaceID, ID: lease.ID, Token: lease.Token})
	}
	if err := f.stores.ResourceHosts.ObserveHost(observeCtx, host.Metadata.WorkspaceID, host.Metadata.ID, 2, resource.HostDeleting, nil, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err = f.stores.ResourceHosts.GetHost(ctx, host.Metadata.WorkspaceID, host.Metadata.ID)
	if err != nil || got.Status.ObservedGeneration != 2 || got.Status.Phase != resource.HostDeleting || got.Metadata.Revision != 4 {
		t.Fatalf("observed Host = %+v, %v", got, err)
	}
	if err := f.stores.ResourceHosts.UpdateHostMetadata(ctx, host.Metadata.WorkspaceID, host.Metadata.ID, 3, "stale", 0, nil, time.Now()); !errors.Is(err, resource.ErrConflict) {
		t.Fatalf("stale update error = %v", err)
	}
	listed, err := f.stores.ResourceHosts.ListHosts(ctx, resource.HostListFilter{WorkspaceID: host.Metadata.WorkspaceID, Limit: 10})
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
	now := time.Now().UTC()
	profile, profileRevision, err := resource.NewAccessProfile(workspaceID, "operation-profile", "", resource.AccessProfileSpec{Connector: resource.ConnectorSSH,
		Username: "deploy", Authentication: resource.AuthenticationPassword, Credential: resource.SecretReference{ID: "operation-secret", Version: 1}, Enabled: true}, 7, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.stores.ResourceProfiles.CreateAccessProfile(ctx, profile, profileRevision); err != nil {
		t.Fatal(err)
	}
	host, err := resource.NewHost(workspaceID, 0, "operation-host", nil, resource.HostSpec{AccessProfileID: profile.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.stores.ResourceHosts.CreateHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	hostID := host.Metadata.ID
	op, err := resource.NewRunOperation(workspaceID, resource.ActorRef{UserID: 7}, resource.OperationSpec{
		Targets: []resource.OperationTargetSpec{{HostID: hostID, HostGeneration: host.Metadata.Generation, DisplayName: "target", AccessProfileID: profile.ID, AccessProfileRevision: profileRevision.Revision}},
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
		if _, ok, err := f.stores.ResourceLeases.AcquireResourceLease(context.Background(), resource.Lease{}, time.Now(), time.Second); !errors.Is(err, resource.ErrLeaseUnsupported) || ok {
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
	first, ok, err := f.stores.ResourceLeases.AcquireResourceLease(ctx, resource.Lease{Kind: "host", WorkspaceID: workspaceID, ID: string(id), Owner: "manager-a"}, now, time.Second)
	if err != nil || !ok || first.Token != 1 {
		t.Fatalf("first lease = %+v, %v, %v", first, ok, err)
	}
	if _, ok, err := f.stores.ResourceLeases.AcquireResourceLease(ctx, resource.Lease{Kind: "host", WorkspaceID: workspaceID, ID: string(id), Owner: "manager-b"}, now.Add(time.Millisecond), time.Second); err != nil || ok {
		t.Fatalf("contended lease acquired = %v, %v", ok, err)
	}
	renewed, ok, err := f.stores.ResourceLeases.RenewResourceLease(ctx, first, now.Add(100*time.Millisecond), time.Second)
	if err != nil || !ok || renewed.Token != first.Token {
		t.Fatalf("renew lease = %+v, %v, %v", renewed, ok, err)
	}
	second, ok, err := f.stores.ResourceLeases.AcquireResourceLease(ctx, resource.Lease{Kind: "host", WorkspaceID: workspaceID, ID: string(id), Owner: "manager-b"}, now.Add(2*time.Second), time.Second)
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

// testOperationTargetWrites covers AUDIT Q-02 and Q-03: target outcomes are
// recorded per row without bumping the Operation revision, terminal targets
// are final, and an Operation-level update holding an older view (such as a
// cancellation racing a completion) can neither move a target backwards nor
// overwrite a stored result.
func testOperationTargetWrites(t *testing.T, f *fixture) {
	ctx := context.Background()
	workspaceID := f.newWorkspace(t, "target-workspace")
	now := time.Now().UTC()
	profile, profileRevision, err := resource.NewAccessProfile(workspaceID, "target-profile", "", resource.AccessProfileSpec{Connector: resource.ConnectorSSH,
		Username: "deploy", Authentication: resource.AuthenticationPassword, Credential: resource.SecretReference{ID: "target-secret", Version: 1}, Enabled: true}, 7, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.stores.ResourceProfiles.CreateAccessProfile(ctx, profile, profileRevision); err != nil {
		t.Fatal(err)
	}
	var targets []resource.OperationTargetSpec
	var hostIDs []resource.ID
	for i := range 3 {
		host, err := resource.NewHost(workspaceID, 0, fmt.Sprintf("target-host-%d", i), nil, resource.HostSpec{AccessProfileID: profile.ID}, now)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.stores.ResourceHosts.CreateHost(ctx, host); err != nil {
			t.Fatal(err)
		}
		hostIDs = append(hostIDs, host.Metadata.ID)
		targets = append(targets, resource.OperationTargetSpec{HostID: host.Metadata.ID, HostGeneration: host.Metadata.Generation,
			DisplayName: host.Metadata.Name, AccessProfileID: profile.ID, AccessProfileRevision: profileRevision.Revision})
	}
	op, err := resource.NewRunOperation(workspaceID, resource.ActorRef{UserID: 7}, resource.OperationSpec{
		Targets: targets, Run: &resource.RunSpec{Argv: []string{"uptime"}, TimeoutSeconds: 30, Concurrency: 1}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.stores.Operations.CreateOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	// A target cannot be written before the Operation runs.
	if err = f.stores.Operations.UpdateOperationTarget(ctx, workspaceID, op.ID, hostIDs[0], resource.TargetRunning, nil); !errors.Is(err, resource.ErrOperationFinished) {
		t.Fatalf("target write on a queued Operation: %v, want ErrOperationFinished", err)
	}
	started, err := f.stores.Operations.GetOperation(ctx, workspaceID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = started.Start(now); err != nil {
		t.Fatal(err)
	}
	if err = f.stores.Operations.UpdateOperation(ctx, started, 1); err != nil {
		t.Fatal(err)
	}
	if state, err := f.stores.Operations.GetOperationState(ctx, workspaceID, op.ID); err != nil || state != resource.OperationRunning {
		t.Fatalf("GetOperationState = %q, %v", state, err)
	}

	// A cancellation request is computed from this (soon stale) view.
	stale, err := f.stores.Operations.GetOperation(ctx, workspaceID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	revision := stale.Revision

	exit := 0
	if err = f.stores.Operations.UpdateOperationTarget(ctx, workspaceID, op.ID, hostIDs[0], resource.TargetRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err = f.stores.Operations.UpdateOperationTarget(ctx, workspaceID, op.ID, hostIDs[0], resource.TargetSucceeded,
		&resource.TargetResult{ExitCode: &exit, Stdout: "up 3 days"}); err != nil {
		t.Fatal(err)
	}
	if err = f.stores.Operations.UpdateOperationTarget(ctx, workspaceID, op.ID, hostIDs[1], resource.TargetFailed,
		&resource.TargetResult{ErrorClass: "remote_execution_failed"}); err != nil {
		t.Fatal(err)
	}
	// Terminal targets are final.
	if err = f.stores.Operations.UpdateOperationTarget(ctx, workspaceID, op.ID, hostIDs[0], resource.TargetFailed, nil); !errors.Is(err, resource.ErrOperationFinished) {
		t.Fatalf("overwrite of a terminal target: %v, want ErrOperationFinished", err)
	}
	if err = f.stores.Operations.UpdateOperationTarget(ctx, workspaceID+1000, op.ID, hostIDs[2], resource.TargetFailed, nil); !errors.Is(err, resource.ErrOperationNotFound) {
		t.Fatalf("cross-workspace target write: %v, want ErrOperationNotFound", err)
	}

	loaded, err := f.stores.Operations.GetOperation(ctx, workspaceID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != revision {
		t.Fatalf("target writes bumped the Operation revision %d -> %d", revision, loaded.Revision)
	}
	if loaded.Status.Targets[0].State != resource.TargetSucceeded || loaded.Status.Targets[0].Result.Stdout != "up 3 days" ||
		loaded.Status.Targets[1].State != resource.TargetFailed || loaded.Status.Targets[2].State != resource.TargetPending {
		t.Fatalf("targets after per-target writes: %+v", loaded.Status.Targets)
	}

	// The stale view still has targets 0 and 1 as Pending. Writing it back
	// (as a racing cancellation would) must not resurrect them.
	if err = stale.RequestCancel(now); err != nil {
		t.Fatal(err)
	}
	if err = stale.SetTargetState(hostIDs[0], resource.TargetCancelled, &resource.TargetResult{ErrorClass: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	if err = f.stores.Operations.UpdateOperation(ctx, stale, revision); !errors.Is(err, resource.ErrConflict) {
		t.Fatalf("stale update over a completed target: %v, want ErrConflict", err)
	}
	// A fresh cancellation that leaves targets untouched succeeds and writes no target rows.
	fresh, err := f.stores.Operations.GetOperation(ctx, workspaceID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = fresh.RequestCancel(now); err != nil {
		t.Fatal(err)
	}
	if err = f.stores.Operations.UpdateOperation(ctx, fresh, revision); err != nil {
		t.Fatalf("fresh cancellation: %v", err)
	}
	final, err := f.stores.Operations.GetOperation(ctx, workspaceID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status.State != resource.OperationCancelRequested || final.Status.Targets[0].Result.Stdout != "up 3 days" {
		t.Fatalf("after cancellation: state=%s targets=%+v", final.Status.State, final.Status.Targets)
	}
	// Targets can still be completed while cancellation is pending.
	if err = f.stores.Operations.UpdateOperationTarget(ctx, workspaceID, op.ID, hostIDs[2], resource.TargetCancelled,
		&resource.TargetResult{ErrorClass: "cancelled"}); err != nil {
		t.Fatalf("completing a target during cancellation: %v", err)
	}
}

// testResourceListing covers the B8a list queries: keyset pages, filters and
// target counts, on both dialects.
func testResourceListing(t *testing.T, f *fixture) {
	ctx := context.Background()
	workspaceID := f.newWorkspace(t, "listing")
	other := f.newWorkspace(t, "listing-other")
	now := time.Now().UTC()
	profile, revision, err := resource.NewAccessProfile(workspaceID, "list-profile", "", resource.AccessProfileSpec{Connector: resource.ConnectorSSH,
		Username: "deploy", Authentication: resource.AuthenticationPassword, Credential: resource.SecretReference{ID: "list-secret", Version: 1}, Enabled: true}, 7, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.stores.ResourceProfiles.CreateAccessProfile(ctx, profile, revision); err != nil {
		t.Fatal(err)
	}
	var hosts []*resource.Host
	for i := range 5 {
		host, err := resource.NewHost(workspaceID, 0, fmt.Sprintf("list-host-%d", i), map[string]string{"tier": "web"}, resource.HostSpec{AccessProfileID: profile.ID}, now)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.stores.ResourceHosts.CreateHost(ctx, host); err != nil {
			t.Fatal(err)
		}
		hosts = append(hosts, host)
	}
	foreign, err := resource.NewHost(other, 0, "foreign", nil, resource.HostSpec{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.stores.ResourceHosts.CreateHost(ctx, foreign); err != nil {
		t.Fatal(err)
	}

	// Hosts: two-item keyset pages cover all five exactly once, in ID order.
	seen := map[resource.ID]bool{}
	var after resource.ID
	for page := 0; ; page++ {
		got, err := f.stores.ResourceHosts.ListHosts(ctx, resource.HostListFilter{WorkspaceID: workspaceID, After: after, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for i, host := range got {
			if seen[host.Metadata.ID] || host.Metadata.WorkspaceID != workspaceID || (i > 0 && got[i-1].Metadata.ID >= host.Metadata.ID) {
				t.Fatalf("page %d returned %s out of order, twice, or from another workspace", page, host.Metadata.ID)
			}
			seen[host.Metadata.ID] = true
		}
		if len(got) < 2 {
			break
		}
		after = got[len(got)-1].Metadata.ID
	}
	if len(seen) != 5 {
		t.Fatalf("paged through %d hosts, want 5", len(seen))
	}
	if got, err := f.stores.ResourceHosts.ListHosts(ctx, resource.HostListFilter{WorkspaceID: workspaceID, Phase: resource.HostDeleting}); err != nil || len(got) != 0 {
		t.Fatalf("phase filter = %d hosts, %v", len(got), err)
	}

	// Access Profiles: deleted ones are hidden.
	if got, err := f.stores.ResourceProfiles.ListAccessProfiles(ctx, workspaceID, "", 10); err != nil || len(got) != 1 || got[0].ID != profile.ID {
		t.Fatalf("ListAccessProfiles = %+v, %v", got, err)
	}

	// Operations: newest first, cursor pages, host filter and target counts.
	var ops []*resource.Operation
	for i := range 3 {
		op, err := resource.NewRunOperation(workspaceID, resource.ActorRef{UserID: 7}, resource.OperationSpec{
			Targets: []resource.OperationTargetSpec{{HostID: hosts[i].Metadata.ID, HostGeneration: hosts[i].Metadata.Generation,
				DisplayName: hosts[i].Metadata.Name, AccessProfileID: profile.ID, AccessProfileRevision: revision.Revision}},
			Run: &resource.RunSpec{Argv: []string{"uptime"}, TimeoutSeconds: 30, Concurrency: 1}}, now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if err = f.stores.Operations.CreateOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		ops = append(ops, op)
	}
	first, err := f.stores.Operations.ListOperations(ctx, resource.OperationListFilter{WorkspaceID: workspaceID, Limit: 2})
	if err != nil || len(first) != 2 || first[0].ID != ops[2].ID || first[1].ID != ops[1].ID {
		t.Fatalf("first Operation page = %+v, %v", first, err)
	}
	if first[0].Targets[resource.TargetPending] != 1 || first[0].State != resource.OperationQueued {
		t.Fatalf("summary = %+v, want one pending target", first[0])
	}
	rest, err := f.stores.Operations.ListOperations(ctx, resource.OperationListFilter{WorkspaceID: workspaceID, After: first[1].Cursor, Limit: 2})
	if err != nil || len(rest) != 1 || rest[0].ID != ops[0].ID {
		t.Fatalf("second Operation page = %+v, %v", rest, err)
	}
	byHost, err := f.stores.Operations.ListOperations(ctx, resource.OperationListFilter{WorkspaceID: workspaceID, HostID: hosts[1].Metadata.ID})
	if err != nil || len(byHost) != 1 || byHost[0].ID != ops[1].ID {
		t.Fatalf("host filter = %+v, %v", byHost, err)
	}
	if got, err := f.stores.Operations.ListOperations(ctx, resource.OperationListFilter{WorkspaceID: other}); err != nil || len(got) != 0 {
		t.Fatalf("other workspace sees %d Operations, %v", len(got), err)
	}
	if _, err := f.stores.Operations.ListOperations(ctx, resource.OperationListFilter{WorkspaceID: workspaceID, After: "!!"}); !errors.Is(err, resource.ErrInvalidResource) {
		t.Fatalf("bad cursor error = %v", err)
	}
}

// testHostKeyTrust covers migration 000003 (observed_host_keys) and the
// audited, evented TrustHostKeys write on both engines.
func testHostKeyTrust(t *testing.T, f *fixture) {
	ctx := context.Background()
	host := newManagedHost(t, f, "host-keys")
	if err := f.stores.ResourceHosts.CreateHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	ws, id := host.Metadata.WorkspaceID, host.Metadata.ID
	observeCtx := ctx
	if f.engine == "postgres" {
		lease, ok, err := f.stores.ResourceLeases.AcquireResourceLease(ctx, resource.Lease{Kind: "host", WorkspaceID: ws,
			ID: string(id), Owner: "contract-test"}, time.Now(), time.Minute)
		if err != nil || !ok {
			t.Fatalf("acquire observation lease = %v, %v", ok, err)
		}
		observeCtx = resource.WithLeaseFence(ctx, resource.LeaseFence{Kind: lease.Kind, WorkspaceID: lease.WorkspaceID, ID: lease.ID, Token: lease.Token})
	}
	const fpA, fpB = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	seen := time.Now().UTC().Truncate(time.Microsecond)
	keys := []resource.ObservedHostKey{
		{Algorithm: "ssh-ed25519", Fingerprint: fpA, FirstSeenAt: seen, LastSeenAt: seen},
		{Algorithm: "ecdsa-sha2-nistp256", Fingerprint: fpB, FirstSeenAt: seen, LastSeenAt: seen},
	}
	if err := f.stores.ResourceHosts.ObserveHost(observeCtx, ws, id, 1, resource.HostActive, nil, keys, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := f.stores.ResourceHosts.GetHost(ctx, ws, id)
	if err != nil || len(got.Status.ObservedHostKeys) != 2 || got.Status.ObservedHostKeys[1].Fingerprint != fpB ||
		!got.Status.ObservedHostKeys[0].LastSeenAt.Equal(seen) {
		t.Fatalf("observed keys = %+v, %v", got.Status.ObservedHostKeys, err)
	}
	// nil keeps the stored observation.
	if err := f.stores.ResourceHosts.ObserveHost(observeCtx, ws, id, 1, resource.HostActive, nil, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _ = f.stores.ResourceHosts.GetHost(ctx, ws, id); len(got.Status.ObservedHostKeys) != 2 {
		t.Fatalf("an observation without keys dropped them: %+v", got.Status.ObservedHostKeys)
	}

	if err := f.stores.ResourceHosts.TrustHostKeys(ctx, ws, id, got.Metadata.Revision-1, []string{fpA}, false, "owner", time.Now()); !errors.Is(err, resource.ErrConflict) {
		t.Fatalf("stale revision = %v", err)
	}
	if err := f.stores.ResourceHosts.TrustHostKeys(ctx, ws, id, got.Metadata.Revision, []string{fpA, fpB}, false, "owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	trusted, err := f.stores.ResourceHosts.GetHost(ctx, ws, id)
	if err != nil || strings.Join(trusted.Spec.AccessTarget.Trust, ",") != fpA+","+fpB || trusted.Metadata.Generation != got.Metadata.Generation+1 {
		t.Fatalf("trusted host = %+v, %v", trusted, err)
	}
	if err := f.stores.ResourceHosts.TrustHostKeys(ctx, ws, id, trusted.Metadata.Revision, []string{fpB}, false, "owner", time.Now()); !errors.Is(err, resource.ErrConflict) {
		t.Fatalf("changing trust without replace = %v", err)
	}
	if err := f.stores.ResourceHosts.TrustHostKeys(ctx, ws, id, trusted.Metadata.Revision, []string{fpB}, true, "owner", time.Now()); err != nil {
		t.Fatal(err)
	}

	var audits, events int
	if err := f.stores.DB.QueryRowContext(ctx, f.pick(`SELECT COUNT(*) FROM audit_events WHERE action = ? AND target_id = ?`,
		`SELECT COUNT(*) FROM audit_events WHERE action = $1 AND target_id = $2`), "resource_host.host_key_trusted", string(id)).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("trust audit rows = %d, %v", audits, err)
	}
	if err := f.stores.DB.QueryRowContext(ctx, f.pick(`SELECT COUNT(*) FROM outbox_events WHERE event_type = ? AND subject_id = ? AND payload LIKE '%host-keys-trusted%'`,
		`SELECT COUNT(*) FROM outbox_events WHERE event_type = $1 AND subject_id = $2 AND payload::text LIKE '%host-keys-trusted%'`), eventing.HostSpecChanged, string(id)).Scan(&events); err != nil || events != 2 {
		t.Fatalf("trust outbox events = %d, %v", events, err)
	}
}
