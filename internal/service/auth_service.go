// internal/service/auth_service.go
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
)

var _ domain.AuthService = (*authService)(nil)

type authService struct {
	userStore domain.UserStore
	jwtSecret []byte
	tokenTTL  time.Duration
}

const defaultTokenTTL = 24 * time.Hour

func NewAuthService(userStore domain.UserStore, jwtSecret string, tokenTTL time.Duration) domain.AuthService {
	if tokenTTL <= 0 {
		tokenTTL = defaultTokenTTL
	}
	return &authService{
		userStore: userStore,
		jwtSecret: []byte(jwtSecret),
		tokenTTL:  tokenTTL,
	}
}

func (s *authService) SignUp(ctx context.Context, req domain.AuthSignUpRequest) (*domain.AuthTokenResponse, error) {
	req.Username = strings.ToLower(strings.TrimSpace(req.Username))
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Username == "" || len([]byte(req.Password)) < 12 || len([]byte(req.Password)) > 72 {
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

	return s.generateSessionToken(user)
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

	return s.generateSessionToken(user)
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

func (s *authService) generateSessionToken(user *domain.User) (*domain.AuthTokenResponse, error) {
	if user == nil || user.WorkspaceID <= 0 {
		return nil, errors.New("cannot create a session for a user without a workspace")
	}
	now := time.Now()
	claims := &domain.JWTCustomClaims{
		UserID:      user.ID,
		WorkspaceID: user.WorkspaceID,
		Username:    user.Username,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "machinarchy",
			Subject:   fmt.Sprintf("%d", user.ID),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.tokenTTL)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signedToken, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign token: %w", err)
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

func (s *authService) EnsureAdminUser() error {
	adminUser := "admin"
	adminEmail := "admin@machinarchy.local"
	adminRawPass := "changeme-secure-password"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Check if the seed admin already exists via our immutable system label
	labeledUsers, err := s.userStore.GetUsersByLabel(ctx, "system", "bootstrap-admin")
	if err != nil {
		return fmt.Errorf("failed to check for existing bootstrap admin label: %w", err)
	}

	var userID int
	if len(labeledUsers) > 0 {
		// Found by label! Even if they changed their username, we track them here.
		userID = labeledUsers[0].ID
		fmt.Printf("-> Bootstrap admin found via label (Username: %s), verifying configuration...", labeledUsers[0].Username)
	} else {
		// 2. Not found by label, attempt to sign up the default admin user
		resp, err := s.SignUp(ctx, domain.AuthSignUpRequest{
			Username: adminUser,
			Email:    adminEmail,
			Password: adminRawPass,
		})

		if err != nil {
			if !errors.Is(err, domain.ErrUserAlreadyExists) {
				return fmt.Errorf("failed to bootstrap admin user: %w", err)
			}

			// Edge case: Username exists from a previous partial run, but the label wasn't set yet.
			existing, lookupErr := s.userStore.GetUserByUsername(ctx, adminUser)
			if lookupErr != nil {
				return fmt.Errorf("failed to find existing admin user by username: %w", lookupErr)
			}
			userID = existing.ID
			fmt.Printf("-> Admin user '%s' already exists by username, securing configuration...", adminUser)
		} else {
			userID = resp.User.ID
			fmt.Printf("-> Successfully created default admin account (%s)", adminUser)
		}

		// Stamp the immutable bootstrap label onto this account
		if err := s.userStore.SetUserLabel(ctx, userID, "system", "bootstrap-admin"); err != nil {
			return fmt.Errorf("failed to set bootstrap admin label: %w", err)
		}
	}

	// Enforce that they always have full 'admin' privileges in Workspace 1
	if err := s.userStore.UpdateWorkspaceMemberRole(ctx, 1, userID, string(domain.RoleOwner)); err != nil {
		return fmt.Errorf("failed to enforce admin role: %w", err)
	}

	return nil
}
