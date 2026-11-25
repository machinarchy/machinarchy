package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/machinarchy/machinarchy/internal/store"
	"github.com/machinarchy/machinarchy/internal/testutil/pgtest"
)

// TestPostgresStatementTimeout (AUDIT Q-20): every pooled connection carries
// statement_timeout, so a slow query ends in the database even when the
// caller's context has no deadline. Migrations are exempt (they have their
// own, longer bound).
func TestPostgresStatementTimeout(t *testing.T) {
	url := pgtest.URL(t)
	ctx := context.Background()
	stores, err := store.Open(ctx, store.Config{Engine: store.EnginePostgres, PostgresURL: url, EncryptionSecret: "statement-timeout-secret-long-enough",
		MaxOpenConns: 4, MaxIdleConns: 0, StatementTimeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stores.Close()
	for i := range 3 { // MaxIdleConns 0: a new connection each time
		var setting string
		if err := stores.DB.QueryRowContext(ctx, `SHOW statement_timeout`).Scan(&setting); err != nil || setting != "300ms" {
			t.Fatalf("connection %d: statement_timeout = %q, %v", i+1, setting, err)
		}
	}
	began := time.Now()
	_, err = stores.DB.ExecContext(ctx, `SELECT pg_sleep(3)`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "57014" {
		t.Fatalf("a 3 s query with a 300 ms statement timeout = %v, want SQLSTATE 57014", err)
	}
	if elapsed := time.Since(began); elapsed > 2*time.Second {
		t.Fatalf("the slow query ran %s", elapsed)
	}
}

func TestSQLiteBusyTimeoutFollowsStatementTimeout(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stores, err := store.Open(context.Background(), store.Config{Engine: store.EngineSQLite, SQLitePath: filepath.Join(dir, "s.db"),
		EncryptionSecret: "statement-timeout-secret-long-enough", StatementTimeout: 7 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer stores.Close()
	var busy int
	if err := stores.DB.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil || busy != 7000 {
		t.Fatalf("busy_timeout = %d, %v; want 7000", busy, err)
	}
}
