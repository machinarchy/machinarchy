package hostaccess

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

type ProfileReader interface {
	GetAccessProfile(context.Context, int, resource.ID) (*resource.AccessProfile, error)
	GetAccessProfileRevision(context.Context, int, resource.ID, uint64) (*resource.AccessProfileRevision, error)
}

// HostReconciler resolves the latest profile revision and verifies that the
// managed Host can authenticate to its endpoint. It performs no remote mutation.
type HostReconciler struct {
	profiles       ProfileReader
	connector      HostConnector
	defaultPolicy  resource.HostKeyPolicy
	rescanInterval time.Duration
	now            func() time.Time
	// agents reports the agent bound to an agent-connector Host; nil when
	// the agent gateway is off.
	agents AgentBindings
}

// AgentBindings finds the agent bound to a managed host (ADR-057).
type AgentBindings interface {
	HostAgent(ctx context.Context, workspaceID int, hostID string) (*agentgateway.Agent, error)
}

// WithAgentBindings lets agent-connector Hosts report whether their agent
// is enrolled and connected.
func WithAgentBindings(bindings AgentBindings) Option {
	return func(r *HostReconciler) { r.agents = bindings }
}

// DefaultHostKeyRescanInterval is how long a host-key scan is reused. Every
// scan connection ends before authentication, and OpenSSH 9.8+
// (PerSourcePenalties "noauth") blocks addresses that make many of those, so
// a Host waiting for approval must not be rescanned on every retry.
const DefaultHostKeyRescanInterval = 10 * time.Minute

// Option configures a HostReconciler.
type Option func(*HostReconciler)

// WithDefaultHostKeyPolicy sets the policy for profiles that do not choose
// one (MACHINARCHY_SSH_HOST_KEY_POLICY). Without it, keys need approval.
func WithDefaultHostKeyPolicy(policy resource.HostKeyPolicy) Option {
	return func(r *HostReconciler) { r.defaultPolicy = policy }
}

// WithHostKeyRescanInterval overrides DefaultHostKeyRescanInterval.
func WithHostKeyRescanInterval(interval time.Duration) Option {
	return func(r *HostReconciler) { r.rescanInterval = interval }
}

func NewHostReconciler(profiles ProfileReader, connector HostConnector, options ...Option) (*HostReconciler, error) {
	if profiles == nil || connector == nil {
		return nil, errors.New("managed Host reconciler requires profile storage and a connector")
	}
	r := &HostReconciler{profiles: profiles, connector: connector, defaultPolicy: resource.HostKeyPolicyApprove,
		rescanInterval: DefaultHostKeyRescanInterval, now: time.Now}
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
	if revision.Spec.Connector == resource.ConnectorAgent {
		return r.reconcileAgentHost(ctx, host, phase, conditions), nil
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
		keys, scanned, err := r.scan(ctx, scanner, host)
		if err != nil {
			conditions[0].Reason, conditions[1].Reason = failureReason(err), failureReason(err)
			conditions[0].Message = failureMessage(err)
			return observation(phase, conditions, nil), nil
		}
		conditions[1].Status, conditions[1].Reason = resource.ConditionTrue, "Reachable"
		if policy == resource.HostKeyPolicyTOFU {
			conditions[0].Reason, conditions[0].Message = "HostKeyTrusting", "pinning the host keys seen on first contact"
			result := observation(phase, conditions, scanned)
			result.TrustHostKeys = fingerprints(keys)
			return result, nil
		}
		conditions[0].Reason = "HostKeyPendingApproval"
		conditions[0].Message = "an owner must approve the host keys: machinarchy hosts trust " + host.Metadata.Name +
			" (" + strings.Join(fingerprints(keys), ", ") + ")"
		return observation(phase, conditions, scanned), nil
	}
	if err := r.connector.Verify(ctx, host, revision); err != nil {
		conditions[0].Reason = failureReason(err)
		conditions[1].Reason = failureReason(err)
		conditions[0].Message = failureMessage(err)
		if errors.Is(err, ErrHostKey) && canScan {
			// Record what the host presents now, so an owner can compare.
			if keys, scanned, scanErr := r.scan(ctx, scanner, host); scanErr == nil {
				conditions[1].Status, conditions[1].Reason = resource.ConditionTrue, "Reachable"
				if trusted {
					conditions[0].Reason = "HostKeyChanged"
					conditions[0].Message = fmt.Sprintf("the host presents %s; trusted: %s. Nothing was sent to it. If the change is expected: machinarchy hosts trust %s --replace",
						strings.Join(fingerprints(keys), ", "), strings.Join(host.Spec.AccessTarget.Trust, ", "), host.Metadata.Name)
				} else {
					conditions[0].Reason, conditions[0].Message = "HostKeyNotTrusted", "the host certificate is not signed by a trusted authority"
				}
				return observation(phase, conditions, scanned), nil
			}
		}
		return observation(phase, conditions, nil), nil
	}
	conditions[0].Status, conditions[0].Reason = resource.ConditionTrue, "SSHVerified"
	conditions[1].Status, conditions[1].Reason = resource.ConditionTrue, "Reachable"
	return observation(phase, conditions, nil), nil
}

