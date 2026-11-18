package hostaccess

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

type ProfileReader interface {
	GetAccessProfile(context.Context, int, resource.ID) (*resource.AccessProfile, error)
	GetAccessProfileRevision(context.Context, int, resource.ID, uint64) (*resource.AccessProfileRevision, error)
}

// HostReconciler resolves the latest profile revision and verifies that the
// managed Host can authenticate to its endpoint. It performs no remote mutation.
type HostReconciler struct {
	profiles      ProfileReader
	connector     HostConnector
	defaultPolicy resource.HostKeyPolicy
}

// Option configures a HostReconciler.
type Option func(*HostReconciler)

// WithDefaultHostKeyPolicy sets the policy for profiles that do not choose
// one (MACHINARCHY_SSH_HOST_KEY_POLICY). Without it, keys need approval.
func WithDefaultHostKeyPolicy(policy resource.HostKeyPolicy) Option {
	return func(r *HostReconciler) { r.defaultPolicy = policy }
}

func NewHostReconciler(profiles ProfileReader, connector HostConnector, options ...Option) (*HostReconciler, error) {
	if profiles == nil || connector == nil {
		return nil, errors.New("managed Host reconciler requires profile storage and a connector")
	}
	r := &HostReconciler{profiles: profiles, connector: connector, defaultPolicy: resource.HostKeyPolicyApprove}
	for _, option := range options {
		option(r)
	}
	return r, nil
}

// ReconcileHost verifies a Host, first establishing trust in its SSH host
// keys according to the profile's policy (AUDIT/10b §2):
//   - no trusted keys yet: scan them before any authentication, then pin
//     them (tofu) or wait for an owner (approve); pinned waits for a spec
//     change and never scans;
//   - trusted keys that the host no longer presents: HostKeyChanged, with no
//     authentication attempt and no automatic replacement.
func (r *HostReconciler) ReconcileHost(ctx context.Context, host *resource.Host) (resource.HostObservation, error) {
	phase := resource.HostActive
	conditions := []resource.Condition{
		{Type: "Ready", Status: resource.ConditionFalse, Reason: "AccessNotVerified"},
		{Type: "Reachable", Status: resource.ConditionFalse, Reason: "ConnectionFailed"},
		{Type: "Configured", Status: resource.ConditionTrue, Reason: "ConfigurationPresent"},
	}
	if host.Spec.AccessProfileID == "" {
		conditions[0].Reason, conditions[1].Status, conditions[1].Reason = "AccessProfileRequired", resource.ConditionUnknown, "AccessProfileRequired"
		conditions[2].Status, conditions[2].Reason = resource.ConditionFalse, "AccessProfileRequired"
		return observation(phase, conditions, nil), nil
	}
	profile, err := r.profiles.GetAccessProfile(ctx, host.Metadata.WorkspaceID, host.Spec.AccessProfileID)
	if err != nil || profile.DeletedAt != nil {
		conditions[0].Reason, conditions[1].Reason, conditions[2].Status, conditions[2].Reason = "AccessProfileUnavailable", "AccessProfileUnavailable", resource.ConditionFalse, "AccessProfileUnavailable"
		return observation(phase, conditions, nil), nil
	}
	revision, err := r.profiles.GetAccessProfileRevision(ctx, host.Metadata.WorkspaceID, profile.ID, profile.CurrentRevision)
	if err != nil || !revision.Spec.Enabled {
		conditions[0].Reason, conditions[1].Status, conditions[1].Reason = "AccessProfileDisabled", resource.ConditionUnknown, "AccessProfileDisabled"
		conditions[2].Status, conditions[2].Reason = resource.ConditionFalse, "AccessProfileDisabled"
		return observation(phase, conditions, nil), nil
	}
	policy := revision.Spec.EffectiveHostKeyPolicy(r.defaultPolicy)
	trusted := len(host.Spec.AccessTarget.Trust) > 0
	scanner, canScan := r.connector.(HostKeyScanner)
	if !trusted && policy != resource.HostKeyPolicyCA {
		if policy == resource.HostKeyPolicyPinned || !canScan {
			conditions[0].Reason, conditions[0].Message = "HostKeyNotTrusted", "no trusted host key; set spec.accessTarget.trust"
			conditions[1].Status, conditions[1].Reason = resource.ConditionUnknown, "HostKeyNotTrusted"
			return observation(phase, conditions, nil), nil
		}
		keys, err := scanner.ScanHostKeys(ctx, host)
		if err != nil {
			conditions[0].Reason, conditions[1].Reason = failureReason(err), failureReason(err)
			return observation(phase, conditions, nil), nil
		}
		conditions[1].Status, conditions[1].Reason = resource.ConditionTrue, "Reachable"
		if policy == resource.HostKeyPolicyTOFU {
			conditions[0].Reason, conditions[0].Message = "HostKeyTrusting", "pinning the host keys seen on first contact"
			result := observation(phase, conditions, keys)
			result.TrustHostKeys = fingerprints(keys)
			return result, nil
		}
		conditions[0].Reason = "HostKeyPendingApproval"
		conditions[0].Message = "an owner must approve the host keys: machinarchy trust host " + host.Metadata.Name +
			" (" + strings.Join(fingerprints(keys), ", ") + ")"
		return observation(phase, conditions, keys), nil
	}
	if err := r.connector.Verify(ctx, host, revision); err != nil {
		conditions[0].Reason = failureReason(err)
		conditions[1].Reason = failureReason(err)
		if errors.Is(err, ErrHostKey) && canScan {
			// Record what the host presents now, so an owner can compare.
			if keys, scanErr := scanner.ScanHostKeys(ctx, host); scanErr == nil {
				conditions[1].Status, conditions[1].Reason = resource.ConditionTrue, "Reachable"
				if trusted {
					conditions[0].Reason = "HostKeyChanged"
					conditions[0].Message = fmt.Sprintf("the host presents %s; trusted: %s. Nothing was sent to it. If the change is expected: machinarchy trust host %s --replace",
						strings.Join(fingerprints(keys), ", "), strings.Join(host.Spec.AccessTarget.Trust, ", "), host.Metadata.Name)
				} else {
					conditions[0].Reason, conditions[0].Message = "HostKeyNotTrusted", "the host certificate is not signed by a trusted authority"
				}
				return observation(phase, conditions, keys), nil
			}
		}
		return observation(phase, conditions, nil), nil
	}
	conditions[0].Status, conditions[0].Reason = resource.ConditionTrue, "SSHVerified"
	conditions[1].Status, conditions[1].Reason = resource.ConditionTrue, "Reachable"
	return observation(phase, conditions, nil), nil
}

func observation(phase resource.HostPhase, conditions []resource.Condition, keys []resource.ObservedHostKey) resource.HostObservation {
	return resource.HostObservation{Phase: phase, Conditions: conditions, ObservedHostKeys: keys}
}

func fingerprints(keys []resource.ObservedHostKey) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key.Fingerprint)
	}
	return out
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
