package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func limitedServer(t *testing.T, cfg Config) *echo.Echo {
	t.Helper()
	limiter, err := NewRateLimiter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(limiter.Stop)
	e := echo.New()
	e.IPExtractor = echo.ExtractIPDirect()
	e.GET("/", func(c echo.Context) error { return c.NoContent(http.StatusOK) }, limiter.Middleware())
	return e
}

func hit(e *echo.Echo, remote string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = remote
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, request)
	return recorder
}

// S-10: a token bucket per client network. Past the burst a request gets 429
// with the wait for the next token, refused requests cost nothing, and there
// is no escalating ban.
func TestRateLimiterIsATokenBucketPerClientNetwork(t *testing.T) {
	e := limitedServer(t, Config{RequestsPerSec: 0.5, Burst: 3}) // one token every 2 s
	for i := range 3 {
		if got := hit(e, "192.0.2.1:1000").Code; got != http.StatusOK {
			t.Fatalf("request %d within the burst = %d", i+1, got)
		}
	}
	for range 5 { // hammering does not extend the wait
		refused := hit(e, "192.0.2.1:1000")
		if refused.Code != http.StatusTooManyRequests || refused.Header().Get("Retry-After") != "2" {
			t.Fatalf("past the burst = %d, Retry-After %q, want 429 and 2", refused.Code, refused.Header().Get("Retry-After"))
		}
	}
	if got := hit(e, "192.0.2.2:1000").Code; got != http.StatusOK {
		t.Fatalf("another client = %d", got)
	}

	// IPv6 clients share their /64: rotating the interface ID does not
	// buy new buckets.
	for i, remote := range []string{"[2001:db8:1:2::1]:1", "[2001:db8:1:2::2]:1", "[2001:db8:1:2:aaaa::3]:1"} {
		if got := hit(e, remote).Code; got != http.StatusOK {
			t.Fatalf("IPv6 request %d = %d", i+1, got)
		}
	}
	if got := hit(e, "[2001:db8:1:2:ffff::9]:1").Code; got != http.StatusTooManyRequests {
		t.Fatalf("a 4th address in the same /64 = %d, want 429", got)
	}
	if got := hit(e, "[2001:db8:1:3::1]:1").Code; got != http.StatusOK {
		t.Fatalf("another /64 = %d", got)
	}
}

func TestRateLimiterConfigValidation(t *testing.T) {
	for _, cfg := range []Config{{RequestsPerSec: 0, Burst: 1}, {RequestsPerSec: 1, Burst: 0}} {
		if _, err := NewRateLimiter(cfg); err == nil {
			t.Errorf("%+v accepted", cfg)
		}
	}
}
