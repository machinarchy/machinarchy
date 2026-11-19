package store_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/domain"
)

// Migration 000007 and the signing-key store (Phase 16): one active and one
// next key at most, activation retires the active key, every change is
// audited, and only public data is stored.
func testAgentSigningKeys(t *testing.T, f *fixture) {
	ctx := context.Background()
	owner := domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner}
	now := time.Unix(1_800_000_000, 0).UTC()
	key := func(id string, seed byte, state agentgateway.SigningKeyState) agentgateway.SigningKey {
		return agentgateway.SigningKey{ID: id, State: state, PublicKey: ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32)).Public().(ed25519.PublicKey),
			SecretID: "secret-" + id, SecretVersion: 1, Origin: agentgateway.SigningKeyImported, CreatedAt: now}
	}
	store := f.stores.Agents

	if _, err := store.ActivateSigningKey(ctx, owner, now); !errors.Is(err, agentgateway.ErrNoStagedSigningKey) {
		t.Fatalf("activate with nothing staged = %v", err)
	}
	if err := store.CreateSigningKey(ctx, key("k1", 1, agentgateway.SigningKeyActive), domain.Principal{}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSigningKey(ctx, key("k1b", 9, agentgateway.SigningKeyActive), domain.Principal{}); !errors.Is(err, agentgateway.ErrActiveSigningKeyExists) {
		t.Fatalf("second active key = %v", err)
	}
	if err := store.CreateSigningKey(ctx, key("k2", 2, agentgateway.SigningKeyNext), owner); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSigningKey(ctx, key("k3", 3, agentgateway.SigningKeyNext), owner); !errors.Is(err, agentgateway.ErrSigningKeyStaged) {
		t.Fatalf("second next key = %v", err)
	}
	activated, err := store.ActivateSigningKey(ctx, owner, now.Add(time.Hour))
	if err != nil || activated.ID != "k2" || activated.State != agentgateway.SigningKeyActive {
		t.Fatalf("activate = %+v, %v", activated, err)
	}
	keys, err := store.ListSigningKeys(ctx)
	if err != nil || len(keys) != 2 {
		t.Fatalf("list = %+v, %v", keys, err)
	}
	states := map[string]agentgateway.SigningKey{}
	for _, k := range keys {
		states[k.ID] = k
	}
	if k1 := states["k1"]; k1.State != agentgateway.SigningKeyRetired || k1.RetiredAt == nil || !k1.PublicKey.Equal(key("k1", 1, "").PublicKey) {
		t.Fatalf("old active key = %+v", k1)
	}
	if k2 := states["k2"]; k2.ActivatedAt == nil || !k2.ActivatedAt.Equal(now.Add(time.Hour)) || k2.SecretID != "secret-k2" || k2.CreatedBy != 1 {
		t.Fatalf("activated key = %+v", k2)
	}
	if err := store.CreateSigningKey(ctx, key("k4", 4, agentgateway.SigningKeyNext), owner); err != nil {
		t.Fatal(err)
	}
	if discarded, err := store.DiscardSigningKey(ctx, owner, now.Add(2*time.Hour)); err != nil || discarded.ID != "k4" || discarded.State != agentgateway.SigningKeyRetired {
		t.Fatalf("discard = %+v, %v", discarded, err)
	}
	events, err := f.stores.Audit.ListAuditEvents(ctx, 1, domain.AuditFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, e := range events {
		counts[e.Action]++
	}
	if counts[domain.AuditAgentSigningKeyCreated] != 3 || counts[domain.AuditAgentSigningKeyActivated] != 1 || counts[domain.AuditAgentSigningKeyDiscarded] != 1 {
		t.Fatalf("audit actions = %v", counts)
	}
}
