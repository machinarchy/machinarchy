package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// The types below mirror the JSON of /api/v1/managed. The CLI keeps its own
// copies (ADR-035: it may only depend on the public HTTP API).

// Host is a managed host. It never contains credentials: authentication is
// behind the Access Profile it references.
type Host struct {
	Metadata HostMetadata `json:"metadata" yaml:"metadata"`
	Spec     HostSpec     `json:"spec" yaml:"spec"`
	Status   HostStatus   `json:"status" yaml:"status"`
}

type HostMetadata struct {
	ID            string            `json:"id" yaml:"id"`
	EnvironmentID int               `json:"environmentId,omitempty" yaml:"environmentId,omitempty"`
	Name          string            `json:"name" yaml:"name"`
	Labels        map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Generation    uint64            `json:"generation" yaml:"generation"`
	Revision      uint64            `json:"revision" yaml:"revision"`
	CreatedAt     time.Time         `json:"createdAt" yaml:"createdAt"`
	UpdatedAt     time.Time         `json:"updatedAt" yaml:"updatedAt"`
}

type HostSpec struct {
	Presence        string           `json:"presence,omitempty" yaml:"presence,omitempty"`
	AccessProfileID string           `json:"accessProfileId,omitempty" yaml:"accessProfileId,omitempty"`
	AccessTarget    HostAccessTarget `json:"accessTarget,omitempty" yaml:"accessTarget,omitempty"`
}

