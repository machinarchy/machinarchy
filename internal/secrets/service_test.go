package secrets_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/store"
	"github.com/machinarchy/machinarchy/internal/store/management"
	"github.com/machinarchy/machinarchy/pkg/crypto"
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

// resolveAs resolves like a runtime workload, with the given purpose.
func resolveAs(s *secrets.Service, ref secrets.Ref, purpose secrets.Purpose) ([]byte, error) {
	return s.Resolve(context.Background(), secrets.ResolutionRequest{WorkloadID: secrets.WorkloadControlManager, Ref: ref,
		Purpose: purpose, WorkspaceID: 1, AccessProfileID: "profile", AccessProfileRevision: 1, HostID: "host"})
}

// TestSecretLifecycle covers the Secret Store end to end on a real database:
// owner-only writes, versioned rotation with a precondition, write-only
// values, resolution through the authorizer, key rotation (rewrap) and
// retirement.
func TestSecretLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := repository(t)
	keys := &secrets.LocalProvider{KeyID: "kek-a", Key: keyA}
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
	keys2 := &secrets.LocalProvider{KeyID: "kek-b", Key: keyB, Previous: map[string][]byte{"kek-a": keyA}}
	s2 := service(t, repo, keys2, allowAll{})
	result, err := s2.RewrapKey(ctx, owner, "kek-a", 100)
	if err != nil || result.Rewrapped != 2 || result.Remaining != 0 {
		t.Fatalf("rewrap = %+v, %v", result, err)
	}
	s3 := service(t, repo, &secrets.LocalProvider{KeyID: "kek-b", Key: keyB}, allowAll{})
	if value, err := resolve(s3, secrets.Ref{ID: created.ID, Version: 2}); err != nil || string(value) != "second-password" {
		t.Fatalf("resolve after rewrap without the old key = %q, %v", value, err)
	}
	wrongKey := service(t, repo, &secrets.LocalProvider{KeyID: "kek-b", Key: keyA}, allowAll{})
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
	s := service(t, repository(t), &secrets.LocalProvider{KeyID: "k", Key: keyA}, secrets.DenyAllResolutionAuthorizer{})
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

// Phase 16 upgrade path: a secret stored before the provider interface (bare
// key ID "kek-a", wrapped with that exact ID in the AAD) still resolves with
// the local provider, and rewrapping it by the bare ID the operator knows
// moves it to the new "local:" key.
func TestSecretsStoredBeforeProvidersStillWork(t *testing.T) {
	ctx := context.Background()
	repo := repository(t)
	const id = "legacy-secret-0001"
	dek := bytes.Repeat([]byte{9}, 32)
	wrapAAD := fmt.Sprintf("machinarchy/secret/v1/%s/%d/%s/%s", id, 1, "kek-a", secrets.PurposeSSHCredential)
	wrapped, err := crypto.EncryptBytes(dek, keyA, []byte("dek:"+wrapAAD))
	if err != nil {
		t.Fatal(err)
	}
	valueAAD := fmt.Sprintf("machinarchy/secret/value/v2/%s/%d/%s", id, 1, secrets.PurposeSSHCredential)
	sealed, err := crypto.EncryptBytes([]byte("legacy-password"), dek, []byte("value:"+valueAAD))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := repo.CreateSecret(ctx, secrets.Metadata{ID: id, Name: "legacy", Purpose: secrets.PurposeSSHCredential, CurrentVersion: 1, CreatedAt: now, UpdatedAt: now},
		secrets.StoredVersion{Version: secrets.Version{Ref: secrets.Ref{ID: id, Version: 1}, CreatedAt: now, CreatedBy: 1},
			Ciphertext: secrets.Ciphertext{KeyID: "kek-a", AADVersion: 2, WrappedDEK: wrapped, Value: sealed}}); err != nil {
		t.Fatal(err)
	}

	before := service(t, repo, &secrets.LocalProvider{KeyID: "kek-a", Key: keyA}, allowAll{})
	if value, err := resolve(before, secrets.Ref{ID: id, Version: 1}); err != nil || string(value) != "legacy-password" {
		t.Fatalf("legacy secret after upgrade = %q, %v", value, err)
	}
	rotated := service(t, repo, &secrets.LocalProvider{KeyID: "kek-b", Key: keyB, Previous: map[string][]byte{"kek-a": keyA}}, allowAll{})
	result, err := rotated.RewrapKey(ctx, owner, "kek-a", 10)
	if err != nil || result.Rewrapped != 1 || result.Remaining != 0 || result.KeyID != "local:kek-b" {
		t.Fatalf("rewrap of a legacy key ID = %+v, %v", result, err)
	}
	onlyNew := service(t, repo, &secrets.LocalProvider{KeyID: "kek-b", Key: keyB}, allowAll{})
	if value, err := resolve(onlyNew, secrets.Ref{ID: id, Version: 1}); err != nil || string(value) != "legacy-password" {
		t.Fatalf("after rewrap without the old key = %q, %v", value, err)
	}
}

