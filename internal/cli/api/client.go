// Package api contains the HTTP client used by CLI commands. It is the only
// place in the CLI that speaks HTTP: it applies authentication, bounds every
// request, closes every response body, and converts failures into classified
// errors (see Error and the clierr package).
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"

	"github.com/machinarchy/machinarchy/internal/buildinfo"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/config"
)

const (
	maxResponseBytes int64 = 16 << 20

	// versionSkewHint is for an answer that is valid JSON of the wrong shape:
	// the usual cause is a server and a CLI of different releases.
	versionSkewHint = "The server answered in a way this CLI does not understand. Update the CLI, or check the address: " +
		"this CLI supports servers of the same release (docs/cli/commands.md, section \"Supported servers\")."
	invalidResponseHint = "The server did not return the expected JSON. Check that the server URL points at the Machinarchy API and not at a proxy or web page."
)

// Environment is a named resource scope in Machinarchy.
type Environment struct {
	ID          int    `json:"id" yaml:"id"`
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
}

// Client is the narrow HTTP contract resource commands depend on, making
// command logic testable with a fake that needs no live server. Hosts and
// Operations come from the managed API (/api/v1/managed).
type Client interface {
	ListHosts(context.Context, HostQuery) (Page[Host], error)
	GetHost(context.Context, string) (*Host, error)
	CreateHost(context.Context, HostInput) (*Host, error)
	DeleteHost(context.Context, string, uint64) error
	SetHostLabels(context.Context, string, uint64, map[string]string) (*Host, error)
	TrustHostKeys(context.Context, string, uint64, []string, bool) (*Host, error)
	ListEnvironments(context.Context) ([]Environment, error)
	ListAccessProfiles(context.Context, string) (Page[AccessProfile], error)
	PreviewTargets(context.Context, TargetRule) (*TargetSet, error)
	SubmitOperation(context.Context, OperationInput) (*Operation, error)
	GetOperation(context.Context, string) (*Operation, error)
	CancelOperation(context.Context, string) (*Operation, error)
	TargetOutput(ctx context.Context, operationID, hostID, stream string, from int64) (*TargetOutput, error)
	ListOperations(context.Context, OperationQuery) (Page[OperationSummary], error)
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
// Create one with NewHTTPClient and share it for the lifetime of a command.
type HTTPClient struct {
	baseURL     *url.URL
	token       string
	http        *http.Client
	userAgent   string
	maxResponse int64
}

// Option customises an HTTPClient.
type Option func(*HTTPClient)

// WithHTTPClient supplies the underlying http.Client, for example to install
// a custom Transport. The client is copied: its Timeout is kept when set
// (otherwise the target's request timeout applies) and redirect following is
// always disabled so the bearer token is never replayed to another location.
func WithHTTPClient(client *http.Client) Option {
	return func(c *HTTPClient) {
		if client != nil {
			c.http = client
		}
	}
}

// WithUserAgent overrides the User-Agent header.
func WithUserAgent(userAgent string) Option {
	return func(c *HTTPClient) {
		if strings.TrimSpace(userAgent) != "" {
			c.userAgent = userAgent
		}
	}
}

func withMaxResponseBytes(limit int64) Option {
	return func(c *HTTPClient) {
		c.maxResponse = limit
	}
}

// NewHTTPClient creates a bounded-timeout HTTP client for the resolved target.
// The target's RequestTimeout bounds each request; zero selects the default.
func NewHTTPClient(target config.Target, opts ...Option) (*HTTPClient, error) {
	if err := config.ValidateServer(target.Server); err != nil {
		return nil, err
	}
	if err := config.ValidateToken(target.Token); err != nil {
		return nil, err
	}
	timeout := target.RequestTimeout
	if timeout < 0 {
		return nil, clierr.Usagef("request timeout must be positive")
	}
	if timeout == 0 {
		timeout = config.DefaultRequestTimeout
	}

	baseURL, err := url.Parse(strings.TrimRight(strings.TrimSpace(target.Server), "/"))
	if err != nil {
		return nil, clierr.Usage(fmt.Errorf("parse API server URL: %w", err))
	}

	client := &HTTPClient{
		baseURL:     baseURL,
		token:       target.Token,
		http:        &http.Client{},
		userAgent:   defaultUserAgent(),
		maxResponse: maxResponseBytes,
	}
	for _, opt := range opts {
		opt(client)
	}

	configured := *client.http
	if configured.Timeout == 0 {
		configured.Timeout = timeout
	}
	if configured.Transport == nil && target.CAFile != "" {
		transport, err := transportFor(target.CAFile)
		if err != nil {
			return nil, err
		}
		configured.Transport = transport
	}
	configured.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	client.http = &configured

	return client, nil
}

func defaultUserAgent() string {
	return fmt.Sprintf("machinarchy-cli/%s (%s/%s)", buildinfo.Version, runtime.GOOS, runtime.GOARCH)
}

var (
	_ Client           = (*HTTPClient)(nil)
	_ ManagementClient = (*HTTPClient)(nil)
)

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

// request is the single code path for every API call. It applies
// authentication, honours ctx cancellation and the request timeout, always
// closes the response body, and returns classified errors.
func (c *HTTPClient) request(ctx context.Context, method, endpoint string, payload, destination any) error {
	if c == nil || c.baseURL == nil || c.http == nil {
		return errors.New("API client is not initialized")
	}
	if ctx == nil {
		return errors.New("request context is required")
	}

	displayPath := endpointPath(endpoint)
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s %s: %w", method, displayPath, err)
	}

	requestURL, err := c.resolve(endpoint)
	if err != nil {
		return clierr.Usage(err)
	}

	var requestBody io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode API request: %w", err)
		}
		requestBody = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), requestBody)
	if err != nil {
		return fmt.Errorf("create API request: %w", err)
	}
	c.decorate(request, payload != nil)

	response, err := c.http.Do(request)
	if err != nil {
		return c.transportError(ctx, method, displayPath, err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			slog.Warn("failed to close HTTP response body", "error", err)
		}
	}()

	success := response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices
	limit := c.maxResponse
	if !success {
		limit = maxErrorBodyBytes
	}

	body, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if !success {
		if int64(len(body)) > limit {
			body = body[:limit]
		}

		return newError(response, method, displayPath, body)
	}
	if readErr != nil {
		return c.transportError(ctx, method, displayPath, readErr)
	}
	if int64(len(body)) > limit {
		return clierr.Wrap(clierr.ExitUnavailable,
			fmt.Errorf("API response from %s exceeds %d bytes", displayPath, limit), "")
	}

	return decodeResponse(response.StatusCode, displayPath, body, destination)
}

