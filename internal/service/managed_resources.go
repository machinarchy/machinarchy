package service

import (
	"context"
	"errors"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/secrets"
)

var ErrAccessProfileOwnerRequired = errors.New("workspace owner permission is required for Access Profile management")

// ManagedResourceService contains the authenticated application use cases for
// canonical managed Hosts, Access Profiles, and Operations.
type ManagedResourceService struct {
	hosts        resource.HostRepository
	profiles     resource.AccessProfileRepository
	operations   resource.OperationRepository
	policy       func() domain.ExecutionPolicy
	secretReader SecretReferenceReader
}

type SecretReferenceReader interface {
	Describe(context.Context, domain.Principal, string) (secrets.Metadata, error)
	Versions(context.Context, domain.Principal, string) ([]secrets.Version, error)
}

func NewManagedResourceService(hosts resource.HostRepository, profiles resource.AccessProfileRepository, operations resource.OperationRepository,
	policy func() domain.ExecutionPolicy, secretReaders ...SecretReferenceReader) (*ManagedResourceService, error) {
	if hosts == nil || profiles == nil || operations == nil || policy == nil {
		return nil, errors.New("managed resource service requires repositories and execution policy")
	}
	service := &ManagedResourceService{hosts: hosts, profiles: profiles, operations: operations, policy: policy}
	if len(secretReaders) > 0 {
		service.secretReader = secretReaders[0]
	}
	return service, nil
}

