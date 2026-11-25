package domain

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// Login throttling (AUDIT S-09). Failures are counted per account and per
// client network in the database, so the limits hold across replicas. An
// account is never locked: past a few failures each further attempt must wait
// longer, so an attacker cannot lock an owner out.

// LoginThrottlePolicy sets the limits.
type LoginThrottlePolicy struct {
	// FreeFailures is how many failures an account may have in Window
	// before attempts are delayed.
	FreeFailures int
	// BaseDelay is the first delay; each further failure doubles it, up to
	// MaxDelay.
	BaseDelay time.Duration
	MaxDelay  time.Duration
	// IPMaxFailures is how many failures one client network may have in
	// Window, across all accounts; past it the network waits until the
	// window ends.
	IPMaxFailures int
	// Window is how long failures are remembered.
	Window time.Duration
}

// DefaultLoginThrottlePolicy: 5 free failures, then 1 s doubling to 15 min;
// 20 failures per network per 15 minutes.
func DefaultLoginThrottlePolicy() LoginThrottlePolicy {
	return LoginThrottlePolicy{FreeFailures: 5, BaseDelay: time.Second, MaxDelay: 15 * time.Minute, IPMaxFailures: 20, Window: 15 * time.Minute}
}

// Validate checks the policy's bounds.
func (p LoginThrottlePolicy) Validate() error {
	if p.FreeFailures < 1 || p.FreeFailures > 100 || p.BaseDelay <= 0 || p.MaxDelay < p.BaseDelay || p.MaxDelay > 24*time.Hour ||
		p.IPMaxFailures < 1 || p.IPMaxFailures > 10000 || p.Window < time.Minute || p.Window > 24*time.Hour {
		return fmt.Errorf("%w: login throttle policy is out of bounds", ErrInvalidInput)
	}
	return nil
}

// AccountDelay is the wait after an account's failures-th failure.
func (p LoginThrottlePolicy) AccountDelay(failures int) time.Duration {
	if failures <= p.FreeFailures {
		return 0
	}
	delay := p.BaseDelay
	for i := p.FreeFailures + 1; i < failures && delay < p.MaxDelay; i++ {
		delay *= 2
	}
	return min(delay, p.MaxDelay)
}

// ThrottledError reports a login refused because of earlier failures.
type ThrottledError struct{ RetryAfter time.Duration }

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("too many failed logins; retry after %s", e.RetryAfter.Round(time.Second))
}

// ErrLoginThrottled matches every *ThrottledError with errors.Is.
var ErrLoginThrottled = errors.New("login throttled")

// Is lets errors.Is(err, ErrLoginThrottled) match.
func (e *ThrottledError) Is(target error) bool { return target == ErrLoginThrottled }

// LoginFailure is one failed attempt to record.
type LoginFailure struct {
	// Account identifies the account: "user:ID" for an existing account,
	// "name:…" for one that does not exist (treated the same way).
	Account string
	// Network is the client's ClientKey; "" when unknown.
	Network string
	// WorkspaceID, when the account exists, receives the audit event when
	// the account first enters backoff; 0 for no audit.
	WorkspaceID int
	UserID      int
}

// LoginThrottleStore counts failures.
type LoginThrottleStore interface {
	// LoginBlockedUntil returns when the account and the network may try
	// again (zero when they may now).
	LoginBlockedUntil(ctx context.Context, account, network string, now time.Time) (time.Time, error)
	// RecordLoginFailure counts a failure under policy and returns the
	// account's failure count in the window.
	RecordLoginFailure(ctx context.Context, failure LoginFailure, policy LoginThrottlePolicy, now time.Time) (int, error)
	// ResetLoginFailures forgets an account's failures after a success.
	ResetLoginFailures(ctx context.Context, account string) error
}

// ClientKey groups client addresses for limits: an IPv4 address as is, an
// IPv6 address by its /64 (one host usually owns a whole /64). An
// unparseable value is returned trimmed to 64 bytes.
func ClientKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		if len(ip) > 64 {
			return ip[:64]
		}
		return ip
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return addr.String()
	}
	return prefix.String()
}
