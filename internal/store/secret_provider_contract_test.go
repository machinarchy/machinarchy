package store_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/secrets"
)

type allowResolution struct{}

func (allowResolution) AuthorizeSecretResolution(context.Context, secrets.ResolutionRequest) error {
	return nil
}

// Phase 16 on both engines: provider-named key IDs fit the key_id column at
// the longest configurable length (PostgreSQL has VARCHAR(128)), secrets
// resolve, and a rewrap moves them to the next key.
func testSecretStoreKeyProviders(t *testing.T, f *fixture) {
	ctx := context.Background()
	owner := domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner}
	longest := strings.Repeat("k", 128-len(secrets.LocalKeyIDPrefix))
	keyA, keyB := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	before, err := secrets.New(f.stores.Management, &secrets.LocalProvider{KeyID: longest, Key: keyA}, allowResolution{})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := before.Create(ctx, owner, secrets.Input{Name: "deploy", Purpose: secrets.PurposeSSHCredential, Value: "pw-1"})
	if err != nil {
		t.Fatalf("create under a %d-character key ID: %v", len(secrets.LocalKeyIDPrefix+longest), err)
	}
	resolve := func(s *secrets.Service) (string, error) {
		value, err := s.Resolve(ctx, secrets.ResolutionRequest{WorkloadID: secrets.WorkloadControlManager, Ref: secrets.Ref{ID: meta.ID, Version: 1},
			Purpose: secrets.PurposeSSHCredential, WorkspaceID: 1, AccessProfileID: "profile", AccessProfileRevision: 1, HostID: "host"})
		return string(value), err
	}
	if value, err := resolve(before); err != nil || value != "pw-1" {
		t.Fatalf("resolve = %q, %v", value, err)
	}
	after, err := secrets.New(f.stores.Management, &secrets.LocalProvider{KeyID: "next", Key: keyB, Previous: map[string][]byte{longest: keyA}}, allowResolution{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := after.RewrapKey(ctx, owner, longest, 10)
	if err != nil || result.Rewrapped != 1 || result.Remaining != 0 || result.KeyID != "local:next" {
		t.Fatalf("rewrap = %+v, %v", result, err)
	}
	onlyNext, err := secrets.New(f.stores.Management, &secrets.LocalProvider{KeyID: "next", Key: keyB}, allowResolution{})
	if err != nil {
		t.Fatal(err)
	}
	if value, err := resolve(onlyNext); err != nil || value != "pw-1" {
		t.Fatalf("resolve after rewrap = %q, %v", value, err)
	}
}
