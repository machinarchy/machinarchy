package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/pkg/sshclient"
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
	environments EnvironmentNames
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
	// Binding a host to an Access Profile decides where an owner-provisioned
	// credential is sent, so it needs the same authority as the profile itself.
	if spec.AccessProfileID != "" {
		if err := authorizeProfileOwner(principal); err != nil {
			return nil, err
		}
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
	if spec.AccessProfileID != "" {
		current, err := s.hosts.GetHost(ctx, principal.WorkspaceID, id)
		if err != nil {
			return err
		}
		if current.Metadata.Revision != revision {
			return resource.ErrConflict // the store would reject it too; never authorize against a stale spec
		}
		if credentialBindingChanged(current.Spec, spec) {
			if err := authorizeProfileOwner(principal); err != nil {
				return err
			}
		}
	}
	return s.hosts.UpdateHostSpec(ctx, principal.WorkspaceID, id, revision, generation, spec, time.Now().UTC())
}

// credentialBindingChanged reports whether next changes where the bound
// profile's credential would be presented: the profile or its pinned
// revision, the endpoint, or the trusted host keys. An operator who could
// change these could point an owner's SSH credential at a server they control
// and capture it (AUDIT S-02).
func credentialBindingChanged(current, next resource.HostSpec) bool {
	if current.AccessProfileID != next.AccessProfileID || current.ProfileRevision != next.ProfileRevision ||
		current.AccessTarget.Endpoint != next.AccessTarget.Endpoint || len(current.AccessTarget.Trust) != len(next.AccessTarget.Trust) {
		return true
	}
	for i := range current.AccessTarget.Trust {
		if current.AccessTarget.Trust[i] != next.AccessTarget.Trust[i] {
			return true
		}
	}
	return false
}

func (s *ManagedResourceService) CreateAccessProfile(ctx context.Context, principal domain.Principal, name, description string, spec resource.AccessProfileSpec) (*resource.AccessProfile, error) {
	if err := authorizeProfileOwner(principal); err != nil {
		return nil, err
	}
	if err := s.validateSecretReference(ctx, principal, spec); err != nil {
		return nil, err
	}
	if _, err := sshclient.ParseHostCertificateAuthorities(spec.HostCertificateAuthorities); err != nil {
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
	if _, err := sshclient.ParseHostCertificateAuthorities(spec.HostCertificateAuthorities); err != nil {
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

// SubmitOperationForRule resolves rule (selector, include/exclude lists or
// all) to managed Hosts and submits the Operation on them. When
// expectTargetsHash is set it must equal the hash of the resolved set, so the
// Operation runs exactly on the Hosts the caller previewed.
func (s *ManagedResourceService) SubmitOperationForRule(ctx context.Context, principal domain.Principal, rule domain.TargetRule,
	expectTargetsHash string, run resource.RunSpec) (*resource.Operation, error) {
	if principal.UserID <= 0 || principal.WorkspaceID <= 0 || !principal.Role.Allows(domain.PermissionRunsExecute) {
		return nil, domain.ErrPermissionDenied
	}
	set, err := s.resolveTargets(ctx, principal.WorkspaceID, rule)
	if err != nil {
		return nil, err
	}
	if expectTargetsHash != "" && expectTargetsHash != set.TargetsHash {
		return nil, resource.ErrTargetsChanged
	}
	if len(set.Matched) == 0 {
		return nil, fmt.Errorf("%w: the target rule matches no hosts", domain.ErrInvalidInput)
	}
	ids := make([]resource.ID, len(set.Matched))
	for i, host := range set.Matched {
		ids[i] = host.Metadata.ID
	}
	return s.SubmitOperation(ctx, principal, ids, run)
}

func (s *ManagedResourceService) GetOperation(ctx context.Context, principal domain.Principal, id resource.ID) (*resource.Operation, error) {
	if err := authorizeRead(principal, domain.PermissionOperationsRead); err != nil {
		return nil, err
	}
	op, err := s.operations.GetOperation(ctx, principal.WorkspaceID, id)
	if err != nil {
		return nil, err
	}
	if !principal.Role.Allows(domain.PermissionRunsExecute) {
		return redactOperation(op), nil
	}
	return op, nil
}

// redactOperation removes command arguments and captured output, which can
// carry secrets, for principals who may only read operations. This matches
// the legacy runs API, which shows output only to callers allowed to execute
// (AUDIT S-04). State, timing, exit codes and error classes stay visible.
func redactOperation(op *resource.Operation) *resource.Operation {
	redacted := *op
	if op.Spec.Run != nil {
		run := *op.Spec.Run
		run.Argv = nil
		redacted.Spec.Run = &run
	}
	redacted.Status.Targets = make([]resource.OperationTarget, len(op.Status.Targets))
	for i, target := range op.Status.Targets {
		if target.Result != nil {
			result := *target.Result
			result.Output, result.Stdout, result.Stderr = "", "", ""
			target.Result = &result
		}
		redacted.Status.Targets[i] = target
	}
	return &redacted
}

func (s *ManagedResourceService) GetHost(ctx context.Context, principal domain.Principal, id resource.ID) (*resource.Host, error) {
	if err := authorizeRead(principal, domain.PermissionInventoryRead); err != nil {
		return nil, err
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

// TrustHostKeys pins SSH host key fingerprints for a Host (AUDIT/10b §2).
// Trusting keys decides where an owner's credential may be presented, so it
// is owner-only, like binding a credential (AUDIT S-02).
//
// The caller must name the exact fingerprints it reviewed: approving
// "whatever is observed now" would trust keys that changed between review
// and approval. expectedGeneration is the Host spec generation the caller
// saw; status updates from the controller do not change it, so approval does
// not race them. A Host that already trusts keys needs replace, so a changed
// key is never accepted by accident.
func (s *ManagedResourceService) TrustHostKeys(ctx context.Context, principal domain.Principal, id resource.ID, expectedGeneration uint64,
	fingerprints []string, replace bool) (*resource.Host, error) {
	if err := authorizeProfileOwner(principal); err != nil {
		return nil, err
	}
	if len(fingerprints) == 0 {
		return nil, fmt.Errorf("%w: name the host key fingerprints to trust", domain.ErrInvalidInput)
	}
	for attempt := 0; attempt < 5; attempt++ {
		host, err := s.hosts.GetHost(ctx, principal.WorkspaceID, id)
		if err != nil {
			return nil, err
		}
		if host.Metadata.Generation != expectedGeneration {
			return nil, resource.ErrConflict
		}
		err = s.hosts.TrustHostKeys(ctx, principal.WorkspaceID, id, host.Metadata.Revision, fingerprints, replace, "owner", time.Now().UTC())
		if errors.Is(err, resource.ErrConflict) {
			continue // a status write landed in between; the generation check above decides
		}
		if err != nil {
			return nil, err
		}
		return s.hosts.GetHost(ctx, principal.WorkspaceID, id)
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
