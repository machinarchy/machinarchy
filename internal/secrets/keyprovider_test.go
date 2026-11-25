package secrets_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"testing"

	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/secrets/providertest"
)

func randomKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

// The built-in provider passes the contract every provider must pass.
func TestLocalProviderContract(t *testing.T) {
	n := 0
	providertest.Run(t, providertest.Harness{
		New: func(t *testing.T) secrets.KeyProvider {
			n++
			return &secrets.LocalProvider{KeyID: "k" + string(rune('a'+n%26)), Key: randomKey(t)}
		},
		Rotate: func(t *testing.T, p secrets.KeyProvider) secrets.KeyProvider {
			old := p.(*secrets.LocalProvider)
			previous := map[string][]byte{old.KeyID: old.Key}
			for id, key := range old.Previous {
				previous[id] = key
			}
			return &secrets.LocalProvider{KeyID: old.KeyID + "-next", Key: randomKey(t), Previous: previous}
		},
		Retire: func(_ *testing.T, p secrets.KeyProvider) secrets.KeyProvider {
			current := p.(*secrets.LocalProvider)
			return &secrets.LocalProvider{KeyID: current.KeyID, Key: current.Key}
		},
	})
}

// Key IDs written before Phase 16 have no "local:" prefix; the local
// provider still owns and unwraps them, so no migration is needed.
func TestLocalProviderReadsBareKeyIDs(t *testing.T) {
	ctx := context.Background()
	key := randomKey(t)
	p := &secrets.LocalProvider{KeyID: "bootstrap-v1", Key: key}
	id, _ := p.CurrentKeyID(ctx)
	if id != "local:bootstrap-v1" {
		t.Fatalf("current key ID = %q", id)
	}
	wrapped, err := p.Wrap(ctx, id, bytes.Repeat([]byte{1}, 32), []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Owns("bootstrap-v1") || p.Owns("vault:bootstrap-v1") {
		t.Fatal("bare IDs belong to the local provider; other prefixes do not")
	}
	// The same root key under either spelling (the AAD carries the stored
	// spelling separately, so this only proves key lookup).
	if _, err := p.Unwrap(ctx, "bootstrap-v1", wrapped, []byte("aad")); err != nil {
		t.Fatalf("bare key ID: %v", err)
	}
}