// decorate applies the headers every request carries. It is the only place
// the credential is attached.
func (c *HTTPClient) decorate(request *http.Request, hasBody bool) {
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("User-Agent", c.userAgent)
	if hasBody {
		request.Header.Set("Content-Type", "application/json")
	}
}

// resolve joins an endpoint (an absolute path, optionally with a query) onto
// the base URL. Escaped path segments are preserved, so an identifier
// containing "/" cannot change which route is addressed, and dot or empty
// segments are rejected rather than normalised away.
func (c *HTTPClient) resolve(endpoint string) (*url.URL, error) {
	ref, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse API endpoint: %w", err)
	}
	if ref.Scheme != "" || ref.Host != "" || !strings.HasPrefix(ref.Path, "/") {
		return nil, errors.New("API endpoint must be an absolute path")
	}

	escaped := ref.EscapedPath()
	for index, segment := range strings.Split(escaped, "/") {
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			return nil, fmt.Errorf("invalid API path segment: %w", err)
		}
		if decoded == "." || decoded == ".." {
			return nil, errors.New("API path must not contain dot segments")
		}
		if index > 0 && segment == "" {
			return nil, errors.New("API path must not contain empty segments; is an identifier blank?")
		}
	}

	joined := strings.TrimRight(c.baseURL.EscapedPath(), "/") + escaped
	decodedPath, err := url.PathUnescape(joined)
	if err != nil {
		return nil, fmt.Errorf("invalid API path: %w", err)
	}

	resolved := *c.baseURL
	resolved.Path = decodedPath
	resolved.RawPath = joined
	resolved.RawQuery = ref.RawQuery
	resolved.Fragment = ""

	return &resolved, nil
}

// transportError classifies a failure that produced no HTTP response.
func (c *HTTPClient) transportError(ctx context.Context, method, endpoint string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("%s %s: %w", method, endpoint, context.Canceled)
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return clierr.Wrap(clierr.ExitUnavailable,
			fmt.Errorf("%s %s did not finish before the command deadline: %w", method, endpoint, context.DeadlineExceeded), "")
	}

	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return clierr.Wrap(clierr.ExitUnavailable,
			fmt.Errorf("%s %s timed out after %s", method, endpoint, c.http.Timeout),
			"The server did not respond in time. Increase --request-timeout (or MACHINARCHY_REQUEST_TIMEOUT), or check that the server is healthy.")
	}

	// The URL error repeats the full request URL; report only the cause.
	cause := err
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		cause = urlErr.Err
	}

	return clierr.Wrap(clierr.ExitUnavailable,
		fmt.Errorf("cannot reach the API server at %s: %w", c.baseURL.Scheme+"://"+c.baseURL.Host, cause),
		"Check the server URL ('machinarchy whoami' shows it; --server changes it) and that the API is running.")
}

func decodeResponse(status int, endpoint string, body []byte, destination any) error {
	if destination == nil {
		return nil
	}

	if len(bytes.TrimSpace(body)) == 0 {
		switch status {
		case http.StatusAccepted, http.StatusNoContent, http.StatusResetContent:
			return nil
		default:
			return clierr.Wrap(clierr.ExitUnavailable,
				fmt.Errorf("API response from %s was empty", endpoint), invalidResponseHint)
		}
	}

	if err := json.Unmarshal(body, destination); err != nil {
		hint := invalidResponseHint
		var shape *json.UnmarshalTypeError
		if errors.As(err, &shape) && json.Valid(body) {
			hint = versionSkewHint
		}

		return clierr.Wrap(clierr.ExitUnavailable,
			fmt.Errorf("decode API response from %s: %w", endpoint, err), hint)
	}

	return nil
}

func endpointPath(endpoint string) string {
	path, _, _ := strings.Cut(endpoint, "?")

	return path
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
