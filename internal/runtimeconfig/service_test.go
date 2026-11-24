package runtimeconfig_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/runtimeconfig"
	"github.com/machinarchy/machinarchy/internal/store"
)

var owner = domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner}

func repository(t *testing.T) runtimeconfig.Repository {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stores, err := store.Open(context.Background(), store.Config{Engine: store.EngineSQLite, SQLitePath: filepath.Join(dir, "c.db"),
		EncryptionSecret: "runtime-config-test-key-material-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	return stores.Management
}

// applier records what it is asked to apply and can be told to refuse.
type applier struct {
	refuse    atomic.Bool
	prepared  atomic.Int64
	committed atomic.Int64
	rolled    atomic.Int64
}

func (a *applier) Prepare(_ context.Context, s runtimeconfig.Snapshot) (runtimeconfig.PreparedApply, error) {
	if a.refuse.Load() {
		return runtimeconfig.PreparedApply{}, errors.New("refused")
	}
	a.prepared.Store(int64(s.Revision.Revision))
	return runtimeconfig.PreparedApply{
		Commit:   func() error { a.committed.Store(int64(s.Revision.Revision)); return nil },
		Rollback: func() { a.rolled.Add(1) },
	}, nil
}

func newService(t *testing.T, repo runtimeconfig.Repository, appliers ...runtimeconfig.Applier) (*runtimeconfig.Service, *runtimeconfig.SnapshotManager) {
	t.Helper()
	snapshots, err := runtimeconfig.NewSnapshotManager(repo, "process-1", "api", appliers...)
	if err != nil {
		t.Fatal(err)
	}
	service, err := runtimeconfig.NewService(repo, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	return service, snapshots
}

func TestPublishAndRollback(t *testing.T) {
	ctx := context.Background()
	repo := repository(t)
	first, second := &applier{}, &applier{}
	service, _ := newService(t, repo, first, second)
	if err := service.Bootstrap(ctx, runtimeconfig.Defaults()); err != nil {
		t.Fatal(err)
	}
	snapshot, ok := service.Snapshot()
	if !ok || snapshot.Revision.Revision != 1 || first.committed.Load() != 1 || second.committed.Load() != 1 {
		t.Fatalf("after bootstrap: snapshot %v (ok %v), commits %d %d", snapshot.Revision.Revision, ok, first.committed.Load(), second.committed.Load())
	}

	next := runtimeconfig.Defaults()
	next.Logging.Level = "debug"
	next.Features.SignupEnabled = true
	published, err := service.Publish(ctx, owner, 1, next, "enable signup")
	if err != nil || published.Revision != 2 {
		t.Fatalf("publish = %+v, %v", published, err)
	}
	if snapshot, _ := service.Snapshot(); !snapshot.Revision.Config.Features.SignupEnabled || snapshot.Revision.Revision != 2 {
		t.Fatal("the published revision was not applied to this process")
	}

	for name, tc := range map[string]struct {
		actor    domain.Principal
		expected uint64
		mutate   func(*runtimeconfig.Config)
		want     error
	}{
		"operator":            {domain.Principal{UserID: 2, WorkspaceID: 1, Role: domain.RoleOperator}, 2, nil, domain.ErrPermissionDenied},
		"reader":              {domain.Principal{UserID: 3, WorkspaceID: 1, Role: domain.RoleReader}, 2, nil, domain.ErrPermissionDenied},
		"owner API token":     {domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner, APITokenID: 4}, 2, nil, domain.ErrPermissionDenied},
		"stale revision":      {owner, 1, nil, runtimeconfig.ErrConflict},
		"restart-only change": {owner, 2, func(c *runtimeconfig.Config) { c.Operations.Workers++ }, runtimeconfig.ErrRestartRequired},
	} {
		candidate := next
		if tc.mutate != nil {
			tc.mutate(&candidate)
		}
		if _, err := service.Publish(ctx, tc.actor, tc.expected, candidate, ""); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	invalid := next
	invalid.SSH.AllowedPorts = []int{0}
	if _, err := service.Publish(ctx, owner, 2, invalid, ""); err == nil {
		t.Fatal("an invalid configuration was published")
	}

	rolled, err := service.Rollback(ctx, owner, 2, 1, "undo")
	if err != nil || rolled.Revision != 3 || rolled.Config.Features.SignupEnabled || !strings.Contains(rolled.ChangeNote, "rollback to revision 1: undo") {
		t.Fatalf("rollback = %+v, %v", rolled, err)
	}
	if _, err := service.Rollback(ctx, domain.Principal{UserID: 2, WorkspaceID: 1, Role: domain.RoleOperator}, 3, 1, ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("operator rollback = %v", err)
	}
	if _, err := service.Rollback(ctx, owner, 3, 99, ""); err == nil {
		t.Fatal("rollback to a missing revision succeeded")
	}

	current, err := service.Current(ctx)
	if err != nil || current.Revision != 3 {
		t.Fatalf("current = %d, %v", current.Revision, err)
	}
	if got, err := service.Get(ctx, 2); err != nil || got.ChangeNote != "enable signup" {
		t.Fatalf("get 2 = %+v, %v", got, err)
	}
	if list, err := service.List(ctx, 2, 0); err != nil || len(list) != 2 || list[0].Revision != 3 {
		t.Fatalf("list = %d revisions, %v", len(list), err)
	}
	statuses, err := service.Status(ctx)
	if err != nil || len(statuses) != 1 || statuses[0].ProcessID != "process-1" || statuses[0].AppliedRevision != 3 {
		t.Fatalf("status = %+v, %v", statuses, err)
	}
}

// A participant that cannot prepare stops the revision everywhere in this
// process: the others roll back and the snapshot stays where it was.
func TestApplyIsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	repo := repository(t)
	willing, refusing := &applier{}, &applier{}
	service, snapshots := newService(t, repo, willing, refusing)
	if err := service.Bootstrap(ctx, runtimeconfig.Defaults()); err != nil {
		t.Fatal(err)
	}
	refusing.refuse.Store(true)
	next := runtimeconfig.Defaults()
	next.Logging.Level = "debug"
	if _, err := service.Publish(ctx, owner, 1, next, ""); !errors.Is(err, runtimeconfig.ErrApplyPending) {
		t.Fatalf("publish with a refusing participant = %v, want ErrApplyPending", err)
	}
	if snapshot, _ := snapshots.Current(); snapshot.Revision.Revision != 1 || snapshot.Revision.Config.Logging.Level == "debug" {
		t.Fatal("a refused revision became the current snapshot")
	}
	// Nothing was committed, so nothing is rolled back (Q-16).
	if willing.rolled.Load() != 0 || willing.committed.Load() != 1 {
		t.Fatalf("the willing participant: %d rollbacks, committed revision %d", willing.rolled.Load(), willing.committed.Load())
	}
	statuses, _ := service.Status(ctx)
	if len(statuses) != 1 || statuses[0].LastErrorClass != "apply_failed" || statuses[0].AttemptedRevision != 2 {
		t.Fatalf("status = %+v", statuses)
	}
	refusing.refuse.Store(false)
	if err := snapshots.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if snapshot, _ := snapshots.Current(); snapshot.Revision.Revision != 2 {
		t.Fatal("the revision was not applied once every participant accepted it")
	}
}

// Snapshots are copies: a caller cannot change what other readers see.
func TestSnapshotsAreIsolated(t *testing.T) {
	ctx := context.Background()
	service, snapshots := newService(t, repository(t))
	defaults := runtimeconfig.Defaults()
	defaults.Operations.AllowedPrograms = []string{"uptime"}
	if err := service.Bootstrap(ctx, defaults); err != nil {
		t.Fatal(err)
	}
	first, _ := snapshots.Current()
	first.Revision.Config.Operations.AllowedPrograms[0] = "rm"
	if again, _ := snapshots.Current(); again.Revision.Config.Operations.AllowedPrograms[0] != "uptime" {
		t.Fatal("changing a returned snapshot changed the shared one")
	}
	var nilManager *runtimeconfig.SnapshotManager
	if _, ok := nilManager.Current(); ok {
		t.Fatal("a nil manager reported a snapshot")
	}
}

// Another process publishes; the watcher applies it without a restart.
func TestWatchAppliesRevisionsFromOtherProcesses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := repository(t)
	service, snapshots := newService(t, repo)
	if err := service.Bootstrap(ctx, runtimeconfig.Defaults()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- service.Watch(ctx, time.Hour) }()

	other, _ := newService(t, repo)
	next := runtimeconfig.Defaults()
	next.Features.SignupEnabled = true
	if _, err := other.Publish(ctx, owner, 1, next, "from another process"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if snapshot, _ := snapshots.Current(); snapshot.Revision.Revision == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the watcher did not apply the revision published elsewhere")
		}
		time.Sleep(50 * time.Millisecond) // polling a condition, not synchronising
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Watch returned %v after cancellation", err)
	}
	//nolint:staticcheck // a nil context must be refused, not dereferenced
	if err := snapshots.Run(nil, time.Second); err == nil {
		t.Fatal("Run accepted a nil context")
	}
}

func TestConstructorsAndChecksum(t *testing.T) {
	if _, err := runtimeconfig.NewService(nil, nil); err == nil {
		t.Fatal("a service without dependencies was created")
	}
	if _, err := runtimeconfig.NewSnapshotManager(nil, "p", "api"); err == nil {
		t.Fatal("a snapshot manager without a repository was created")
	}
	if _, err := runtimeconfig.NewSnapshotManager(repository(t), " ", "api"); err == nil {
		t.Fatal("a snapshot manager without a process ID was created")
	}
	a, err := runtimeconfig.Defaults().Checksum()
	if err != nil {
		t.Fatal(err)
	}
	changed := runtimeconfig.Defaults()
	changed.Logging.Level = "debug"
	b, _ := changed.Checksum()
	if a == "" || a == b {
		t.Fatalf("checksums %q and %q must differ for different configurations", a, b)
	}
	if again, _ := runtimeconfig.Defaults().Checksum(); again != a {
		t.Fatal("the checksum is not deterministic")
	}
	if err := (&runtimeconfig.Service{}).Bootstrap(context.Background(), runtimeconfig.Config{}); err == nil {
		t.Fatal("bootstrap accepted an invalid configuration")
	}
}
