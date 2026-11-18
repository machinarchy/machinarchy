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

func parseSession(t *testing.T, token, secret string) *domain.JWTCustomClaims {
	t.Helper()
	claims := &domain.JWTCustomClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) { return []byte(secret), nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuer("machinarchy"))
	if err != nil || !parsed.Valid {
		t.Fatalf("session token does not verify: %v", err)
	}
	return claims
}

func TestSignUp(t *testing.T) {
	users := &memUsers{}
	auth := NewAuthService(users, authSecret, time.Hour)
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
	auth := NewAuthService(users, authSecret, 0)
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
		if claims.ExpiresAt.Sub(claims.IssuedAt.Time) != defaultTokenTTL {
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
	session, err := NewAuthService(users, authSecret, time.Hour).SignUp(context.Background(),
		domain.AuthSignUpRequest{Username: "alice", Password: "correct-horse-battery"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jwt.ParseWithClaims(session.Token, &domain.JWTCustomClaims{},
		func(*jwt.Token) (any, error) { return []byte("another-secret-0123456789abcdef0123"), nil }); err == nil {
		t.Fatal("a session token verified with another secret")
	}
	header := strings.SplitN(session.Token, ".", 2)[0]
	if header != "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" {
		t.Fatalf("unexpected JOSE header %q; sessions must be HS256", header)
	}
}

func TestVerifySession(t *testing.T) {
	users := &memUsers{}
	auth := NewAuthService(users, authSecret, time.Hour)
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
	service := &authService{jwtSecret: []byte(authSecret), tokenTTL: time.Hour}
	if _, err := service.generateSessionToken(&domain.User{ID: 1, Username: "orphan"}); err == nil {
		t.Fatal("a session was issued for a user without a workspace")
	}
	if _, err := service.generateSessionToken(nil); err == nil {
		t.Fatal("a session was issued for no user")
	}
}