// reconcileAgentHost: a Host reached through its agent is Ready while the
// agent bound to it holds a live lease. Nothing is sent to the host.
func (r *HostReconciler) reconcileAgentHost(ctx context.Context, host *resource.Host, phase resource.HostPhase, conditions []resource.Condition) resource.HostObservation {
	if r.agents == nil {
		conditions[0].Reason, conditions[0].Message = "AgentGatewayDisabled", "this controller does not run the agent gateway"
		conditions[1].Status, conditions[1].Reason = resource.ConditionUnknown, "AgentGatewayDisabled"
		return observation(phase, conditions, nil)
	}
	agent, err := r.agents.HostAgent(ctx, host.Metadata.WorkspaceID, string(host.Metadata.ID))
	switch {
	case errors.Is(err, agentgateway.ErrHostNotBound):
		conditions[0].Reason = "AgentNotEnrolled"
		conditions[0].Message = "no agent is bound to this host; issue an enrolment token with hostId " + string(host.Metadata.ID)
		conditions[1].Status, conditions[1].Reason = resource.ConditionUnknown, "AgentNotEnrolled"
	case err != nil:
		conditions[0].Reason, conditions[1].Reason = "AgentBindingUnavailable", "AgentBindingUnavailable"
	case !agent.Connected(r.now()):
		conditions[0].Reason, conditions[0].Message = "AgentDisconnected", "agent "+agent.ID+" has no live lease"
		conditions[1].Reason = "AgentDisconnected"
	default:
		conditions[0].Status, conditions[0].Reason, conditions[0].Message = resource.ConditionTrue, "AgentConnected", "agent "+agent.ID
		conditions[1].Status, conditions[1].Reason = resource.ConditionTrue, "AgentConnected"
	}
	return observation(phase, conditions, nil)
}

// scan returns the host's keys, reusing the last scan while it is recent,
// was made for the current generation (an endpoint change bumps it) and
// shows none of the trusted keys (a scan that still shows them predates a
// key change). scanned is non-nil only for a new scan, so a reused result
// does not refresh LastSeenAt and the reuse window cannot extend itself.
func (r *HostReconciler) scan(ctx context.Context, scanner HostKeyScanner, host *resource.Host) (keys, scanned []resource.ObservedHostKey, err error) {
	if cached := host.Status.ObservedHostKeys; len(cached) > 0 && r.rescanInterval > 0 &&
		host.Status.ObservedGeneration == host.Metadata.Generation {
		fresh := true
		for _, key := range cached {
			if key.LastSeenAt.IsZero() || r.now().Sub(key.LastSeenAt) >= r.rescanInterval || slices.Contains(host.Spec.AccessTarget.Trust, key.Fingerprint) {
				fresh = false
				break
			}
		}
		if fresh {
			return cached, nil, nil
		}
	}
	keys, err = scanner.ScanHostKeys(ctx, host)
	return keys, keys, err
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

// targetNotAllowedMessage names the controller policy behind a denied target,
// so an owner is not left guessing at a network fault.
const targetNotAllowedMessage = "the controller's network policy does not allow this address or port; " +
	"allow it with MACHINARCHY_SSH_ALLOW_LOOPBACK, MACHINARCHY_SSH_ALLOW_PRIVATE, MACHINARCHY_SSH_ALLOWED_CIDRS or MACHINARCHY_SSH_ALLOWED_PORTS"

func failureMessage(err error) string {
	if errors.Is(err, domain.ErrTargetNotAllowed) {
		return targetNotAllowedMessage
	}
	return ""
}

func failureReason(err error) string {
	switch {
	case errors.Is(err, domain.ErrTargetNotAllowed):
		return "TargetNotAllowed"
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
