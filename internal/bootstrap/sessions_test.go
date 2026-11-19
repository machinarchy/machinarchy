package bootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/config"
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
	ctx, cancel := context.WithCancel(context.Background())
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	cfg := config.Config{
		ListenAddr: "127.0.0.1:0", Environment: config.Development, SignupEnabled: true,
		OperatorToken: operatorToken, SessionPreviousKeys: previous, SessionTTL: time.Hour,
		EncryptionSecret: "session-encryption-secret-0123456789abcdef",
		ShutdownTimeout:  10 * time.Second,
		Database:         config.DatabaseConfig{Engine: config.SQLite, Path: dbPath, ConnectTimeout: time.Second, MaxOpenConns: 1, MaxIdleConns: 1},
		BootstrapAdmin:   config.BootstrapAdminConfig{Username: "admin", Email: "admin@example.test", Password: "session-admin-password"},
	}
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
