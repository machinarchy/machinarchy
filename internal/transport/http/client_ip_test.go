package http

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/machinarchy/machinarchy/internal/config"
)

// S-10: X-Forwarded-For is believed only from the configured proxies, and
// only as far as the first address that is not one of them.
func TestClientIPExtractor(t *testing.T) {
	proxies := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24"), netip.MustParsePrefix("2001:db8:ffff::/48")}
	for _, tc := range []struct {
		name    string
		trusted []netip.Prefix
		remote  string
		xff     string
		want    string
	}{
		{"no proxies: the header is ignored", nil, "203.0.113.5:1", "198.51.100.1", "203.0.113.5"},
		{"untrusted peer: a forged header is ignored", proxies, "203.0.113.5:1", "198.51.100.1", "203.0.113.5"},
		{"trusted proxy: the client it saw", proxies, "10.0.0.7:1", "198.51.100.1", "198.51.100.1"},
		{"a client cannot prepend its own address", proxies, "10.0.0.7:1", "1.1.1.1, 198.51.100.1", "198.51.100.1"},
		{"a chain of trusted proxies", proxies, "10.0.0.7:1", "198.51.100.1, 10.0.0.9", "198.51.100.1"},
		{"private ranges are not trusted unless listed", proxies, "10.0.0.7:1", "198.51.100.1, 192.168.1.1", "192.168.1.1"},
		{"loopback is not trusted unless listed", proxies, "127.0.0.1:1", "198.51.100.1", "127.0.0.1"},
		{"IPv6 proxy", proxies, "[2001:db8:ffff::1]:1", "2001:db8:1::5", "2001:db8:1::5"},
		{"trusted proxy without the header", proxies, "10.0.0.7:1", "", "10.0.0.7"},
	} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.RemoteAddr = tc.remote
		if tc.xff != "" {
			request.Header.Set("X-Forwarded-For", tc.xff)
		}
		if got := ClientIPExtractor(tc.trusted)(request); got != tc.want {
			t.Errorf("%s: client = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAllowedOrigins(t *testing.T) {
	if _, err := allowedOrigins(config.Production, nil); err == nil {
		t.Fatal("production started without CORS origins")
	}
	if got, err := allowedOrigins(config.Development, nil); err != nil || len(got) != 2 {
		t.Fatalf("development default = %v, %v", got, err)
	}
	if got, err := allowedOrigins(config.Production, []string{"https://console.example"}); err != nil || got[0] != "https://console.example" {
		t.Fatalf("configured = %v, %v", got, err)
	}
}
