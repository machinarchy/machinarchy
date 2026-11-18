package secrets_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/store"
	"github.com/machinarchy/machinarchy/internal/store/management"
)

type allowAll struct{}

func (allowAll) AuthorizeSecretResolution(context.Context, secrets.ResolutionRequest) error {
	return nil
}

var (
	owner  = domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner}
	reader = domain.Principal{UserID: 2, WorkspaceID: 1, Role: domain.RoleReader}
	keyA   = bytes.Repeat([]byte{1}, 32)
	keyB   = bytes.Repeat([]byte{2}, 32)
)

func repository(t *testing.T) *management.Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stores, err := store.Open(context.Background(), store.Config{Engine: store.EngineSQLite, SQLitePath: filepath.Join(dir, "s.db"),
		EncryptionSecret: "secrets-test-key-material-that-is-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	return stores.Management
}

func service(t *testing.T, repo secrets.Repository, keys secrets.KeyProvider, authorizer secrets.ResolutionAuthorizer) *secrets.Service {
	t.Helper()
	s, err := secrets.New(repo, keys, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func resolve(s *secrets.Service, ref secrets.Ref) ([]byte, error) {
	return s.Resolve(context.Background(), secrets.ResolutionRequest{WorkloadID: secrets.WorkloadControlManager, Ref: ref,
		Purpose: secrets.PurposeSSHCredential, WorkspaceID: 1, AccessProfileID: "profile", AccessProfileRevision: 1, HostID: "host"})
}

// TestSecretLifecycle covers the Secret Store end to end on a real database:
// owner-only writes, versioned rotation with a precondition, write-only
// values, resolution through the authorizer, key rotation (rewrap) and
// retirement.
func TestSecretLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := repository(t)
	keys := &secrets.StaticKeyProvider{KeyID: "kek-a", Key: keyA}
	s := service(t, repo, keys, allowAll{})

	if _, err := s.Create(ctx, reader, secrets.Input{Name: "x", Purpose: secrets.PurposeSSHCredential, Value: "v"}); !errors.Is(err, secrets.ErrUnauthorized) {
		t.Fatalf("reader create = %v, want ErrUnauthorized", err)
	}
	if _, err := s.Create(ctx, owner, secrets.Input{Name: "x", Purpose: "nope", Value: "v"}); !errors.Is(err, secrets.ErrInvalid) {
		t.Fatalf("bad purpose = %v, want ErrInvalid", err)
	}
	created, err := s.Create(ctx, owner, secrets.Input{Name: "deploy", Purpose: secrets.PurposeSSHCredential, Value: "first-password"})
	if err != nil || created.CurrentVersion != 1 {
		t.Fatalf("create = %+v, %v", created, err)
	}
	if value, err := resolve(s, secrets.Ref{ID: created.ID, Version: 1}); err != nil || string(value) != "first-password" {
		t.Fatalf("resolve v1 = %q, %v", value, err)
	}

	if _, err := s.Rotate(ctx, owner, created.ID, 7, secrets.Input{Value: "x"}); !errors.Is(err, secrets.ErrConflict) {
		t.Fatalf("stale rotate = %v, want ErrConflict", err)
	}
	rotated, err := s.Rotate(ctx, owner, created.ID, 1, secrets.Input{Value: "second-password"})
	if err != nil || rotated.CurrentVersion != 2 {
		t.Fatalf("rotate = %+v, %v", rotated, err)
	}
	if versions, err := s.Versions(ctx, owner, created.ID); err != nil || len(versions) != 2 {
		t.Fatalf("versions = %+v, %v", versions, err)
	}
	if value, err := resolve(s, secrets.Ref{ID: created.ID, Version: 1}); err != nil || string(value) != "first-password" {
		t.Fatalf("old version still resolves to its own value: %q, %v", value, err)
	}

	// Rotate the key-encryption key: new writes use kek-b, old DEKs are
	// rewrapped, and nothing decrypts once kek-a is gone.
	keys2 := &secrets.StaticKeyProvider{KeyID: "kek-b", Key: keyB, Previous: map[string][]byte{"kek-a": keyA}}
	s2 := service(t, repo, keys2, allowAll{})
	result, err := s2.RewrapKey(ctx, owner, "kek-a", 100)
	if err != nil || result.Rewrapped != 2 || result.Remaining != 0 {
		t.Fatalf("rewrap = %+v, %v", result, err)
	}
	s3 := service(t, repo, &secrets.StaticKeyProvider{KeyID: "kek-b", Key: keyB}, allowAll{})
	if value, err := resolve(s3, secrets.Ref{ID: created.ID, Version: 2}); err != nil || string(value) != "second-password" {
		t.Fatalf("resolve after rewrap without the old key = %q, %v", value, err)
	}
	wrongKey := service(t, repo, &secrets.StaticKeyProvider{KeyID: "kek-b", Key: keyA}, allowAll{})
	if _, err := resolve(wrongKey, secrets.Ref{ID: created.ID, Version: 2}); err == nil {
		t.Fatal("a wrong key decrypted the secret")
	}

	if err := s3.Delete(ctx, owner, created.ID); err != nil {
		t.Fatal(err)
	}
	if meta, err := s3.Describe(ctx, owner, created.ID); err != nil || meta.RetiredAt == nil {
		t.Fatalf("retired metadata = %+v, %v", meta, err)
	}
}

func TestResolutionNeedsTheAuthorizer(t *testing.T) {
	ctx := context.Background()
	s := service(t, repository(t), &secrets.StaticKeyProvider{KeyID: "k", Key: keyA}, secrets.DenyAllResolutionAuthorizer{})
	created, err := s.Create(ctx, owner, secrets.Input{Name: "deploy", Purpose: secrets.PurposeSSHCredential, Value: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(s, secrets.Ref{ID: created.ID, Version: 1}); err == nil {
		t.Fatal("a denied workload resolved the secret")
	}
	if _, err := resolve(s, secrets.Ref{ID: "missing", Version: 1}); err == nil {
		t.Fatal("an unknown secret resolved")
	}
}
