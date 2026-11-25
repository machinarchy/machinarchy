// Package agentkeys manages the controller's agent-gateway signing keys
// (AUDIT M-3, Phase 16). Each private seed is a system secret in the Secret
// Store, protected by the configured key provider; the agent-gateway store
// keeps the public key and the rotation state (next, active, retired).
//
// The Manager hands the gateway the active key and the keys agents must pin,
// and refreshes them from the database so every controller replica follows
// an activation within one refresh interval.
package agentkeys

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/secrets"
)

// SecretStore is the part of the Secret Store the manager uses.
type SecretStore interface {
	CreateSystem(ctx context.Context, name string, purpose secrets.Purpose, value []byte) (secrets.Ref, error)
	// RetireSystem retires a system secret that no key refers to.
	RetireSystem(ctx context.Context, ref secrets.Ref, purpose secrets.Purpose) error
	ResolveSystem(ctx context.Context, ref secrets.Ref, purpose secrets.Purpose) ([]byte, error)
}

// DefaultRefreshInterval is how often a replica re-reads the keys.
const DefaultRefreshInterval = 5 * time.Second

// Manager owns the signing keys of one controller process.
type Manager struct {
	store   agentgateway.SigningKeyStore
	secrets SecretStore
	logger  *slog.Logger
	now     func() time.Time

	// publicOnly managers never load a private key (API replicas).
	publicOnly bool

	mu       sync.Mutex // serializes Refresh
	activeID string
	current  atomic.Pointer[ed25519.PrivateKey]
	pins     atomic.Pointer[[]string]
}

// New creates a Manager. Call Ensure before using Signer.
func New(store agentgateway.SigningKeyStore, secretStore SecretStore, logger *slog.Logger) (*Manager, error) {
	if store == nil || secretStore == nil {
		return nil, errors.New("agent key manager requires a signing key store and the Secret Store")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{store: store, secrets: secretStore, logger: logger, now: time.Now}, nil
}

// NewPublicOnly creates a Manager for a process that runs no gateway (an API
// replica, AUDIT/20 F20-02): it serves the public keys agents pin and stages,
// activates and discards keys, but never loads a private key. Its Signer
// must not be used.
func NewPublicOnly(store agentgateway.SigningKeyStore, secretStore SecretStore, logger *slog.Logger) (*Manager, error) {
	m, err := New(store, secretStore, logger)
	if err != nil {
		return nil, err
	}
	m.publicOnly = true
	return m, nil
}

// Ensure makes sure an active key exists and loads it. On the first start
// after the upgrade, importSeed (MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE)
// becomes the active key so enrolled agents keep working; without one a key
// is generated. Once a key is active the file is no longer used.
func (m *Manager) Ensure(ctx context.Context, importSeed []byte) error {
	keys, err := m.store.ListSigningKeys(ctx)
	if err != nil {
		return err
	}
	active := find(keys, agentgateway.SigningKeyActive)
	switch {
	case active != nil && importSeed != nil:
		if !active.PublicKey.Equal(ed25519.NewKeyFromSeed(importSeed).Public()) {
			m.logger.Warn("MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE differs from the active signing key in the Secret Store and is ignored; remove the setting")
		}
	case active == nil:
		origin, seed := agentgateway.SigningKeyImported, importSeed
		if seed == nil {
			origin = agentgateway.SigningKeyGenerated
			if seed, err = newSeed(); err != nil {
				return err
			}
		}
		err := m.create(ctx, seed, agentgateway.SigningKeyActive, origin, domain.Principal{})
		if err != nil && !errors.Is(err, agentgateway.ErrActiveSigningKeyExists) { // another replica won the race
			return err
		}
		if err == nil {
			m.logger.Info("agent signing key stored in the Secret Store", "origin", string(origin))
		}
	}
	return m.Refresh(ctx)
}

// Refresh re-reads the keys and loads the active private key if it changed.
func (m *Manager) Refresh(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys, err := m.store.ListSigningKeys(ctx)
	if err != nil {
		return err
	}
	active := find(keys, agentgateway.SigningKeyActive)
	if active == nil {
		return agentgateway.ErrNoActiveSigningKey
	}
	if active.ID != m.activeID && !m.publicOnly {
		seed, err := m.secrets.ResolveSystem(ctx, secrets.Ref{ID: active.SecretID, Version: active.SecretVersion}, secrets.PurposeAgentSigning)
		if err != nil {
			return fmt.Errorf("load agent signing key %s: %w", active.ID, err)
		}
		raw, err := base64.StdEncoding.DecodeString(string(seed))
		if err != nil || len(raw) != ed25519.SeedSize {
			return fmt.Errorf("agent signing key %s: stored seed is malformed", active.ID)
		}
		key := ed25519.NewKeyFromSeed(raw)
		if !active.PublicKey.Equal(key.Public()) {
			return fmt.Errorf("agent signing key %s: the stored seed does not match its public key", active.ID)
		}
		if m.activeID != "" {
			m.logger.Info("agent gateway now signs with a new key", "key_id", active.ID,
				"public_key", agentv1.EncodePublicKey(active.PublicKey))
		}
		m.current.Store(&key)
		m.activeID = active.ID
	}
	pins := []string{agentv1.EncodePublicKey(active.PublicKey)}
	if next := find(keys, agentgateway.SigningKeyNext); next != nil {
		pins = append(pins, agentv1.EncodePublicKey(next.PublicKey))
	}
	m.pins.Store(&pins)
	return nil
}

// Run refreshes every interval until ctx ends; errors are logged and the
// last good keys stay in use.
func (m *Manager) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultRefreshInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := m.Refresh(ctx)
			if m.publicOnly && errors.Is(err, agentgateway.ErrNoActiveSigningKey) {
				continue // no gateway has created a key yet: agents are not in use
			}
			if err != nil && ctx.Err() == nil {
				m.logger.Warn("agent signing keys not refreshed; keeping the current ones", "error", err)
			}
		}
	}
}

