package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/machinarchy/machinarchy/internal/domain"
)

type targetService struct {
	hosts        domain.HostStore
	environments domain.EnvironmentStore
}

var _ domain.TargetService = (*targetService)(nil)

// NewTargetService creates the service that previews which hosts a rule targets.
func NewTargetService(hosts domain.HostStore, environments domain.EnvironmentStore) (domain.TargetService, error) {
	if hosts == nil || environments == nil {
		return nil, errors.New("target service requires host and environment stores")
	}
	return &targetService{hosts: hosts, environments: environments}, nil
}

func (s *targetService) Preview(ctx context.Context, principal domain.Principal, rule domain.TargetRule) (*domain.TargetPreview, error) {
	if ctx == nil {
		return nil, errors.New("target preview context is required")
	}
	if principal.WorkspaceID <= 0 || !principal.Role.Allows(domain.PermissionInventoryRead) {
		return nil, domain.ErrPermissionDenied
	}
	if err := rule.Validate(); err != nil {
		return nil, err
	}
	hosts, err := s.hosts.ListAllHosts(ctx, principal.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("list inventory for target preview: %w", err)
	}
	environments, err := s.environments.ListEnvironments(ctx, principal.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("list environments for target preview: %w", err)
	}
	names := make(map[int]string, len(environments))
	for _, environment := range environments {
		names[environment.ID] = environment.Name
	}

	set, err := domain.ResolveTargets(rule, hosts, names)
	if err != nil {
		return nil, err
	}
	preview := &domain.TargetPreview{
		Total:           len(set.Matched),
		Hosts:           make([]domain.TargetHost, 0, minInt(len(set.Matched), domain.MaxPreviewHosts)),
		Excluded:        make([]domain.TargetHost, 0, minInt(len(set.Excluded), domain.MaxPreviewHosts)),
		UnknownIncludes: nonNil(set.UnknownIncludes),
		UnknownExcludes: nonNil(set.UnknownExcludes),
	}
	for i, host := range set.Matched {
		if i == domain.MaxPreviewHosts {
			preview.Truncated = true
			break
		}
		preview.Hosts = append(preview.Hosts, targetHost(host, names))
	}
	for i, host := range set.Excluded {
		if i == domain.MaxPreviewHosts {
			break
		}
		preview.Excluded = append(preview.Excluded, targetHost(host, names))
	}
	return preview, nil
}

// targetHost copies only non-secret fields; credentials never enter a preview.
func targetHost(h *domain.Host, environments map[int]string) domain.TargetHost {
	labels := make(map[string]string, len(h.Labels))
	for key, value := range h.Labels {
		labels[key] = value
	}
	view := domain.TargetHost{
		ID: h.ID, Name: h.Name, Hostname: h.Hostname, Address: h.IPAddress,
		Environment: strings.TrimSpace(environments[h.EnvironmentID]), Labels: labels,
	}
	if h.SSH != nil {
		view.Status = h.SSH.Status
	}
	return view
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