// Phase 16: the controller's own secrets (the agent signing seed) use the
// same envelope, but the owner API and runtime workloads cannot reach them.
func TestSystemSecretsAreOnlyForTheController(t *testing.T) {
	ctx := context.Background()
	s := service(t, repository(t), &secrets.LocalProvider{KeyID: "k", Key: keyA}, allowAll{})
	ref, err := s.CreateSystem(ctx, "agent-gateway-signing-key-x", secrets.PurposeAgentSigning, []byte("seed-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if value, err := s.ResolveSystem(ctx, ref, secrets.PurposeAgentSigning); err != nil || string(value) != "seed-bytes" {
		t.Fatalf("ResolveSystem = %q, %v", value, err)
	}
	if _, err := resolveAs(s, ref, secrets.PurposeAgentSigning); !errors.Is(err, secrets.ErrInvalid) && !errors.Is(err, secrets.ErrUnauthorized) {
		t.Fatalf("a workload resolved a system secret: %v", err)
	}
	if _, err := s.Rotate(ctx, owner, ref.ID, 1, secrets.Input{Value: "x"}); !errors.Is(err, secrets.ErrInvalid) {
		t.Fatalf("owner rotate of a system secret = %v", err)
	}
	if err := s.Delete(ctx, owner, ref.ID); !errors.Is(err, secrets.ErrInvalid) {
		t.Fatalf("owner delete of a system secret = %v", err)
	}
	if _, err := s.Create(ctx, owner, secrets.Input{Name: "fake", Purpose: secrets.PurposeAgentSigning, Value: "x"}); !errors.Is(err, secrets.ErrInvalid) {
		t.Fatalf("owner created a system-purpose secret: %v", err)
	}
	// AUDIT F16-06: an unused system secret can be retired, and an owner's
	// secret cannot be retired through the system path.
	orphan, err := s.CreateSystem(ctx, "agent-gateway-signing-key-orphan", secrets.PurposeAgentSigning, []byte("unused-seed"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RetireSystem(ctx, orphan, secrets.PurposeAgentSigning); err != nil {
		t.Fatalf("RetireSystem = %v", err)
	}
	if err := s.RetireSystem(ctx, orphan, secrets.PurposeAgentSigning); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("retiring twice = %v, want ErrNotFound", err)
	}
	if err := s.RetireSystem(ctx, ref, secrets.PurposeSSHCredential); !errors.Is(err, secrets.ErrInvalid) {
		t.Fatalf("RetireSystem with an owner purpose = %v", err)
	}
	ownerSecret, err := s.Create(ctx, owner, secrets.Input{Name: "keep-me", Purpose: secrets.PurposeSSHCredential, Value: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RetireSystem(ctx, secrets.Ref{ID: ownerSecret.ID, Version: 1}, secrets.PurposeAgentSigning); !errors.Is(err, secrets.ErrUnauthorized) {
		t.Fatalf("retiring an owner's secret through the system path = %v", err)
	}
	if _, err := s.CreateSystem(ctx, "x", secrets.PurposeSSHCredential, []byte("x")); !errors.Is(err, secrets.ErrInvalid) {
		t.Fatalf("CreateSystem with an owner purpose = %v", err)
	}
	user, err := s.Create(ctx, owner, secrets.Input{Name: "deploy", Purpose: secrets.PurposeSSHCredential, Value: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveSystem(ctx, secrets.Ref{ID: user.ID, Version: 1}, secrets.PurposeAgentSigning); !errors.Is(err, secrets.ErrUnauthorized) {
		t.Fatalf("ResolveSystem of an owner secret = %v", err)
	}
}
