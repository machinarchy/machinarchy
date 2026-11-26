package api

import (
	"context"
	"net/http"
	"time"
)

// AccessClient creates and removes the things that grant access: Access
// Profiles, API tokens and agent enrolment tokens (AUDIT G-13C-03).
type AccessClient interface {
	CreateAccessProfile(ctx context.Context, input AccessProfileInput) (*AccessProfile, error)
	DeleteAccessProfile(ctx context.Context, id string) error
	CreateAPIToken(ctx context.Context, name, role string, expiresInHours int) (*IssuedToken, error)
	ListAPITokens(ctx context.Context) ([]Token, error)
	RevokeAPIToken(ctx context.Context, id string) error
	CreateEnrollmentToken(ctx context.Context, input EnrollmentTokenInput) (*IssuedEnrollmentToken, error)
	ListEnrollmentTokens(ctx context.Context) ([]EnrollmentToken, error)
	RevokeEnrollmentToken(ctx context.Context, id string) error
}

var _ AccessClient = (*HTTPClient)(nil)

// AccessProfileInput creates an Access Profile. Spec is the server's
// AccessProfileSpec (connectorKind, username, credential, ...).
type AccessProfileInput struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Spec        map[string]any `json:"spec"`
}

// Token is an API token's metadata; the secret is never listed.
type Token struct {
	ID        int        `json:"id" yaml:"id"`
	Name      string     `json:"name" yaml:"name"`
	Role      string     `json:"role" yaml:"role"`
	CreatedAt time.Time  `json:"createdAt" yaml:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty" yaml:"expiresAt,omitempty"`
	RevokedAt *time.Time `json:"revokedAt,omitempty" yaml:"revokedAt,omitempty"`
}

// IssuedToken is a new API token with its secret, shown once.
type IssuedToken struct {
	Token  `yaml:",inline"`
	Secret string `json:"token" yaml:"token"`
}

// EnrollmentTokenInput issues a single-use agent enrolment token.
type EnrollmentTokenInput struct {
	Hostname   string `json:"hostname,omitempty"`
	HostID     string `json:"hostId,omitempty"`
	TTLSeconds int    `json:"ttlSeconds,omitempty"`
}

// EnrollmentToken is an enrolment token's metadata; the secret is never listed.
type EnrollmentToken struct {
	ID          string     `json:"id" yaml:"id"`
	Hostname    string     `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	HostID      string     `json:"hostId,omitempty" yaml:"hostId,omitempty"`
	CreatedAt   time.Time  `json:"createdAt" yaml:"createdAt"`
	ExpiresAt   time.Time  `json:"expiresAt" yaml:"expiresAt"`
	UsedAt      *time.Time `json:"usedAt,omitempty" yaml:"usedAt,omitempty"`
	UsedByAgent string     `json:"usedByAgent,omitempty" yaml:"usedByAgent,omitempty"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty" yaml:"revokedAt,omitempty"`
}

// IssuedEnrollmentToken is a new enrolment token with its secret, shown once.
type IssuedEnrollmentToken struct {
	EnrollmentToken `yaml:",inline"`
	Token           string `json:"token" yaml:"token"`
}

func (c *HTTPClient) CreateAccessProfile(ctx context.Context, input AccessProfileInput) (*AccessProfile, error) {
	var profile AccessProfile
	if err := c.request(ctx, http.MethodPost, managedPath("access-profiles"), input, &profile); err != nil {
		return nil, err
	}
	return &profile, nil
}

func (c *HTTPClient) DeleteAccessProfile(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodDelete, managedPath("access-profiles", id), nil, nil)
}

func (c *HTTPClient) CreateAPIToken(ctx context.Context, name, role string, expiresInHours int) (*IssuedToken, error) {
	var issued IssuedToken
	body := map[string]any{"name": name, "role": role, "expiresInHours": expiresInHours}
	if err := c.request(ctx, http.MethodPost, "/api/v1/tokens", body, &issued); err != nil {
		return nil, err
	}
	return &issued, nil
}

func (c *HTTPClient) ListAPITokens(ctx context.Context) ([]Token, error) {
	var tokens []Token
	err := c.get(ctx, "/api/v1/tokens", &tokens)
	return tokens, err
}

func (c *HTTPClient) RevokeAPIToken(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodDelete, "/api/v1/tokens/"+id, nil, nil)
}

func (c *HTTPClient) CreateEnrollmentToken(ctx context.Context, input EnrollmentTokenInput) (*IssuedEnrollmentToken, error) {
	var issued IssuedEnrollmentToken
	if err := c.request(ctx, http.MethodPost, "/api/v1/agents/enrollment-tokens", input, &issued); err != nil {
		return nil, err
	}
	return &issued, nil
}

func (c *HTTPClient) ListEnrollmentTokens(ctx context.Context) ([]EnrollmentToken, error) {
	var page struct {
		Items []EnrollmentToken `json:"items"`
	}
	err := c.get(ctx, "/api/v1/agents/enrollment-tokens", &page)
	return page.Items, err
}

func (c *HTTPClient) RevokeEnrollmentToken(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodDelete, "/api/v1/agents/enrollment-tokens/"+id, nil, nil)
}
