package controlmanager

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

// heldLeases simulates another replica holding the resync lease until a
// time (it crashed and its lease has not expired yet).
type heldLeases struct {
	mu         sync.Mutex
	heldUntil  time.Time
	acquiredAt time.Time
	ttl        time.Duration
	attempts   int
}

func (h *heldLeases) AcquireResourceLease(_ context.Context, lease resource.Lease, now time.Time, ttl time.Duration) (resource.Lease, bool, error) {
	if lease.Kind != "resync" {
		return lease, true, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.attempts++
	if now.Before(h.heldUntil) {
		return lease, false, nil
	}
	if h.acquiredAt.IsZero() {
		h.acquiredAt, h.ttl = now, ttl
	}
	lease.Token, lease.ExpiresAt = 2, now.Add(ttl)
	return lease, true, nil
}
func (h *heldLeases) RenewResourceLease(_ context.Context, lease resource.Lease, now time.Time, ttl time.Duration) (resource.Lease, bool, error) {
	lease.ExpiresAt = now.Add(ttl)
	return lease, true, nil
}
func (h *heldLeases) ReleaseResourceLease(context.Context, resource.Lease) error { return nil }

// TestSurvivorTakesTheResyncLeaseSoonAfterItExpires (AUDIT/20 F20-08): when
// the replica holding the resync lease crashes, the other one must take the
// lease soon after it expires, not a full interval later; and the lease
// must not outlive the next scan by more than a fraction of the interval.
func TestSurvivorTakesTheResyncLeaseSoonAfterItExpires(t *testing.T) {
	const interval = time.Second
	began := time.Now()
	leases := &heldLeases{heldUntil: began.Add(300 * time.Millisecond)}
	engine := newTestEngine(t, &fakeHostKeys{}, &fakeOutbox{}, 1)
	engine.cfg.ResyncInterval = interval
	engine.deps.Leases = leases
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- engine.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	deadline := time.Now().Add(3 * time.Second)
	for {
		leases.mu.Lock()
		acquiredAt, ttl := leases.acquiredAt, leases.ttl
		leases.mu.Unlock()
		if !acquiredAt.IsZero() {
			if waited := acquiredAt.Sub(began); waited > 800*time.Millisecond {
				t.Errorf("took the lease %s after start (it expired at 300 ms); want well under one interval (%s)", waited, interval)
			}
			if ttl > interval+interval/4+time.Millisecond {
				t.Errorf("resync lease TTL %s; want at most 1.25 × the interval (%s)", ttl, interval)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("never took the resync lease")
		}
		time.Sleep(20 * time.Millisecond) // poll interval of a deadline-bounded wait
	}
}
