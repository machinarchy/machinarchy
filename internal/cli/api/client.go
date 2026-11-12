// Package api contains the small HTTP client surface used by CLI commands.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/cli/config"
)

const maxResponseBytes = 16 << 20

// Host is the credential-free host representation returned by the API.
type Host struct {
	ID            string            `json:"id" yaml:"id"`
	EnvironmentID int               `json:"environmentId" yaml:"environmentId"`
	Name          string            `json:"name" yaml:"name"`
	Hostname      string            `json:"hostname" yaml:"hostname"`
	IPAddress     string            `json:"ip_address" yaml:"ip_address"`
	Connection    string            `json:"connection" yaml:"connection"`
	Labels        map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	SSH           *SSHState         `json:"ssh,omitempty" yaml:"ssh,omitempty"`
	CreatedAt     time.Time         `json:"created_at" yaml:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at" yaml:"updated_at"`
}

// SSHState contains operational SSH state without authentication secrets.
type SSHState struct {
	Username         string     `json:"username" yaml:"username"`
	Port             int        `json:"port" yaml:"port"`
	Status           string     `json:"status" yaml:"status"`
	LastCheckAt      *time.Time `json:"last_check_at,omitempty" yaml:"last_check_at,omitempty"`
	LastCheckMS      *int64     `json:"last_check_duration_ms,omitempty" yaml:"last_check_duration_ms,omitempty"`
	LastCheckMessage string     `json:"last_check_message,omitempty" yaml:"last_check_message,omitempty"`
}

// Environment is a named resource scope in Machinarchy.
type Environment struct {
	ID          int    `json:"id" yaml:"id"`
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
}

// Client is the narrow HTTP contract commands depend on, making command logic
// testable with a fake that needs no live server.
// Client is the read-only API surface used by current resource commands.
type Client interface {
	ListHosts(context.Context) ([]Host, error)
	ListEnvironments(context.Context) ([]Environment, error)
}

// ManagementClient is the API-only surface for runtime configuration and
// write-only Secret Store administration.
type ManagementClient interface {
	RuntimeConfig(context.Context) (json.RawMessage, error)
	ConfigHistory(context.Context, uint64) (json.RawMessage, error)
	ConfigStatus(context.Context) (json.RawMessage, error)
	ValidateRuntimeConfig(context.Context, json.RawMessage) (json.RawMessage, error)
	PublishRuntimeConfig(context.Context, uint64, json.RawMessage, string) (json.RawMessage, error)
	RollbackRuntimeConfig(context.Context, uint64, uint64, string) (json.RawMessage, error)
	ListSecrets(context.Context) (json.RawMessage, error)
	CreateSecret(context.Context, json.RawMessage) (json.RawMessage, error)
	SecretVersions(context.Context, string) (json.RawMessage, error)
	RotateSecret(context.Context, string, uint64, string) (json.RawMessage, error)
	RewrapSecrets(context.Context, string, int) (json.RawMessage, error)
	DeleteSecret(context.Context, string) error
}

// HTTPClient is an immutable, concurrency-safe client for the Machinarchy API.
type HTTPClient struct {
	baseURL *url.URL
	token   string
	http    *http.Client
}

// NewHTTPClient creates a bounded-timeout HTTP client for the resolved target.
func NewHTTPClient(target config.Target) (*HTTPClient, error) {
	if err := config.ValidateServer(target.Server); err != nil {
		return nil, err
	}
	baseURL, err := url.Parse(strings.TrimRight(strings.TrimSpace(target.Server), "/"))
	if err != nil {
		return nil, fmt.Errorf("parse API server URL: %w", err)
	}
	return &HTTPClient{
		baseURL: baseURL,
		token:   target.Token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// ListHosts retrieves all hosts visible to the configured API token.
func (c *HTTPClient) ListHosts(ctx context.Context) ([]Host, error) {
	var hosts []Host
	if err := c.get(ctx, "/api/v1/hosts", &hosts); err != nil {
		return nil, err
	}
	if hosts == nil {
		hosts = make([]Host, 0)
	}
	return hosts, nil
}

// ListEnvironments retrieves environments used to resolve the --env filter.
func (c *HTTPClient) ListEnvironments(ctx context.Context) ([]Environment, error) {
	var environments []Environment
	if err := c.get(ctx, "/api/v1/environments", &environments); err != nil {
		return nil, err
	}
	if environments == nil {
		environments = make([]Environment, 0)
	}
	return environments, nil
}

func (c *HTTPClient) get(ctx context.Context, endpoint string, destination any) error {
	return c.request(ctx, http.MethodGet, endpoint, nil, destination)
}

func (c *HTTPClient) request(ctx context.Context, method, endpoint string, payload, destination any) error {
	if c == nil || c.baseURL == nil || c.http == nil {
		return errors.New("API client is not initialized")
	}
	if ctx == nil {
		return errors.New("request context is required")
	}
	requestURL := *c.baseURL
	endpointURL, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse API endpoint: %w", err)
	}
	requestURL.Path = path.Join(requestURL.Path, endpointURL.Path)
	requestURL.RawQuery = endpointURL.RawQuery
	var requestBody io.Reader
	if payload != nil {
		encoded, e := json.Marshal(payload)
		if e != nil {
			return fmt.Errorf("encode API request: %w", e)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), requestBody)
	if err != nil {
		return fmt.Errorf("create API request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("request %s: %w", endpoint, err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			slog.Warn("failed to close HTTP response body", "error", err)
		}
	}()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read API response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return fmt.Errorf("API response exceeds %d bytes", maxResponseBytes)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return apiResponseError(response.StatusCode, body)
	}
	if destination == nil {
		return nil
	}
	if err := json.Unmarshal(body, destination); err != nil {
		return fmt.Errorf("decode API response from %s: %w", endpoint, err)
	}
	return nil
}

