package agentkeys

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/secrets"
)

// memStore is an in-memory SigningKeyStore with the database's constraints.
type memStore struct {
	mu   sync.Mutex
	keys []agentgateway.SigningKey
}

func (s *memStore) ListSigningKeys(context.Context) ([]agentgateway.SigningKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]agentgateway.SigningKey, len(s.keys))
	for i := range s.keys {
		out[len(s.keys)-1-i] = s.keys[i]
	}
	return out, nil
}

func (s *memStore) CreateSigningKey(_ context.Context, key agentgateway.SigningKey, actor domain.Principal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keys {
		if k.State == key.State {
			if key.State == agentgateway.SigningKeyNext {
				return agentgateway.ErrSigningKeyStaged
			}
			return agentgateway.ErrActiveSigningKeyExists
		}
	}
	key.CreatedBy = actor.UserID
	s.keys = append(s.keys, key)
	return nil
}

func (s *memStore) move(now time.Time, activate bool) (agentgateway.SigningKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := -1
	for i, k := range s.keys {
		if k.State == agentgateway.SigningKeyNext {
			next = i
		}
	}
	if next < 0 {
		return agentgateway.SigningKey{}, agentgateway.ErrNoStagedSigningKey
	}
	if !activate {
		s.keys[next].State, s.keys[next].RetiredAt = agentgateway.SigningKeyRetired, &now
		return s.keys[next], nil
	}
	for i := range s.keys {
		if s.keys[i].State == agentgateway.SigningKeyActive {
			s.keys[i].State, s.keys[i].RetiredAt = agentgateway.SigningKeyRetired, &now
		}
	}
	s.keys[next].State, s.keys[next].ActivatedAt = agentgateway.SigningKeyActive, &now
	return s.keys[next], nil
}

func (s *memStore) ActivateSigningKey(_ context.Context, _ domain.Principal, now time.Time) (agentgateway.SigningKey, error) {
	return s.move(now, true)
}

func (s *memStore) DiscardSigningKey(_ context.Context, _ domain.Principal, now time.Time) (agentgateway.SigningKey, error) {
	return s.move(now, false)
}

// memSecrets is an in-memory SecretStore.
type memSecrets struct {
	mu       sync.Mutex
	values   map[string][]byte
	resolves int
}

func (s *memSecrets) CreateSystem(_ context.Context, name string, purpose secrets.Purpose, value []byte) (secrets.Ref, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if purpose != secrets.PurposeAgentSigning {
		return secrets.Ref{}, secrets.ErrInvalid
	}
	if s.values == nil {
		s.values = map[string][]byte{}
	}
	s.values[name] = append([]byte(nil), value...)
	return secrets.Ref{ID: name, Version: 1}, nil
}

func (s *memSecrets) RetireSystem(_ context.Context, ref secrets.Ref, _ secrets.Purpose) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, ref.ID)
	return nil
}

