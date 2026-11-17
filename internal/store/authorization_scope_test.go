package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/service"
	"github.com/machinarchy/machinarchy/internal/store"
	_ "modernc.org/sqlite"
)

func TestSQLiteWorkspaceAuthorizationAndAPITokens(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "machinarchy.db")
	// #nosec G302 -- Directories require execute permission for traversal.
	if err := os.Chmod(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatalf("restrict test database directory: %v", err)
	}
	stores, err := store.Open(ctx, store.Config{
		Engine:           store.EngineSQLite,
		SQLitePath:       databasePath,
		EncryptionSecret: "integration-test-secret-with-more-than-thirty-two-bytes",
	})
	if err != nil {
		t.Fatalf("open migrated SQLite store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close stores: %v", err)
		}
	})

	ownerA, err := stores.Users.CreateUser(ctx, "owner-a", "owner-a@example.test", "hash-a")
	if err != nil {
		t.Fatalf("create first user: %v", err)
	}
	ownerB, err := stores.Users.CreateUser(ctx, "owner-b", "owner-b@example.test", "hash-b")
	if err != nil {
		t.Fatalf("create second user: %v", err)
	}
	// Single shared workspace: every signup joins the default workspace as a
	// read-only member, and no extra workspace rows are created.
	if ownerA.WorkspaceID != domain.DefaultWorkspaceID || ownerB.WorkspaceID != domain.DefaultWorkspaceID ||
		ownerA.Role != domain.RoleReader || ownerB.Role != domain.RoleReader {
		t.Fatalf("new users must join the default workspace as readers: A=%+v B=%+v", ownerA, ownerB)
	}
	if err := stores.Users.UpdateWorkspaceMemberRole(ctx, ownerA.WorkspaceID, ownerA.ID, domain.RoleOwner); err != nil {
		t.Fatalf("promote first user to owner: %v", err)
	}
	// Tenant isolation is a storage property that must keep working for future
	// multi-workspace use, so a second workspace is created directly.
	workspaceBResult, err := stores.DB.ExecContext(ctx, `INSERT INTO workspaces (name, created_at) VALUES (?, ?)`,
		"workspace-b", time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("create second workspace: %v", err)
	}
	workspaceBID, err := workspaceBResult.LastInsertId()
	if err != nil {
		t.Fatalf("read second workspace ID: %v", err)
	}
	workspaceB := int(workspaceBID)

	envA := &domain.Environment{Name: "production", WorkspaceID: ownerA.WorkspaceID}
	if err := stores.Environments.CreateEnvironment(ctx, envA); err != nil {
		t.Fatalf("create workspace A environment: %v", err)
	}
	envB := &domain.Environment{Name: "production", WorkspaceID: workspaceB}
	if err := stores.Environments.CreateEnvironment(ctx, envB); err != nil {
		t.Fatalf("create workspace B environment: %v", err)
	}
	if err := stores.Environments.CreateEnvironment(ctx, &domain.Environment{
		WorkspaceID: ownerA.WorkspaceID, Name: "production",
	}); !errors.Is(err, domain.ErrEnvironmentAlreadyExists) {
		t.Fatalf("duplicate environment name in one workspace error = %v, want conflict", err)
	}

	visibleA, err := stores.Environments.ListEnvironments(ctx, ownerA.WorkspaceID)
	if err != nil || len(visibleA) != 1 || visibleA[0].ID != envA.ID {
		t.Fatalf("workspace A environments = %#v, err=%v; want only env A", visibleA, err)
	}
	if _, err := stores.Environments.GetEnvironmentByID(ctx, ownerA.WorkspaceID, envB.ID); !errors.Is(err, domain.ErrEnvironmentNotFound) {
		t.Fatalf("cross-workspace environment lookup error = %v, want not found", err)
	}

	tokenService, err := service.NewAPITokenService(stores.Access)
	if err != nil {
		t.Fatalf("create API token service: %v", err)
	}
	issued, err := tokenService.Create(ctx, domain.Principal{
		UserID: ownerA.ID, WorkspaceID: ownerA.WorkspaceID, Role: domain.RoleOwner,
	}, "ci-reader", domain.RoleReader, 24)
	if err != nil {
		t.Fatalf("create API token: %v", err)
	}
	resolved, err := stores.Access.ResolveAPIToken(ctx, service.HashAPIToken(issued.Token))
	if err != nil || resolved == nil || resolved.Role != domain.RoleReader || resolved.WorkspaceID != ownerA.WorkspaceID {
		t.Fatalf("resolve API token = %+v err=%v", resolved, err)
	}
	if resolved.Role.Allows(domain.PermissionInventoryWrite) {
		t.Fatal("reader API token unexpectedly has inventory write permission")
	}
	var persistedHash string
	if err := stores.DB.QueryRowContext(ctx, `SELECT token_hash FROM api_tokens WHERE id = ?`, issued.ID).Scan(&persistedHash); err != nil {
		t.Fatalf("read persisted API token digest: %v", err)
	}
	if persistedHash != service.HashAPIToken(issued.Token) {
		t.Fatal("persisted API token value is not the secret's SHA-256 digest")
	}
	if _, err := tokenService.Create(ctx, domain.Principal{
		UserID: ownerA.ID, WorkspaceID: ownerA.WorkspaceID, Role: domain.RoleOwner,
	}, "ci-reader", domain.RoleReader, 24); !errors.Is(err, domain.ErrAPITokenAlreadyExists) {
		t.Fatalf("duplicate API token name error = %v, want conflict", err)
	}
	operatorToken, err := tokenService.Create(ctx, domain.Principal{
		UserID: ownerA.ID, WorkspaceID: ownerA.WorkspaceID, Role: domain.RoleOwner,
	}, "ci-operator", domain.RoleOperator, 24)
	if err != nil {
		t.Fatalf("create operator API token: %v", err)
	}
	if _, err := stores.DB.ExecContext(ctx, `UPDATE workspace_memberships SET role = ?
		WHERE user_id = ? AND workspace_id = ?`, string(domain.RoleReader), ownerA.ID, ownerA.WorkspaceID); err != nil {
		t.Fatalf("downgrade current membership: %v", err)
	}
	resolved, err = stores.Access.ResolveAPIToken(ctx, service.HashAPIToken(operatorToken.Token))
	if err != nil || resolved == nil || resolved.Role != domain.RoleReader {
		t.Fatalf("operator token after membership downgrade = %+v err=%v; want reader", resolved, err)
	}
	if _, err := stores.DB.ExecContext(ctx, `UPDATE workspace_memberships SET role = ?
		WHERE user_id = ? AND workspace_id = ?`, string(domain.RoleOwner), ownerA.ID, ownerA.WorkspaceID); err != nil {
		t.Fatalf("restore owner membership: %v", err)
	}
	listed, err := tokenService.List(ctx, domain.Principal{
		UserID: ownerA.ID, WorkspaceID: ownerA.WorkspaceID, Role: domain.RoleOwner,
	})
	if err != nil || len(listed) != 2 || listed[0].TokenHash != "" || listed[1].TokenHash != "" {
		t.Fatalf("listed token metadata = %#v err=%v; want hash excluded", listed, err)
	}
	if err := tokenService.Revoke(ctx, domain.Principal{
		UserID: ownerA.ID, WorkspaceID: ownerA.WorkspaceID, Role: domain.RoleOwner,
	}, issued.ID); err != nil {
		t.Fatalf("revoke API token: %v", err)
	}
	if _, err := stores.Access.ResolveAPIToken(ctx, service.HashAPIToken(issued.Token)); !errors.Is(err, domain.ErrAPITokenNotFound) {
		t.Fatalf("resolve revoked token error = %v, want token not found", err)
	}
	expiredAt := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := stores.DB.ExecContext(ctx, `UPDATE api_tokens SET expires_at = ? WHERE id = ?`, expiredAt, operatorToken.ID); err != nil {
		t.Fatalf("expire operator token: %v", err)
	}
	if _, err := stores.Access.ResolveAPIToken(ctx, service.HashAPIToken(operatorToken.Token)); !errors.Is(err, domain.ErrAPITokenNotFound) {
		t.Fatalf("resolve expired token error = %v, want token not found", err)
	}
	if time.Until(*issued.ExpiresAt) <= 0 {
		t.Fatal("issued token should have a future expiry")
	}
}

