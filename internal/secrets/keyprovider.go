package secrets

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/machinarchy/machinarchy/pkg/crypto"
)

// KeyProvider is the root of trust behind the Secret Store (AUDIT/10 Phase
// 16). The Secret Store keeps every secret, key ID, version and audit record
// itself; a provider only wraps and unwraps the small per-secret data keys.
//
// The interface never hands out root-key bytes, because a KMS (Vault, AWS,
// GCP, Azure) never does: it encrypts and decrypts on its side. The built-in
// LocalProvider is the default; the K-phases add external providers that
// implement this interface and pass the contract suite in
// keyprovider_contract_test.go.
type KeyProvider interface {
	// Name identifies the provider ("local", "vault", ...). Key IDs it
	// issues start with Name()+":".
	Name() string
	// CurrentKeyID names the key Wrap uses now. Callers bind it into the
	// AAD before wrapping, so it is read first.
	CurrentKeyID(ctx context.Context) (string, error)
	// Owns reports whether this provider can unwrap data under keyID, so a
	// Keyring can route Unwrap during a provider switch. A KMS that cannot
	// list its key versions may answer by key-ID prefix.
	Owns(keyID string) bool
	// Wrap encrypts dataKey under keyID, bound to aad. keyID must be the
	// current key: new data is never wrapped under an old one.
	Wrap(ctx context.Context, keyID string, dataKey, aad []byte) ([]byte, error)
	// Unwrap reverses Wrap. It fails unless keyID, wrapped and aad all
	// match.
	Unwrap(ctx context.Context, keyID string, wrapped, aad []byte) ([]byte, error)
	// Capabilities reports optional features.
	Capabilities() Capabilities
}

// Capabilities are a provider's optional features. Wrap and Unwrap are
// required of every provider.
type Capabilities struct {
	// Ed25519Sign: the provider can hold an Ed25519 key and sign with it,
	// so the agent-gateway key need not leave it. Without it the key is
	// kept in the Secret Store, protected by Wrap.
	Ed25519Sign bool
}

// ErrKeyNotCurrent reports a Wrap under a key other than the current one.
var ErrKeyNotCurrent = errors.New("data keys are wrapped only under the current key")

// LocalKeyIDPrefix starts every key ID the built-in provider issues.
const LocalKeyIDPrefix = "local:"

// LocalProvider is the built-in, default provider. Its root keys come from
// the operator's bootstrap secret (MACHINARCHY_ENCRYPTION_SECRET[_FILE] and
// the keyring file), derived per key ID; they are never persisted.
//
// Key IDs are "local:<id>". Key IDs written before Phase 16 have no prefix;
// they are still accepted, so existing data reads without migration.
type LocalProvider struct {
	// KeyID is the current key's bare ID (MACHINARCHY_ENCRYPTION_KEY_ID).
	KeyID string
	// Key is the current 32-byte root key.
	Key []byte
	// Previous holds earlier root keys by bare ID, to unwrap and rewrap.
	Previous map[string][]byte
}

// StaticKeyProvider is the former name of LocalProvider.
//
// Deprecated: use LocalProvider.
type StaticKeyProvider = LocalProvider

var _ KeyProvider = (*LocalProvider)(nil)

// Name implements KeyProvider.
func (p *LocalProvider) Name() string { return "local" }

// Capabilities implements KeyProvider: wrapping only.
func (p *LocalProvider) Capabilities() Capabilities { return Capabilities{} }

// CurrentKeyID implements KeyProvider.
func (p *LocalProvider) CurrentKeyID(context.Context) (string, error) {
	if p == nil || p.KeyID == "" || len(p.Key) != 32 {
		return "", ErrKeyUnavailable
	}
	return LocalKeyIDPrefix + p.KeyID, nil
}

// Owns implements KeyProvider: a key this provider holds, named "local:<id>"
// or, as written before Phase 16, "<id>".
func (p *LocalProvider) Owns(keyID string) bool {
	if p == nil || (strings.Contains(keyID, ":") && !strings.HasPrefix(keyID, LocalKeyIDPrefix)) {
		return false
	}
	_, err := p.root(keyID)
	return err == nil
}