func (s *ManagedResourceService) CreateHost(ctx context.Context, principal domain.Principal, name string, labels map[string]string, spec resource.HostSpec) (*resource.Host, error) {
	if err := authorizeManagedHost(principal); err != nil {
		return nil, err
	}
	host, err := resource.NewHost(principal.WorkspaceID, 0, name, labels, spec, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if err = s.hosts.CreateHost(ctx, host); err != nil {
		return nil, err
	}
	return host, nil
}

func (s *ManagedResourceService) UpdateHost(ctx context.Context, principal domain.Principal, id resource.ID, revision, generation uint64, spec resource.HostSpec) error {
	if err := authorizeManagedHost(principal); err != nil {
		return err
	}
	return s.hosts.UpdateHostSpec(ctx, principal.WorkspaceID, id, revision, generation, spec, time.Now().UTC())
}

func (s *ManagedResourceService) CreateAccessProfile(ctx context.Context, principal domain.Principal, name, description string, spec resource.AccessProfileSpec) (*resource.AccessProfile, error) {
	if err := authorizeProfileOwner(principal); err != nil {
		return nil, err
	}
	if err := s.validateSecretReference(ctx, principal, spec); err != nil {
		return nil, err
	}
	profile, revision, err := resource.NewAccessProfile(principal.WorkspaceID, name, description, spec, principal.UserID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if err = s.profiles.CreateAccessProfile(ctx, profile, revision); err != nil {
		return nil, err
	}
	return profile, nil
}

func (s *ManagedResourceService) UpdateAccessProfile(ctx context.Context, principal domain.Principal, id resource.ID, expected uint64, spec resource.AccessProfileSpec) (*resource.AccessProfileRevision, error) {
	if err := authorizeProfileOwner(principal); err != nil {
		return nil, err
	}
	if err := s.validateSecretReference(ctx, principal, spec); err != nil {
		return nil, err
	}
	return s.profiles.UpdateAccessProfile(ctx, principal.WorkspaceID, id, expected, spec, principal.UserID, time.Now().UTC())
}

func (s *ManagedResourceService) SubmitOperation(ctx context.Context, principal domain.Principal, hostIDs []resource.ID, run resource.RunSpec) (*resource.Operation, error) {
	if principal.UserID <= 0 || principal.WorkspaceID <= 0 || !principal.Role.Allows(domain.PermissionRunsExecute) {
		return nil, domain.ErrPermissionDenied
	}
	if err := domain.ValidateArgv(run.Argv); err != nil {
		return nil, err
	}
	if err := s.policy().Check(run.Argv[0]); err != nil {
		return nil, err
	}
	targets := make([]resource.OperationTargetSpec, 0, len(hostIDs))
	for _, id := range hostIDs {
		host, err := s.hosts.GetHost(ctx, principal.WorkspaceID, id)
		if err != nil {
			return nil, err
		}
		if host.Spec.Presence != resource.PresenceManaged || host.Spec.AccessProfileID == "" {
			return nil, resource.ErrInvalidResource
		}
		profile, err := s.profiles.GetAccessProfile(ctx, principal.WorkspaceID, host.Spec.AccessProfileID)
		if err != nil || profile.DeletedAt != nil {
			return nil, resource.ErrAccessProfileNotFound
		}
		revision, err := s.profiles.GetAccessProfileRevision(ctx, principal.WorkspaceID, profile.ID, profile.CurrentRevision)
		if err != nil || !revision.Spec.Enabled {
			return nil, resource.ErrInvalidResource
		}
		targets = append(targets, resource.OperationTargetSpec{HostID: host.Metadata.ID, HostGeneration: host.Metadata.Generation, DisplayName: host.Metadata.Name,
			AccessProfileID: profile.ID, AccessProfileRevision: profile.CurrentRevision})
	}
	operation, err := resource.NewRunOperation(principal.WorkspaceID, resource.ActorRef{UserID: principal.UserID, APITokenID: principal.APITokenID},
		resource.OperationSpec{Targets: targets, Run: &run}, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if err = s.operations.CreateOperation(ctx, operation); err != nil {
		return nil, err
	}
	return operation, nil
}

func (s *ManagedResourceService) GetOperation(ctx context.Context, principal domain.Principal, id resource.ID) (*resource.Operation, error) {
	if principal.WorkspaceID <= 0 || !principal.Role.Allows(domain.PermissionOperationsRead) {
		return nil, domain.ErrPermissionDenied
	}
	return s.operations.GetOperation(ctx, principal.WorkspaceID, id)
}

func (s *ManagedResourceService) GetHost(ctx context.Context, principal domain.Principal, id resource.ID) (*resource.Host, error) {
	if principal.WorkspaceID <= 0 || !principal.Role.Allows(domain.PermissionInventoryRead) {
		return nil, domain.ErrPermissionDenied
	}
	return s.hosts.GetHost(ctx, principal.WorkspaceID, id)
}

func (s *ManagedResourceService) CancelOperation(ctx context.Context, principal domain.Principal, id resource.ID) (*resource.Operation, error) {
	if principal.UserID <= 0 || principal.WorkspaceID <= 0 || !principal.Role.Allows(domain.PermissionRunsExecute) {
		return nil, domain.ErrPermissionDenied
	}
	for attempt := 0; attempt < 8; attempt++ {
		op, err := s.operations.GetOperation(ctx, principal.WorkspaceID, id)
		if err != nil {
			return nil, err
		}
		if op.Status.CancelAsked {
			return op, nil
		}
		expected := op.Revision
		if err = op.RequestCancel(time.Now().UTC()); err != nil {
			return nil, err
		}
		if err = s.operations.UpdateOperation(ctx, op, expected); err == nil {
			return op, nil
		}
		if !errors.Is(err, resource.ErrConflict) {
			return nil, err
		}
	}
	return nil, resource.ErrConflict
}

func authorizeManagedHost(principal domain.Principal) error {
	if principal.UserID <= 0 || principal.WorkspaceID <= 0 || !principal.Role.Allows(domain.PermissionInventoryWrite) {
		return domain.ErrPermissionDenied
	}
	return nil
}
func authorizeProfileOwner(principal domain.Principal) error {
	if principal.UserID <= 0 || principal.WorkspaceID <= 0 || principal.Role != domain.RoleOwner {
		return ErrAccessProfileOwnerRequired
	}
	return nil
}

func (s *ManagedResourceService) validateSecretReference(ctx context.Context, principal domain.Principal, spec resource.AccessProfileSpec) error {
	if s.secretReader == nil {
		return errors.New("access profile secret validation is unavailable")
	}
	metadata, err := s.secretReader.Describe(ctx, principal, spec.Credential.ID)
	if err != nil || metadata.Purpose != secrets.PurposeSSHCredential || metadata.RetiredAt != nil {
		return errors.New("access profile SecretRef is unavailable")
	}
	versions, err := s.secretReader.Versions(ctx, principal, spec.Credential.ID)
	if err != nil {
		return errors.New("access profile SecretRef is unavailable")
	}
	for _, version := range versions {
		if version.Ref.Version == spec.Credential.Version && version.RetiredAt == nil {
			return nil
		}
	}
	return errors.New("access profile SecretRef version is unavailable")
}