func (c *HTTPClient) raw(ctx context.Context, method, path string, payload any) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.request(ctx, method, path, payload, &out)
	return out, err
}
func (c *HTTPClient) RuntimeConfig(ctx context.Context) (json.RawMessage, error) {
	return c.raw(ctx, http.MethodGet, "/api/v1/runtime-config", nil)
}
func (c *HTTPClient) ConfigHistory(ctx context.Context, before uint64) (json.RawMessage, error) {
	p := "/api/v1/runtime-config/revisions"
	if before > 0 {
		p += fmt.Sprintf("?before=%d", before)
	}
	return c.raw(ctx, http.MethodGet, p, nil)
}
func (c *HTTPClient) ConfigStatus(ctx context.Context) (json.RawMessage, error) {
	return c.raw(ctx, http.MethodGet, "/api/v1/runtime-config/status", nil)
}
func (c *HTTPClient) ValidateRuntimeConfig(ctx context.Context, config json.RawMessage) (json.RawMessage, error) {
	return c.raw(ctx, http.MethodPost, "/api/v1/runtime-config/validate", map[string]json.RawMessage{"config": config})
}
func (c *HTTPClient) PublishRuntimeConfig(ctx context.Context, expected uint64, config json.RawMessage, note string) (json.RawMessage, error) {
	return c.raw(ctx, http.MethodPut, "/api/v1/runtime-config", map[string]any{"expectedRevision": expected, "config": config, "changeNote": note})
}
func (c *HTTPClient) RollbackRuntimeConfig(ctx context.Context, expected, target uint64, note string) (json.RawMessage, error) {
	return c.raw(ctx, http.MethodPost, "/api/v1/runtime-config/rollback", map[string]any{"expectedRevision": expected, "targetRevision": target, "changeNote": note})
}
func (c *HTTPClient) ListSecrets(ctx context.Context) (json.RawMessage, error) {
	return c.raw(ctx, http.MethodGet, "/api/v1/secrets", nil)
}
func (c *HTTPClient) CreateSecret(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	return c.raw(ctx, http.MethodPost, "/api/v1/secrets", input)
}
func (c *HTTPClient) SecretVersions(ctx context.Context, id string) (json.RawMessage, error) {
	return c.raw(ctx, http.MethodGet, "/api/v1/secrets/"+url.PathEscape(id)+"/versions", nil)
}
func (c *HTTPClient) RotateSecret(ctx context.Context, id string, expected uint64, value string) (json.RawMessage, error) {
	return c.raw(ctx, http.MethodPost, "/api/v1/secrets/"+url.PathEscape(id)+"/rotate", map[string]any{"expectedVersion": expected, "value": value})
}
func (c *HTTPClient) RewrapSecrets(ctx context.Context, oldKeyID string, batchSize int) (json.RawMessage, error) {
	return c.raw(ctx, http.MethodPost, "/api/v1/secrets/rewrap", map[string]any{"oldKeyId": oldKeyID, "batchSize": batchSize})
}
func (c *HTTPClient) DeleteSecret(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodDelete, "/api/v1/secrets/"+url.PathEscape(id), nil, nil)
}

func apiResponseError(status int, body []byte) error {
	message := http.StatusText(status)
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && strings.TrimSpace(payload.Error) != "" {
		message = strings.TrimSpace(payload.Error)
	}
	return fmt.Errorf("API returned HTTP %d: %s", status, message)
}
