package controlmanager

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/eventing"
)

type fakeHostKeys struct {
	resource.HostRepository
	keys []resource.HostKey
}

func (f *fakeHostKeys) ListHostKeys(_ context.Context, cursor resource.HostKeyCursor) ([]resource.HostKey, error) {
	keys := append([]resource.HostKey(nil), f.keys...)
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].WorkspaceID != keys[j].WorkspaceID {
			return keys[i].WorkspaceID < keys[j].WorkspaceID
		}
		return keys[i].ID < keys[j].ID
	})
	filtered := make([]resource.HostKey, 0, cursor.Limit)
	for _, key := range keys {
		if key.WorkspaceID < cursor.AfterWorkspaceID || (key.WorkspaceID == cursor.AfterWorkspaceID && key.ID <= resource.ResourceID(cursor.AfterID)) {
			continue
		}
		filtered = append(filtered, key)
		if len(filtered) >= cursor.Limit {
			break
		}
	}
	return filtered, nil
}

type fakeOutbox struct {
	mu          sync.Mutex
	deliveries  []eventing.ClaimedEvent
	claimed     bool
	acks        []resource.ResourceID
	retries     []resource.ResourceID
	quarantined []resource.ResourceID
}

func (f *fakeOutbox) Claim(_ context.Context, _, _ string, _ time.Time, _ time.Duration, _ int) ([]eventing.ClaimedEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimed {
		return nil, nil
	}
	f.claimed = true
	return append([]eventing.ClaimedEvent(nil), f.deliveries...), nil
}
func (f *fakeOutbox) Acknowledge(_ context.Context, _, _ string, id resource.ResourceID, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acks = append(f.acks, id)
	return nil
}
func (f *fakeOutbox) Retry(_ context.Context, _, _ string, id resource.ResourceID, _ time.Time, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retries = append(f.retries, id)
	return nil
}
func (f *fakeOutbox) Quarantine(_ context.Context, _, _ string, id resource.ResourceID, _ time.Time, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.quarantined = append(f.quarantined, id)
	return nil
}
func (f *fakeOutbox) Prune(context.Context, time.Time) (int64, error) { return 0, nil }

type controllerFunc struct {
	name string
	kind ResourceKind
	run  func(context.Context, ResourceKey) (ReconcileResult, error)
}

func (c controllerFunc) Name() string       { return c.name }
func (c controllerFunc) Kind() ResourceKind { return c.kind }
func (c controllerFunc) Reconcile(ctx context.Context, key ResourceKey) (ReconcileResult, error) {
	return c.run(ctx, key)
}

func newTestEngine(t *testing.T, hosts *fakeHostKeys, outbox *fakeOutbox, workerCount int) *Engine {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Workers, cfg.QueueCapacity = workerCount, 8
	cfg.OutboxPollInterval, cfg.ResyncInterval = 10*time.Millisecond, time.Hour
	cfg.ReconcileTimeout, cfg.ShutdownTimeout = time.Second, 2*time.Second
	engine, err := NewEngine(Dependencies{Hosts: hosts, Outbox: outbox}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Register(controllerFunc{name: "host-test", kind: HostResourceKind, run: func(context.Context, ResourceKey) (ReconcileResult, error) { return ReconcileResult{}, nil }}); err != nil {
		t.Fatal(err)
	}
	return engine
}

func TestEngineUsesLatestStateForDuplicateAndOutOfOrderEventsAndResyncsMisses(t *testing.T) {
	id1, _ := resource.NewResourceID()
	id2, _ := resource.NewResourceID()
	keys := &fakeHostKeys{keys: []resource.HostKey{{WorkspaceID: 1, ID: id1}, {WorkspaceID: 1, ID: id2}}}
	gen2, _ := eventing.NewHostEvent(eventing.HostSpecChanged, 1, eventing.Subject{Kind: "host", ID: id1, Generation: 2, Revision: 2}, "spec-changed", time.Now(), "", "", nil)
	gen1, _ := eventing.NewHostEvent(eventing.HostCreated, 1, eventing.Subject{Kind: "host", ID: id1, Generation: 1, Revision: 1}, "created", time.Now().Add(time.Second), "", "", nil)
	outbox := &fakeOutbox{deliveries: []eventing.ClaimedEvent{{Envelope: gen2, Attempt: 1}, {Envelope: gen1, Attempt: 1}}}
	var calls atomic.Int32
	var observed sync.Map
	engine := newTestEngine(t, keys, outbox, 2)
	engine.controllers[HostResourceKind] = controllerFunc{name: "latest-state", kind: HostResourceKind, run: func(ctx context.Context, key ResourceKey) (ReconcileResult, error) {
		calls.Add(1)
		// This represents the current-state database read; event generations are
		// deliberately not part of the key passed to the controller.
		observed.Store(key.ID, uint64(7))
		return ReconcileResult{}, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- engine.Start(ctx) }()
	deadline := time.After(3 * time.Second)
	for calls.Load() < 2 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("startup recovery and event notifications did not reconcile keys")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := engine.CheckReadiness(context.Background()); err != nil {
		cancel()
		t.Fatalf("manager readiness after initial scan = %v", err)
	}
	if _, ok := observed.Load(string(id2)); !ok {
		cancel()
		t.Fatal("startup scan did not recover a resource without an event")
	}
	outbox.mu.Lock()
	ackCount := len(outbox.acks)
	outbox.mu.Unlock()
	if ackCount != 2 {
		cancel()
		t.Fatalf("outbox acknowledgments = %d, want duplicate/out-of-order events admitted", ackCount)
	}
	cancel()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manager did not shut down")
	}
}

func TestEngineRecoversWorkerPanicAndDrainsOnShutdown(t *testing.T) {
	id, _ := resource.NewResourceID()
	keys := &fakeHostKeys{keys: []resource.HostKey{{WorkspaceID: 1, ID: id}}}
	engine := newTestEngine(t, keys, &fakeOutbox{}, 1)
	var calls atomic.Int32
	recovered := make(chan struct{}, 1)
	engine.controllers[HostResourceKind] = controllerFunc{name: "panic-once", kind: HostResourceKind, run: func(context.Context, ResourceKey) (ReconcileResult, error) {
		if calls.Add(1) == 1 {
			panic("transient worker fault")
		}
		recovered <- struct{}{}
		return ReconcileResult{}, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- engine.Start(ctx) }()
	select {
	case <-recovered:
	case <-time.After(4 * time.Second):
		cancel()
		t.Fatal("worker did not recover after panic retry")
	}
	if calls.Load() < 2 {
		cancel()
		t.Fatalf("controller calls = %d, want retry after worker panic", calls.Load())
	}
	cancel()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manager failed to drain and stop")
	}
}

func TestEngineSerializesConcurrentReconcileForSameKey(t *testing.T) {
	id, _ := resource.NewResourceID()
	key := ResourceKey{Kind: HostResourceKind, WorkspaceID: 1, ID: string(id)}
	engine := newTestEngine(t, &fakeHostKeys{keys: []resource.HostKey{{WorkspaceID: 1, ID: id}}}, &fakeOutbox{}, 2)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls, active, maxActive atomic.Int32
	engine.controllers[HostResourceKind] = controllerFunc{name: "serial", kind: HostResourceKind, run: func(ctx context.Context, _ ResourceKey) (ReconcileResult, error) {
		call := calls.Add(1)
		count := active.Add(1)
		for current := maxActive.Load(); count > current && !maxActive.CompareAndSwap(current, count); current = maxActive.Load() {
		}
		defer active.Add(-1)
		if call == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ReconcileResult{}, ctx.Err()
			}
		}
		return ReconcileResult{}, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- engine.Start(ctx) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("first reconciliation did not start")
	}
	if got := engine.queue.Add(key); got != QueueAccepted {
		cancel()
		close(release)
		t.Fatalf("event while processing = %v", got)
	}
	time.Sleep(50 * time.Millisecond)
	if maxActive.Load() != 1 || calls.Load() != 1 {
		cancel()
		close(release)
		t.Fatalf("same key ran concurrently: calls=%d max_active=%d", calls.Load(), maxActive.Load())
	}
	close(release)
	deadline := time.After(2 * time.Second)
	for calls.Load() < 2 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("dirty key was not scheduled after processing")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manager did not drain")
	}
	if maxActive.Load() != 1 {
		t.Fatalf("maximum concurrent same-key reconciles = %d", maxActive.Load())
	}
}

