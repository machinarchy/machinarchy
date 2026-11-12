package management_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/runtimeconfig"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/store"
	"github.com/machinarchy/machinarchy/internal/store/management"
	"github.com/machinarchy/machinarchy/internal/store/sqlite"
	"github.com/machinarchy/machinarchy/pkg/crypto"
)

type fixture struct {
	db   *sql.DB
	repo *management.Store
	path string
}

func openFixture(t *testing.T, path string) fixture {
	t.Helper()
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.OpenDB(sqlite.Config{DatabasePath: path})
	if err != nil {
		t.Fatal(err)
	}
	if err = sqlite.Configure(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err = store.NewMigrator(db, store.EngineSQLite, nil).Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	repo, err := management.New(db, management.SQLite, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return fixture{db: db, repo: repo, path: path}
}

func TestConfigurationRevisionsConflictValidationHistoryAndOutbox(t *testing.T) {
	ctx := context.Background()
	f := openFixture(t, filepath.Join(t.TempDir(), "config.db"))
	defer f.db.Close()
	svc, manager := newConfigService(t, f.repo, "process-a")
	if err := svc.Bootstrap(ctx, runtimeconfig.Defaults()); err != nil {
		t.Fatal(err)
	}
	initial, err := svc.Current(ctx)
	if err != nil || initial.Revision != 1 {
		t.Fatalf("initial revision=%+v err=%v", initial, err)
	}
	bad := initial.Config
	bad.Logging.Level = "trace"
	if err = bad.Validate(); !errors.Is(err, runtimeconfig.ErrInvalid) {
		t.Fatalf("invalid config err=%v", err)
	}
	owner := domain.Principal{UserID: 7, WorkspaceID: 1, Role: domain.RoleOwner}
	next := initial.Config
	next.Logging.Level = "debug"
	published, err := svc.Publish(ctx, owner, 1, next, "raise verbosity")
	if err != nil || published.Revision != 2 {
		t.Fatalf("publish=%+v err=%v", published, err)
	}
	if _, err = svc.Publish(ctx, owner, 1, next, "stale"); !errors.Is(err, runtimeconfig.ErrConflict) {
		t.Fatalf("stale write err=%v", err)
	}
	if _, err = svc.List(ctx, 20, 0); err != nil {
		t.Fatal(err)
	}
	history, err := svc.List(ctx, 20, 0)
	if err != nil || len(history) != 2 {
		t.Fatalf("history len=%d err=%v", len(history), err)
	}
	if _, err = svc.Rollback(ctx, owner, 2, 1, "revert"); err != nil {
		t.Fatal(err)
	}
	current, _ := svc.Current(ctx)
	if current.Revision != 3 || current.Config.Logging.Level != "info" {
		t.Fatalf("rollback did not create new revision: %+v", current)
	}
	var count int
	if err = f.db.QueryRow(`SELECT count(*) FROM outbox_events WHERE event_type=?`, "machinarchy.runtime-config.changed").Scan(&count); err != nil || count != 2 {
		t.Fatalf("config outbox count=%d err=%v", count, err)
	}
	if snap, ok := manager.Current(); !ok || snap.Revision.Revision != 3 {
		t.Fatalf("snapshot not advanced: %+v ok=%v", snap, ok)
	}
}

func TestFailedSnapshotApplyKeepsLastGoodAndProcessRestartReloads(t *testing.T) {
	ctx := context.Background()
	f := openFixture(t, filepath.Join(t.TempDir(), "snapshot.db"))
	defer f.db.Close()
	if _, err := f.repo.Seed(ctx, runtimeconfig.Defaults()); err != nil {
		t.Fatal(err)
	}
	shouldFail := false
	applier := failingApplier{fail: &shouldFail}
	manager, err := runtimeconfig.NewSnapshotManager(f.repo, "process-a", "api", applier)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	first, _ := manager.Current()
	changed := first.Revision.Config
	changed.Features.SignupEnabled = true
	if _, err = f.repo.Publish(ctx, 1, changed, 1, "enable signup"); err != nil {
		t.Fatal(err)
	}
	shouldFail = true
	if err = manager.Reload(ctx); err == nil {
		t.Fatal("expected injected apply failure")
	}
	still, _ := manager.Current()
	if still.Revision.Revision != 1 {
		t.Fatalf("last good snapshot lost: %d", still.Revision.Revision)
	}
	shouldFail = false
	if err = manager.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	applied, _ := manager.Current()
	if applied.Revision.Revision != 2 || !applied.Revision.Config.Features.SignupEnabled {
		t.Fatalf("revision was not recovered: %+v", applied)
	}
	newManager, err := runtimeconfig.NewSnapshotManager(f.repo, "process-b", "control-manager")
	if err != nil {
		t.Fatal(err)
	}
	if err = newManager.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	restarted, _ := newManager.Current()
	if restarted.Revision.Revision != 2 {
		t.Fatalf("process restart loaded revision %d", restarted.Revision.Revision)
	}
}

func TestMissedConfigWakeupIsRecoveredFromOutboxAndPeriodicCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := openFixture(t, filepath.Join(t.TempDir(), "missed.db"))
	defer f.db.Close()
	if _, err := f.repo.Seed(ctx, runtimeconfig.Defaults()); err != nil {
		t.Fatal(err)
	}
	manager, err := runtimeconfig.NewSnapshotManager(f.repo, "watcher", "api")
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	go func() { _ = manager.Run(ctx, time.Hour) }()
	candidate := runtimeconfig.Defaults()
	candidate.Features.SignupEnabled = true
	if _, err = f.repo.Publish(ctx, 1, candidate, 4, "missed notification simulation"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s, ok := manager.Current(); ok && s.Revision.Revision == 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("watcher did not observe persisted outbox notification")
}

type failingApplier struct{ fail *bool }

func (a failingApplier) Prepare(context.Context, runtimeconfig.Snapshot) (runtimeconfig.PreparedApply, error) {
	if *a.fail {
		return runtimeconfig.PreparedApply{}, errors.New("synthetic prepare failure")
	}
	return runtimeconfig.PreparedApply{}, nil
}

func TestSecretEncryptionAuthorizationRotationAndDatabaseRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "secrets.db")
	f := openFixture(t, path)
	root := []byte("0123456789abcdef0123456789abcdef")
	provider := &secrets.StaticKeyProvider{KeyID: "external-v1", Key: root}
	svc, err := secrets.New(f.repo, provider, allowSecretResolution{})
	if err != nil {
		t.Fatal(err)
	}
	owner := domain.Principal{UserID: 3, WorkspaceID: 1, Role: domain.RoleOwner}
	if _, err = svc.Create(ctx, domain.Principal{UserID: 3, WorkspaceID: 1, Role: domain.RoleOperator}, secrets.Input{Name: "ssh", Purpose: secrets.PurposeSSHCredential, Value: "plain-secret-value"}); !errors.Is(err, secrets.ErrUnauthorized) {
		t.Fatalf("unauthorized create err=%v", err)
	}
	meta, err := svc.Create(ctx, owner, secrets.Input{Name: "ssh-prod", Description: "deploy key", Purpose: secrets.PurposeSSHCredential, Value: "plain-secret-value"})
	if err != nil {
		t.Fatal(err)
	}
	ref := secrets.Ref{ID: meta.ID, Version: 1}
	plain, err := svc.Resolve(ctx, resolutionRequest(ref, secrets.PurposeSSHCredential))
	if err != nil || string(plain) != "plain-secret-value" {
		t.Fatalf("resolve=%q err=%v", plain, err)
	}
	if _, err = svc.Resolve(ctx, resolutionRequest(ref, secrets.PurposeOperation)); !errors.Is(err, secrets.ErrUnauthorized) {
		t.Fatalf("wrong-purpose resolution err=%v", err)
	}
	encoded, _ := json.Marshal(meta)
	if strings.Contains(string(encoded), "plain-secret-value") {
		t.Fatal("secret metadata included plaintext")
	}
	var ciphertext string
	if err = f.db.QueryRow(`SELECT CAST(ciphertext AS TEXT) FROM secret_versions WHERE secret_id=?`, meta.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if ciphertext == "plain-secret-value" {
		t.Fatal("plaintext stored in database")
	}
	if _, err = svc.Rotate(ctx, owner, meta.ID, 1, secrets.Input{Name: "rotation", Value: "new-secret-value"}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Rotate(ctx, owner, meta.ID, 1, secrets.Input{Name: "rotation", Value: "stale-value"}); !errors.Is(err, secrets.ErrConflict) {
		t.Fatalf("stale secret rotation err=%v", err)
	}
	versions, err := svc.Versions(ctx, owner, meta.ID)
	if err != nil || len(versions) != 2 {
		t.Fatalf("secret versions=%d err=%v", len(versions), err)
	}
	wrong, err := secrets.New(f.repo, &secrets.StaticKeyProvider{KeyID: "external-v1", Key: []byte("abcdef0123456789abcdef0123456789")}, allowSecretResolution{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = wrong.Resolve(ctx, resolutionRequest(secrets.Ref{ID: meta.ID, Version: 2}, secrets.PurposeSSHCredential)); err == nil {
		t.Fatal("incorrect key decrypted secret")
	}
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f = openFixture(t, path)
	defer f.db.Close()
	svc, err = secrets.New(f.repo, provider, allowSecretResolution{})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := svc.Resolve(ctx, resolutionRequest(secrets.Ref{ID: meta.ID, Version: 2}, secrets.PurposeSSHCredential))
	if err != nil || string(loaded) != "new-secret-value" {
		t.Fatalf("restart recovery=%q err=%v", loaded, err)
	}
	if err = svc.Delete(ctx, owner, meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Resolve(ctx, resolutionRequest(secrets.Ref{ID: meta.ID, Version: 1}, secrets.PurposeSSHCredential)); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("retired secret remained resolvable: %v", err)
	}
}

func TestSecretServiceDoesNotWipeProviderOwnedKeyMaterial(t *testing.T) {
	ctx := context.Background()
	f := openFixture(t, filepath.Join(t.TempDir(), "key-copy.db"))
	defer f.db.Close()
	provider := sharedKeyProvider{keyID: "shared-v1", key: []byte("0123456789abcdef0123456789abcdef")}
	svc, err := secrets.New(f.repo, provider, allowSecretResolution{})
	if err != nil {
		t.Fatal(err)
	}
	owner := domain.Principal{UserID: 3, WorkspaceID: 1, Role: domain.RoleOwner}
	meta, err := svc.Create(ctx, owner, secrets.Input{Name: "ssh", Purpose: secrets.PurposeSSHCredential, Value: "secret-value"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Resolve(ctx, resolutionRequest(secrets.Ref{ID: meta.ID, Version: 1}, secrets.PurposeSSHCredential)); err != nil {
		t.Fatal(err)
	}
	if string(provider.key) != "0123456789abcdef0123456789abcdef" {
		t.Fatal("secret service modified key-provider-owned memory")
	}
}

func TestSecretKEKRewrapResumesInBoundedBatches(t *testing.T) {
	ctx := context.Background()
	f := openFixture(t, filepath.Join(t.TempDir(), "key-rewrap.db"))
	defer f.db.Close()
	oldKey := []byte("0123456789abcdef0123456789abcdef")
	newKey := []byte("abcdef0123456789abcdef0123456789")
	oldService, err := secrets.New(f.repo, &secrets.StaticKeyProvider{KeyID: "kek-v1", Key: oldKey}, allowSecretResolution{})
	if err != nil {
		t.Fatal(err)
	}
	owner := domain.Principal{UserID: 3, WorkspaceID: 1, Role: domain.RoleOwner}
	refs := make([]secrets.Ref, 0, 3)
	for i := 0; i < 3; i++ {
		meta, createErr := oldService.Create(ctx, owner, secrets.Input{Name: []string{"ssh-a", "ssh-b", "ssh-c"}[i], Purpose: secrets.PurposeSSHCredential, Value: "secret-value"})
		if createErr != nil {
			t.Fatal(createErr)
		}
		refs = append(refs, secrets.Ref{ID: meta.ID, Version: 1})
	}
	legacyID, err := resource.NewResourceID()
	if err != nil {
		t.Fatal(err)
	}
	legacyRef := secrets.Ref{ID: string(legacyID), Version: 1}
	legacyAAD := "machinarchy/secret/v1/" + legacyRef.ID + "/1/kek-v1/ssh-credential"
	dek := []byte("fedcba9876543210fedcba9876543210")
	wrappedDEK, err := crypto.EncryptBytes(dek, oldKey, []byte("dek:"+legacyAAD))
	if err != nil {
		t.Fatal(err)
	}
	legacyCiphertext, err := crypto.EncryptBytes([]byte("legacy-secret-value"), dek, []byte("value:"+legacyAAD))
	if err != nil {
		t.Fatal(err)
	}
	legacyCreated := time.Now().UTC()
	if err = f.repo.CreateSecret(ctx, secrets.Metadata{ID: legacyRef.ID, Name: "legacy-ssh", Purpose: secrets.PurposeSSHCredential, CurrentVersion: 1, CreatedAt: legacyCreated, UpdatedAt: legacyCreated}, secrets.StoredVersion{
		Version:    secrets.Version{Ref: legacyRef, CreatedAt: legacyCreated, CreatedBy: owner.UserID},
		Ciphertext: secrets.Ciphertext{KeyID: "kek-v1", AADVersion: 1, DataAADKeyID: "kek-v1", WrappedDEK: wrappedDEK, Value: legacyCiphertext},
	}); err != nil {
		t.Fatal(err)
	}
	refs = append(refs, legacyRef)
	provider := &secrets.StaticKeyProvider{KeyID: "kek-v2", Key: newKey, Previous: map[string][]byte{"kek-v1": oldKey}}
	service, err := secrets.New(f.repo, provider, allowSecretResolution{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.RewrapKey(ctx, domain.Principal{UserID: 4, WorkspaceID: 1, Role: domain.RoleOperator}, "kek-v1", 2); !errors.Is(err, secrets.ErrUnauthorized) {
		t.Fatalf("unauthorized rewrap err=%v", err)
	}
	result, err := service.RewrapKey(ctx, owner, "kek-v1", 2)
	if err != nil || result.Rewrapped != 2 || result.Remaining != 2 {
		t.Fatalf("first rewrap=%+v err=%v", result, err)
	}
	result, err = service.RewrapKey(ctx, owner, "kek-v1", 2)
	if err != nil || result.Rewrapped != 2 || result.Remaining != 0 {
		t.Fatalf("second rewrap=%+v err=%v", result, err)
	}
	for _, ref := range refs {
		plain, resolveErr := service.Resolve(ctx, resolutionRequest(ref, secrets.PurposeSSHCredential))
		want := "secret-value"
		if ref == legacyRef {
			want = "legacy-secret-value"
		}
		if resolveErr != nil || string(plain) != want {
			t.Fatalf("resolve %s after rewrap=%q err=%v", ref.ID, plain, resolveErr)
		}
	}
	var keyID string
	if err = f.db.QueryRow(`SELECT key_id FROM secret_versions WHERE secret_id=?`, refs[0].ID).Scan(&keyID); err != nil || keyID != "kek-v2" {
		t.Fatalf("rewrapped key ID=%q err=%v", keyID, err)
	}
}

type sharedKeyProvider struct {
	keyID string
	key   []byte
}

type allowSecretResolution struct{}

func (allowSecretResolution) AuthorizeSecretResolution(_ context.Context, request secrets.ResolutionRequest) error {
	if request.WorkloadID == "" || request.WorkspaceID <= 0 || request.AccessProfileID == "" ||
		request.AccessProfileRevision == 0 || request.HostID == "" {
		return secrets.ErrUnauthorized
	}
	return nil
}

func resolutionRequest(ref secrets.Ref, purpose secrets.Purpose) secrets.ResolutionRequest {
	return secrets.ResolutionRequest{WorkloadID: secrets.WorkloadOperationWorker, Ref: ref, Purpose: purpose,
		WorkspaceID: 1, AccessProfileID: "00000000-0000-4000-8000-000000000002", AccessProfileRevision: 1,
		HostID: "00000000-0000-4000-8000-000000000003", OperationID: "00000000-0000-4000-8000-000000000004"}
}

func TestSecretResolutionRequiresScopedWorkloadAuthorization(t *testing.T) {
	ctx := context.Background()
	f := openFixture(t, filepath.Join(t.TempDir(), "resolution-auth.db"))
	defer f.db.Close()
	owner := domain.Principal{UserID: 9, WorkspaceID: 1, Role: domain.RoleOwner}
	creator, err := secrets.New(f.repo, &secrets.StaticKeyProvider{KeyID: "k1", Key: []byte("0123456789abcdef0123456789abcdef")}, allowSecretResolution{})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := creator.Create(ctx, owner, secrets.Input{Name: "scoped", Purpose: secrets.PurposeSSHCredential, Value: "secret-value"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := secrets.New(f.repo, &secrets.StaticKeyProvider{KeyID: "k1", Key: []byte("0123456789abcdef0123456789abcdef")}, secrets.DenyAllResolutionAuthorizer{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Resolve(ctx, resolutionRequest(secrets.Ref{ID: meta.ID, Version: 1}, secrets.PurposeSSHCredential)); !errors.Is(err, secrets.ErrUnauthorized) {
		t.Fatalf("unauthorized workload resolution err=%v", err)
	}
	if _, err = service.Resolve(ctx, secrets.ResolutionRequest{Ref: secrets.Ref{ID: meta.ID, Version: 1}, Purpose: secrets.PurposeSSHCredential}); !errors.Is(err, secrets.ErrInvalid) {
		t.Fatalf("unscoped resolution err=%v", err)
	}
}

func (p sharedKeyProvider) Current(context.Context) (string, []byte, error) {
	return p.keyID, p.key, nil
}

func (p sharedKeyProvider) ByID(context.Context, string) ([]byte, error) {
	return p.key, nil
}

func newConfigService(t *testing.T, repo *management.Store, id string) (*runtimeconfig.Service, *runtimeconfig.SnapshotManager) {
	t.Helper()
	m, err := runtimeconfig.NewSnapshotManager(repo, id, "api")
	if err != nil {
		t.Fatal(err)
	}
	s, err := runtimeconfig.NewService(repo, m)
	if err != nil {
		t.Fatal(err)
	}
	return s, m
}

func TestProcessStatusUsesFreshHeartbeat(t *testing.T) {
	f := openFixture(t, filepath.Join(t.TempDir(), "status.db"))
	defer f.db.Close()
	now := time.Now().UTC()
	if err := f.repo.RecordProcessStatus(context.Background(), runtimeconfig.ProcessStatus{ProcessID: "p1", Role: "api", AppliedRevision: 1, AttemptedRevision: 1, Heartbeat: now}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.repo.ProcessStatuses(context.Background(), now.Add(-time.Minute))
	if err != nil || len(rows) != 1 {
		t.Fatalf("process statuses=%v err=%v", rows, err)
	}
}
