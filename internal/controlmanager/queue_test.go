package controlmanager

import (
	"context"
	"testing"
	"time"
)

func TestResourceQueueDeduplicatesAndReschedulesDirtyKey(t *testing.T) {
	q := NewResourceQueue(2, time.Millisecond, 20*time.Millisecond)
	defer q.Shutdown()
	key := ResourceKey{Kind: HostResourceKind, WorkspaceID: 1, ID: "host-1"}
	if got := q.Add(key); got != QueueAccepted {
		t.Fatalf("first Add = %v", got)
	}
	if got := q.Add(key); got != QueueCoalesced {
		t.Fatalf("duplicate Add = %v", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	first, ok := q.Get(ctx)
	if !ok || first != key {
		t.Fatalf("first Get = %+v, %v", first, ok)
	}
	if got := q.Add(key); got != QueueAccepted {
		t.Fatalf("Add while processing = %v", got)
	}
	if got := q.Add(key); got != QueueCoalesced {
		t.Fatalf("second Add while processing = %v", got)
	}
	q.Done(key)
	second, ok := q.Get(ctx)
	if !ok || second != key {
		t.Fatalf("dirty follow-up Get = %+v, %v", second, ok)
	}
	q.Done(key)
	if q.Len() != 0 {
		t.Fatalf("queue length after Done = %d", q.Len())
	}
}

func TestResourceQueueBackpressureRateLimitAndShutdown(t *testing.T) {
	q := NewResourceQueue(1, 10*time.Millisecond, 40*time.Millisecond)
	keyA := ResourceKey{Kind: HostResourceKind, WorkspaceID: 1, ID: "a"}
	keyB := ResourceKey{Kind: HostResourceKind, WorkspaceID: 1, ID: "b"}
	if q.Add(keyA) != QueueAccepted || q.Add(keyB) != QueueBackpressured {
		t.Fatal("bounded admission did not apply backpressure")
	}
	if q.AddRateLimited(keyA) != QueueCoalesced || q.NumRequeues(keyA) != 1 {
		t.Fatal("rate-limited retry was not tracked/deduplicated")
	}
	q.Forget(keyA)
	if q.NumRequeues(keyA) != 0 {
		t.Fatal("successful processing did not reset retry history")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, ok := q.Get(ctx); !ok {
		t.Fatal("Get did not return accepted key")
	}
	q.Done(keyA)
	q.Shutdown()
	if q.Add(keyB) != QueueShuttingDown {
		t.Fatal("queue admitted work after shutdown")
	}
	if _, ok := q.Get(ctx); ok {
		t.Fatal("Get returned work after shutdown drained")
	}
}

func TestResourceQueueDelayedRetryAndCancellation(t *testing.T) {
	q := NewResourceQueue(4, time.Second, time.Minute)
	defer q.Shutdown()
	key := ResourceKey{Kind: HostResourceKind, WorkspaceID: 1, ID: "delayed"}
	if q.AddAfter(key, 60*time.Millisecond) != QueueAccepted {
		t.Fatal("AddAfter rejected key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, ok := q.Get(ctx); ok {
		t.Fatal("Get should honor canceled context while queue is delayed")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if got, ok := q.Get(ctx2); !ok || got != key {
		t.Fatalf("delayed Get = %+v, %v", got, ok)
	}
	q.Done(key)
}

func TestResourceQueueShutdownWakesAllIdleWorkers(t *testing.T) {
	q := NewResourceQueue(4, time.Millisecond, time.Second)
	const workers = 4
	results := make(chan bool, workers)
	started := make(chan struct{}, workers)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for i := 0; i < workers; i++ {
		go func() {
			started <- struct{}{}
			_, ok := q.Get(ctx)
			results <- ok
		}()
	}
	for i := 0; i < workers; i++ {
		<-started
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		q.mu.Lock()
		waiting := q.waiting
		q.mu.Unlock()
		if waiting == workers {
			break
		}
		select {
		case <-q.waitChange:
		case <-deadline.C:
			t.Fatal("workers did not enter Get")
		}
	}
	q.Shutdown()
	for i := 0; i < workers; i++ {
		select {
		case ok := <-results:
			if ok {
				t.Fatal("empty queue returned work during shutdown")
			}
		case <-time.After(time.Second):
			t.Fatal("Shutdown did not wake every idle worker")
		}
	}
}

// A pending delayed retry must not delay a new event for the same key:
// before the fix the Add was coalesced into the delayed entry, so an owner's
// host-key approval waited for the connector retry interval.
func TestResourceQueueAddOverridesDelayedRetry(t *testing.T) {
	q := NewResourceQueue(4, time.Millisecond, 20*time.Millisecond)
	defer q.Shutdown()
	key := ResourceKey{Kind: HostResourceKind, WorkspaceID: 1, ID: "host-1"}
	if got := q.AddAfter(key, time.Hour); got != QueueAccepted {
		t.Fatalf("AddAfter = %v", got)
	}
	if got := q.Add(key); got != QueueCoalesced {
		t.Fatalf("Add over a delayed retry = %v", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if got, ok := q.Get(ctx); !ok || got != key {
		t.Fatalf("Get after Add = %+v, %v; the delayed retry held the key back", got, ok)
	}

	// While the key is processing, a delayed retry followed by an event
	// re-queues it as soon as the current reconcile is done.
	if got := q.AddAfter(key, time.Hour); got != QueueAccepted {
		t.Fatalf("AddAfter while processing = %v", got)
	}
	q.Add(key)
	q.Done(key)
	if got, ok := q.Get(ctx); !ok || got != key {
		t.Fatalf("Get after Done = %+v, %v", got, ok)
	}
	q.Done(key)
	if q.Len() != 0 {
		t.Fatalf("queue length = %d", q.Len())
	}
}

// Delayed retries (every host that is not Ready waits for its next check)
// must not use the capacity of the queue of work that is ready to run, or a
// fleet with more not-Ready hosts than the capacity can no longer admit new
// events or finish its startup scan.
func TestResourceQueueDelayedKeysDoNotUseTheCapacity(t *testing.T) {
	q := NewResourceQueue(2, time.Millisecond, time.Second)
	defer q.Shutdown()
	for _, id := range []string{"a", "b", "c"} {
		if got := q.AddAfter(ResourceKey{Kind: HostResourceKind, WorkspaceID: 1, ID: id}, time.Hour); got != QueueAccepted {
			t.Fatalf("AddAfter %s = %v, want accepted", id, got)
		}
	}
	if got := q.Add(ResourceKey{Kind: HostResourceKind, WorkspaceID: 1, ID: "new"}); got != QueueAccepted {
		t.Fatalf("Add with 3 delayed keys and capacity 2 = %v, want accepted", got)
	}
}

// A key that is added again while a worker processes it is reconciled again
// afterwards, even if the queue is full at the moment the worker finishes.
func TestResourceQueueRequeuesAfterDoneWhenFull(t *testing.T) {
	q := NewResourceQueue(2, time.Millisecond, time.Second)
	defer q.Shutdown()
	key := func(id string) ResourceKey { return ResourceKey{Kind: HostResourceKind, WorkspaceID: 1, ID: id} }
	q.Add(key("a"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if got, ok := q.Get(ctx); !ok || got != key("a") {
		t.Fatalf("Get = %+v, %v", got, ok)
	}
	if got := q.Add(key("a")); got != QueueAccepted { // a is processing: marked dirty
		t.Fatalf("Add while processing = %v", got)
	}
	q.Add(key("b"))
	q.Add(key("c")) // the queue is full now
	q.Done(key("a"))
	seen := map[string]bool{}
	for range 3 {
		got, ok := q.Get(ctx)
		if !ok {
			break
		}
		seen[got.ID] = true
		q.Done(got)
	}
	if !seen["a"] || !seen["b"] || !seen["c"] {
		t.Fatalf("keys reconciled = %v; a key added during processing was lost", seen)
	}
}

// Stopping drops pending retries: they are in memory only, and the next
// start scans everything. Running them at shutdown would only delay the stop.
func TestResourceQueueShutdownDropsDelayedRetries(t *testing.T) {
	q := NewResourceQueue(8, time.Millisecond, time.Second)
	q.AddAfter(ResourceKey{Kind: HostResourceKind, WorkspaceID: 1, ID: "later"}, time.Hour)
	q.Add(ResourceKey{Kind: HostResourceKind, WorkspaceID: 1, ID: "now"})
	q.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var got []string
	for {
		key, ok := q.Get(ctx)
		if !ok {
			break
		}
		got = append(got, key.ID)
		q.Done(key)
	}
	if len(got) != 1 || got[0] != "now" {
		t.Fatalf("work after shutdown = %v, want only the ready key", got)
	}
}
