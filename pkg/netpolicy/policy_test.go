package netpolicy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

func TestDefaultPolicyDecisions(t *testing.T) {
	policy := Default()
	tests := []struct {
		addr    string
		allowed bool
	}{
		{"8.8.8.8", true},
		{"192.0.2.10", true}, // documentation range used in examples
		{"2606:4700:4700::1111", true},
		{"10.0.0.5", true},
		{"172.16.4.9", true},
		{"192.168.1.20", true},
		{"100.64.0.7", true}, // CGNAT counts as private
		{"fd12:3456::1", true},
		{"127.0.0.1", false},
		{"::1", false},
		{"169.254.10.10", false},
		{"169.254.169.254", false}, // cloud metadata
		{"fe80::1", false},
		{"fd00:ec2::254", false}, // AWS IPv6 metadata
		{"100.100.100.200", false},
		{"0.0.0.0", false},
		{"::", false},
		{"0.1.2.3", false},
		{"255.255.255.255", false},
		{"224.0.0.1", false},
		{"ff02::1", false},
		{"::ffff:127.0.0.1", false}, // IPv4-mapped loopback
		{"::ffff:169.254.169.254", false},
		{"::ffff:10.0.0.1", true},
		{"fe80::1%eth0", false},
	}
	for _, tc := range tests {
		err := policy.CheckAddr(netip.MustParseAddr(tc.addr))
		if tc.allowed && err != nil {
			t.Errorf("CheckAddr(%s) = %v, want allowed", tc.addr, err)
		}
		if !tc.allowed {
			if err == nil {
				t.Errorf("CheckAddr(%s) allowed, want denied", tc.addr)
			} else if !errors.Is(err, domain.ErrTargetNotAllowed) {
				t.Errorf("CheckAddr(%s) error = %v, want ErrTargetNotAllowed", tc.addr, err)
			}
		}
	}
}

func TestZeroPolicyIsStrict(t *testing.T) {
	var policy Policy
	if err := policy.CheckAddr(netip.MustParseAddr("10.0.0.5")); err == nil {
		t.Error("the zero policy must not allow private addresses")
	}
	if err := policy.CheckAddr(netip.MustParseAddr("8.8.8.8")); err != nil {
		t.Errorf("the zero policy should still allow public addresses: %v", err)
	}
}

