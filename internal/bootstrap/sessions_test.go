package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/config"
	"github.com/machinarchy/machinarchy/internal/domain"
)

// sessionServer is the fully wired API server on a SQLite database that
// survives restarts within the test.
type sessionServer struct {
	t      *testing.T
	dbPath string
	client apiClient
	close  func()
}

const (
	sessionKeyA = "session-key-a-0123456789abcdef0123456789"
	sessionKeyB = "session-key-b-0123456789abcdef0123456789"
)

func startSessionServer(t *testing.T, dbPath, operatorToken string, previous ...string) *sessionServer {
	t.Helper()
	return startSessionServerWith(t, dbPath, func(cfg *config.Config) {
		cfg.OperatorToken, cfg.SessionPreviousKeys = operatorToken, previous
	})
}

func startSessionServerWith(t *testing.T, dbPath string, mutate func(*config.Config)) *sessionServer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cfg := config.Config{
		ListenAddr: "127.0.0.1:0", Environment: config.Development, SignupEnabled: true,
		OperatorToken: sessionKeyA, SessionTTL: time.Hour,
		EncryptionSecret: "session-encryption-secret-0123456789abcdef",
		ShutdownTimeout:  10 * time.Second,
		Database:         config.DatabaseConfig{Engine: config.SQLite, Path: dbPath, ConnectTimeout: time.Second, MaxOpenConns: 1, MaxIdleConns: 1},
		BootstrapAdmin:   config.BootstrapAdminConfig{Username: "admin", Email: "admin@example.test", Password: "session-admin-password"},
	}
	mutate(&cfg)
	runtime, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server, err := runtime.NewAPIServer(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := &sessionServer{t: t, dbPath: dbPath, client: apiClient{t: t, handler: server.Handler(), calls: new(int)}}
	s.close = func() {
		_ = server.Shutdown(context.Background())
		_ = runtime.Close()
		cancel()
	}
	t.Cleanup(s.close)
	return s
}

func (s *sessionServer) login(password string) (string, int) {
	s.t.Helper()
	status, body := s.client.call(http.MethodPost, "/api/auth/login", "", map[string]string{"username": "admin", "password": password})
	if status != http.StatusOK {
		return "", status
	}
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		s.t.Fatal(err)
	}
	return session.Token, status
}

func (s *sessionServer) me(token string) int {
	status, _ := s.client.call(http.MethodGet, "/api/v1/me", token, nil)
	return status
}

func newDBPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "s.db")
}

