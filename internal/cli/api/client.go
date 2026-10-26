// Package api contains the small HTTP client surface used by CLI commands.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	if c == nil || c.baseURL == nil || c.http == nil {
		return errors.New("API client is not initialized")
	}
	if ctx == nil {
		return errors.New("request context is required")
	}
	requestURL := *c.baseURL
	requestURL.Path = path.Join(requestURL.Path, endpoint)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return fmt.Errorf("create API request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("request %s: %w", endpoint, err)
	}
	defer response.Body.Close()

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
	if err := json.Unmarshal(body, destination); err != nil {
		return fmt.Errorf("decode API response from %s: %w", endpoint, err)
	}
	return nil
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
