package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/sessionkey"
)

// memUsers implements the parts of domain.UserStore the auth service uses;
// any other method panics through the nil embedded interface.
type memUsers struct {
	domain.UserStore
	mu     sync.Mutex
	users  []*domain.User
	failed error // returned by lookups when set
}

func (s *memUsers) CreateUser(_ context.Context, username, email, hash string) (*domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, user := range s.users {
		if user.Username == username || (email != "" && user.Email == email) {
			return nil, domain.ErrUserAlreadyExists
		}
	}
	user := &domain.User{ID: len(s.users) + 1, Username: username, Email: email, PasswordHash: hash, WorkspaceID: 1}
	s.users = append(s.users, user)
	return user, nil
}

func (s *memUsers) find(match func(*domain.User) bool) (*domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return nil, s.failed
	}
	for _, user := range s.users {
		if match(user) {
			copied := *user
			return &copied, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (s *memUsers) GetUserByUsername(_ context.Context, username string) (*domain.User, error) {
	return s.find(func(u *domain.User) bool { return u.Username == username })
}

func (s *memUsers) GetUserByEmail(_ context.Context, email string) (*domain.User, error) {
	return s.find(func(u *domain.User) bool { return u.Email == email })
}

const authSecret = "auth-test-secret-0123456789abcdef0123"

// memSessions is an in-memory domain.SessionStore.
type memSessions struct {
	mu       sync.Mutex
	sessions map[string]*domain.Session
	revoked  map[string]string
	hashes   map[int]string
}

func newMemSessions() *memSessions {
	return &memSessions{sessions: map[string]*domain.Session{}, revoked: map[string]string{}, hashes: map[int]string{}}
}

func (m *memSessions) CreateSession(_ context.Context, session domain.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[session.ID] = &session
	return nil
}

func (m *memSessions) active(id string, now time.Time) (*domain.Session, bool) {
	session, ok := m.sessions[id]
	_, revoked := m.revoked[id]
	return session, ok && !revoked && now.Before(session.ExpiresAt)
}

func (m *memSessions) GetSessionRole(_ context.Context, id string, userID, workspaceID int, now time.Time) (domain.Role, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if session, ok := m.active(id, now); ok && session.UserID == userID && session.WorkspaceID == workspaceID {
		return domain.RoleOwner, nil
	}
	return "", domain.ErrSessionNotFound
}

func (m *memSessions) ListSessions(_ context.Context, userID int, now time.Time) ([]domain.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Session
	for id, session := range m.sessions {
		if _, ok := m.active(id, now); ok && session.UserID == userID {
			out = append(out, *session)
		}
	}
	return out, nil
}

func (m *memSessions) RevokeSession(_ context.Context, id string, userID int, reason string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if session, ok := m.active(id, now); ok && session.UserID == userID {
		m.revoked[id] = reason
		return nil
	}
	return domain.ErrSessionNotFound
}

func (m *memSessions) RevokeUserSessions(_ context.Context, userID int, reason string, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.revokeAll(userID, reason, now), nil
}

func (m *memSessions) revokeAll(userID int, reason string, now time.Time) int {
	n := 0
	for id, session := range m.sessions {
		if _, ok := m.active(id, now); ok && session.UserID == userID {
			m.revoked[id] = reason
			n++
		}
	}
	return n
}

func (m *memSessions) ChangePassword(_ context.Context, userID int, hash string, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hashes[userID] = hash
	return m.revokeAll(userID, domain.SessionRevokedPasswordChanged, now), nil
}

func newTestAuth(t *testing.T, users domain.UserStore, ttl time.Duration) (*authService, *memSessions) {
	t.Helper()
	keys, err := sessionkey.New(authSecret, nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions := newMemSessions()
	service, err := NewAuthService(users, sessions, keys, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return service.(*authService), sessions
}

func parseSession(t *testing.T, token, secret string) *domain.JWTCustomClaims {
	t.Helper()
	keys, err := sessionkey.New(secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := keys.Parse(token, 7*24*time.Hour, time.Now())
	if err != nil {
		t.Fatalf("session token does not verify: %v", err)
	}
	return claims
}

func TestSignUp(t *testing.T) {
	users := &memUsers{}
	auth, sessions := newTestAuth(t, users, time.Hour)
	ctx := context.Background()

	session, err := auth.SignUp(ctx, domain.AuthSignUpRequest{Username: "  Alice ", Email: " Alice@Example.TEST ", Password: "correct-horse-battery"})
	if err != nil {
		t.Fatal(err)
	}
	stored := users.users[0]
	switch {
	case stored.Username != "alice" || stored.Email != "alice@example.test":
		t.Fatalf("stored identity %q %q is not normalised", stored.Username, stored.Email)
	case strings.Contains(stored.PasswordHash, "correct-horse"):
		t.Fatal("the password is stored in clear text")
	case bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte("correct-horse-battery")) != nil:
		t.Fatal("the stored hash does not match the password")
	}
	claims := parseSession(t, session.Token, authSecret)
	if claims.UserID != stored.ID || claims.WorkspaceID != 1 || claims.Username != "alice" || claims.Subject != "1" {
		t.Fatalf("claims = %+v", claims)
	}
	if lifetime := claims.ExpiresAt.Sub(claims.IssuedAt.Time); lifetime != time.Hour {
		t.Fatalf("session lifetime %s, want the configured hour", lifetime)
	}
	if stored, ok := sessions.sessions[claims.SessionID]; !ok || stored.UserID != 1 || !stored.ExpiresAt.Equal(claims.ExpiresAt.Time) {
		t.Fatalf("the token's session %q was not recorded: %+v", claims.SessionID, sessions.sessions)
	}

	for name, request := range map[string]domain.AuthSignUpRequest{
		"no username":        {Username: " ", Password: "correct-horse-battery"},
		"short password":     {Username: "bob", Password: "elevenbytes"},
		"over bcrypt's 72 B": {Username: "bob", Password: strings.Repeat("p", 73)},
	} {
		if _, err := auth.SignUp(ctx, request); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%s: %v, want ErrInvalidInput", name, err)
		}
	}
	if _, err := auth.SignUp(ctx, domain.AuthSignUpRequest{Username: "ALICE", Password: "another-long-password"}); !errors.Is(err, domain.ErrUserAlreadyExists) {
		t.Fatalf("duplicate username (different case) = %v", err)
	}
}

func TestLogin(t *testing.T) {
	users := &memUsers{}
	auth, _ := newTestAuth(t, users, 0)
	ctx := context.Background()
	if _, err := auth.SignUp(ctx, domain.AuthSignUpRequest{Username: "alice", Email: "alice@example.test", Password: "correct-horse-battery"}); err != nil {
		t.Fatal(err)
	}

	for name, request := range map[string]domain.AuthLoginRequest{
		"username":           {Username: "alice", Password: "correct-horse-battery"},
		"username, any case": {Username: " ALICE ", Password: "correct-horse-battery"},
		"email":              {Email: "Alice@Example.test", Password: "correct-horse-battery"},
	} {
		session, err := auth.Login(ctx, request)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		claims := parseSession(t, session.Token, authSecret)
		if claims.ExpiresAt.Sub(claims.IssuedAt.Time) != DefaultSessionTTL {
			t.Fatalf("%s: a zero TTL must give the 24h default", name)
		}
	}

	// A wrong password and an unknown account give the same error, so the
	// response does not reveal which accounts exist.
	for name, request := range map[string]domain.AuthLoginRequest{
		"wrong password": {Username: "alice", Password: "not-the-password"},
		"unknown user":   {Username: "mallory", Password: "correct-horse-battery"},
		"unknown email":  {Email: "mallory@example.test", Password: "correct-horse-battery"},
		"empty password": {Username: "alice"},
	} {
		if _, err := auth.Login(ctx, request); !errors.Is(err, domain.ErrInvalidCredentials) {
			t.Errorf("%s: %v, want ErrInvalidCredentials", name, err)
		}
	}

	users.failed = errors.New("database is down")
	if _, err := auth.Login(ctx, domain.AuthLoginRequest{Username: "alice", Password: "correct-horse-battery"}); err == nil ||
		errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("a store failure must not look like bad credentials: %v", err)
	}
}

func TestSessionTokensAreBoundToTheSecret(t *testing.T) {
	users := &memUsers{}
	auth, _ := newTestAuth(t, users, time.Hour)
	session, err := auth.SignUp(context.Background(), domain.AuthSignUpRequest{Username: "alice", Password: "correct-horse-battery"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jwt.ParseWithClaims(session.Token, &domain.JWTCustomClaims{},
		func(*jwt.Token) (any, error) { return []byte("another-secret-0123456789abcdef0123"), nil }); err == nil {
		t.Fatal("a session token verified with another secret")
	}
	parsed, _, err := jwt.NewParser().ParseUnverified(session.Token, &domain.JWTCustomClaims{})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Method.Alg() != "HS256" || parsed.Header["kid"] != sessionkey.KeyID(authSecret) {
		t.Fatalf("header = %v; sessions must be HS256 with the signing key's kid", parsed.Header)
	}
}

func TestVerifySession(t *testing.T) {
	users := &memUsers{}
	auth, _ := newTestAuth(t, users, time.Hour)
	ctx := context.Background()
	if _, err := auth.SignUp(ctx, domain.AuthSignUpRequest{Username: "alice", Email: "a@example.test", Password: "correct-horse-battery"}); err != nil {
		t.Fatal(err)
	}
	user, err := auth.VerifySession(ctx, " Alice ")
	if err != nil || user.Username != "alice" || user.Email != "a@example.test" {
		t.Fatalf("VerifySession = %+v, %v", user, err)
	}
	if _, err := auth.VerifySession(ctx, "ghost"); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("unknown session user = %v", err)
	}
	users.failed = errors.New("database is down")
	if _, err := auth.VerifySession(ctx, "alice"); err == nil || errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("store failure = %v", err)
	}
}

func TestSessionRequiresAWorkspace(t *testing.T) {
	service, _ := newTestAuth(t, &memUsers{}, time.Hour)
	if _, err := service.startSession(context.Background(), &domain.User{ID: 1, Username: "orphan"}, domain.ClientInfo{}); err == nil {
		t.Fatal("a session was issued for a user without a workspace")
	}
	if _, err := service.startSession(context.Background(), nil, domain.ClientInfo{}); err == nil {
		t.Fatal("a session was issued for no user")
	}
}

func principalFor(t *testing.T, token string) domain.Principal {
	t.Helper()
	claims := parseSession(t, token, authSecret)
	return domain.Principal{UserID: claims.UserID, WorkspaceID: claims.WorkspaceID, Username: claims.Username, SessionID: claims.SessionID, Role: domain.RoleOwner}
}

// S-08: logout and "log out everywhere" end sessions on the server.
func TestLogoutAndRevocation(t *testing.T) {
	users := &memUsers{}
	auth, sessions := newTestAuth(t, users, time.Hour)
	ctx := context.Background()
	first, err := auth.SignUp(ctx, domain.AuthSignUpRequest{Username: "alice", Password: "correct-horse-battery"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := auth.Login(ctx, domain.AuthLoginRequest{Username: "alice", Password: "correct-horse-battery",
		Client: domain.ClientInfo{IP: "192.0.2.7", UserAgent: "machinarchy-cli"}})
	if err != nil {
		t.Fatal(err)
	}
	one, two := principalFor(t, first.Token), principalFor(t, second.Token)
	list, err := auth.ListSessions(ctx, two)
	if err != nil || len(list) != 2 {
		t.Fatalf("sessions = %+v, %v", list, err)
	}
	for _, session := range list {
		if session.Current != (session.ID == two.SessionID) {
			t.Fatalf("current flag wrong: %+v", session)
		}
	}
	// Logout revokes exactly the session of the token sent.
	if err := auth.Logout(ctx, first.Token); err != nil {
		t.Fatal(err)
	}
	if sessions.revoked[one.SessionID] != domain.SessionRevokedLogout {
		t.Fatalf("logout did not revoke the session: %v", sessions.revoked)
	}
	if _, active := sessions.active(two.SessionID, time.Now()); !active {
		t.Fatal("logout revoked another session")
	}
	// Logging out again, or with garbage, is not an error.
	for _, token := range []string{first.Token, "", "not-a-token"} {
		if err := auth.Logout(ctx, token); err != nil {
			t.Fatalf("logout(%q) = %v", token, err)
		}
	}
	// Revoking someone else's session is not found.
	other := two
	other.UserID = 99
	if err := auth.RevokeSession(ctx, other, two.SessionID); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("revoke another user's session = %v", err)
	}
	third, _ := auth.Login(ctx, domain.AuthLoginRequest{Username: "alice", Password: "correct-horse-battery"})
	if n, err := auth.RevokeAllSessions(ctx, two); err != nil || n != 2 {
		t.Fatalf("log out everywhere = %d, %v; want the 2 active sessions", n, err)
	}
	if _, active := sessions.active(principalFor(t, third.Token).SessionID, time.Now()); active {
		t.Fatal("log out everywhere left a session active")
	}
	// API tokens cannot manage sessions.
	token := two
	token.APITokenID, token.SessionID = 5, ""
	if _, err := auth.ListSessions(ctx, token); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("API token listing sessions = %v", err)
	}
}

// S-08: a password change checks the current password and revokes every
// session of the user.
func TestChangePassword(t *testing.T) {
	users := &memUsers{}
	auth, sessions := newTestAuth(t, users, time.Hour)
	ctx := context.Background()
	first, err := auth.SignUp(ctx, domain.AuthSignUpRequest{Username: "alice", Password: "correct-horse-battery"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Login(ctx, domain.AuthLoginRequest{Username: "alice", Password: "correct-horse-battery"}); err != nil {
		t.Fatal(err)
	}
	me := principalFor(t, first.Token)
	for name, tc := range map[string]struct {
		current, next string
		want          error
	}{
		"wrong current":  {"not-the-password", "a-brand-new-password", domain.ErrInvalidCredentials},
		"too short":      {"correct-horse-battery", "short", domain.ErrInvalidInput},
		"too long":       {"correct-horse-battery", strings.Repeat("x", 73), domain.ErrInvalidInput},
		"same as before": {"correct-horse-battery", "correct-horse-battery", domain.ErrInvalidInput},
	} {
		if err := auth.ChangePassword(ctx, me, tc.current, tc.next); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	if len(sessions.hashes) != 0 {
		t.Fatal("a refused change stored a password")
	}
	if err := auth.ChangePassword(ctx, me, "correct-horse-battery", "a-brand-new-password"); err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(sessions.hashes[me.UserID]), []byte("a-brand-new-password")) != nil {
		t.Fatal("the new password was not stored as a bcrypt hash")
	}
	if active, _ := sessions.ListSessions(ctx, me.UserID, time.Now()); len(active) != 0 {
		t.Fatalf("%d sessions survived the password change", len(active))
	}
}
