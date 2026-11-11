// Package sqlite provides a SQLite database store implementation for Machinarchy.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/machinarchy/machinarchy/internal/domain"
	resourceStore "github.com/machinarchy/machinarchy/internal/store/resources"
	"github.com/machinarchy/machinarchy/pkg/crypto"
	// Import modernc.org/sqlite to register the SQLite database driver.
	_ "modernc.org/sqlite"
)

// Config contains the settings required to open a SQLite database.
type Config struct {
	DatabasePath     string
	EncryptionSecret string
}

// Store provides SQLite-backed persistence for Machinarchy.
type Store struct {
	DB            *sql.DB
	Resources     *resourceStore.Store
	encryptionKey []byte
	logger        *slog.Logger
}

// NewStore creates a Store using the provided database connection,
// encryption secret, and logger.
// It returns an error if the store cannot be initialized.
func NewStore(db *sql.DB, secret string, logger *slog.Logger) (*Store, error) {
	if db == nil {
		return nil, errors.New("SQLite database handle is required")
	}
	key, err := crypto.DeriveKey(secret, "sqlite-host-credentials")
	if err != nil {
		return nil, fmt.Errorf("derive SQLite credential encryption key: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	operationKey, err := crypto.DeriveKey(secret, "machinarchy-operation-spec-v1")
	if err != nil {
		return nil, fmt.Errorf("derive SQLite Operation spec encryption key: %w", err)
	}
	resources, err := resourceStore.New(db, resourceStore.SQLite, operationKey, logger,
		func(ctx context.Context, tx *sql.Tx, event domain.AuditEvent) error {
			return appendAudit(ctx, tx, event)
		})
	if err != nil {
		return nil, fmt.Errorf("initialize SQLite managed resource repositories: %w", err)
	}
	return &Store{DB: db, encryptionKey: key, logger: logger, Resources: resources}, nil
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

// Configure applies the required SQLite connection and database settings.
// It returns an error if any configuration operation fails.
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
