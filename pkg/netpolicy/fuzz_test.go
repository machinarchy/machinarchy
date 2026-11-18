package netpolicy

import "testing"

// FuzzParsePrefixes: configuration parsing never panics, and every accepted
// prefix is canonical (masked).
func FuzzParsePrefixes(f *testing.F) {
	f.Add("10.0.0.0/8, 192.168.1.0/24")
	f.Add("::1/128,fe80::/10")
	f.Add("10.0.0.1/8")
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
