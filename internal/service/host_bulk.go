package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/machinarchy/machinarchy/internal/domain"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	// MaxBulkRegisterHosts is the largest batch one request may submit.
	MaxBulkRegisterHosts = 100
	// bulkRegisterWorkers bounds concurrent verifications per batch. The shared
	// SSH capacity limiter still applies on top of this per-batch bound.
	bulkRegisterWorkers = 4
)

// BulkRegisterOptions tunes a bulk registration run.
type BulkRegisterOptions struct {
	// ValidateOnly checks records and duplicates without contacting any host or
	// writing to the inventory.
	ValidateOnly bool
}

// BulkRegisterHosts implements HostService.
//
// Semantics: partial success. Records are normalized and checked for
// duplicates sequentially (against the current inventory and earlier records in
// the batch), then verified and persisted by a small bounded worker pool using
// the same RegisterHost path as single registration. Duplicate detection is a
// read-then-write check, so two concurrent batches can still insert the same
// host; the schema has no uniqueness constraint to prevent that.
func (s *hostService) BulkRegisterHosts(ctx context.Context, workspaceID int, entries []domain.BulkHostEntry, opts BulkRegisterOptions) (*domain.BulkImportResponse, error) {
	if ctx == nil {
		return nil, errors.New("bulk registration context is required")
	}
	if workspaceID <= 0 {
		return nil, fmt.Errorf("%w: workspace is required", domain.ErrInvalidInput)
	}
	if len(entries) == 0 || len(entries) > MaxBulkRegisterHosts {
		return nil, fmt.Errorf("%w: a batch must contain between 1 and %d hosts", domain.ErrInvalidInput, MaxBulkRegisterHosts)
	}
	if s.repo == nil {
		return nil, errors.New("host service dependencies are not configured")
	}

	ctx, span := otel.Tracer("machinarchy/internal/service").Start(ctx, "host.bulk_register",
		trace.WithAttributes(
			attribute.Int("host.batch_size", len(entries)),
			attribute.Bool("host.validate_only", opts.ValidateOnly),
		))
	defer span.End()

	existing, err := s.repo.ListAllHosts(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list inventory for duplicate detection: %w", err)
	}
	seen := make(map[string]struct{}, len(existing)*2+len(entries)*2)
	for _, h := range existing {
		for _, key := range hostIdentityKeys(h) {
			seen[key] = struct{}{}
		}
	}

	results := make([]domain.HostOperationResult, len(entries))
	var pending []int
	for i, entry := range entries {
		results[i] = domain.HostOperationResult{Index: i}
		if entry.PreError != nil {
			if entry.Host != nil {
				fillIdentity(&results[i], entry.Host)
			}
			results[i].Status = domain.StatusFailed
			results[i].Error = entry.PreError.Error()
			continue
		}
		h := entry.Host
		if h == nil {
			results[i].Status = domain.StatusFailed
			results[i].Error = "host is required"
			continue
		}
		h.WorkspaceID = workspaceID
		if err := h.SSHValidate(); err != nil {
			fillIdentity(&results[i], h)
			results[i].Status = domain.StatusFailed
			results[i].Error = bulkFailureMessage(err)
			continue
		}
		fillIdentity(&results[i], h)
		keys := hostIdentityKeys(h)
		if dup := firstSeen(seen, keys); dup != "" {
			results[i].Status = domain.StatusDuplicate
			results[i].Error = "a host with the same name or address:port already exists or appears earlier in this batch"
			continue
		}
		for _, key := range keys {
			seen[key] = struct{}{}
		}
		if opts.ValidateOnly {
			results[i].Status = domain.StatusValid
			continue
		}
		pending = append(pending, i)
	}

	s.registerPending(ctx, entries, results, pending)

	response := domain.NewBulkImportResponse(opts.ValidateOnly, results)
	span.SetAttributes(
		attribute.Int("host.success_count", response.SuccessCount),
		attribute.Int("host.failure_count", response.FailureCount),
		attribute.Int("host.duplicate_count", response.DuplicateCount),
	)
	return response, nil
}

// registerPending verifies and persists pending records with bounded
// concurrency. Each worker writes only its own results[i], so no lock is needed.
// Records never started because ctx ended are reported as failed.
func (s *hostService) registerPending(ctx context.Context, entries []domain.BulkHostEntry, results []domain.HostOperationResult, pending []int) {
	if len(pending) == 0 {
		return
	}
	workers := bulkRegisterWorkers
	if len(pending) < workers {
		workers = len(pending)
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				h := entries[i].Host
				if err := s.RegisterHost(ctx, h); err != nil {
					results[i].Status = domain.StatusFailed
					results[i].Error = bulkFailureMessage(err)
					continue
				}
				results[i].Status = domain.StatusSuccess
				results[i].ID = h.ID
			}
		}()
	}
dispatch:
	for _, i := range pending {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()

	for _, i := range pending {
		if results[i].Status == "" {
			results[i].Status = domain.StatusFailed
			results[i].Error = "not processed: request was cancelled or timed out"
		}
	}
}

func fillIdentity(r *domain.HostOperationResult, h *domain.Host) {
	r.Name, r.Hostname, r.Address = h.Name, h.Hostname, h.IPAddress
}

// hostIdentityKeys returns the keys under which a host counts as a duplicate:
// its case-insensitive name and its address:port.
func hostIdentityKeys(h *domain.Host) []string {
	if h == nil {
		return nil
	}
	port := 22
	if h.SSH != nil && h.SSH.Port > 0 {
		port = h.SSH.Port
	}
	keys := make([]string, 0, 2)
	if name := strings.ToLower(strings.TrimSpace(h.Name)); name != "" {
		keys = append(keys, "name:"+name)
	}
	if address := strings.ToLower(strings.TrimSpace(h.IPAddress)); address != "" {
		keys = append(keys, fmt.Sprintf("addr:%s:%d", address, port))
	}
	return keys
}

func firstSeen(seen map[string]struct{}, keys []string) string {
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			return key
		}
	}
	return ""
}

// bulkFailureMessage turns an error into a message that is safe to return to
// API clients. Unexpected errors are logged and reduced to a generic message.
func bulkFailureMessage(err error) string {
	var unknownKey *domain.ErrHostKeyUnknown
	switch {
	case errors.As(err, &unknownKey):
		return fmt.Sprintf("untrusted SSH host key (%s %s)", unknownKey.Algorithm, unknownKey.Fingerprint)
	case errors.Is(err, domain.ErrAuthenticationFailed):
		return "SSH authentication failed"
	case errors.Is(err, domain.ErrInvalidInput):
		return err.Error()
	case errors.Is(err, domain.ErrEnvironmentNotFound):
		return "environment not found in this workspace"
	case errors.Is(err, domain.ErrServiceBusy):
		return "host verification capacity is temporarily full; retry this record"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "host verification timed out or was cancelled"
	case errors.Is(err, domain.ErrConnectionTimeout):
		return "connection to the host timed out"
	case errors.Is(err, domain.ErrConnectionRefused):
		return "the host refused the connection"
	default:
		slog.Error("bulk host registration failed", "error", err)
		return "registration failed"
	}
}
