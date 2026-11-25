// Package pgtest gives a test its own PostgreSQL schema on the server named
// by MACHINARCHY_TEST_POSTGRES_URL (see CONTRIBUTING.md).
package pgtest

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the pgx driver
)

// URL returns a connection URL whose search_path is a new schema, dropped
// when the test ends. Without MACHINARCHY_TEST_POSTGRES_URL the test is
// skipped, or fails when MACHINARCHY_REQUIRE_POSTGRES_TESTS is set.
func URL(t testing.TB) string {
	t.Helper()
	base := os.Getenv("MACHINARCHY_TEST_POSTGRES_URL")
	if base == "" {
		if os.Getenv("MACHINARCHY_REQUIRE_POSTGRES_TESTS") != "" {
			t.Fatal("MACHINARCHY_REQUIRE_POSTGRES_TESTS is set but MACHINARCHY_TEST_POSTGRES_URL is empty")
		}
		t.Skip("PostgreSQL tests skipped: set MACHINARCHY_TEST_POSTGRES_URL (see CONTRIBUTING.md)")
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("MACHINARCHY_TEST_POSTGRES_URL must be a postgres:// URL")
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	schema := "test_" + hex.EncodeToString(suffix)
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("open PostgreSQL admin connection: %v", err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		_ = admin.Close()
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Logf("drop test schema %s: %v", schema, err)
		}
		if err := admin.Close(); err != nil {
			t.Logf("close PostgreSQL admin connection: %v", err)
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
