package service

import (
	"context"
	"slices"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

// guardEnvironments refuses the call when it touches a protected environment
// whose exact name is not among the names the caller confirmed
// (domain.ConfirmedEnvironments). It holds for every client: the check lives
// here, not in the CLI. IDs of 0 (no environment) are ignored. Without an
// environment source (tests) nothing is protected.
func (s *ManagedResourceService) guardEnvironments(ctx context.Context, workspaceID int, ids ...int) error {
	if s.environments == nil {
		return nil
	}
	touched := map[int]bool{}
	for _, id := range ids {
		if id > 0 {
			touched[id] = true
		}
	}
	if len(touched) == 0 {
		return nil
	}
	list, err := s.environments.ListEnvironments(ctx, workspaceID)
	if err != nil {
		return err
	}
	confirmed := domain.ConfirmedEnvironments(ctx)
	var missing []string
	for _, environment := range list {
		if touched[environment.ID] && environment.Protected && !slices.Contains(confirmed, environment.Name) {
			missing = append(missing, environment.Name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return &domain.ProtectedEnvironmentError{Names: missing}
	}
	return nil
}

// guardHost guards the environment host id is in, plus extra (a move target).
func (s *ManagedResourceService) guardHost(ctx context.Context, workspaceID int, id resource.ID, extra ...int) error {
	if s.environments == nil {
		return nil
	}
	host, err := s.hosts.GetHost(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	return s.guardEnvironments(ctx, workspaceID, append(extra, host.Metadata.EnvironmentID)...)
}