func TestOverridesAndPrecedence(t *testing.T) {
	prefixes := func(list string) []netip.Prefix {
		t.Helper()
		parsed, err := ParsePrefixes(list)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	t.Run("private can be disabled", func(t *testing.T) {
		if err := (Policy{}).CheckAddr(netip.MustParseAddr("192.168.1.1")); err == nil {
			t.Error("private should be denied when AllowPrivate is false")
		}
	})
	t.Run("loopback needs an explicit opt-in", func(t *testing.T) {
		if err := (Policy{AllowLoopback: true}).CheckAddr(netip.MustParseAddr("127.0.0.1")); err != nil {
			t.Errorf("loopback should be allowed with AllowLoopback: %v", err)
		}
	})
	t.Run("allow list can open a link-local range", func(t *testing.T) {
		policy := Policy{Allow: prefixes("169.254.50.0/24")}
		if err := policy.CheckAddr(netip.MustParseAddr("169.254.50.9")); err != nil {
			t.Errorf("explicitly allowed range was denied: %v", err)
		}
		if err := policy.CheckAddr(netip.MustParseAddr("169.254.51.9")); err == nil {
			t.Error("addresses outside the allowed range must stay denied")
		}
	})
	t.Run("metadata stays denied even inside an allowed range", func(t *testing.T) {
		policy := Policy{AllowPrivate: true, Allow: prefixes("169.254.0.0/16")}
		if err := policy.CheckAddr(netip.MustParseAddr("169.254.169.254")); err == nil {
			t.Error("the metadata address must never be allowed")
		}
	})
	t.Run("deny wins over allow", func(t *testing.T) {
		policy := Policy{Allow: prefixes("10.0.0.0/8"), Deny: prefixes("10.9.0.0/16")}
		if err := policy.CheckAddr(netip.MustParseAddr("10.9.1.1")); err == nil {
			t.Error("a denied range must win over an allowed one")
		}
		if err := policy.CheckAddr(netip.MustParseAddr("10.1.1.1")); err != nil {
			t.Errorf("address outside the denied range was rejected: %v", err)
		}
	})
	t.Run("deny can carve public space out", func(t *testing.T) {
		policy := Policy{AllowPrivate: true, Deny: prefixes("203.0.113.0/24")}
		if err := policy.CheckAddr(netip.MustParseAddr("203.0.113.5")); err == nil {
			t.Error("denied public range was allowed")
		}
	})
}

func TestParsePrefixes(t *testing.T) {
	got, err := ParsePrefixes(" 10.0.0.0/8 , 192.0.2.7,fd00::/8,, ")
	if err != nil || len(got) != 3 {
		t.Fatalf("ParsePrefixes() = %v, %v; want 3 prefixes", got, err)
	}
	if got[1].Bits() != 32 {
		t.Errorf("a bare IPv4 address must become a /32, got %s", got[1])
	}
	if none, err := ParsePrefixes(""); err != nil || len(none) != 0 {
		t.Errorf("empty list = %v, %v; want none", none, err)
	}
	if _, err := ParsePrefixes("10.0.0.0/8,not-an-ip"); err == nil {
		t.Error("an invalid entry must be rejected, not skipped")
	}
}

func TestCheckHostOnlyInspectsLiterals(t *testing.T) {
	policy := Default()
	for _, host := range []string{"127.0.0.1", "[::1]", " 169.254.169.254 "} {
		if err := policy.CheckHost(host); !errors.Is(err, domain.ErrTargetNotAllowed) {
			t.Errorf("CheckHost(%q) = %v, want ErrTargetNotAllowed", host, err)
		}
	}
	for _, host := range []string{"web-01.example.com", "localhost", "10.0.0.5"} {
		if err := policy.CheckHost(host); err != nil {
			t.Errorf("CheckHost(%q) = %v; hostnames are enforced at dial time and private is allowed", host, err)
		}
	}
}

type fakeResolver struct {
	answers map[string][]netip.Addr
	err     error
	calls   atomic.Int32
}

func (f *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return f.answers[host], nil
}

func TestResolveRequiresEveryAnswerToBeAllowed(t *testing.T) {
	resolver := &fakeResolver{answers: map[string][]netip.Addr{
		"good.example":    {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.9")},
		"mixed.example":   {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("169.254.169.254")},
		"loop.example":    {netip.MustParseAddr("127.0.0.1")},
		"mapped.example":  {netip.MustParseAddr("::ffff:127.0.0.1")},
		"nothing.example": {},
	}}
	policy := Default()
	policy.Resolver = resolver

	if addrs, err := policy.Resolve(context.Background(), "good.example"); err != nil || len(addrs) != 2 {
		t.Fatalf("Resolve(good) = %v, %v", addrs, err)
	}
	for _, host := range []string{"mixed.example", "loop.example", "mapped.example"} {
		if _, err := policy.Resolve(context.Background(), host); !errors.Is(err, domain.ErrTargetNotAllowed) {
			t.Errorf("Resolve(%s) error = %v; one bad answer (rebinding set) must deny the host", host, err)
		}
	}
	if _, err := policy.Resolve(context.Background(), "nothing.example"); err == nil {
		t.Error("a hostname with no addresses must fail")
	}
	before := resolver.calls.Load()
	if _, err := policy.Resolve(context.Background(), "10.0.0.5"); err != nil {
		t.Fatal(err)
	}
	if resolver.calls.Load() != before {
		t.Error("an IP literal must not trigger a DNS lookup")
	}
	resolver.err = errors.New("lookup failed")
	if _, err := policy.Resolve(context.Background(), "good.example"); err == nil || errors.Is(err, domain.ErrTargetNotAllowed) {
		t.Errorf("a DNS failure should surface as a resolution error, not a policy denial: %v", err)
	}
}

func listen(t *testing.T) (port string, accepted *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted = new(atomic.Int32)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = conn.Close()
		}
	}()
	_, port, _ = net.SplitHostPort(listener.Addr().String())
	return port, accepted
}

func TestDialContextDialsTheValidatedAddressOnly(t *testing.T) {
	port, accepted := listen(t)
	resolver := &fakeResolver{answers: map[string][]netip.Addr{
		"svc.internal.test": {netip.MustParseAddr("127.0.0.1")},
	}}

	t.Run("denied before any connection is attempted", func(t *testing.T) {
		policy := Default()
		policy.Resolver = resolver
		if _, err := policy.DialContext(context.Background(), "tcp", net.JoinHostPort("svc.internal.test", port)); !errors.Is(err, domain.ErrTargetNotAllowed) {
			t.Fatalf("DialContext error = %v, want ErrTargetNotAllowed", err)
		}
		if _, err := policy.DialContext(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", port)); !errors.Is(err, domain.ErrTargetNotAllowed) {
			t.Fatalf("literal loopback error = %v, want ErrTargetNotAllowed", err)
		}
		time.Sleep(50 * time.Millisecond)
		if accepted.Load() != 0 {
			t.Fatalf("the listener saw %d connections; a denied target must never be dialed", accepted.Load())
		}
	})

	t.Run("allowed target resolves once and dials the pinned address", func(t *testing.T) {
		resolver.calls.Store(0)
		policy := Policy{AllowLoopback: true, Resolver: resolver}
		conn, err := policy.DialContext(context.Background(), "tcp", net.JoinHostPort("svc.internal.test", port))
		if err != nil {
			t.Fatalf("DialContext() error = %v", err)
		}
		defer conn.Close()
		if got := resolver.calls.Load(); got != 1 {
			t.Errorf("hostname resolved %d times, want exactly 1 (no re-resolution between check and dial)", got)
		}
		if remote := conn.RemoteAddr().String(); remote != net.JoinHostPort("127.0.0.1", port) {
			t.Errorf("connected to %s, want the validated IP", remote)
		}
	})

	t.Run("only tcp and valid addresses are accepted", func(t *testing.T) {
		policy := Default()
		if _, err := policy.DialContext(context.Background(), "udp", "8.8.8.8:53"); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("udp error = %v, want ErrInvalidInput", err)
		}
		if _, err := policy.DialContext(context.Background(), "tcp", "no-port"); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("missing port error = %v, want ErrInvalidInput", err)
		}
	})
}
