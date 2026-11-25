package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

// EnvironmentNames resolves environment IDs to names for the "environment"
// selector field.
type EnvironmentNames interface {
	ListEnvironments(ctx context.Context, workspaceID int) ([]*domain.Environment, error)
}

// UseEnvironments enables the "environment" selector field.
func (s *ManagedResourceService) UseEnvironments(environments EnvironmentNames) {
	s.environments = environments
}

// HostQuery filters a Host listing. Selector uses the shared selector syntax
// (see domain.Selector and resource.Host.SelectorField).
type HostQuery struct {
	Selector      string
	EnvironmentID int
	Phase         resource.HostPhase
	After         resource.ID
	Limit         int
}

func (s *ManagedResourceService) ListHosts(ctx context.Context, principal domain.Principal, query HostQuery) (*resource.Page[resource.Host], error) {
	if err := authorizeRead(principal, domain.PermissionInventoryRead); err != nil {
		return nil, err
	}
	limit := resource.PageLimit(query.Limit)
	filter := resource.HostListFilter{WorkspaceID: principal.WorkspaceID, EnvironmentID: query.EnvironmentID,
		Phase: query.Phase, After: query.After, Limit: limit}
	if strings.TrimSpace(query.Selector) == "" {
		hosts, err := s.hosts.ListHosts(ctx, filter)
		if err != nil {
			return nil, err
		}
		page := &resource.Page[resource.Host]{Items: hosts}
		if len(hosts) == limit {
			page.NextCursor = string(hosts[len(hosts)-1].Metadata.ID)
		}
		return page, nil
	}

	selector, err := domain.ParseSelector(query.Selector)
	if err != nil {
		return nil, err
	}
	environments, err := s.environmentNames(ctx, principal.WorkspaceID)
	if err != nil {
		return nil, err
	}
	// Label requirements run in the database (A-12); the selector is still
	// applied to every candidate, which keeps built-in fields and wildcards
	// exact. Without label requirements this reads the inventory in pages
	// until the page is full, bounded by the request's deadline.
	filter.Labels = selector.LabelConstraints(resource.IsHostBuiltinField)
	filter.Limit = resource.MaxPageSize
	page := &resource.Page[resource.Host]{Items: []resource.Host{}}
	for {
		batch, err := s.hosts.ListHosts(ctx, filter)
		if err != nil {
			return nil, err
		}
		for i := range batch {
			filter.After = batch[i].Metadata.ID
			if selector.MatchesFields(batch[i].SelectorField(environments[batch[i].Metadata.EnvironmentID])) {
				page.Items = append(page.Items, batch[i])
				if len(page.Items) == limit {
					page.NextCursor = string(filter.After)
					return page, nil
				}
			}
		}
		if len(batch) < filter.Limit {
			return page, nil // end of inventory
		}
	}
}

func (s *ManagedResourceService) DeleteHost(ctx context.Context, principal domain.Principal, id resource.ID, revision uint64) error {
	if err := authorizeManagedHost(principal); err != nil {
		return err
	}
	return s.hosts.DeleteHost(ctx, principal.WorkspaceID, id, revision)
}

// SetHostLabels replaces a Host's labels. Labels never change where a
// credential is presented, so inventory:write is sufficient.
func (s *ManagedResourceService) SetHostLabels(ctx context.Context, principal domain.Principal, id resource.ID, revision uint64, labels map[string]string) (*resource.Host, error) {
	if err := authorizeManagedHost(principal); err != nil {
		return nil, err
	}
	host, err := s.hosts.GetHost(ctx, principal.WorkspaceID, id)
	if err != nil {
		return nil, err
	}
	if host.Metadata.Revision != revision {
		return nil, resource.ErrConflict
	}
	if err = s.hosts.UpdateHostMetadata(ctx, principal.WorkspaceID, id, revision, host.Metadata.Name,
		host.Metadata.EnvironmentID, labels, time.Now().UTC()); err != nil {
		return nil, err
	}
	return s.hosts.GetHost(ctx, principal.WorkspaceID, id)
}

// AccessProfileView is a profile with its current revision. The spec holds a
// SecretRef (ID and version), never secret material.
type AccessProfileView struct {
	resource.AccessProfile
	Spec resource.AccessProfileSpec `json:"spec"`
}

func (s *ManagedResourceService) GetAccessProfile(ctx context.Context, principal domain.Principal, id resource.ID) (*AccessProfileView, error) {
	if err := authorizeRead(principal, domain.PermissionInventoryRead); err != nil {
		return nil, err
	}
	profile, err := s.profiles.GetAccessProfile(ctx, principal.WorkspaceID, id)
	if err != nil {
		return nil, err
	}
	if profile.DeletedAt != nil {
		return nil, resource.ErrAccessProfileNotFound
	}
	revision, err := s.profiles.GetAccessProfileRevision(ctx, principal.WorkspaceID, id, profile.CurrentRevision)
	if err != nil {
		return nil, err
	}
	return &AccessProfileView{AccessProfile: *profile, Spec: revision.Spec}, nil
}

