package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

// MaxHostBatch is the most hosts one batch request may carry.
const MaxHostBatch = 100

// ErrBatchSize reports a batch with no hosts or more than MaxHostBatch.
var ErrBatchSize = fmt.Errorf("%w: a batch needs 1 to %d hosts", domain.ErrInvalidInput, MaxHostBatch)

// Per-host outcomes of a batch.
const (
	BatchCreated   = "created"
	BatchValid     = "valid"
	BatchConflict  = "conflict"
	BatchInvalid   = "invalid"
	BatchForbidden = "forbidden"
	BatchError     = "error"
)

// HostCreate is one host of a batch, shaped like a single create.
type HostCreate struct {
	Name          string
	EnvironmentID int
	Labels        map[string]string
	Spec          resource.HostSpec
}

// BatchHostOutcome is the result for the host at Index of the request.
type BatchHostOutcome struct {
	Index  int         `json:"index"`
	Name   string      `json:"name"`
	Status string      `json:"status"`
	ID     resource.ID `json:"id,omitempty"`
	Error  string      `json:"error,omitempty"`
}

// BatchHostsResult is the per-host report of CreateHostsBatch.
type BatchHostsResult struct {
	ValidateOnly bool               `json:"validateOnly"`
	Created      int                `json:"created"`
	Failed       int                `json:"failed"`
	Results      []BatchHostOutcome `json:"results"`
}

// CreateHostsBatch registers up to MaxHostBatch hosts. Items are independent:
// one failure never rolls back another, and an existing name is reported as a
// conflict, never overwritten. With validateOnly nothing is written. Each item
// gets the checks of a single create, so a profile-bound host still needs the
// owner. It never runs commands, probes the host or trusts host keys.
func (s *ManagedResourceService) CreateHostsBatch(ctx context.Context, principal domain.Principal, hosts []HostCreate, validateOnly bool) (*BatchHostsResult, error) {
	if err := authorizeManagedHost(principal); err != nil {
		return nil, err
	}
	if len(hosts) == 0 || len(hosts) > MaxHostBatch {
		return nil, ErrBatchSize
	}
	result := &BatchHostsResult{ValidateOnly: validateOnly, Results: make([]BatchHostOutcome, len(hosts))}
	seen := map[string]bool{}
	for i, item := range hosts {
		out := BatchHostOutcome{Index: i, Name: item.Name}
		if err := ctx.Err(); err != nil {
			out.Status, out.Error = BatchError, "cancelled"
		} else {
			s.batchOne(ctx, principal, item, validateOnly, seen, &out)
		}
		switch out.Status {
		case BatchCreated:
			result.Created++
		case BatchValid:
		default:
			result.Failed++
		}
		result.Results[i] = out
	}
	names := make([]string, 0, result.Created)
	for _, out := range result.Results {
		if out.Status == BatchCreated {
			names = append(names, out.Name)
		}
	}
	// Each created host is already a chained audit event in the store; this
	// line records who ran the batch.
	slog.Info("audit", "event", "host.batch_registered", "actor_user_id", principal.UserID,
		"workspace_id", principal.WorkspaceID, "validate_only", validateOnly,
		"requested", len(hosts), "created", result.Created, "failed", result.Failed,
		"names", strings.Join(names, ","))
	return result, nil
}

func (s *ManagedResourceService) batchOne(ctx context.Context, principal domain.Principal, item HostCreate, validateOnly bool, seen map[string]bool, out *BatchHostOutcome) {
	if item.Spec.AccessProfileID != "" {
		if err := authorizeProfileOwner(principal); err != nil {
			out.Status, out.Error = BatchForbidden, "workspace owner permission is required to bind an Access Profile"
			return
		}
	}
	environmentID, err := s.resolveEnvironment(ctx, principal.WorkspaceID, item.EnvironmentID)
	if err != nil {
		out.Status, out.Error = BatchInvalid, "unknown environment"
		return
	}
	host, err := resource.NewHost(principal.WorkspaceID, environmentID, item.Name, item.Labels, item.Spec, time.Now().UTC())
	if err != nil {
		out.Status, out.Error = BatchInvalid, shortError(err)
		return
	}
	key := fmt.Sprintf("%d/%s", environmentID, strings.ToLower(host.Metadata.Name))
	if seen[key] {
		out.Status, out.Error = BatchConflict, "duplicate in request"
		return
	}
	seen[key] = true
	if validateOnly {
		exists, err := s.hostNameExists(ctx, principal.WorkspaceID, environmentID, host.Metadata.Name)
		switch {
		case err != nil:
			out.Status, out.Error = BatchError, "lookup failed"
		case exists:
			out.Status, out.Error = BatchConflict, "host already exists"
		default:
			out.Status = BatchValid
		}
		return
	}
	switch err := s.hosts.CreateHost(ctx, host); {
	case err == nil:
		out.Status, out.ID = BatchCreated, host.Metadata.ID
	case errors.Is(err, resource.ErrAlreadyExists):
		out.Status, out.Error = BatchConflict, "host already exists"
	case errors.Is(err, resource.ErrInvalidResource), errors.Is(err, resource.ErrAccessProfileNotFound):
		out.Status, out.Error = BatchInvalid, shortError(err)
	default:
		out.Status, out.Error = BatchError, "could not create host"
	}
}

func (s *ManagedResourceService) hostNameExists(ctx context.Context, workspaceID, environmentID int, name string) (bool, error) {
	filter := resource.HostListFilter{WorkspaceID: workspaceID, EnvironmentID: environmentID, Lookup: []string{name}, Limit: 100}
	for {
		page, err := s.hosts.ListHosts(ctx, filter)
		if err != nil {
			return false, err
		}
		for i := range page {
			if strings.EqualFold(page[i].Metadata.Name, name) && page[i].Metadata.EnvironmentID == environmentID {
				return true, nil
			}
		}
		if len(page) < filter.Limit {
			return false, nil
		}
		filter.After = page[len(page)-1].Metadata.ID
	}
}

func shortError(err error) string {
	text := err.Error()
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}
