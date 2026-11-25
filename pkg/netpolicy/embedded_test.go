package netpolicy

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
)

func nat64(v4 string) netip.Addr {
	b := netip.MustParseAddr("64:ff9b::").As16()
	four := netip.MustParseAddr(v4).As4()
	copy(b[12:], four[:])
	return netip.AddrFrom16(b)
}

func sixToFour(v4 string) netip.Addr {
	var b [16]byte
	b[0], b[1] = 0x20, 0x02
	four := netip.MustParseAddr(v4).As4()
	copy(b[2:6], four[:])
	b[15] = 1
	return netip.AddrFrom16(b)
}

func teredo(server, client string) netip.Addr {
	var b [16]byte
	b[0], b[1] = 0x20, 0x01
	s, c := netip.MustParseAddr(server).As4(), netip.MustParseAddr(client).As4()
	copy(b[4:8], s[:])
	for i := range 4 {
		b[12+i] = c[i] ^ 0xff // the client address is stored inverted
	}
	return netip.AddrFrom16(b)
}

// TestEmbeddedIPv4IsChecked (AUDIT S-19): an IPv6 address that carries an
// IPv4 address (NAT64, 6to4, Teredo) reaches that IPv4 host, so the policy
// must judge the IPv4 address too.
func TestEmbeddedIPv4IsChecked(t *testing.T) {
	strict := Policy{}
	private := Policy{AllowPrivate: true}
	denyDocs := Policy{Deny: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}}
	denied := []struct {
		name   string
		policy Policy
		addr   netip.Addr
	}{
		{"NAT64 loopback", strict, nat64("127.0.0.1")},
		{"NAT64 private", strict, nat64("10.1.2.3")},
		{"NAT64 metadata, even with private allowed", private, nat64("169.254.169.254")},
		{"NAT64 link-local", private, nat64("169.254.10.10")},
		{"NAT64 into a denied range", denyDocs, nat64("192.0.2.7")},
		{"6to4 private", strict, sixToFour("192.168.1.1")},
		{"6to4 loopback", private, sixToFour("127.0.0.1")},
		{"6to4 this-network", private, sixToFour("0.0.0.1")},
		{"Teredo private client", strict, teredo("65.54.227.120", "10.0.0.1")},
		{"Teredo loopback server", private, teredo("127.0.0.1", "8.8.8.8")},
		{"Teredo metadata client", private, teredo("65.54.227.120", "169.254.169.254")},
	}
	for _, c := range denied {
		if err := c.policy.CheckAddr(c.addr); !errors.Is(err, domain.ErrTargetNotAllowed) {
			t.Errorf("%s (%s): allowed", c.name, c.addr)
		}
	}
	for _, addr := range []netip.Addr{nat64("8.8.8.8"), sixToFour("8.8.4.4"), teredo("65.54.227.120", "203.0.114.9"), netip.MustParseAddr("2001:db8::1")} {
		if err := strict.CheckAddr(addr); err != nil {
			t.Errorf("%s embeds a public address: %v", addr, err)
		}
	}
	// With private destinations allowed, a private embedded address is fine.
	if err := private.CheckAddr(nat64("10.1.2.3")); err != nil {
		t.Errorf("NAT64 of a private address with AllowPrivate: %v", err)
	}
}

// FuzzEmbeddedIPv4: whenever the IPv4 address inside a NAT64, 6to4 or Teredo
// address is denied, the IPv6 address is denied too.
func FuzzEmbeddedIPv4(f *testing.F) {
	f.Add([]byte{0, 0x64, 0xff, 0x9b, 0, 0, 0, 0, 0, 0, 0, 0, 127, 0, 0, 1}, false)
	f.Add([]byte{0x20, 0x02, 10, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, true)
	f.Add([]byte{0x20, 0x01, 0, 0, 65, 54, 227, 120, 0, 0, 0, 0, 0xf5, 0xff, 0xff, 0xfe}, false)
	f.Fuzz(func(t *testing.T, raw []byte, allowPrivate bool) {
		if len(raw) != 16 {
			return
		}
		addr := netip.AddrFrom16([16]byte(raw))
		policy := Policy{AllowPrivate: allowPrivate}
		for _, v4 := range embeddedIPv4(addr) {
			if policy.CheckAddr(v4) != nil && policy.CheckAddr(addr) == nil {
				t.Fatalf("%s embeds denied %s but is allowed", addr, v4)
			}
		}
	})
}
