package secrets

import (
	"context"
	"errors"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

type ProfileBindingReader interface {
	GetAccessProfile(context.Context, int, resource.ID) (*resource.AccessProfile, error)
	GetAccessProfileRevision(context.Context, int, resource.ID, uint64) (*resource.AccessProfileRevision, error)
	GetHost(context.Context, int, resource.ID) (*resource.Host, error)
	GetOperation(context.Context, int, resource.ID) (*resource.Operation, error)
}

// ProfileAuthorizer is bound to a trusted workload identity supplied at
// runtime composition. Caller-supplied IDs are matched against persisted rows.
type ProfileAuthorizer struct {
	reader   ProfileBindingReader
	workload WorkloadID
}

func NewProfileAuthorizer(reader ProfileBindingReader, workload WorkloadID) (*ProfileAuthorizer, error) {
	if reader == nil || (workload != WorkloadControlManager && workload != WorkloadOperationWorker) {
		return nil, errors.New("profile authorizer requires a binding reader and supported workload identity")
	}
	return &ProfileAuthorizer{reader: reader, workload: workload}, nil
}

func (a *ProfileAuthorizer) AuthorizeSecretResolution(ctx context.Context, req ResolutionRequest) error {
	if a == nil || ctx == nil || req.WorkloadID != a.workload || req.WorkspaceID <= 0 || req.Purpose != PurposeSSHCredential {
		return ErrUnauthorized
	}
	profileID, err := resource.ParseResourceID(req.AccessProfileID)
	if err != nil {
		return ErrUnauthorized
	}
	hostID, err := resource.ParseResourceID(req.HostID)
	if err != nil {
		return ErrUnauthorized
	}
	profile, err := a.reader.GetAccessProfile(ctx, req.WorkspaceID, profileID)
	if err != nil || profile.DeletedAt != nil {
		return ErrUnauthorized
	}
	revision, err := a.reader.GetAccessProfileRevision(ctx, req.WorkspaceID, profileID, req.AccessProfileRevision)
	if err != nil || !revision.Spec.Enabled || revision.Spec.Credential.ID != req.Ref.ID || revision.Spec.Credential.Version != req.Ref.Version {
		return ErrUnauthorized
	}
	host, err := a.reader.GetHost(ctx, req.WorkspaceID, hostID)
	if err != nil || host.Spec.Presence != resource.PresenceManaged || host.Spec.AccessProfileID != profileID {
		return ErrUnauthorized
	}
	if req.WorkloadID == WorkloadControlManager {
		if req.OperationID != "" || profile.CurrentRevision != req.AccessProfileRevision ||
			(host.Spec.ProfileRevision != 0 && host.Spec.ProfileRevision != req.AccessProfileRevision) {
			return ErrUnauthorized
		}
		return nil
	}
	operationID, err := resource.ParseResourceID(req.OperationID)
	if err != nil {
		return ErrUnauthorized
	}
	currentRevision, err := a.reader.GetAccessProfileRevision(ctx, req.WorkspaceID, profileID, profile.CurrentRevision)
	if err != nil || !currentRevision.Spec.Enabled {
		return ErrUnauthorized
	}
	matches := func(target resource.OperationTargetSpec) bool {
		return target.HostID == hostID && target.HostGeneration == host.Metadata.Generation &&
			target.AccessProfileID == profileID && target.AccessProfileRevision == req.AccessProfileRevision
	}
	// Detached polls resolve a credential per SSH session: read the one
	// target row instead of the whole Operation when the reader can.
	if targets, ok := a.reader.(resource.OperationTargetReader); ok {
		state, target, err := targets.GetOperationTarget(ctx, req.WorkspaceID, operationID, hostID)
		if err != nil || (&resource.Operation{Status: resource.OperationStatus{State: state}}).IsTerminal() || !matches(target) {
			return ErrUnauthorized
		}
		return nil
	}
	operation, err := a.reader.GetOperation(ctx, req.WorkspaceID, operationID)
	if err != nil || operation.IsTerminal() {
		return ErrUnauthorized
	}
	for _, target := range operation.Spec.Targets {
		if matches(target) {
			return nil
		}
	}
	return ErrUnauthorized
}