type HostAccessTarget struct {
	Endpoint string   `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	Trust    []string `json:"trust,omitempty" yaml:"trust,omitempty"`
}

type HostStatus struct {
	Phase            string            `json:"phase" yaml:"phase"`
	Conditions       []Condition       `json:"conditions,omitempty" yaml:"conditions,omitempty"`
	ObservedHostKeys []ObservedHostKey `json:"observedHostKeys,omitempty" yaml:"observedHostKeys,omitempty"`
}

// ObservedHostKey is an SSH host key the controller saw on the network.
type ObservedHostKey struct {
	Algorithm   string    `json:"algorithm" yaml:"algorithm"`
	Fingerprint string    `json:"fingerprint" yaml:"fingerprint"`
	FirstSeenAt time.Time `json:"firstSeenAt" yaml:"firstSeenAt"`
}

type Condition struct {
	Type    string `json:"type" yaml:"type"`
	Status  string `json:"status" yaml:"status"`
	Reason  string `json:"reason" yaml:"reason"`
	Message string `json:"message,omitempty" yaml:"message,omitempty"`
}

// AccessProfile is a credential profile; Credential is a reference only.
type AccessProfile struct {
	ID              string `json:"id" yaml:"id"`
	Name            string `json:"name" yaml:"name"`
	Description     string `json:"description,omitempty" yaml:"description,omitempty"`
	CurrentRevision uint64 `json:"currentRevision" yaml:"currentRevision"`
}

// TargetRule selects hosts: a selector, include/exclude lists, or all.
type TargetRule struct {
	Selector string   `json:"selector,omitempty" yaml:"selector,omitempty"`
	Include  []string `json:"include,omitempty" yaml:"include,omitempty"`
	Exclude  []string `json:"exclude,omitempty" yaml:"exclude,omitempty"`
	All      bool     `json:"all,omitempty" yaml:"all,omitempty"`
}

// TargetSet is a preview of the hosts a rule selects.
type TargetSet struct {
	Hosts           []Host   `json:"hosts" yaml:"hosts"`
	Excluded        []Host   `json:"excluded" yaml:"excluded"`
	UnknownIncludes []string `json:"unknownIncludes" yaml:"unknownIncludes"`
	UnknownExcludes []string `json:"unknownExcludes" yaml:"unknownExcludes"`
	TargetsHash     string   `json:"targetsHash" yaml:"targetsHash"`
}

type RunSpec struct {
	Argv           []string `json:"argv" yaml:"argv"`
	TimeoutSeconds int      `json:"timeoutSeconds" yaml:"timeoutSeconds"`
	Concurrency    int      `json:"concurrency" yaml:"concurrency"`
}

// OperationInput submits a run on a rule, bound to a previewed target set.
type OperationInput struct {
	Targets           TargetRule `json:"targets"`
	ExpectTargetsHash string     `json:"expectTargetsHash,omitempty"`
	Run               RunSpec    `json:"run"`
}

type Operation struct {
	ID        string          `json:"id" yaml:"id"`
	CreatedAt time.Time       `json:"createdAt" yaml:"createdAt"`
	Spec      OperationSpec   `json:"spec" yaml:"spec"`
	Status    OperationStatus `json:"status" yaml:"status"`
}

type OperationSpec struct {
	Run *RunSpec `json:"run,omitempty" yaml:"run,omitempty"`
}

type OperationStatus struct {
	State      string            `json:"state" yaml:"state"`
	Conditions []Condition       `json:"conditions,omitempty" yaml:"conditions,omitempty"`
	Targets    []OperationTarget `json:"targets" yaml:"targets"`
}

type OperationTarget struct {
	Spec   OperationTargetSpec `json:"spec" yaml:"spec"`
	State  string              `json:"state" yaml:"state"`
	Result *TargetResult       `json:"result,omitempty" yaml:"result,omitempty"`
}

type OperationTargetSpec struct {
	HostID      string `json:"hostId" yaml:"hostId"`
	DisplayName string `json:"displayName" yaml:"displayName"`
}

type TargetResult struct {
	ExitCode   *int   `json:"exitCode,omitempty" yaml:"exitCode,omitempty"`
	Output     string `json:"output,omitempty" yaml:"output,omitempty"`
	ErrorClass string `json:"errorClass,omitempty" yaml:"errorClass,omitempty"`
}

// OperationSummary is the list view of an Operation (no argv or output).
type OperationSummary struct {
	ID              string         `json:"id" yaml:"id"`
	State           string         `json:"state" yaml:"state"`
	CancelRequested bool           `json:"cancelRequested,omitempty" yaml:"cancelRequested,omitempty"`
	CreatedAt       time.Time      `json:"createdAt" yaml:"createdAt"`
	FinishedAt      *time.Time     `json:"finishedAt,omitempty" yaml:"finishedAt,omitempty"`
	Targets         map[string]int `json:"targets" yaml:"targets"`
}

// Page is one page of a list response.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// HostQuery filters GET /managed/hosts.
type HostQuery struct {
	Selector      string
	EnvironmentID int
	Cursor        string
}

// OperationQuery filters GET /managed/operations.
type OperationQuery struct {
	State  string
	HostID string
	Cursor string
	Limit  int
}

func managedPath(segments ...string) string {
	path := "/api/v1/managed"
	for _, segment := range segments {
		path += "/" + url.PathEscape(segment)
	}
	return path
}

func withQuery(path string, values url.Values) string {
	if encoded := values.Encode(); encoded != "" {
		return path + "?" + encoded
	}
	return path
}

func (c *HTTPClient) ListHosts(ctx context.Context, query HostQuery) (Page[Host], error) {
	values := url.Values{}
	if query.Selector != "" {
		values.Set("selector", query.Selector)
	}
	if query.EnvironmentID > 0 {
		values.Set("environment", strconv.Itoa(query.EnvironmentID))
	}
	if query.Cursor != "" {
		values.Set("cursor", query.Cursor)
	}
	var page Page[Host]
	err := c.get(ctx, withQuery(managedPath("hosts"), values), &page)
	return page, err
}

func (c *HTTPClient) GetHost(ctx context.Context, id string) (*Host, error) {
	var host Host
	if err := c.get(ctx, managedPath("hosts", id), &host); err != nil {
		return nil, err
	}
	return &host, nil
}

// HostInput creates a managed host.
type HostInput struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Spec   HostSpec          `json:"spec"`
}

func (c *HTTPClient) CreateHost(ctx context.Context, input HostInput) (*Host, error) {
	var host Host
	if err := c.request(ctx, http.MethodPost, managedPath("hosts"), input, &host); err != nil {
		return nil, err
	}
	return &host, nil
}

func (c *HTTPClient) DeleteHost(ctx context.Context, id string, revision uint64) error {
	return c.request(ctx, http.MethodDelete, withQuery(managedPath("hosts", id),
		url.Values{"revision": {strconv.FormatUint(revision, 10)}}), nil, nil)
}

func (c *HTTPClient) SetHostLabels(ctx context.Context, id string, revision uint64, labels map[string]string) (*Host, error) {
	var host Host
	err := c.request(ctx, http.MethodPatch, managedPath("hosts", id, "labels"),
		map[string]any{"revision": revision, "labels": labels}, &host)
	if err != nil {
		return nil, err
	}
	return &host, nil
}

// TrustHostKeys approves the given host key fingerprints for a Host whose
// spec generation is still the one the caller reviewed.
func (c *HTTPClient) TrustHostKeys(ctx context.Context, id string, generation uint64, fingerprints []string, replace bool) (*Host, error) {
	var host Host
	err := c.request(ctx, http.MethodPost, managedPath("hosts", id, "trust"),
		map[string]any{"generation": generation, "fingerprints": fingerprints, "replace": replace}, &host)
	if err != nil {
		return nil, err
	}
	return &host, nil
}

func (c *HTTPClient) ListAccessProfiles(ctx context.Context, cursor string) (Page[AccessProfile], error) {
	var page Page[AccessProfile]
	values := url.Values{}
	if cursor != "" {
		values.Set("cursor", cursor)
	}
	err := c.get(ctx, withQuery(managedPath("access-profiles"), values), &page)
	return page, err
}

func (c *HTTPClient) PreviewTargets(ctx context.Context, rule TargetRule) (*TargetSet, error) {
	var set TargetSet
	if err := c.request(ctx, http.MethodPost, managedPath("operations", "preview"), map[string]any{"targets": rule}, &set); err != nil {
		return nil, err
	}
	return &set, nil
}

func (c *HTTPClient) SubmitOperation(ctx context.Context, input OperationInput) (*Operation, error) {
	var op Operation
	if err := c.request(ctx, http.MethodPost, managedPath("operations"), input, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

func (c *HTTPClient) GetOperation(ctx context.Context, id string) (*Operation, error) {
	var op Operation
	if err := c.get(ctx, managedPath("operations", id), &op); err != nil {
		return nil, err
	}
	return &op, nil
}

func (c *HTTPClient) CancelOperation(ctx context.Context, id string) (*Operation, error) {
	var op Operation
	if err := c.request(ctx, http.MethodPost, managedPath("operations", id, "cancel"), nil, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

func (c *HTTPClient) ListOperations(ctx context.Context, query OperationQuery) (Page[OperationSummary], error) {
	values := url.Values{}
	for key, value := range map[string]string{"state": query.State, "host": query.HostID, "cursor": query.Cursor} {
		if value != "" {
			values.Set(key, value)
		}
	}
	if query.Limit > 0 {
		values.Set("limit", strconv.Itoa(query.Limit))
	}
	var page Page[OperationSummary]
	err := c.get(ctx, withQuery(managedPath("operations"), values), &page)
	return page, err
}
