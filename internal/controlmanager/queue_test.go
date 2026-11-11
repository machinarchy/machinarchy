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
