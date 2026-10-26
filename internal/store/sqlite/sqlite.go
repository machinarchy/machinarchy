package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/machinarchy/machinarchy/pkg/crypto"
	_ "modernc.org/sqlite"
)

type Config struct {
	DatabasePath     string
	EncryptionSecret string
}
type SQLiteStore struct {
	DB            *sql.DB
	encryptionKey []byte
}

func NewSQLiteStore(db *sql.DB, secret string) (*SQLiteStore, error) {
	if db == nil {
		return nil, errors.New("SQLite database handle is required")
	}
	key, err := crypto.DeriveKey(secret, "sqlite-host-credentials")
	if err != nil {
		return nil, fmt.Errorf("derive SQLite credential encryption key: %w", err)
	}
	return &SQLiteStore{DB: db, encryptionKey: key}, nil
}

func OpenDB(cfg Config) (*sql.DB, error) {
	path := strings.TrimSpace(cfg.DatabasePath)
	if path == "" {
		return nil, errors.New("SQLite database path is required")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve SQLite database path: %w", err)
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

	db, err := sql.Open("sqlite", absolutePath)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}

	// SQLite is intentionally a single-controller store. Limiting the pool to
	// one connection also makes connection-scoped PRAGMAs deterministic.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	return db, nil
}

func Configure(ctx context.Context, db *sql.DB) error {
	for _, statement := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure SQLite: %w", err)
		}
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
