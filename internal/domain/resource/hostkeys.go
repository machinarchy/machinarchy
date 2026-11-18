package resource

import (
	"fmt"
	"strings"
	"time"
)

// HostKeyPolicy says how a Host's SSH host keys become trusted (AUDIT/10b §2).
type HostKeyPolicy string

const (
	// HostKeyPolicyDefault follows the deployment default
	// (MACHINARCHY_SSH_HOST_KEY_POLICY).
	HostKeyPolicyDefault HostKeyPolicy = ""
	// HostKeyPolicyTOFU pins the keys seen on first contact, before any
	// authentication. Only allowed with private-key authentication.
	HostKeyPolicyTOFU HostKeyPolicy = "tofu"
	// HostKeyPolicyApprove records the keys seen and waits for an owner.
	HostKeyPolicyApprove HostKeyPolicy = "approve"
	// HostKeyPolicyCA trusts host certificates signed by the profile's CAs.
	HostKeyPolicyCA HostKeyPolicy = "ca"
	// HostKeyPolicyPinned trusts only fingerprints set in the Host spec.
	HostKeyPolicyPinned HostKeyPolicy = "pinned"
)

// Bounds for host-key trust data.
const (
	MaxTrustedHostKeys      = 8
	MaxHostCertAuthorities  = 8
	maxHostCertAuthorityLen = 2048
)

// Valid reports whether p is a known policy (the default included).
func (p HostKeyPolicy) Valid() bool {
	switch p {
	case HostKeyPolicyDefault, HostKeyPolicyTOFU, HostKeyPolicyApprove, HostKeyPolicyCA, HostKeyPolicyPinned:
		return true
	}
	return false
}

// EffectiveHostKeyPolicy resolves the profile's policy against the
// deployment default. TOFU never applies to password profiles: on first
// contact an impostor would receive the password itself, so they fall back
// to owner approval.
func (s AccessProfileSpec) EffectiveHostKeyPolicy(deploymentDefault HostKeyPolicy) HostKeyPolicy {
	policy := s.HostKeyPolicy
	if policy == HostKeyPolicyDefault {
		policy = deploymentDefault
	}
	switch {
	case policy == HostKeyPolicyDefault || policy == HostKeyPolicyCA && len(s.HostCertificateAuthorities) == 0:
		return HostKeyPolicyApprove
	case policy == HostKeyPolicyTOFU && s.Authentication != AuthenticationPrivateKey:
		return HostKeyPolicyApprove
	}
	return policy
}

func (s AccessProfileSpec) validateHostKeys() error {
	if !s.HostKeyPolicy.Valid() {
		return fmt.Errorf("%w: unknown host key policy %q", ErrInvalidResource, s.HostKeyPolicy)
	}
	if s.HostKeyPolicy == HostKeyPolicyTOFU && s.Authentication != AuthenticationPrivateKey {
		return fmt.Errorf("%w: the tofu host key policy requires private-key authentication; with a password, use approve or ca", ErrInvalidResource)
	}
	if s.HostKeyPolicy == HostKeyPolicyCA && len(s.HostCertificateAuthorities) == 0 {
		return fmt.Errorf("%w: the ca host key policy needs at least one host certificate authority", ErrInvalidResource)
	}
	if len(s.HostCertificateAuthorities) > MaxHostCertAuthorities {
		return fmt.Errorf("%w: at most %d host certificate authorities", ErrInvalidResource, MaxHostCertAuthorities)
	}
	for _, authority := range s.HostCertificateAuthorities {
		if strings.TrimSpace(authority) == "" || len(authority) > maxHostCertAuthorityLen {
			return fmt.Errorf("%w: invalid host certificate authority", ErrInvalidResource)
		}
	}
	return nil
}

// ObservedHostKey is a host key the controller saw on the network.
type ObservedHostKey struct {
	Algorithm   string    `json:"algorithm"`
	Fingerprint string    `json:"fingerprint"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
}

// ValidFingerprint reports whether value looks like an OpenSSH SHA-256 key
// fingerprint ("SHA256:" and 43 base64 characters).
func ValidFingerprint(value string) bool {
	rest, ok := strings.CutPrefix(value, "SHA256:")
	if !ok || len(rest) != 43 {
		return false
	}
	return strings.Trim(rest, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/") == ""
}

// TrustHostKeys sets the Host's trusted host key fingerprints. Unless
// replace is set, it refuses a Host that already trusts keys, so a changed
// key is never accepted by accident.
func (h *Host) TrustHostKeys(expectedRevision uint64, fingerprints []string, replace bool, now time.Time) error {
	if h == nil {
		return fmt.Errorf("%w: Host is required", ErrInvalidResource)
	}
	if h.Metadata.Revision != expectedRevision {
		return ErrConflict
	}
	if len(fingerprints) == 0 || len(fingerprints) > MaxTrustedHostKeys {
		return fmt.Errorf("%w: trust 1 to %d host key fingerprints", ErrInvalidResource, MaxTrustedHostKeys)
	}
	seen := map[string]bool{}
	for _, fingerprint := range fingerprints {
		if !ValidFingerprint(fingerprint) || seen[fingerprint] {
			return fmt.Errorf("%w: host key fingerprints must be distinct SHA256:… values", ErrInvalidResource)
		}
		seen[fingerprint] = true
	}
	if len(h.Spec.AccessTarget.Trust) > 0 && !replace {
		return fmt.Errorf("%w: the Host already trusts host keys; replace them explicitly", ErrConflict)
	}
	h.Spec.AccessTarget.Trust = append([]string(nil), fingerprints...)
	h.Metadata.Generation++
	h.Metadata.Revision++
	h.Metadata.UpdatedAt = now.UTC()
	return h.Validate()
}

// HostObservation is one reconcile result for a Host.
type HostObservation struct {
	Phase      HostPhase
	Conditions []Condition
	// ObservedHostKeys replaces the stored keys when not nil.
	ObservedHostKeys []ObservedHostKey
	// TrustHostKeys, when set, asks the controller to pin these fingerprints
	// (TOFU) before anything authenticates; the Host is then verified again.
	TrustHostKeys []string
}