func (s *memSecrets) ResolveSystem(_ context.Context, ref secrets.Ref, _ secrets.Purpose) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolves++
	value, ok := s.values[ref.ID]
	if !ok {
		return nil, secrets.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func newManager(t *testing.T, store *memStore, sec *memSecrets) *Manager {
	t.Helper()
	m, err := New(store, sec, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func publicOf(seed []byte) string {
	return agentv1.EncodePublicKey(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
}

func signerPublic(s crypto.Signer) string {
	return agentv1.EncodePublicKey(s.Public().(ed25519.PublicKey))
}

// The first start imports the key file so enrolled agents keep working;
// later starts ignore a different file; without a file a key is generated.
func TestEnsureImportsOnceThenKeepsTheStoredKey(t *testing.T) {
	ctx := context.Background()
	store, sec := &memStore{}, &memSecrets{}
	seed := bytes.Repeat([]byte{1}, 32)
	first := newManager(t, store, sec)
	if err := first.Ensure(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if pins := first.PublicKeys(); len(pins) != 1 || pins[0] != publicOf(seed) || signerPublic(first.Signer()) != publicOf(seed) {
		t.Fatalf("imported key: pins %v", pins)
	}
	if store.keys[0].Origin != agentgateway.SigningKeyImported {
		t.Fatalf("origin = %s", store.keys[0].Origin)
	}
	// The seed is stored base64, as a system secret.
	for _, value := range sec.values {
		if string(value) != base64.StdEncoding.EncodeToString(seed) {
			t.Fatal("the secret does not hold the imported seed")
		}
	}
	restarted := newManager(t, store, sec)
	if err := restarted.Ensure(ctx, bytes.Repeat([]byte{2}, 32)); err != nil {
		t.Fatal(err)
	}
	if signerPublic(restarted.Signer()) != publicOf(seed) || len(store.keys) != 1 {
		t.Fatal("a different key file replaced the stored key")
	}

	fresh := newManager(t, &memStore{}, &memSecrets{})
	if err := fresh.Ensure(ctx, nil); err != nil || len(fresh.PublicKeys()) != 1 {
		t.Fatalf("generated key: %v %v", fresh.PublicKeys(), err)
	}
}

// Two replicas starting at once both end up with the same single key.
func TestConcurrentReplicasAgreeOnOneKey(t *testing.T) {
	ctx := context.Background()
	store, sec := &memStore{}, &memSecrets{}
	var wg sync.WaitGroup
	managers := make([]*Manager, 4)
	for i := range managers {
		managers[i] = newManager(t, store, sec)
		wg.Add(1)
		go func(m *Manager) {
			defer wg.Done()
			if err := m.Ensure(ctx, nil); err != nil {
				t.Error(err)
			}
		}(managers[i])
	}
	wg.Wait()
	for _, m := range managers {
		if signerPublic(m.Signer()) != signerPublic(managers[0].Signer()) {
			t.Fatal("replicas sign with different keys")
		}
	}
	if len(store.keys) != 1 {
		t.Fatalf("%d keys recorded", len(store.keys))
	}
	// AUDIT F16-06: a replica that lost the race retired the secret it had
	// written, so only the winner's seed stays stored.
	sec.mu.Lock()
	defer sec.mu.Unlock()
	if len(sec.values) != 1 {
		t.Fatalf("%d signing-key secrets stored after the race, want only the winner's", len(sec.values))
	}
}

// failingStore refuses to record a key, as after a lost connection.
type failingStore struct{ *memStore }

func (failingStore) CreateSigningKey(context.Context, agentgateway.SigningKey, domain.Principal) error {
	return errors.New("database unavailable")
}

// AUDIT F16-06: when the key row cannot be written, the secret written just
// before it is retired instead of left behind.
func TestFailedKeyRecordLeavesNoSecret(t *testing.T) {
	sec := &memSecrets{}
	m := newManager(t, &memStore{}, sec)
	m.store = failingStore{&memStore{}}
	if err := m.Ensure(context.Background(), nil); err == nil {
		t.Fatal("Ensure succeeded although the key could not be recorded")
	}
	sec.mu.Lock()
	defer sec.mu.Unlock()
	if len(sec.values) != 0 {
		t.Fatalf("%d secrets left behind", len(sec.values))
	}
}

// Stage announces the next key; Activate switches signing; another replica
// follows at its next refresh; a snapshot taken before keeps its key.
func TestRotationAcrossReplicas(t *testing.T) {
	ctx := context.Background()
	store, sec := &memStore{}, &memSecrets{}
	a, b := newManager(t, store, sec), newManager(t, store, sec)
	if err := a.Ensure(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Ensure(ctx, nil); err != nil {
		t.Fatal(err)
	}
	old := signerPublic(a.Signer())
	snapshot := a.Signer().(interface{ Snapshot() crypto.Signer }).Snapshot()

	owner := domain.Principal{UserID: 7, Role: domain.RoleOwner}
	next, err := a.Stage(ctx, owner)
	if err != nil || next.State != agentgateway.SigningKeyNext || next.CreatedBy != 7 {
		t.Fatalf("stage = %+v, %v", next, err)
	}
	if _, err := a.Stage(ctx, owner); !errors.Is(err, agentgateway.ErrSigningKeyStaged) {
		t.Fatalf("second stage = %v", err)
	}
	if pins := a.PublicKeys(); len(pins) != 2 || pins[0] != old || signerPublic(a.Signer()) != old {
		t.Fatalf("after stage: pins %v, signing with the new key early", pins)
	}
	if _, err := a.Activate(ctx, owner); err != nil {
		t.Fatal(err)
	}
	newKey := signerPublic(a.Signer())
	if newKey == old || len(a.PublicKeys()) != 1 || a.PublicKeys()[0] != newKey {
		t.Fatalf("after activate: signing %s, pins %v", newKey, a.PublicKeys())
	}
	if signerPublic(snapshot) != old {
		t.Fatal("a snapshot changed key")
	}
	if signerPublic(b.Signer()) != old {
		t.Fatal("replica b switched before refreshing")
	}
	if err := b.Refresh(ctx); err != nil || signerPublic(b.Signer()) != newKey {
		t.Fatalf("replica b after refresh: %v", err)
	}
	if _, err := a.Discard(ctx, owner); !errors.Is(err, agentgateway.ErrNoStagedSigningKey) {
		t.Fatalf("discard with nothing staged = %v", err)
	}
}

// A stored seed that does not match its recorded public key is refused.
func TestRefreshRefusesAMismatchedSeed(t *testing.T) {
	ctx := context.Background()
	store, sec := &memStore{}, &memSecrets{}
	m := newManager(t, store, sec)
	if err := m.Ensure(ctx, bytes.Repeat([]byte{1}, 32)); err != nil {
		t.Fatal(err)
	}
	for name := range sec.values {
		sec.values[name] = []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	}
	if err := newManager(t, store, sec).Refresh(ctx); err == nil {
		t.Fatal("a tampered seed was loaded")
	}
}

// TestPublicOnlyManagerServesTheAPI (AUDIT/20 F20-02): an API replica runs no
// gateway but serves /controller-key and the rotation endpoints. It reads
// and changes the key list without ever loading a private key; the
// Control Manager follows its changes.
func TestPublicOnlyManagerServesTheAPI(t *testing.T) {
	ctx := context.Background()
	store := &memStore{}
	cmSecrets, apiSecrets := &memSecrets{}, &memSecrets{}
	controlManager := newManager(t, store, cmSecrets)
	if err := controlManager.Ensure(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// The two share the Secret Store; the API side counts its own reads.
	apiSecrets.values = cmSecrets.values
	api, err := NewPublicOnly(store, apiSecrets, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := api.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if pins := api.PublicKeys(); len(pins) != 1 || pins[0] != signerPublic(controlManager.Signer()) {
		t.Fatalf("API pins %v, Control Manager signs with %s", pins, signerPublic(controlManager.Signer()))
	}
	owner := domain.Principal{UserID: 7, Role: domain.RoleOwner}
	if _, err := api.Stage(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if len(api.PublicKeys()) != 2 {
		t.Fatalf("after staging through the API: pins %v", api.PublicKeys())
	}
	if _, err := api.Activate(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := controlManager.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if pins := api.PublicKeys(); len(pins) != 1 || pins[0] != signerPublic(controlManager.Signer()) {
		t.Fatalf("after activation: API pins %v, Control Manager signs with %s", pins, signerPublic(controlManager.Signer()))
	}
	apiSecrets.mu.Lock()
	defer apiSecrets.mu.Unlock()
	if apiSecrets.resolves != 0 {
		t.Fatalf("the API replica loaded a private key %d times", apiSecrets.resolves)
	}
}
