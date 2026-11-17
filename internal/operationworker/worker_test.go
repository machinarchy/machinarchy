package operationworker

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/eventing"
	"github.com/machinarchy/machinarchy/internal/hostaccess"
)

// panickingStore panics on the first GetOperation (simulating a bug inside one
// Operation's execution) and behaves like a store afterwards.
type panickingStore struct {
	resource.OperationStore
	mu      sync.Mutex
	op      *resource.Operation
	loads   int
	updated *resource.Operation
}

func (p *panickingStore) GetOperation(context.Context, int, resource.ID) (*resource.Operation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loads++
	if p.loads == 1 {
		panic("store bug")
	}
	copied := *p.op
	return &copied, nil
}

func (p *panickingStore) UpdateOperation(_ context.Context, op *resource.Operation, expected uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if expected != p.op.Revision {
		return resource.ErrConflict
	}
	p.op, p.updated = op, op
	return nil
}

type stubQueue struct {
	resource.OperationQueueRepository
}
type stubHosts struct{ resource.HostRepository }
type stubProfiles struct{}

func (stubProfiles) GetAccessProfile(context.Context, int, resource.ID) (*resource.AccessProfile, error) {
	return nil, resource.ErrAccessProfileNotFound
}

func (stubProfiles) GetAccessProfileRevision(context.Context, int, resource.ID, uint64) (*resource.AccessProfileRevision, error) {
	return nil, resource.ErrAccessProfileNotFound
}

type stubOutbox struct{ eventing.OutboxStore }
type stubConnector struct{ hostaccess.HostConnector }

// TestPanicInOneOperationIsIsolated covers AUDIT Q-04: a panic while executing
// one Operation must not take the process down; the Operation is finished as
// Interrupted (reason worker_panic) and never retried.
func TestPanicInOneOperationIsIsolated(t *testing.T) {
	hostID, err := resource.NewResourceID()
	if err != nil {
		t.Fatal(err)
	}
	op, err := resource.NewRunOperation(1, resource.ActorRef{UserID: 1}, resource.OperationSpec{
		Targets: []resource.OperationTargetSpec{{HostID: hostID, HostGeneration: 1, DisplayName: "web-1"}},
		Run:     &resource.RunSpec{Argv: []string{"uptime"}, TimeoutSeconds: 10, Concurrency: 1},
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	store := &panickingStore{op: op}
	worker, err := New(Dependencies{
		Operations: store, Queue: stubQueue{}, Hosts: stubHosts{}, Profiles: stubProfiles{}, Outbox: stubOutbox{},
		Connector: stubConnector{}, ExecutionPolicy: func() domain.ExecutionPolicy { return domain.ExecutionPolicy{} },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, 1)
	if err != nil {
		t.Fatal(err)
	}

	worker.safeExecute(context.Background(), resource.OperationKey{WorkspaceID: 1, ID: op.ID}) // must not panic

	if store.updated == nil || store.updated.Status.State != resource.OperationInterrupted {
		t.Fatalf("Operation after panic = %+v, want Interrupted", store.updated)
	}
	if store.updated.Status.Targets[0].State != resource.TargetInterrupted {
		t.Fatalf("target after panic = %s, want Interrupted", store.updated.Status.Targets[0].State)
	}
	if len(store.updated.Status.Conditions) == 0 || store.updated.Status.Conditions[0].Reason != "worker_panic" {
		t.Fatalf("conditions = %+v, want reason worker_panic", store.updated.Status.Conditions)
	}
	worker.mu.Lock()
	active := len(worker.active)
	worker.mu.Unlock()
	if active != 0 {
		t.Fatalf("panicking Operation left %d active entries", active)
	}
}
