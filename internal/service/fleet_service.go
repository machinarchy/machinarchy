package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// FleetService runs operations across selected hosts.
//
// Not wired into the API yet: remote execution is not implemented, so
// ExecuteOnFleet reports ErrExecutionNotImplemented for every selected host.
type FleetService interface {
	ExecuteOnFleet(ctx context.Context, workspaceID int, rule domain.TargetRule, command string) (*domain.FleetExecutionResult, error)
}

// ErrExecutionNotImplemented is returned until an SSH execution transport exists.
var ErrExecutionNotImplemented = errors.New("remote command execution is not implemented")

type fleetService struct {
	repo         domain.HostStore
	environments domain.EnvironmentStore
	sshLimit     *SSHCapacity
}

func NewFleetService(repo domain.HostStore, environments domain.EnvironmentStore, sshLimit *SSHCapacity) FleetService {
	return &fleetService{
		repo:         repo,
		environments: environments,
		sshLimit:     sshLimit,
	}
}

// FleetExecutionResult captures aggregated and per-host outcomes.
type FleetExecutionResult struct {
	TotalTargeted int                    `json:"total_targeted"`
	SuccessCount  int                    `json:"success_count"`
	FailureCount  int                    `json:"failure_count"`
	Results       []domain.HostJobResult `json:"results"`
}

func (s *fleetService) ExecuteOnFleet(ctx context.Context, workspaceID int, rule domain.TargetRule, command string) (*domain.FleetExecutionResult, error) {
	// The command text is deliberately not recorded: it may contain secrets.
	ctx, span := otel.Tracer("machinarchy/internal/service").Start(ctx, "fleet.execute",
		trace.WithAttributes(attribute.Int("workspace.id", workspaceID)),
	)
	defer span.End()

	// 1. Fetch all hosts from inventory (or query via optimized store method if available)
	hosts, err := s.repo.ListAllHosts(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("failed to list inventory for fleet execution: %w", err)
	}

	// 2. Resolve the target rule. An empty rule is an error, never "all hosts".
	environmentNames := map[int]string{}
	if s.environments != nil {
		environments, err := s.environments.ListEnvironments(ctx, workspaceID)
		if err != nil {
			return nil, fmt.Errorf("failed to list environments for fleet execution: %w", err)
		}
		for _, environment := range environments {
			environmentNames[environment.ID] = environment.Name
		}
	}
	targets, err := domain.ResolveTargets(rule, hosts, environmentNames)
	if err != nil {
		return nil, err
	}
	if len(targets.UnknownIncludes) > 0 {
		return nil, fmt.Errorf("%w: include entries match no host: %v", domain.ErrInvalidInput, targets.UnknownIncludes)
	}
	targetHosts := targets.Matched

	result := &domain.FleetExecutionResult{
		TotalTargeted: len(targetHosts),
		Results:       make([]domain.HostJobResult, len(targetHosts)),
	}

	if len(targetHosts) == 0 {
		return result, nil
	}

	// 3. Execute concurrently with bounded worker pool / bulkhead
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i, host := range targetHosts {
		wg.Add(1)
		go func(idx int, h domain.Host) {
			defer wg.Done()

			// Enforce per-host operation timeout
			hostCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			// Acquire concurrency slot from SSH bulkhead
			release, acquireErr := s.sshLimit.Acquire(hostCtx)
			if acquireErr != nil {
				mu.Lock()
				result.Results[idx] = domain.HostJobResult{
					Hostname: h.Hostname,
					Status:   "failed",
					Error:    fmt.Sprintf("concurrency limit acquire failed: %v", acquireErr),
				}
				result.FailureCount++
				mu.Unlock()
				return
			}
			defer release()

			// Execute remote operation via SSH transport
			execErr := runRemoteCommand(hostCtx, h, command)

			mu.Lock()
			if execErr != nil {
				result.Results[idx] = domain.HostJobResult{
					Hostname: h.Hostname,
					Status:   "failed",
					Error:    execErr.Error(),
				}
				result.FailureCount++
			} else {
				result.Results[idx] = domain.HostJobResult{
					Hostname: h.Hostname,
					Status:   "success",
				}
				result.SuccessCount++
			}
			mu.Unlock()
		}(i, *host)
	}

	wg.Wait()
	return result, nil
}

func runRemoteCommand(ctx context.Context, h domain.Host, command string) error {
	// Reuses existing dial parameters and sshclient execution logic safely
	// A placeholder must never report success for work that did not happen.
	return ErrExecutionNotImplemented
}
