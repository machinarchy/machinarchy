package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/sessionkey"
)

var _ domain.AuthService = (*authService)(nil)

type authService struct {
	userStore domain.UserStore
	sessions  domain.SessionStore
	keys      *sessionkey.Keyring
	tokenTTL  time.Duration
	now       func() time.Time
}

// DefaultSessionTTL is a login session's lifetime when none is configured.
const DefaultSessionTTL = 24 * time.Hour

// NewAuthService creates the authentication service. Every login creates a
// server-side session in sessions (AUDIT S-08); its token is signed by keys.
func NewAuthService(userStore domain.UserStore, sessions domain.SessionStore, keys *sessionkey.Keyring, tokenTTL time.Duration) (domain.AuthService, error) {
	if userStore == nil || sessions == nil || keys == nil {
		return nil, errors.New("auth service requires a user store, a session store and a signing keyring")
	}
	if tokenTTL <= 0 {
		tokenTTL = DefaultSessionTTL
	}
	return &authService{userStore: userStore, sessions: sessions, keys: keys, tokenTTL: tokenTTL, now: time.Now}, nil
}

func validPassword(password string) bool {
	return len([]byte(password)) >= 12 && len([]byte(password)) <= 72
}

func (s *authService) SignUp(ctx context.Context, req domain.AuthSignUpRequest) (*domain.AuthTokenResponse, error) {
	req.Username = strings.ToLower(strings.TrimSpace(req.Username))
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Username == "" || !validPassword(req.Password) {
		return nil, fmt.Errorf("%w: username is required and password must be between 12 and 72 bytes", domain.ErrInvalidInput)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to process password hashing: %w", err)
	}

	user, err := s.userStore.CreateUser(ctx, req.Username, req.Email, string(hash))
	if err != nil {
		if errors.Is(err, domain.ErrUserAlreadyExists) {
			return nil, domain.ErrUserAlreadyExists
		}
		return nil, fmt.Errorf("create user: %w", err)
	}

	return s.startSession(ctx, user, req.Client)
}

func (s *authService) Login(ctx context.Context, req domain.AuthLoginRequest) (*domain.AuthTokenResponse, error) {
	var user *domain.User
	var err error

	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.Username = strings.ToLower(strings.TrimSpace(req.Username))
	if req.Email != "" {
		user, err = s.userStore.GetUserByEmail(ctx, req.Email)
	} else {
		user, err = s.userStore.GetUserByUsername(ctx, req.Username)
	}

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrInvalidCredentials
		}
		return nil, fmt.Errorf("look up login account: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return nil, domain.ErrInvalidCredentials
	}

	return s.startSession(ctx, user, req.Client)
}

func (s *authService) VerifySession(ctx context.Context, username string) (*domain.UserResponse, error) {
	user, err := s.userStore.GetUserByUsername(ctx, strings.ToLower(strings.TrimSpace(username)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrUserNotFound
		}
		return nil, fmt.Errorf("look up session user: %w", err)
	}

	return &domain.UserResponse{
		ID:       user.ID,
		Username: user.Username,
		Email:    user.Email,
	}, nil
}

func (s *authService) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	claims, err := s.keys.Parse(token, s.tokenTTL, s.now())
	if err != nil {
		return nil // nothing usable to revoke
	}
	ctx = withActor(ctx, claims.UserID, claims.Username)
	err = s.sessions.RevokeSession(ctx, claims.SessionID, claims.UserID, domain.SessionRevokedLogout, s.now())
	if errors.Is(err, domain.ErrSessionNotFound) {
		return nil // already ended
	}
	return err
}

func (s *authService) ListSessions(ctx context.Context, principal domain.Principal) ([]domain.Session, error) {
	if err := requireSessionPrincipal(principal); err != nil {
		return nil, err
	}
	sessions, err := s.sessions.ListSessions(ctx, principal.UserID, s.now())
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		sessions[i].Current = sessions[i].ID == principal.SessionID
	}
	return sessions, nil
}

func (s *authService) RevokeSession(ctx context.Context, principal domain.Principal, sessionID string) error {
	if err := requireSessionPrincipal(principal); err != nil {
		return err
	}
	return s.sessions.RevokeSession(ctx, sessionID, principal.UserID, domain.SessionRevokedByUser, s.now())
}

func (s *authService) RevokeAllSessions(ctx context.Context, principal domain.Principal) (int, error) {
	if err := requireSessionPrincipal(principal); err != nil {
		return 0, err
	}
	return s.sessions.RevokeUserSessions(ctx, principal.UserID, domain.SessionRevokedAll, s.now())
}

func (s *authService) ChangePassword(ctx context.Context, principal domain.Principal, currentPassword, newPassword string) error {
	if err := requireSessionPrincipal(principal); err != nil {
		return err
	}
	if !validPassword(newPassword) {
		return fmt.Errorf("%w: the new password must be between 12 and 72 bytes", domain.ErrInvalidInput)
	}
	if newPassword == currentPassword {
		return fmt.Errorf("%w: the new password must differ from the current one", domain.ErrInvalidInput)
	}
	user, err := s.userStore.GetUserByUsername(ctx, principal.Username)
	if err != nil || user.ID != principal.UserID {
		return domain.ErrInvalidCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(currentPassword)) != nil {
		return domain.ErrInvalidCredentials
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash new password: %w", err)
	}
	_, err = s.sessions.ChangePassword(ctx, principal.UserID, string(hash), s.now())
	return err
}

// requireSessionPrincipal admits a human login session: API tokens manage
// neither sessions nor passwords.
func requireSessionPrincipal(principal domain.Principal) error {
	if principal.UserID <= 0 || principal.APITokenID != 0 || principal.SessionID == "" {
		return domain.ErrPermissionDenied
	}
	return nil
}

func withActor(ctx context.Context, userID int, username string) context.Context {
	if _, ok := domain.AuditActorFrom(ctx); ok {
		return ctx
	}
	return domain.WithAuditActor(ctx, domain.AuditActor{UserID: userID, Username: username, Source: "api"})
}

// startSession records a session and returns its signed token.
func (s *authService) startSession(ctx context.Context, user *domain.User, client domain.ClientInfo) (*domain.AuthTokenResponse, error) {
	if user == nil || user.WorkspaceID <= 0 {
		return nil, errors.New("cannot create a session for a user without a workspace")
	}
	sessionID, err := domain.NewSessionID()
	if err != nil {
		return nil, err
	}
	// Whole seconds: the JWT carries seconds, so the token and its session
	// expire at the same instant.
	now := s.now().UTC().Truncate(time.Second)
	expires := now.Add(s.tokenTTL)
	if err := s.sessions.CreateSession(ctx, domain.Session{ID: sessionID, UserID: user.ID, WorkspaceID: user.WorkspaceID,
		CreatedAt: now, ExpiresAt: expires, UserAgent: client.UserAgent, ClientIP: client.IP}); err != nil {
		return nil, fmt.Errorf("record session: %w", err)
	}
	claims := &domain.JWTCustomClaims{
		UserID:      user.ID,
		WorkspaceID: user.WorkspaceID,
		Username:    user.Username,
		SessionID:   sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "machinarchy",
			Subject:   fmt.Sprintf("%d", user.ID),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expires),
		},
	}
	signedToken, err := s.keys.Sign(claims)
	if err != nil {
		return nil, err
	}

	return &domain.AuthTokenResponse{
		Token: signedToken,
		User: domain.UserResponse{
			ID:       user.ID,
			Username: user.Username,
			Email:    user.Email,
		},
	}, nil
}
