package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// InventoryClient is the rest of the inventory surface the env, access and
// host commands need (M3): environments, one Access Profile, agents and the
// controller key. Hosts, profile list/create and enrolment tokens live in
// Client and AccessClient; secret creation is ManagementClient.CreateSecret.
type InventoryClient interface {
	CreateEnvironment(ctx context.Context, name, description string, protected bool) (*Environment, error)
	DeleteEnvironment(ctx context.Context, id int) error
	GetAccessProfile(ctx context.Context, id string) (*AccessProfileDetail, error)
	ListAgents(ctx context.Context) (json.RawMessage, error)
	RevokeAgent(ctx context.Context, id string) error
	ControllerKey(ctx context.Context) (*ControllerKey, error)
}

var _ InventoryClient = (*HTTPClient)(nil)

// AccessProfileDetail is one Access Profile with its current spec. The spec
// holds secret references only, never secret values.
type AccessProfileDetail struct {
	ID              string         `json:"id" yaml:"id"`
	Name            string         `json:"name" yaml:"name"`
	Description     string         `json:"description,omitempty" yaml:"description,omitempty"`
	CurrentRevision uint64         `json:"currentRevision" yaml:"currentRevision"`
	Spec            map[string]any `json:"spec,omitempty" yaml:"spec,omitempty"`
	CreatedAt       time.Time      `json:"createdAt" yaml:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt" yaml:"updatedAt"`
}

// ControllerKey is the public key(s) an agent must trust. PublicKeys is the
// value for the agent's MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY.
type ControllerKey struct {
	PublicKey     string   `json:"publicKey" yaml:"publicKey"`
	PublicKeys    []string `json:"publicKeys" yaml:"publicKeys"`
	NextPublicKey string   `json:"nextPublicKey,omitempty" yaml:"nextPublicKey,omitempty"`
}

func (c *HTTPClient) CreateEnvironment(ctx context.Context, name, description string, protected bool) (*Environment, error) {
	var environment Environment
	body := map[string]any{"name": name, "description": description}
	if protected {
		body["protected"] = true // a server that predates the field ignores it
	}
	if err := c.request(ctx, http.MethodPost, "/api/v1/environments", body, &environment); err != nil {
		return nil, err
	}
	return &environment, nil
}

func (c *HTTPClient) DeleteEnvironment(ctx context.Context, id int) error {
	return c.request(ctx, http.MethodDelete, "/api/v1/environments/"+strconv.Itoa(id), nil, nil)
}

func (c *HTTPClient) GetAccessProfile(ctx context.Context, id string) (*AccessProfileDetail, error) {
	var profile AccessProfileDetail
	if err := c.get(ctx, managedPath("access-profiles", id), &profile); err != nil {
		return nil, err
	}
	return &profile, nil
}

// ListAgents returns the response as the server sent it: its shape is not in
// the API contract (gap C-09), so callers decode only the fields they need.
func (c *HTTPClient) ListAgents(ctx context.Context) (json.RawMessage, error) {
	return c.raw(ctx, http.MethodGet, "/api/v1/agents", nil)
}

func (c *HTTPClient) RevokeAgent(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodPost, "/api/v1/agents/"+url.PathEscape(id)+"/revoke", nil, nil)
}

func (c *HTTPClient) ControllerKey(ctx context.Context) (*ControllerKey, error) {
	var key ControllerKey
	if err := c.get(ctx, "/api/v1/agents/controller-key", &key); err != nil {
		return nil, err
	}
	return &key, nil
}
