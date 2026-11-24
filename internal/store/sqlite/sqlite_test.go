package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPragmasSurviveReconnect (AUDIT Q-10): foreign keys and the busy timeout
// are connection settings. Set by Exec they hold only for the connection that
// ran it; every connection the pool opens later must have them too.
func TestPragmasSurviveReconnect(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := OpenDB(Config{DatabasePath: filepath.Join(dir, "p.db"), BusyTimeout: 7 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := Configure(ctx, db); err != nil {
		t.Fatal(err)
	}
	// No idle connections: every query below runs on a newly opened one.
	db.SetMaxIdleConns(0)
	for i := range 3 {
		var foreignKeys, busyTimeout int
		if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatal(err)
		}
		if foreignKeys != 1 || busyTimeout != 7000 {
			t.Fatalf("connection %d: foreign_keys=%d busy_timeout=%d, want 1 and 7000", i+1, foreignKeys, busyTimeout)
		}
	}
	if stats := db.Stats(); stats.OpenConnections > 1 {
		t.Errorf("open connections = %d", stats.OpenConnections)
	}
}

func TestOpenDBRefusesAQueryInThePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDB(Config{DatabasePath: filepath.Join(dir, "p.db?_pragma=foreign_keys(0)")}); err == nil {
		t.Fatal("a path with a query string was accepted")
	}
}
