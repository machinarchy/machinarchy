package store_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/machinarchy/machinarchy/internal/store"
)

// Migration 000008 rebuilds operations and operation_targets on SQLite to
// widen the kind CHECK. Migrations run with foreign keys on, so dropping the
// old operations table cascade-deletes the rows that reference it; a
// parent-only rebuild would lose every target (checked by hand: this test
// then fails). This proves the rebuild keeps Operations, targets and their
// detached columns, and that the foreign key still cascades afterwards.
func TestSQLiteMigration8KeepsOperations(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "m.db")
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INT PRIMARY KEY, applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("migrations/sqlite/00000[1-7]_*.up.sql")
	if err != nil || len(files) != 7 {
		t.Fatalf("migrations 1-7: %v, %v", files, err)
	}
	for i, file := range files {
		content, err := os.ReadFile(file) // #nosec G304 -- repository migration files
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, i+1); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO operations (id, workspace_id, kind, state, revision, spec_encrypted, created_at) VALUES ('op-1', 1, 'Run', 'Succeeded', 3, 'x', 't')`,
		`INSERT INTO operation_targets (operation_id, host_id, host_generation, display_name, state, output, launch, live)
			VALUES ('op-1', 'h-1', 1, 'a', 'Succeeded', 'out-1', 'L', 'V'), ('op-1', 'h-2', 1, 'b', 'Lost', 'out-2', '', '')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	stores, err := store.Open(ctx, store.Config{Engine: store.EngineSQLite, SQLitePath: path,
		EncryptionSecret: "migration-test-secret-with-more-than-32-bytes"})
	if err != nil {
		t.Fatal(err)
	}
	defer stores.Close()
	var targets int
	var outputs string
	if err := stores.DB.QueryRowContext(ctx, `SELECT count(*), group_concat(output || '/' || launch || '/' || live, ',') FROM operation_targets
		WHERE operation_id = 'op-1'`).Scan(&targets, &outputs); err != nil {
		t.Fatal(err)
	}
	if targets != 2 || outputs != "out-1/L/V,out-2//" {
		t.Fatalf("targets after migration 8: %d %q", targets, outputs)
	}
	if _, err := stores.DB.ExecContext(ctx, `INSERT INTO operations (id, workspace_id, kind, state, revision, spec_encrypted, created_at)
		VALUES ('op-2', 1, 'Capability', 'Queued', 1, 'x', 't')`); err != nil {
		t.Fatalf("a Capability Operation after migration 8: %v", err)
	}
	if _, err := stores.DB.ExecContext(ctx, `DELETE FROM operations WHERE id = 'op-1'`); err != nil {
		t.Fatal(err)
	}
	if err := stores.DB.QueryRowContext(ctx, `SELECT count(*) FROM operation_targets WHERE operation_id = 'op-1'`).Scan(&targets); err != nil || targets != 0 {
		t.Fatalf("targets of a deleted Operation: %d, %v (the foreign key must still cascade)", targets, err)
	}
}