// PublicKeys are the keys agents must pin: the active one, then the next
// one while a rotation is staged.
func (m *Manager) PublicKeys() []string {
	if pins := m.pins.Load(); pins != nil {
		return append([]string(nil), (*pins)...)
	}
	return nil
}

// Signer is the gateway's crypto.Signer. Each message is sealed with one
// snapshot of the active key (Snapshot), so a switch between Public and
// Sign cannot mix two keys.
func (m *Manager) Signer() crypto.Signer { return signer{m} }

type signer struct{ m *Manager }

func (s signer) key() ed25519.PrivateKey {
	if key := s.m.current.Load(); key != nil {
		return *key
	}
	return nil
}

func (s signer) Public() crypto.PublicKey {
	if key := s.key(); key != nil {
		return key.Public()
	}
	return nil
}

func (s signer) Sign(r io.Reader, message []byte, opts crypto.SignerOpts) ([]byte, error) {
	key := s.key()
	if key == nil {
		return nil, agentgateway.ErrNoActiveSigningKey
	}
	return key.Sign(r, message, opts)
}

// Snapshot returns the active key itself, fixed for one message.
func (s signer) Snapshot() crypto.Signer {
	if key := s.key(); key != nil {
		return key
	}
	return s
}

// List returns the keys for the owner API.
func (m *Manager) List(ctx context.Context) ([]agentgateway.SigningKey, error) {
	return m.store.ListSigningKeys(ctx)
}

// Stage generates a next key and announces it.
func (m *Manager) Stage(ctx context.Context, actor domain.Principal) (agentgateway.SigningKey, error) {
	keys, err := m.store.ListSigningKeys(ctx)
	if err != nil {
		return agentgateway.SigningKey{}, err
	}
	if find(keys, agentgateway.SigningKeyNext) != nil {
		return agentgateway.SigningKey{}, agentgateway.ErrSigningKeyStaged
	}
	seed, err := newSeed()
	if err != nil {
		return agentgateway.SigningKey{}, err
	}
	if err := m.create(ctx, seed, agentgateway.SigningKeyNext, agentgateway.SigningKeyGenerated, actor); err != nil {
		return agentgateway.SigningKey{}, err
	}
	if err := m.Refresh(ctx); err != nil {
		return agentgateway.SigningKey{}, err
	}
	keys, err = m.store.ListSigningKeys(ctx)
	if err != nil {
		return agentgateway.SigningKey{}, err
	}
	return *find(keys, agentgateway.SigningKeyNext), nil
}

// Activate makes the next key the signing key; the active key retires.
func (m *Manager) Activate(ctx context.Context, actor domain.Principal) (agentgateway.SigningKey, error) {
	key, err := m.store.ActivateSigningKey(ctx, actor, m.now())
	if err != nil {
		return key, err
	}
	return key, m.Refresh(ctx)
}

// Discard drops the next key without using it.
func (m *Manager) Discard(ctx context.Context, actor domain.Principal) (agentgateway.SigningKey, error) {
	key, err := m.store.DiscardSigningKey(ctx, actor, m.now())
	if err != nil {
		return key, err
	}
	return key, m.Refresh(ctx)
}

// create stores the seed as a system secret, then records the key.
func (m *Manager) create(ctx context.Context, seed []byte, state agentgateway.SigningKeyState, origin agentgateway.SigningKeyOrigin, actor domain.Principal) error {
	id, err := resource.NewResourceID()
	if err != nil {
		return err
	}
	ref, err := m.secrets.CreateSystem(ctx, "agent-gateway-signing-key-"+string(id), secrets.PurposeAgentSigning,
		[]byte(base64.StdEncoding.EncodeToString(seed)))
	if err != nil {
		return fmt.Errorf("store agent signing key: %w", err)
	}
	err = m.store.CreateSigningKey(ctx, agentgateway.SigningKey{
		ID: string(id), State: state, PublicKey: ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey),
		SecretID: ref.ID, SecretVersion: ref.Version, Origin: origin, CreatedBy: actor.UserID, CreatedAt: m.now().UTC(),
	}, actor)
	if err != nil {
		// No key refers to the secret (another replica won the first-start
		// race, or the row could not be written): retire it rather than leave
		// an unused encrypted seed behind (AUDIT F16-06). Not the caller's
		// context, which may be the reason for the failure.
		retireCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if retireErr := m.secrets.RetireSystem(retireCtx, ref, secrets.PurposeAgentSigning); retireErr != nil {
			m.logger.Warn("retire the unused agent signing-key secret failed", "secret_id", ref.ID, "error", retireErr)
		}
	}
	return err
}

func newSeed() ([]byte, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("generate agent signing key: %w", err)
	}
	return seed, nil
}

func find(keys []agentgateway.SigningKey, state agentgateway.SigningKeyState) *agentgateway.SigningKey {
	for i := range keys {
		if keys[i].State == state {
			return &keys[i]
		}
	}
	return nil
}