func TestEngineShutdownWaitsForActiveReconcile(t *testing.T) {
	id, _ := resource.NewResourceID()
	engine := newTestEngine(t, &fakeHostKeys{keys: []resource.HostKey{{WorkspaceID: 1, ID: id}}}, &fakeOutbox{}, 1)
	entered, release := make(chan struct{}), make(chan struct{})
	engine.controllers[HostResourceKind] = controllerFunc{name: "drain", kind: HostResourceKind, run: func(ctx context.Context, _ ResourceKey) (ReconcileResult, error) {
		close(entered)
		select {
		case <-release:
			return ReconcileResult{}, nil
		case <-ctx.Done():
			return ReconcileResult{}, ctx.Err()
		}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- engine.Start(ctx) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("reconciliation did not start")
	}
	cancel()
	select {
	case <-started:
		t.Fatal("manager returned before active reconciliation drained")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manager did not stop after reconciliation drain")
	}
}

func TestEngineRejectsDuplicateRegistrationAndBadStartup(t *testing.T) {
	keys := &fakeHostKeys{}
	outbox := &fakeOutbox{}
	cfg := DefaultConfig()
	engine, err := NewEngine(Dependencies{Hosts: keys, Outbox: outbox}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctrl := controllerFunc{name: "one", kind: HostResourceKind, run: func(context.Context, ResourceKey) (ReconcileResult, error) { return ReconcileResult{}, nil }}
	if err := engine.Register(ctrl); err != nil {
		t.Fatal(err)
	}
	if err := engine.Register(ctrl); !errors.Is(err, ErrDuplicateController) {
		t.Fatalf("duplicate registration error = %v", err)
	}
	noControllers, err := NewEngine(Dependencies{Hosts: keys, Outbox: outbox}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := noControllers.Start(context.Background()); !errors.Is(err, ErrNoControllers) {
		t.Fatalf("empty registry startup error = %v", err)
	}
}

func TestEngineQuarantinesUnsupportedVersion(t *testing.T) {
	id, _ := resource.NewResourceID()
	envelope, err := eventing.NewHostEvent(eventing.HostCreated, 1, eventing.Subject{Kind: "host", ID: id, Generation: 1, Revision: 1}, "created", time.Now(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	envelope.SchemaVersion = 9
	outbox := &fakeOutbox{deliveries: []eventing.ClaimedEvent{{Envelope: envelope, Attempt: 1}}}
	engine := newTestEngine(t, &fakeHostKeys{}, outbox, 1)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- engine.Start(ctx) }()
	deadline := time.After(2 * time.Second)
	for {
		outbox.mu.Lock()
		count := len(outbox.quarantined)
		outbox.mu.Unlock()
		if count == 1 {
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatal("unsupported event version was not quarantined")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manager did not stop")
	}
}
