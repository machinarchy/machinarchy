// Package netpolicy decides which network destinations Machinarchy may dial and
// dials them safely. Hostnames are resolved once under the policy and the
// validated address is the address actually dialed, so a DNS answer that changes
// between the check and the connection (DNS rebinding) cannot bypass it.
//
// Rules, in order:
//
//  1. Unspecified, multicast, broadcast, "this network" (0.0.0.0/8), zoned
//     addresses, and well-known cloud metadata addresses are always denied.
//  2. Addresses inside Deny are denied.
//  3. Addresses inside Allow are allowed. This is the way to permit loopback or
//     link-local ranges deliberately.
//  4. Loopback is denied unless AllowLoopback is set.
//  5. Link-local unicast (169.254.0.0/16, fe80::/10) is denied.
//  6. Private ranges (RFC 1918, CGNAT 100.64.0.0/10, fc00::/7) are allowed only
//     when AllowPrivate is set.
//  7. Everything else (public addresses) is allowed.
//
// A hostname is allowed only if every address it resolves to is allowed.
//
// The policy limits where connections may go; it does not replace network-layer
// egress controls, which should be applied as defense in depth.
package netpolicy

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

// Resolver looks up the addresses of a hostname. *net.Resolver satisfies it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Policy is an immutable-by-convention destination policy. The zero value is the
// strictest: no private, no loopback, no explicit allow list.
type Policy struct {
	AllowPrivate  bool
	AllowLoopback bool
	Allow         []netip.Prefix
	Deny          []netip.Prefix
	// Resolver defaults to net.DefaultResolver.
	Resolver Resolver
	// DialTimeout bounds one connection attempt; it defaults to 10 seconds.
	DialTimeout time.Duration
}

// Default allows public and private destinations and denies loopback,
// link-local, metadata, multicast, and unspecified addresses.
func Default() Policy { return Policy{AllowPrivate: true} }

var (
	cgnat = netip.MustParsePrefix("100.64.0.0/10")

	// Addresses of cloud instance-metadata services. Reaching them through a
	// managed-host record would expose instance credentials, so they stay denied
	// even when an Allow range covers them.
	metadataAddrs = []netip.Addr{
		netip.MustParseAddr("169.254.169.254"),
		netip.MustParseAddr("169.254.170.2"),
		netip.MustParseAddr("100.100.100.200"),
		netip.MustParseAddr("fd00:ec2::254"),
	}
)

// CheckAddr reports whether dialing addr is permitted.
func (p Policy) CheckAddr(addr netip.Addr) error {
	addr = addr.Unmap()
	deny := func(reason string) error {
		return fmt.Errorf("%w: %s is %s", domain.ErrTargetNotAllowed, addr, reason)
	}
	switch {
	case !addr.IsValid():
		return fmt.Errorf("%w: invalid address", domain.ErrTargetNotAllowed)
	case addr.Zone() != "":
		return deny("a zoned address")
	case addr.IsUnspecified():
		return deny("unspecified")
	case addr.IsMulticast():
		return deny("multicast")
	case addr.Is4() && (addr == netip.AddrFrom4([4]byte{255, 255, 255, 255}) || addr.As4()[0] == 0):
		return deny("a broadcast or this-network address")
	}
	for _, metadata := range metadataAddrs {
		if addr == metadata {
			return deny("a cloud metadata address")
		}
	}
	for _, prefix := range p.Deny {
		if prefix.Contains(addr) {
			return deny("in a denied range")
		}
	}
	for _, prefix := range p.Allow {
		if prefix.Contains(addr) {
			return nil
		}
	}
	switch {
	case addr.IsLoopback():
		if !p.AllowLoopback {
			return deny("a loopback address")
		}
	case addr.IsLinkLocalUnicast():
		return deny("a link-local address")
	case addr.IsPrivate() || cgnat.Contains(addr):
		if !p.AllowPrivate {
			return deny("a private address")
		}
	}
	return nil
}

// CheckHost checks host only when it is an IP literal, without any DNS lookup.
// Hostnames pass here and are enforced when they are resolved for dialing.
func (p Policy) CheckHost(host string) error {
	if addr, ok := parseLiteral(host); ok {
		return p.CheckAddr(addr)
	}
	return nil
}

// Resolve returns the addresses host may be dialed at. An IP literal is checked
// directly; a hostname is resolved once and every answer must be permitted.
func (p Policy) Resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if addr, ok := parseLiteral(host); ok {
		if err := p.CheckAddr(addr); err != nil {
			return nil, err
		}
		return []netip.Addr{addr.Unmap()}, nil
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, fmt.Errorf("%w: host is required", domain.ErrInvalidInput)
	}
	resolver := p.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addrs, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve target host: %w", err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("resolve target host: no addresses found")
	}
	for _, addr := range addrs {
		if err := p.CheckAddr(addr); err != nil {
			return nil, err
		}
	}
	return addrs, nil
}

// DialContext resolves address under the policy and dials the validated IP
// directly, never the hostname. It has the signature of net.Dialer.DialContext
// and only supports the "tcp" network.
func (p Policy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" {
		return nil, fmt.Errorf("%w: unsupported network %q", domain.ErrInvalidInput, network)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid target address: %v", domain.ErrInvalidInput, err)
	}
	addrs, err := p.Resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	timeout := p.DialTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	var lastErr error
	for _, addr := range addrs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// ParsePrefixes parses a comma-separated list of CIDRs or single IP addresses.
// An empty string yields no prefixes.
func ParsePrefixes(list string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(item); err == nil {
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(item)
		if err != nil {
			return nil, fmt.Errorf("%q is not a valid CIDR or IP address", item)
		}
		addr = addr.Unmap()
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return prefixes, nil
}

func parseLiteral(host string) (netip.Addr, bool) {
	host = strings.TrimSpace(host)
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr, true
}
