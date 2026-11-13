package hostaccess

import (
	"context"
	"errors"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

type ProfileReader interface {
	GetAccessProfile(context.Context, int, resource.ID) (*resource.AccessProfile, error)
	GetAccessProfileRevision(context.Context, int, resource.ID, uint64) (*resource.AccessProfileRevision, error)
}

// HostReconciler resolves the latest profile revision and verifies that the
// managed Host can authenticate to its endpoint. It performs no remote mutation.
type HostReconciler struct {
	profiles  ProfileReader
	connector HostConnector
}

func NewHostReconciler(profiles ProfileReader, connector HostConnector) (*HostReconciler, error) {
	if profiles == nil || connector == nil {
		return nil, errors.New("managed Host reconciler requires profile storage and a connector")
	}
	return &HostReconciler{profiles: profiles, connector: connector}, nil
}

func (r *HostReconciler) ReconcileHost(ctx context.Context, host *resource.Host) (resource.HostPhase, []resource.Condition, error) {
	phase := resource.HostActive
	conditions := []resource.Condition{
		{Type: "Ready", Status: resource.ConditionFalse, Reason: "AccessNotVerified"},
		{Type: "Reachable", Status: resource.ConditionFalse, Reason: "ConnectionFailed"},
		{Type: "Configured", Status: resource.ConditionTrue, Reason: "ConfigurationPresent"},
	}
	if host.Spec.AccessProfileID == "" {
		conditions[0].Reason, conditions[1].Status, conditions[1].Reason = "AccessProfileRequired", resource.ConditionUnknown, "AccessProfileRequired"
		conditions[2].Status, conditions[2].Reason = resource.ConditionFalse, "AccessProfileRequired"
		return phase, conditions, nil
	}
	profile, err := r.profiles.GetAccessProfile(ctx, host.Metadata.WorkspaceID, host.Spec.AccessProfileID)
	if err != nil || profile.DeletedAt != nil {
		conditions[0].Reason, conditions[1].Reason, conditions[2].Status, conditions[2].Reason = "AccessProfileUnavailable", "AccessProfileUnavailable", resource.ConditionFalse, "AccessProfileUnavailable"
		return phase, conditions, nil
	}
	revision, err := r.profiles.GetAccessProfileRevision(ctx, host.Metadata.WorkspaceID, profile.ID, profile.CurrentRevision)
	if err != nil || !revision.Spec.Enabled {
		conditions[0].Reason, conditions[1].Status, conditions[1].Reason = "AccessProfileDisabled", resource.ConditionUnknown, "AccessProfileDisabled"
		conditions[2].Status, conditions[2].Reason = resource.ConditionFalse, "AccessProfileDisabled"
		return phase, conditions, nil
	}
	if err := r.connector.Verify(ctx, host, revision); err != nil {
		conditions[0].Reason = failureReason(err)
		conditions[1].Reason = failureReason(err)
		return phase, conditions, nil
	}
	conditions[0].Status, conditions[0].Reason = resource.ConditionTrue, "SSHVerified"
	conditions[1].Status, conditions[1].Reason = resource.ConditionTrue, "Reachable"
	return phase, conditions, nil
}

func failureReason(err error) string {
	switch {
	case errors.Is(err, ErrUnavailable):
		return "AccessUnavailable"
	case errors.Is(err, ErrCapacity):
		return "ConnectorAtCapacity"
	case errors.Is(err, ErrConnection):
		return "ConnectionFailed"
	case errors.Is(err, ErrHostKey):
		return "HostKeyRejected"
	case errors.Is(err, ErrAuth):
		return "AuthenticationFailed"
	case errors.Is(err, ErrTimeout):
		return "ConnectionTimedOut"
	case errors.Is(err, ErrRefused):
		return "ConnectionRefused"
	default:
		return "ConnectionFailed"
	}
}
