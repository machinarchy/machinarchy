package netpolicy

import "testing"

// FuzzParsePrefixes: configuration parsing never panics, and every accepted
// prefix is canonical (masked).
func FuzzParsePrefixes(f *testing.F) {
	f.Add("10.0.0.0/8, 192.168.1.0/24")
	f.Add("::1/128,fe80::/10")
	f.Add("10.0.0.1/8")
	f.Add("64:ff9b::/96") // NAT64 (S-19)
	f.Add("2002::/16")    // 6to4
	f.Add("2001::/32")    // Teredo
	f.Add("64:ff9b::a00:1/128, 2002:a00:1::/48, 2001:0:4136:e378::/64")
	f.Fuzz(func(t *testing.T, list string) {
		prefixes, err := ParsePrefixes(list)
		if err != nil {
			return
		}
		for _, prefix := range prefixes {
			if !prefix.IsValid() {
				t.Fatalf("invalid prefix accepted from %q", list)
			}
		}
	})
}
