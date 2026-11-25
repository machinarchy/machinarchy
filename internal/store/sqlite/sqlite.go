// Package sqlite opens and configures the SQLite database: file permissions,
// one connection, foreign keys, busy timeout and WAL. Queries live in the
// dialect-neutral stores.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	// Import modernc.org/sqlite to register the SQLite database driver.
	_ "modernc.org/sqlite"
)

// Config contains the settings required to open a SQLite database.
type Config struct {
	DatabasePath string
	// BusyTimeout is how long a statement waits for a lock (default 5s).
	BusyTimeout time.Duration
}

// OpenDB opens a SQLite database using the provided configuration.
// It returns an error if the database cannot be opened or configured.
func OpenDB(cfg Config) (*sql.DB, error) {
	path := strings.TrimSpace(cfg.DatabasePath)
	if path == "" {
		return nil, errors.New("SQLite database path is required")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve SQLite database path: %w", err)
	}
	if strings.ContainsAny(absolutePath, "?#") {
		// The path becomes a DSN with a query string of settings.
		return nil, errors.New("SQLite database path must not contain '?' or '#'")
	}
	dir := filepath.Dir(absolutePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create SQLite database directory: %w", err)
	}
	if info, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("inspect SQLite database directory: %w", err)
	} else if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("SQLite database directory %q must have permissions 0700 or stricter; run chmod 700 on it", dir)
	}
	if info, err := os.Lstat(absolutePath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("SQLite database path %q must be a regular file, not a symlink or special file", absolutePath)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect SQLite database path: %w", err)
	}

	busy := cfg.BusyTimeout
	if busy <= 0 {
		busy = DefaultBusyTimeout
	}
	// Connection-scoped settings go in the DSN, so the driver applies them to
	// every connection it opens, including after a reconnect (AUDIT Q-10).
	dsn := fmt.Sprintf("%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(%d)", absolutePath, busy.Milliseconds())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}

	// SQLite is intentionally a single-controller store.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	return db, nil
}

// DefaultBusyTimeout is how long a statement waits for a lock by default.
const DefaultBusyTimeout = 5 * time.Second

// Configure applies the database-wide settings (WAL, stored in the file) and
// checks that the connection settings from the DSN are in effect.
func Configure(ctx context.Context, db *sql.DB) error {
	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("configure SQLite: %w", err)
	}
	if foreignKeys != 1 {
		return errors.New("SQLite foreign keys are off")
	}
	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return fmt.Errorf("enable SQLite WAL mode: %w", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("SQLite refused WAL mode (reported %q)", journalMode)
	}
	return nil
}

// SecureFile sets restrictive filesystem permissions on the specified database file.
// It returns an error if the file permissions cannot be updated.
func SecureFile(path string) error {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve SQLite file path: %w", err)
	}
	info, err := os.Lstat(absolutePath)
	if err != nil {
		return fmt.Errorf("inspect SQLite database file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("SQLite database path %q must be a regular file, not a symlink or special file", absolutePath)
	}
	if err := os.Chmod(absolutePath, 0o600); err != nil {
		return fmt.Errorf("restrict SQLite database file permissions: %w", err)
	}
	return nil
}
