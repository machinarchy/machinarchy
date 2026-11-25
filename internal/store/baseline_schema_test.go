package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/machinarchy/machinarchy/internal/store"
)

// The SQLite baseline keeps foreign keys on: deleting an Operation deletes
// its targets, a Capability Operation and a Lost target are accepted, and the
// detached columns exist. (These were checked through the table rebuilds of
// the migrations that the baseline replaced.)
func TestSQLiteBaselineOperations(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	stores, err := store.Open(ctx, store.Config{Engine: store.EngineSQLite, SQLitePath: filepath.Join(dir, "m.db"),
		EncryptionSecret: "migration-test-secret-with-more-than-32-bytes"})
	if err != nil {
		t.Fatal(err)
	}
	defer stores.Close()
	exec := func(statement string) {
		t.Helper()
		if _, err := stores.DB.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	exec(`INSERT INTO operations (id, workspace_id, kind, state, revision, spec_encrypted, created_at) VALUES ('op-1', 1, 'Capability', 'Succeeded', 1, 'x', 't')`)
	exec(`INSERT INTO operation_targets (operation_id, host_id, host_generation, display_name, state, output, launch, live)
		VALUES ('op-1', 'h-1', 1, 'a', 'Succeeded', 'out-1', 'L', 'V'), ('op-1', 'h-2', 1, 'b', 'Lost', 'out-2', '', '')`)
	var targets int
	if err := stores.DB.QueryRowContext(ctx, `SELECT count(*) FROM operation_targets WHERE operation_id = 'op-1'`).Scan(&targets); err != nil || targets != 2 {
		t.Fatalf("targets = %d, %v", targets, err)
	}
	exec(`DELETE FROM operations WHERE id = 'op-1'`)
	if err := stores.DB.QueryRowContext(ctx, `SELECT count(*) FROM operation_targets WHERE operation_id = 'op-1'`).Scan(&targets); err != nil || targets != 0 {
		t.Fatalf("targets of a deleted Operation: %d, %v (the foreign key must cascade)", targets, err)
	}
}