func TestSQLiteSignupsShareDefaultWorkspaceAndBootstrapOwnerIsIdempotent(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "machinarchy.db")
	// #nosec G302 -- Directories require execute permission for traversal.
	if err := os.Chmod(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatalf("restrict test database directory: %v", err)
	}
	stores, err := store.Open(ctx, store.Config{
		Engine:           store.EngineSQLite,
		SQLitePath:       databasePath,
		EncryptionSecret: "integration-test-secret-with-more-than-thirty-two-bytes",
	})
	if err != nil {
		t.Fatalf("open migrated SQLite store: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })

	owners, err := stores.Users.CountWorkspaceOwners(ctx, domain.DefaultWorkspaceID)
	if err != nil || owners != 0 {
		t.Fatalf("fresh database owners = %d, err=%v; want 0", owners, err)
	}

	created, err := service.BootstrapOwner(ctx, stores.Users, "admin", "admin@example.test", "a-long-test-password")
	if err != nil || !created {
		t.Fatalf("first bootstrap = %v, %v; want created", created, err)
	}
	created, err = service.BootstrapOwner(ctx, stores.Users, "admin", "admin@example.test", "a-long-test-password")
	if err != nil || created {
		t.Fatalf("second bootstrap = %v, %v; want no-op", created, err)
	}
	admin, err := stores.Users.GetUserByUsername(ctx, "admin")
	if err != nil || admin.Role != domain.RoleOwner || admin.WorkspaceID != domain.DefaultWorkspaceID {
		t.Fatalf("bootstrap admin = %+v, err=%v; want owner in the default workspace", admin, err)
	}

	for _, name := range []string{"reader-1", "reader-2", "reader-3"} {
		user, err := stores.Users.CreateUser(ctx, name, name+"@example.test", "hash")
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if user.Role != domain.RoleReader || user.WorkspaceID != domain.DefaultWorkspaceID {
			t.Fatalf("%s = %+v, want reader in default workspace", name, user)
		}
		stored, err := stores.Users.GetUserByUsername(ctx, name)
		if err != nil || stored.Role != domain.RoleReader {
			t.Fatalf("persisted %s = %+v, err=%v; the stored role must match the returned role", name, stored, err)
		}
	}

	var workspaces int
	if err := stores.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces`).Scan(&workspaces); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	if workspaces != 1 {
		t.Fatalf("workspace rows = %d, want 1; signups must not create orphan workspaces", workspaces)
	}
	owners, err = stores.Users.CountWorkspaceOwners(ctx, domain.DefaultWorkspaceID)
	if err != nil || owners != 1 {
		t.Fatalf("owners = %d, err=%v; want exactly 1", owners, err)
	}
	if err := stores.Users.UpdateWorkspaceMemberRole(ctx, domain.DefaultWorkspaceID, 9999, domain.RoleOwner); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("promoting a non-member error = %v, want ErrUserNotFound", err)
	}
}
