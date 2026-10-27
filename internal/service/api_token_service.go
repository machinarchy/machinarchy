package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

const (
	apiTokenPrefix          = "mch_pat_"
	defaultAPITokenLifetime = 30 * 24 * time.Hour
	maxAPITokenLifetime     = 365 * 24 * time.Hour
)

type apiTokenService struct {
	store domain.AccessStore
	now   func() time.Time
}

var _ domain.APITokenService = (*apiTokenService)(nil)

func NewAPITokenService(store domain.AccessStore) (domain.APITokenService, error) {
	if store == nil {
		return nil, errors.New("API token store is required")
	}
	return &apiTokenService{store: store, now: time.Now}, nil
}

func (s *apiTokenService) Create(ctx context.Context, principal domain.Principal, name string, role domain.Role, expiresInHours int) (*domain.IssuedAPIToken, error) {
	if ctx == nil {
		return nil, errors.New("API token context is required")
	}
	if !principal.Role.Allows(domain.PermissionTokenManage) || principal.UserID <= 0 || principal.WorkspaceID <= 0 {
		return nil, domain.ErrPermissionDenied
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return nil, fmt.Errorf("%w: token name must contain 1 to 100 characters", domain.ErrInvalidInput)
	}
	// API tokens are deliberately unable to mint owner-equivalent credentials.
	if role != domain.RoleOperator && role != domain.RoleReader {
		return nil, fmt.Errorf("%w: API token role must be operator or reader", domain.ErrInvalidInput)
	}
	if expiresInHours < 0 || expiresInHours > int(maxAPITokenLifetime/time.Hour) {
		return nil, fmt.Errorf("%w: token lifetime must be 0 (default) or between 1 hour and 365 days", domain.ErrInvalidInput)
	}
	lifetime := defaultAPITokenLifetime
	if expiresInHours > 0 {
		lifetime = time.Duration(expiresInHours) * time.Hour
	}

	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, fmt.Errorf("generate API token: %w", err)
	}
	secret := apiTokenPrefix + base64.RawURLEncoding.EncodeToString(secretBytes)
	hash := sha256.Sum256([]byte(secret))
	created := s.now().UTC()
	expires := created.Add(lifetime)
	token := &domain.APIToken{
		UserID:      principal.UserID,
		WorkspaceID: principal.WorkspaceID,
		Name:        name,
		Role:        role,
		TokenHash:   hex.EncodeToString(hash[:]),
		CreatedAt:   created,
		ExpiresAt:   &expires,
	}
	if err := s.store.CreateAPIToken(ctx, token); err != nil {
		return nil, fmt.Errorf("create API token: %w", err)
	}
	return &domain.IssuedAPIToken{APIToken: *token, Token: secret}, nil
}

func (s *apiTokenService) List(ctx context.Context, principal domain.Principal) ([]domain.APIToken, error) {
	if ctx == nil {
		return nil, errors.New("API token context is required")
	}
	if !principal.Role.Allows(domain.PermissionTokenManage) || principal.UserID <= 0 || principal.WorkspaceID <= 0 {
		return nil, domain.ErrPermissionDenied
	}
	return s.store.ListAPITokens(ctx, principal.UserID, principal.WorkspaceID)
}

func (s *apiTokenService) Revoke(ctx context.Context, principal domain.Principal, id int) error {
	if ctx == nil {
		return errors.New("API token context is required")
	}
	if !principal.Role.Allows(domain.PermissionTokenManage) || principal.UserID <= 0 || principal.WorkspaceID <= 0 {
		return domain.ErrPermissionDenied
	}
	if id <= 0 {
		return fmt.Errorf("%w: token ID must be positive", domain.ErrInvalidInput)
	}
	return s.store.RevokeAPIToken(ctx, id, principal.UserID, principal.WorkspaceID)
}

func HashAPIToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}
