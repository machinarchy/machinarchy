// Package providertest is the contract every secrets.KeyProvider must pass
// (AUDIT/10 Phase 16.6). The built-in local provider runs it in
// internal/secrets; each optional KMS provider (K-phases) runs it against the
// real service or an emulator. Passing it is what "adding a provider" means.
package providertest

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/machinarchy/machinarchy/internal/secrets"
)

// Harness builds providers for the contract.
type Harness struct {
	// New returns a fresh provider with one current key.
	New func(t *testing.T) secrets.KeyProvider
	// Rotate returns a provider whose current key is new and which still
	// unwraps data wrapped by p's current key (the old key retained).
	Rotate func(t *testing.T, p secrets.KeyProvider) secrets.KeyProvider
	// Retire returns a provider with p's current key and none of the older
	// ones, as after the old key is removed.
	Retire func(t *testing.T, p secrets.KeyProvider) secrets.KeyProvider
}

// Run runs the contract.
func Run(t *testing.T, h Harness) {
	t.Helper()
	ctx := context.Background()
	dataKey := bytes.Repeat([]byte{0x5a}, 32)
	aad := []byte("dek:secret/abc/1/purpose")

	t.Run("key IDs name the provider", func(t *testing.T) {
		p := h.New(t)
		id, err := p.CurrentKeyID(ctx)
		if err != nil || !strings.HasPrefix(id, p.Name()+":") || len(id) > 128 {
			t.Fatalf("CurrentKeyID = %q, %v; want %q prefix, at most 128 bytes", id, err, p.Name()+":")
		}
		if !p.Owns(id) || p.Owns("other-provider:key") {
			t.Fatal("Owns must accept its own key ID and refuse another provider's")
		}
	})

	t.Run("wrap and unwrap", func(t *testing.T) {
		p := h.New(t)
		id := currentKeyID(t, p)
		wrapped, err := p.Wrap(ctx, id, dataKey, aad)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(wrapped, dataKey) {
			t.Fatal("the wrapped key contains the data key in clear")
		}
		got, err := p.Unwrap(ctx, id, wrapped, aad)
		if err != nil || !bytes.Equal(got, dataKey) {
			t.Fatalf("Unwrap = %x, %v", got, err)
		}
	})

	t.Run("anything changed fails", func(t *testing.T) {
		p := h.New(t)
		id := currentKeyID(t, p)
		wrapped, err := p.Wrap(ctx, id, dataKey, aad)
		if err != nil {
			t.Fatal(err)
		}
		tampered := append([]byte(nil), wrapped...)
		tampered[len(tampered)-1] ^= 1
		for name, try := range map[string]func() ([]byte, error){
			"other AAD":      func() ([]byte, error) { return p.Unwrap(ctx, id, wrapped, []byte("dek:secret/abc/2/purpose")) },
			"tampered":       func() ([]byte, error) { return p.Unwrap(ctx, id, tampered, aad) },
			"unknown key ID": func() ([]byte, error) { return p.Unwrap(ctx, p.Name()+":no-such-key", wrapped, aad) },
		} {
			if got, err := try(); err == nil || got != nil {
				t.Errorf("%s: unwrap succeeded", name)
			}
		}
		if _, err := p.Unwrap(ctx, p.Name()+":no-such-key", wrapped, aad); !errors.Is(err, secrets.ErrKeyUnavailable) {
			t.Errorf("unknown key ID: %v, want ErrKeyUnavailable", err)
		}
	})

	t.Run("wraps only under the current key", func(t *testing.T) {
		p := h.New(t)
		if _, err := p.Wrap(ctx, p.Name()+":not-current", dataKey, aad); err == nil {
			t.Fatal("wrapped under a key that is not current")
		}
	})

	t.Run("rotation keeps old data readable until retired", func(t *testing.T) {
		before := h.New(t)
		oldID := currentKeyID(t, before)
		wrapped, err := before.Wrap(ctx, oldID, dataKey, aad)
		if err != nil {
			t.Fatal(err)
		}
		after := h.Rotate(t, before)
		newID := currentKeyID(t, after)
		if newID == oldID || !after.Owns(oldID) {
			t.Fatalf("after rotation: current %q (was %q), owns old = %v", newID, oldID, after.Owns(oldID))
		}
		if got, err := after.Unwrap(ctx, oldID, wrapped, aad); err != nil || !bytes.Equal(got, dataKey) {
			t.Fatalf("old data after rotation: %v", err)
		}
		rewrapped, err := after.Wrap(ctx, newID, dataKey, aad)
		if err != nil {
			t.Fatal(err)
		}
		retired := h.Retire(t, after)
		if _, err := retired.Unwrap(ctx, oldID, wrapped, aad); err == nil {
			t.Fatal("old data still unwraps after the old key was retired")
		}
		if got, err := retired.Unwrap(ctx, newID, rewrapped, aad); err != nil || !bytes.Equal(got, dataKey) {
			t.Fatalf("rewrapped data after retirement: %v", err)
		}
	})

	t.Run("concurrent use", func(t *testing.T) {
		p := h.New(t)
		id := currentKeyID(t, p)
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		for range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				wrapped, err := p.Wrap(ctx, id, dataKey, aad)
				if err == nil {
					_, err = p.Unwrap(ctx, id, wrapped, aad)
				}
				if err != nil {
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
	})

	t.Run("a keyring routes old data during a provider switch", func(t *testing.T) {
		old := h.New(t)
		oldID := currentKeyID(t, old)
		wrapped, err := old.Wrap(ctx, oldID, dataKey, aad)
		if err != nil {
			t.Fatal(err)
		}
		next := h.Rotate(t, h.New(t)) // stands in for "another provider"
		ring := &secrets.Keyring{Current: next, Previous: []secrets.KeyProvider{old}}
		if got, err := ring.Unwrap(ctx, oldID, wrapped, aad); err != nil || !bytes.Equal(got, dataKey) {
			t.Fatalf("keyring unwrap of old data: %v", err)
		}
	})
}

func currentKeyID(t *testing.T, p secrets.KeyProvider) string {
	t.Helper()
	id, err := p.CurrentKeyID(context.Background())
	if err != nil {
		t.Fatalf("CurrentKeyID: %v", err)
	}
	return id
}
