package controlmanager

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/eventing"
)

// hangingOutbox's Retry blocks until its context ends, like a database that
// stops answering during shutdown.
type hangingOutbox struct {
	fakeOutbox
	mu             sync.Mutex
	calls          int
	firstCancelled bool
}

func (h *hangingOutbox) Retry(ctx context.Context, _, _ string, _ resource.ID, _ time.Time, _ string) error {
	h.mu.Lock()
	h.calls++
	if h.calls == 1 {
		h.firstCancelled = ctx.Err() != nil
	}
	h.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

// TestStoppingDispatcherReleasesClaimsWithinABound (AUDIT Q-18): when the
// dispatcher stops with deliveries claimed, it hands them back with a bounded
// context: neither forever (a hung database would block shutdown) nor already
// cancelled (the release would fail at once and wait for the lease).
func TestStoppingDispatcherReleasesClaimsWithinABound(t *testing.T) {
	previous := stopReleaseTimeout
	stopReleaseTimeout = 200 * time.Millisecond
	t.Cleanup(func() { stopReleaseTimeout = previous })

	var deliveries []eventing.ClaimedEvent
	for i := range 3 {
		id, _ := resource.NewResourceID()
		event, err := eventing.NewHostEvent(eventing.HostCreated, 1, eventing.Subject{Kind: "host", ID: id, Generation: 1, Revision: 1}, "created", time.Now(), "", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		deliveries = append(deliveries, eventing.ClaimedEvent{Envelope: event, Attempt: i + 1})
	}
	outbox := &hangingOutbox{fakeOutbox: fakeOutbox{deliveries: deliveries}}
	engine := newTestEngine(t, &fakeHostKeys{}, &outbox.fakeOutbox, 1)
	engine.deps.Outbox = outbox

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the dispatcher is stopping
	done := make(chan error, 1)
	began := time.Now()
	go func() { done <- engine.dispatchBatch(ctx) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("dispatchBatch did not return: the shutdown release has no bound")
	}
	if elapsed := time.Since(began); elapsed > time.Second {
		t.Errorf("releasing 3 claims took %s; the bound is shared, %s", elapsed, stopReleaseTimeout)
	}
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	// Every claim is handed back; the first release must not start on the
	// already-cancelled stop context. (Later ones may find the shared bound
	// used up by the hung first one; they are reclaimed after the lease.)
	if outbox.calls != 3 || outbox.firstCancelled {
		t.Errorf("Retry calls=%d, first started cancelled=%v; want 3 calls, false", outbox.calls, outbox.firstCancelled)
	}
}
