package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

// testLoginThrottle covers migration 000006 and the login throttle store
// (AUDIT S-09) on both engines.
func testLoginThrottle(t *testing.T, f *fixture) {
	ctx := context.Background()
	alice := f.member(t, "alice", domain.RoleOwner)
	policy := domain.LoginThrottlePolicy{FreeFailures: 3, BaseDelay: time.Second, MaxDelay: 8 * time.Second, IPMaxFailures: 6, Window: 10 * time.Minute}
	now := time.Now().UTC()
	account := fmt.Sprintf("user:%d", alice.ID)
	fail := func(account, network string, at time.Time) int {
		t.Helper()
		n, err := f.stores.LoginThrottle.RecordLoginFailure(ctx, domain.LoginFailure{Account: account, Network: network,
			UserID: alice.ID, WorkspaceID: alice.WorkspaceID}, policy, at)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	blocked := func(account, network string, at time.Time) time.Time {
		t.Helper()
		until, err := f.stores.LoginThrottle.LoginBlockedUntil(ctx, account, network, at)
		if err != nil {
			t.Fatal(err)
		}
		return until
	}

	// Free failures, then a delay that doubles.
	for i := 1; i <= 3; i++ {
		if n := fail(account, "192.0.2.1", now); n != i || !blocked(account, "192.0.2.1", now).IsZero() {
			t.Fatalf("failure %d: count %d, blocked until %v", i, n, blocked(account, "192.0.2.1", now))
		}
	}
	fail(account, "192.0.2.1", now)
	if until := blocked(account, "192.0.2.99", now); !until.Equal(now.Add(time.Second).Truncate(time.Nanosecond)) && until.Sub(now) != time.Second {
		t.Fatalf("4th failure: blocked until +%s, want +1s", until.Sub(now))
	}
	fail(account, "192.0.2.1", now)
	if until := blocked(account, "", now); until.Sub(now) != 2*time.Second {
		t.Fatalf("5th failure: +%s, want +2s", until.Sub(now))
	}
	if !blocked(account, "", now.Add(3*time.Second)).IsZero() {
		t.Fatal("the delay did not end")
	}
	// Entering backoff is audited once.
	var audits int
	if err := f.stores.DB.QueryRowContext(ctx, f.pick(`SELECT COUNT(*) FROM audit_events WHERE action = ?`,
		`SELECT COUNT(*) FROM audit_events WHERE action = $1`), domain.AuditLoginThrottled).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("backoff audit events = %d, %v", audits, err)
	}
	// A success resets the account; a new window starts over.
	if err := f.stores.LoginThrottle.ResetLoginFailures(ctx, account); err != nil {
		t.Fatal(err)
	}
	if n := fail(account, "", now); n != 1 {
		t.Fatalf("after reset: count %d", n)
	}
	if n := fail(account, "", now.Add(11*time.Minute)); n != 1 {
		t.Fatalf("after the window: count %d, want a new window", n)
	}

	// A network is limited across accounts, until its window ends.
	for i := range policy.IPMaxFailures {
		fail(fmt.Sprintf("name:guess-%d", i), "198.51.100.0/24-ish", now)
	}
	if until := blocked("name:someone-else", "198.51.100.0/24-ish", now); until.Before(now.Add(9 * time.Minute)) {
		t.Fatalf("network after %d failures: blocked until +%s, want about the window", policy.IPMaxFailures, until.Sub(now))
	}
	if !blocked("name:someone-else", "203.0.113.5", now).IsZero() {
		t.Fatal("another network was blocked")
	}

	// Concurrent failures are all counted (atomic upsert).
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.stores.LoginThrottle.RecordLoginFailure(ctx, domain.LoginFailure{Account: "name:race"}, policy, now); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := fail("name:race", "", now); n != 21 {
		t.Fatalf("after 20 concurrent failures and one more: count %d, want 21", n)
	}
}
