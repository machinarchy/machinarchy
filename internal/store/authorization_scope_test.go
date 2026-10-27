package store_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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

	host := &domain.Host{
		WorkspaceID:   ownerA.WorkspaceID,
		EnvironmentID: envB.ID,
		Name:          "cross-workspace-reference",
		SSH:           &domain.SSHAccess{Username: "deploy"},
	}
	if err := stores.Hosts.CreateHost(ctx, host); !errors.Is(err, domain.ErrEnvironmentNotFound) {
		t.Fatalf("create host with foreign environment error = %v, want not found", err)
	}
	host.EnvironmentID = envA.ID
	host.Name = "workspace-a-host"
	if err := stores.Hosts.CreateHost(ctx, host); err != nil {
		t.Fatalf("create workspace A host: %v", err)
	}
	hostID, err := strconv.Atoi(host.ID)
	if err != nil {
		t.Fatalf("parse host ID: %v", err)
	}
	if _, err := stores.Hosts.GetHostByIDInWorkspace(ctx, workspaceB, hostID); !errors.Is(err, domain.ErrHostNotFound) {
		t.Fatalf("cross-workspace host lookup error = %v, want not found", err)
	}
	host.EnvironmentID = envB.ID
	if err := stores.Hosts.UpdateHost(ctx, host); !errors.Is(err, domain.ErrEnvironmentNotFound) {
		t.Fatalf("update host with foreign environment error = %v, want not found", err)
	}
	if err := stores.Hosts.DeleteHost(ctx, workspaceB, host.ID); !errors.Is(err, domain.ErrHostNotFound) {
		t.Fatalf("cross-workspace host delete error = %v, want not found", err)
	}

	job := &domain.Job{WorkspaceID: ownerA.WorkspaceID, HostID: hostID, Command: "internal", Status: domain.JobStatusPending}
	if err := stores.Jobs.CreateJob(ctx, job); err != nil {
		t.Fatalf("create workspace A job: %v", err)
	}
	if _, err := stores.Jobs.GetJobByID(ctx, workspaceB, job.ID); err == nil {
		t.Fatalf("cross-workspace job lookup unexpectedly succeeded")
	}
	jobsB, totalB, err := stores.Jobs.ListJobs(ctx, domain.JobFilter{WorkspaceID: workspaceB, Limit: 10})
	if err != nil || totalB != 0 || len(jobsB) != 0 {
		t.Fatalf("workspace B jobs = %#v total=%d err=%v; want none", jobsB, totalB, err)
	}

	if err := stores.Metrics.SaveMetricPoint(ctx, ownerA.WorkspaceID, 10, 20); err != nil {
		t.Fatalf("save workspace A metrics: %v", err)
	}
	if err := stores.Metrics.SaveMetricPoint(ctx, workspaceB, 80, 90); err != nil {
		t.Fatalf("save workspace B metrics: %v", err)
	}
	metricsA, err := stores.Metrics.GetStoredHistory(ctx, ownerA.WorkspaceID, 10)
	if err != nil || len(metricsA) != 1 || metricsA[0].CPUPercent != 10 {
		t.Fatalf("workspace A metrics = %#v err=%v; want only A's point", metricsA, err)
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

func TestSQLiteMigrationAssignsLegacyDataToEarliestUser(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "legacy.db")
	if err := os.Chmod(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatalf("restrict test database directory: %v", err)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate migration test source")
	}
	migrationDir := filepath.Join(filepath.Dir(sourceFile), "migrations", "sqlite")
	for _, migration := range []string{"000001_init_hosts.up.sql", "000002_application_tables.up.sql"} {
		content, err := os.ReadFile(filepath.Join(migrationDir, migration))
		if err != nil {
			t.Fatalf("read legacy migration %s: %v", migration, err)
		}
		if _, err := db.ExecContext(ctx, string(content)); err != nil {
			t.Fatalf("apply legacy migration %s: %v", migration, err)
		}
	}
	for _, version := range []int{1, 2} {
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (?)`, version); err != nil {
			t.Fatalf("record legacy migration %d: %v", version, err)
		}
	}
	for _, user := range []struct{ name, email string }{
		{name: "oldest", email: "oldest@example.test"},
		{name: "later", email: "later@example.test"},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO users(username, email, password_hash, created_at)
			VALUES (?, ?, 'hash', '2026-01-01T00:00:00Z')`, user.name, user.email); err != nil {
			t.Fatalf("seed legacy user %q: %v", user.name, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO environments(name, description, created_at)
		VALUES ('legacy-production', 'legacy', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed legacy environment: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO hosts
		(id, environment_id, name, hostname, ip_address, connection, status, labels, port,
		username, password, private_key, passphrase, expected_fingerprint, created_at, updated_at)
		VALUES ('1', 1, 'legacy-host', 'legacy-host', '192.0.2.1', 'SSH', 'PENDING', '{}', 22,
		'deploy', '', '', '', 'SHA256:legacy', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed legacy host: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO jobs(host_id, command, status, started_at)
		VALUES (1, 'legacy', 'SUCCEEDED', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed legacy job: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO metrics_history(timestamp, cpu_percent, mem_percent)
		VALUES ('2026-01-01T00:00:00Z', 25, 50)`); err != nil {
		t.Fatalf("seed legacy metric: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seeded legacy database: %v", err)
	}

	stores, err := store.Open(ctx, store.Config{
		Engine:           store.EngineSQLite,
		SQLitePath:       databasePath,
		EncryptionSecret: "integration-test-secret-with-more-than-thirty-two-bytes",
	})
	if err != nil {
		t.Fatalf("upgrade legacy database: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close upgraded stores: %v", err)
		}
	})
	oldest, err := stores.Users.GetUserByUsername(ctx, "oldest")
	if err != nil {
		t.Fatalf("load earliest account: %v", err)
	}
	later, err := stores.Users.GetUserByUsername(ctx, "later")
	if err != nil {
		t.Fatalf("load later account: %v", err)
	}
	if oldest.WorkspaceID != 1 || oldest.Role != domain.RoleOwner || later.WorkspaceID == oldest.WorkspaceID {
		t.Fatalf("legacy account workspaces: oldest=%+v later=%+v", oldest, later)
	}
	legacyHosts, err := stores.Hosts.ListAllHosts(ctx, oldest.WorkspaceID)
	if err != nil || len(legacyHosts) != 1 || legacyHosts[0].ID != "1" {
		t.Fatalf("legacy host inventory = %#v err=%v; want record assigned to earliest user", legacyHosts, err)
	}
	laterHosts, err := stores.Hosts.ListAllHosts(ctx, later.WorkspaceID)
	if err != nil || len(laterHosts) != 0 {
		t.Fatalf("later user's hosts = %#v err=%v; want empty workspace", laterHosts, err)
	}
	legacyEnvironments, err := stores.Environments.ListEnvironments(ctx, oldest.WorkspaceID)
	if err != nil || len(legacyEnvironments) != 1 {
		t.Fatalf("legacy environments = %#v err=%v", legacyEnvironments, err)
	}
	laterEnvironments, err := stores.Environments.ListEnvironments(ctx, later.WorkspaceID)
	if err != nil {
		t.Fatalf("list later user's environments: %v", err)
	}
	if len(laterEnvironments) != 0 {
		t.Fatalf("later user's environments = %#v; want empty workspace", laterEnvironments)
	}
	legacyJobs, total, err := stores.Jobs.ListJobs(ctx, domain.JobFilter{WorkspaceID: oldest.WorkspaceID, Limit: 10})
	if err != nil || total != 1 || len(legacyJobs) != 1 {
		t.Fatalf("legacy jobs = %#v total=%d err=%v", legacyJobs, total, err)
	}
	legacyMetrics, err := stores.Metrics.GetStoredHistory(ctx, oldest.WorkspaceID, 10)
	if err != nil || len(legacyMetrics) != 1 || legacyMetrics[0].CPUPercent != 25 {
		t.Fatalf("legacy metrics = %#v err=%v", legacyMetrics, err)
	}
}

func TestSQLiteSignupsShareDefaultWorkspaceAndBootstrapOwnerIsIdempotent(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "machinarchy.db")
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
