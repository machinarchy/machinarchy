package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

func TestManagedResourceContract(t *testing.T) {
	for _, engine := range availableEngines(t) {
		t.Run(engine, func(t *testing.T) {
			t.Run("host revision and status", func(t *testing.T) { testResourceHost(t, newFixture(t, engine)) })
			t.Run("operation persistence and CAS", func(t *testing.T) { testResourceOperation(t, newFixture(t, engine)) })
			t.Run("transaction rollback", func(t *testing.T) { testResourceRollback(t, newFixture(t, engine)) })
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
	if err := f.stores.ResourceHosts.ObserveHost(ctx, host.Metadata.WorkspaceID, host.Metadata.ID, 2, resource.HostDeleting, nil, time.Now()); err != nil {
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
