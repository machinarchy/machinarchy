package agentgateway

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

// Controller signing keys (AUDIT M-3, Phase 16). The private seed of each key
// is a Secret Store secret; the store keeps only public data and the
// rotation state:
//
//	next    — announced at /controller-key so agents can pin it; not used yet
//	active  — signs everything the gateway sends (exactly one)
//	retired — no longer used or announced; kept for the audit trail
//
// Rotation: stage a next key, let agents pin both, activate it (the active
// key retires), later agents drop the old pin.

// SigningKeyState is a key's place in the rotation.
type SigningKeyState string

// Signing key states.
const (
	SigningKeyNext    SigningKeyState = "next"
	SigningKeyActive  SigningKeyState = "active"
	SigningKeyRetired SigningKeyState = "retired"
)

// SigningKeyOrigin records where a key came from.
type SigningKeyOrigin string

// Signing key origins.
const (
	SigningKeyGenerated SigningKeyOrigin = "generated"
	// SigningKeyImported came from MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE
	// at the first start after the upgrade, so enrolled agents keep working.
	SigningKeyImported SigningKeyOrigin = "imported"
)

// SigningKey is one controller signing key.
type SigningKey struct {
	ID            string            `json:"id"`
	State         SigningKeyState   `json:"state"`
	PublicKey     ed25519.PublicKey `json:"-"`
	SecretID      string            `json:"-"`
	SecretVersion uint64            `json:"-"`
	Origin        SigningKeyOrigin  `json:"origin"`
	CreatedBy     int               `json:"createdBy,omitempty"`
	CreatedAt     time.Time         `json:"createdAt"`
	ActivatedAt   *time.Time        `json:"activatedAt,omitempty"`
	RetiredAt     *time.Time        `json:"retiredAt,omitempty"`
}

// Signing key errors.
var (
	// ErrNoActiveSigningKey: the store holds no active key yet.
	ErrNoActiveSigningKey = errors.New("no active agent signing key")
	// ErrSigningKeyStaged: a next key already exists.
	ErrSigningKeyStaged = errors.New("a next agent signing key is already staged")
	// ErrNoStagedSigningKey: there is no next key to activate or discard.
	ErrNoStagedSigningKey = errors.New("no next agent signing key is staged")
	// ErrActiveSigningKeyExists: another replica created the first key.
	ErrActiveSigningKeyExists = errors.New("an active agent signing key already exists")
)

// SigningKeyStore persists signing keys. Every change is audited in the
// same transaction.
type SigningKeyStore interface {
	// ListSigningKeys returns all keys, newest first.
	ListSigningKeys(ctx context.Context) ([]SigningKey, error)
	// CreateSigningKey inserts key in state active (only when none is
	// active) or next (only when none is staged). created_by is the actor
	// (0 for the controller itself, e.g. the first-start import).
	CreateSigningKey(ctx context.Context, key SigningKey, actor domain.Principal) error
	// ActivateSigningKey makes the next key active and retires the active
	// one, returning the key now active.
	ActivateSigningKey(ctx context.Context, actor domain.Principal, now time.Time) (SigningKey, error)
	// DiscardSigningKey retires the next key without using it, returning it.
	DiscardSigningKey(ctx context.Context, actor domain.Principal, now time.Time) (SigningKey, error)
}