func (s *ManagedResourceService) ListAccessProfiles(ctx context.Context, principal domain.Principal, after resource.ID, limit int) (*resource.Page[resource.AccessProfile], error) {
	if err := authorizeRead(principal, domain.PermissionInventoryRead); err != nil {
		return nil, err
	}
	limit = resource.PageLimit(limit)
	profiles, err := s.profiles.ListAccessProfiles(ctx, principal.WorkspaceID, after, limit)
	if err != nil {
		return nil, err
	}
	page := &resource.Page[resource.AccessProfile]{Items: profiles}
	if len(profiles) == limit {
		page.NextCursor = string(profiles[len(profiles)-1].ID)
	}
	return page, nil
}

// DeleteAccessProfile tombstones an unused profile. It fails with ErrConflict
// while any Host is bound to it.
func (s *ManagedResourceService) DeleteAccessProfile(ctx context.Context, principal domain.Principal, id resource.ID) error {
	if err := authorizeProfileOwner(principal); err != nil {
		return err
	}
	return s.profiles.DeleteAccessProfile(ctx, principal.WorkspaceID, id, principal.UserID, time.Now().UTC())
}

func (s *ManagedResourceService) ListOperations(ctx context.Context, principal domain.Principal, filter resource.OperationListFilter) (*resource.Page[resource.OperationSummary], error) {
	if err := authorizeRead(principal, domain.PermissionOperationsRead); err != nil {
		return nil, err
	}
	filter.WorkspaceID = principal.WorkspaceID
	filter.Limit = resource.PageLimit(filter.Limit)
	summaries, err := s.operations.ListOperations(ctx, filter)
	if err != nil {
		return nil, err
	}
	page := &resource.Page[resource.OperationSummary]{Items: summaries}
	if len(summaries) == filter.Limit {
		page.NextCursor = summaries[len(summaries)-1].Cursor
	}
	return page, nil
}

// PreviewTargets resolves rule against the workspace's managed Hosts without
// doing anything to them.
func (s *ManagedResourceService) PreviewTargets(ctx context.Context, principal domain.Principal, rule domain.TargetRule) (*resource.HostTargetSet, error) {
	if err := authorizeRead(principal, domain.PermissionInventoryRead); err != nil {
		return nil, err
	}
	return s.resolveTargets(ctx, principal.WorkspaceID, rule)
}

func (s *ManagedResourceService) resolveTargets(ctx context.Context, workspaceID int, rule domain.TargetRule) (*resource.HostTargetSet, error) {
	if err := rule.Validate(); err != nil {
		return nil, err
	}
	selector, err := rule.ParsedSelector()
	if err != nil {
		return nil, err
	}
	// Load only the candidates: every Host for an explicit "all", Hosts that
	// pass the selector's label constraints (A-12), and the Hosts named in
	// include and exclude. ResolveHostTargets then applies the rule exactly.
	environments, err := s.environmentNames(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	seen := map[resource.ID]bool{}
	hosts := make([]resource.Host, 0)
	// collect adds the Hosts of filter for which keep is true (nil: all).
	collect := func(filter resource.HostListFilter, keep func(*resource.Host) bool) error {
		filter.WorkspaceID, filter.Limit = workspaceID, resource.MaxPageSize
		for {
			batch, err := s.hosts.ListHosts(ctx, filter)
			if err != nil {
				return err
			}
			for i := range batch {
				if keep != nil && !keep(&batch[i]) {
					continue
				}
				if !seen[batch[i].Metadata.ID] {
					seen[batch[i].Metadata.ID] = true
					hosts = append(hosts, batch[i])
				}
			}
			if len(hosts) > resource.MaxTargetHosts {
				return fmt.Errorf("%w: the selection covers more than %d hosts; narrow it", domain.ErrInvalidInput, resource.MaxTargetHosts)
			}
			if len(batch) < filter.Limit {
				return nil
			}
			filter.After = batch[len(batch)-1].Metadata.ID
		}
	}
	switch {
	case rule.All:
		err = collect(resource.HostListFilter{}, nil)
	case selector != nil:
		err = collect(resource.HostListFilter{Labels: selector.LabelConstraints(resource.IsHostBuiltinField)}, func(host *resource.Host) bool {
			return selector.MatchesFields(host.SelectorField(environments[host.Metadata.EnvironmentID]))
		})
	}
	if err != nil {
		return nil, err
	}
	if named := append(append([]string(nil), rule.Include...), rule.Exclude...); len(named) > 0 && !rule.All {
		if err := collect(resource.HostListFilter{Lookup: named}, nil); err != nil {
			return nil, err
		}
	}
	return resource.ResolveHostTargets(rule, hosts, environments)
}

func (s *ManagedResourceService) environmentNames(ctx context.Context, workspaceID int) (map[int]string, error) {
	names := map[int]string{}
	if s.environments == nil {
		return names, nil
	}
	environments, err := s.environments.ListEnvironments(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	for _, environment := range environments {
		names[environment.ID] = environment.Name
	}
	return names, nil
}

func authorizeRead(principal domain.Principal, permission domain.Permission) error {
	if principal.WorkspaceID <= 0 || !principal.Role.Allows(permission) {
		return domain.ErrPermissionDenied
	}
	return nil
}