// Wrap implements KeyProvider with AES-256-GCM under the root key.
func (p *LocalProvider) Wrap(ctx context.Context, keyID string, dataKey, aad []byte) ([]byte, error) {
	current, err := p.CurrentKeyID(ctx)
	if err != nil {
		return nil, err
	}
	if keyID != current {
		return nil, ErrKeyNotCurrent
	}
	return crypto.EncryptBytes(dataKey, p.Key, aad)
}

// Unwrap implements KeyProvider.
func (p *LocalProvider) Unwrap(_ context.Context, keyID string, wrapped, aad []byte) ([]byte, error) {
	key, err := p.root(keyID)
	if err != nil {
		return nil, err
	}
	plain, err := crypto.DecryptBytes(wrapped, key, aad)
	if err != nil {
		return nil, errors.New("data key unwrap failed")
	}
	return plain, nil
}

// root returns the root key for a key ID, with or without the prefix.
func (p *LocalProvider) root(keyID string) ([]byte, error) {
	if p == nil {
		return nil, ErrKeyUnavailable
	}
	bare := strings.TrimPrefix(keyID, LocalKeyIDPrefix)
	if bare == p.KeyID && len(p.Key) == 32 {
		return p.Key, nil
	}
	if k := p.Previous[bare]; len(k) == 32 {
		return k, nil
	}
	return nil, ErrKeyUnavailable
}

// Keyring combines the current provider with earlier ones during a provider
// switch (e.g. local → vault). New data is wrapped by Current; Unwrap goes to
// whichever provider owns the stored key ID, so old data stays readable
// until it is rewrapped.
type Keyring struct {
	Current  KeyProvider
	Previous []KeyProvider
}

var _ KeyProvider = (*Keyring)(nil)

// Name implements KeyProvider: the current provider's name.
func (k *Keyring) Name() string { return k.Current.Name() }

// Capabilities implements KeyProvider: the current provider's.
func (k *Keyring) Capabilities() Capabilities { return k.Current.Capabilities() }

// CurrentKeyID implements KeyProvider.
func (k *Keyring) CurrentKeyID(ctx context.Context) (string, error) {
	return k.Current.CurrentKeyID(ctx)
}

// Owns implements KeyProvider.
func (k *Keyring) Owns(keyID string) bool { return k.owner(keyID) != nil }

// Wrap implements KeyProvider with the current provider.
func (k *Keyring) Wrap(ctx context.Context, keyID string, dataKey, aad []byte) ([]byte, error) {
	return k.Current.Wrap(ctx, keyID, dataKey, aad)
}

// Unwrap implements KeyProvider with the provider that owns keyID.
func (k *Keyring) Unwrap(ctx context.Context, keyID string, wrapped, aad []byte) ([]byte, error) {
	owner := k.owner(keyID)
	if owner == nil {
		return nil, fmt.Errorf("%w: no configured provider owns key %q", ErrKeyUnavailable, keyID)
	}
	return owner.Unwrap(ctx, keyID, wrapped, aad)
}

func (k *Keyring) owner(keyID string) KeyProvider {
	for _, p := range append([]KeyProvider{k.Current}, k.Previous...) {
		if p != nil && p.Owns(keyID) {
			return p
		}
	}
	return nil
}

// DefaultLocalKeyID is the key ID when MACHINARCHY_ENCRYPTION_KEY_ID is unset.
const DefaultLocalKeyID = "bootstrap-v1"

// NewLocalProvider derives the built-in provider's root keys from the
// operator's bootstrap secret (MACHINARCHY_ENCRYPTION_SECRET[_FILE]) and the
// keyring of earlier secrets by key ID. Every user of the root of trust
// (Secret Store, Operation data) must build it this way so they agree.
func NewLocalProvider(keyID, secret string, previous map[string]string) (*LocalProvider, error) {
	if keyID == "" {
		keyID = DefaultLocalKeyID
	}
	root, err := crypto.DeriveKey(secret, "machinarchy/secret-kek/"+keyID)
	if err != nil {
		return nil, fmt.Errorf("derive root key %s: %w", keyID, err)
	}
	p := &LocalProvider{KeyID: keyID, Key: root, Previous: map[string][]byte{}}
	for oldID, oldSecret := range previous {
		old, err := crypto.DeriveKey(oldSecret, "machinarchy/secret-kek/"+oldID)
		if err != nil {
			return nil, fmt.Errorf("derive previous root key %s: %w", oldID, err)
		}
		p.Previous[oldID] = old
	}
	return p, nil
}
