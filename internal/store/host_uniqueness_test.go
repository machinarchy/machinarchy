package store_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/store"
	_ "modernc.org/sqlite"
)

func sqliteConfig(t *testing.T) store.Config {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "machinarchy.db")
	if err := os.Chmod(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatalf("restrict test database directory: %v", err)
	}
	return store.Config{
		Engine:           store.EngineSQLite,
		SQLitePath:       databasePath,
		EncryptionSecret: "integration-test-secret-with-more-than-thirty-two-bytes",
	}
}

func testHost(workspaceID int, name, address string, port int) *domain.Host {
	return &domain.Host{
		WorkspaceID: workspaceID, Name: name, Hostname: name + ".example.test", IPAddress: address,
		Connection: domain.MethodSSH,
		SSH: &domain.SSHAccess{
			Port: port, Username: "deploy", Status: domain.HostStatusPending,
			AuthType: domain.SSHAuthType{Method: domain.AuthTypePassword, Password: "pw", ExpectedFingerprint: "SHA256:test"},
		},
	}
}

func TestSQLiteHostUniquenessIsEnforcedByTheDatabase(t *testing.T) {
	ctx := context.Background()
	stores, err := store.Open(ctx, sqliteConfig(t))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })

	for _, h := range []*domain.Host{
		testHost(1, "Web-01", "10.0.0.1", 22),
		testHost(1, "dns-1", "Db.Example.Test", 22),
	} {
		if err := stores.Hosts.CreateHost(ctx, h); err != nil {
			t.Fatalf("create %s: %v", h.Name, err)
		}
	}

	conflicts := map[string]*domain.Host{
		"same name, different case":     testHost(1, "WEB-01", "10.0.0.2", 22),
		"same address and port":         testHost(1, "other", "10.0.0.1", 22),
		"same hostname, different case": testHost(1, "dns-2", "db.example.test", 22),
	}
	for name, host := range conflicts {
		if err := stores.Hosts.CreateHost(ctx, host); !errors.Is(err, domain.ErrHostAlreadyExists) {
			t.Errorf("%s: error = %v, want ErrHostAlreadyExists", name, err)
		}
	}

	if err := stores.Hosts.CreateHost(ctx, testHost(1, "web-01-alt-port", "10.0.0.1", 2222)); err != nil {
		t.Errorf("the same address on a different port is a different target: %v", err)
	}

	// Other workspaces are independent.
	result, err := stores.DB.ExecContext(ctx, `INSERT INTO workspaces (name, created_at) VALUES (?, ?)`,
		"workspace-b", time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("create second workspace: %v", err)
	}
	workspaceB64, _ := result.LastInsertId()
	if err := stores.Hosts.CreateHost(ctx, testHost(int(workspaceB64), "web-01", "10.0.0.1", 22)); err != nil {
		t.Errorf("the same name and address in another workspace must be allowed: %v", err)
	}

	hosts, err := stores.Hosts.ListAllHosts(ctx, 1)
	if err != nil || len(hosts) != 3 {
		t.Fatalf("workspace 1 hosts = %d, %v; rejected inserts must leave no rows", len(hosts), err)
	}
}

func TestSQLiteUpdateHostCannotCollide(t *testing.T) {
	ctx := context.Background()
	stores, err := store.Open(ctx, sqliteConfig(t))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })

	first := testHost(1, "web-01", "10.0.0.1", 22)
	second := testHost(1, "db-01", "10.0.0.9", 22)
	for _, h := range []*domain.Host{first, second} {
		if err := stores.Hosts.CreateHost(ctx, h); err != nil {
			t.Fatalf("create %s: %v", h.Name, err)
		}
	}

	second.Name = "WEB-01"
	if err := stores.Hosts.UpdateHost(ctx, second); !errors.Is(err, domain.ErrHostAlreadyExists) {
		t.Fatalf("rename onto an existing name error = %v, want ErrHostAlreadyExists", err)
	}
	second.Name = "db-01"
	second.IPAddress = "10.0.0.1"
	if err := stores.Hosts.UpdateHost(ctx, second); !errors.Is(err, domain.ErrHostAlreadyExists) {
		t.Fatalf("move onto an existing address:port error = %v, want ErrHostAlreadyExists", err)
	}

	id, _ := strconv.Atoi(second.ID)
	stored, err := stores.Hosts.GetHostByIDInWorkspace(ctx, 1, id)
	if err != nil || stored.Name != "db-01" || stored.IPAddress != "10.0.0.9" {
		t.Fatalf("stored host = %+v, %v; a refused update must change nothing", stored, err)
	}

	// Saving a host without changing its identity is not a conflict with itself.
	second.IPAddress = "10.0.0.9"
	second.SSH.Username = "ops"
	if err := stores.Hosts.UpdateHost(ctx, second); err != nil {
		t.Fatalf("self-update error = %v", err)
	}
}

func TestMigrationPreflightReportsExistingDuplicates(t *testing.T) {
	ctx := context.Background()
	cfg := sqliteConfig(t)

	// Build a database that looks like it was last migrated before version 4,
	// holding hosts that would violate the new indexes.
	stores, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	for _, statement := range []string{
		`DROP INDEX idx_hosts_workspace_name`,
		`DROP INDEX idx_hosts_workspace_address`,
		`DELETE FROM schema_migrations WHERE version = 4`,
	} {
		if _, err := stores.DB.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	const insert = `INSERT INTO hosts (id, workspace_id, environment_id, name, hostname, ip_address,
		connection, status, port, username, password, private_key, passphrase, expected_fingerprint,
		created_at, updated_at) VALUES (?, 1, 0, ?, 'h', ?, 'SSH', 'PENDING', 22, 'u', '', '', '', 'fp', 'now', 'now')`
	for _, row := range [][]any{
		{"901", "web-01", "10.0.0.1"},
		{"902", "WEB-01", "10.0.0.2"}, // name duplicate
		{"903", "app-01", "10.0.0.5"},
		{"904", "app-02", "10.0.0.5"}, // address:port duplicate
	} {
		if _, err := stores.DB.ExecContext(ctx, insert, row...); err != nil {
			t.Fatalf("seed host %v: %v", row[0], err)
		}
	}
	if err := stores.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = store.Open(ctx, cfg)
	if err == nil {
		t.Fatal("Open() succeeded although existing hosts violate the uniqueness constraints")
	}
	for _, want := range []string{"web-01", "workspace 1", "901", "902", "10.0.0.5:22", "903", "904", "Nothing has been changed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q:\n%v", want, err)
		}
	}

	// Resolve the conflicts by hand, as the message instructs; startup then works
	// and the constraints exist.
	raw, err := openRaw(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `DELETE FROM hosts WHERE id IN ('902', '904')`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	stores, err = store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() after resolving duplicates: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	var indexes int
	if err := stores.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index'
		AND name IN ('idx_hosts_workspace_name', 'idx_hosts_workspace_address')`).Scan(&indexes); err != nil || indexes != 2 {
		t.Fatalf("uniqueness indexes present = %d, %v; want 2", indexes, err)
	}
}

// openRaw opens the database file directly, bypassing migrations, so a test can
// repair data that makes store.Open refuse to start.
func openRaw(t *testing.T, cfg store.Config) (*sql.DB, error) {
	t.Helper()
	db, err := sql.Open("sqlite", cfg.SQLitePath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