// S-08 end to end: logout and "log out everywhere" end sessions on the
// server, so a copied token stops working at once.
func TestSessionRevocationEndToEnd(t *testing.T) {
	s := startSessionServer(t, newDBPath(t), sessionKeyA)
	laptop, _ := s.login("session-admin-password")
	copied := laptop // e.g. stolen from a log
	phone, _ := s.login("session-admin-password")
	if s.me(laptop) != http.StatusOK || s.me(phone) != http.StatusOK {
		t.Fatal("fresh sessions do not work")
	}

	var sessions struct {
		Items []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"items"`
	}
	s.client.mustJSON(http.MethodGet, "/api/v1/sessions", phone, nil, http.StatusOK, &sessions)
	if len(sessions.Items) != 2 {
		t.Fatalf("sessions = %+v", sessions.Items)
	}

	// Logout revokes the session: the copy is refused too.
	if status, _ := s.client.call(http.MethodPost, "/api/auth/logout", laptop, nil); status != http.StatusOK {
		t.Fatalf("logout = %d", status)
	}
	if got := s.me(copied); got != http.StatusUnauthorized {
		t.Fatalf("a copy of a logged-out token = %d, want 401", got)
	}
	if s.me(phone) != http.StatusOK {
		t.Fatal("logging out one session ended another")
	}

	// Revoke one session by ID from another.
	tablet, _ := s.login("session-admin-password")
	s.client.mustJSON(http.MethodGet, "/api/v1/sessions", phone, nil, http.StatusOK, &sessions)
	var tabletID string
	for _, item := range sessions.Items {
		if !item.Current {
			tabletID = item.ID
		}
	}
	if status, _ := s.client.call(http.MethodDelete, "/api/v1/sessions/"+tabletID, phone, nil); status != http.StatusOK {
		t.Fatalf("revoke one session = %d", status)
	}
	if s.me(tablet) != http.StatusUnauthorized || s.me(phone) != http.StatusOK {
		t.Fatal("revoking a session by ID did not end exactly that session")
	}

	// Log out everywhere ends every session, the caller's included.
	other, _ := s.login("session-admin-password")
	var revoked struct {
		Revoked int `json:"revoked"`
	}
	s.client.mustJSON(http.MethodDelete, "/api/v1/sessions", phone, nil, http.StatusOK, &revoked)
	if revoked.Revoked != 2 || s.me(phone) != http.StatusUnauthorized || s.me(other) != http.StatusUnauthorized {
		t.Fatalf("log out everywhere revoked %d; sessions still answer %d, %d", revoked.Revoked, s.me(phone), s.me(other))
	}
}

// S-08: a password change revokes every session and needs the current
// password; the old password stops working.
func TestPasswordChangeEndToEnd(t *testing.T) {
	s := startSessionServer(t, newDBPath(t), sessionKeyA)
	first, _ := s.login("session-admin-password")
	second, _ := s.login("session-admin-password")
	change := func(token, current, next string) int {
		status, _ := s.client.call(http.MethodPost, "/api/v1/me/password", token,
			map[string]string{"currentPassword": current, "newPassword": next})
		return status
	}
	if got := change(first, "wrong-password-123", "a-brand-new-password"); got != http.StatusForbidden {
		t.Fatalf("wrong current password = %d, want 403", got)
	}
	if got := change(first, "session-admin-password", "short"); got != http.StatusBadRequest {
		t.Fatalf("short new password = %d, want 400", got)
	}
	if s.me(first) != http.StatusOK {
		t.Fatal("a refused change ended the session")
	}
	if got := change(first, "session-admin-password", "a-brand-new-password"); got != http.StatusOK {
		t.Fatalf("password change = %d", got)
	}
	if s.me(first) != http.StatusUnauthorized || s.me(second) != http.StatusUnauthorized {
		t.Fatal("a session survived the password change")
	}
	if _, status := s.login("session-admin-password"); status != http.StatusUnauthorized {
		t.Fatalf("the old password still logs in: %d", status)
	}
	if token, status := s.login("a-brand-new-password"); status != http.StatusOK || s.me(token) != http.StatusOK {
		t.Fatalf("the new password does not log in: %d", status)
	}
}

// S-08: rotating the signing key keeps existing sessions while the old key
// is listed in MACHINARCHY_SESSION_PREVIOUS_KEYS, and ends them once it is
// dropped. New sessions use the new key.
func TestSessionKeyRotationEndToEnd(t *testing.T) {
	dbPath := newDBPath(t)
	s := startSessionServer(t, dbPath, sessionKeyA)
	old, _ := s.login("session-admin-password")
	s.close()

	rotated := startSessionServer(t, dbPath, sessionKeyB, sessionKeyA)
	if got := rotated.me(old); got != http.StatusOK {
		t.Fatalf("a session signed with the previous key = %d, want 200 during rotation", got)
	}
	fresh, _ := rotated.login("session-admin-password")
	rotated.close()

	done := startSessionServer(t, dbPath, sessionKeyB)
	if got := done.me(old); got != http.StatusUnauthorized {
		t.Fatalf("a session signed with a retired key = %d, want 401", got)
	}
	if got := done.me(fresh); got != http.StatusOK {
		t.Fatalf("a session signed with the current key = %d", got)
	}
}

// loginFrom logs in from a fixed client address.
func (s *sessionServer) loginFrom(remote, username, password string) *httptest.ResponseRecorder {
	s.t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	request.RemoteAddr = remote
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	s.client.handler.ServeHTTP(recorder, request)
	return recorder
}

// S-09 end to end: failures per account and per client network are counted
// in the database; past the free failures a login waits (429 with
// Retry-After), the right password included, and the start of backoff is
// audited.
func TestLoginThrottlingEndToEnd(t *testing.T) {
	s := startSessionServerWith(t, newDBPath(t), func(cfg *config.Config) {
		cfg.LoginThrottle = domain.LoginThrottlePolicy{FreeFailures: 3, BaseDelay: time.Second, MaxDelay: time.Minute,
			IPMaxFailures: 8, Window: 10 * time.Minute}
	})
	const attacker = "192.0.2.10:5000"
	for i := 1; i <= 4; i++ {
		if got := s.loginFrom(attacker, "admin", "wrong-password-123").Code; got != http.StatusUnauthorized {
			t.Fatalf("failure %d = %d, want 401", i, got)
		}
	}
	// The 4th failure started a 1 s backoff: even the right password waits.
	refused := s.loginFrom("198.51.100.20:5000", "admin", "session-admin-password")
	if refused.Code != http.StatusTooManyRequests || refused.Header().Get("Retry-After") != "1" {
		t.Fatalf("login during backoff = %d, Retry-After %q", refused.Code, refused.Header().Get("Retry-After"))
	}
	time.Sleep(1100 * time.Millisecond) // the backoff being tested
	if got := s.loginFrom("198.51.100.20:5000", "admin", "session-admin-password").Code; got != http.StatusOK {
		t.Fatalf("login after the backoff = %d", got)
	}
	// The success reset the account: a new failure is free again.
	if got := s.loginFrom(attacker, "admin", "wrong-password-123").Code; got != http.StatusUnauthorized {
		t.Fatalf("failure after a success = %d", got)
	}

	// The network limit spans accounts, and unknown accounts count the same.
	for i := range 3 {
		s.loginFrom(attacker, fmt.Sprintf("nobody-%d", i), "wrong-password-123")
	}
	if got := s.loginFrom(attacker, "someone-else", "wrong-password-123").Code; got != http.StatusTooManyRequests {
		t.Fatalf("after 8 failures from one network = %d, want 429", got)
	}
	if got := s.loginFrom("203.0.113.9:5000", "admin", "session-admin-password").Code; got != http.StatusOK {
		t.Fatalf("another network = %d, want 200", got)
	}

	token, _ := s.login("session-admin-password")
	var events []struct {
		Action  string            `json:"action"`
		Outcome string            `json:"outcome"`
		Details map[string]string `json:"details"`
	}
	s.client.mustJSON(http.MethodGet, "/api/v1/audit/events?action=auth.login_throttled", token, nil, http.StatusOK, &events)
	if len(events) != 1 || events[0].Outcome != "denied" || events[0].Details["network"] != "192.0.2.10" {
		t.Fatalf("backoff audit events = %+v", events)
	}
}

// viaProxy sends a login through a reverse proxy at proxy that saw client.
func (s *sessionServer) viaProxy(proxy, xff, username, password string) *httptest.ResponseRecorder {
	s.t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	request.RemoteAddr = proxy
	request.Header.Set("X-Forwarded-For", xff)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	s.client.handler.ServeHTTP(recorder, request)
	return recorder
}

// S-10 end to end: behind a trusted proxy, limits apply to the real client
// from X-Forwarded-For, so clients sharing the proxy do not share limits; a
// client that is not a trusted proxy cannot pick its address with the
// header.
func TestLimitsUseTheClientBehindATrustedProxy(t *testing.T) {
	s := startSessionServerWith(t, newDBPath(t), func(cfg *config.Config) {
		cfg.HTTP.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}
		cfg.HTTP.RateLimitRPS, cfg.HTTP.RateLimitBurst = 0.01, 4
		cfg.LoginThrottle = domain.LoginThrottlePolicy{FreeFailures: 5, BaseDelay: time.Second, MaxDelay: time.Minute,
			IPMaxFailures: 3, Window: 10 * time.Minute}
	})
	const proxy = "10.0.0.2:40000"

	// Login throttling: client A's failures block client A's network only.
	for i := range 3 {
		s.viaProxy(proxy, "198.51.100.7", fmt.Sprintf("nobody-%d", i), "wrong-password-123")
	}
	if got := s.viaProxy(proxy, "198.51.100.7", "admin", "session-admin-password").Code; got != http.StatusTooManyRequests {
		t.Fatalf("client A after its network limit = %d, want 429", got)
	}
	if got := s.viaProxy(proxy, "203.0.113.50", "admin", "session-admin-password").Code; got != http.StatusOK {
		t.Fatalf("client B through the same proxy = %d, want 200", got)
	}

	// Rate limiting: client B has used one of its 4 tokens.
	for range 3 {
		s.viaProxy(proxy, "203.0.113.50", "admin", "session-admin-password")
	}
	if got := s.viaProxy(proxy, "203.0.113.50", "admin", "session-admin-password"); got.Code != http.StatusTooManyRequests ||
		got.Header().Get("Retry-After") == "" {
		t.Fatalf("client B past its burst = %d, Retry-After %q", got.Code, got.Header().Get("Retry-After"))
	}
	if got := s.viaProxy(proxy, "192.0.2.80", "admin", "session-admin-password").Code; got != http.StatusOK {
		t.Fatalf("client C through the same proxy = %d, want 200", got)
	}

	// Not a trusted proxy: the header is ignored, so changing it does not
	// escape the limit.
	for i := range 4 {
		s.viaProxy("192.0.2.200:1", fmt.Sprintf("198.51.100.%d", 100+i), "admin", "session-admin-password")
	}
	if got := s.viaProxy("192.0.2.200:1", "198.51.100.250", "admin", "session-admin-password").Code; got != http.StatusTooManyRequests {
		t.Fatalf("forged X-Forwarded-For from an untrusted peer = %d, want 429", got)
	}
}
