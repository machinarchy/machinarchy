package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/store/sqlx"
)

//go:embed migrations/*
var storageModule embed.FS

// ErrIncompatibleSchema reports a database created before the schema baseline.
var ErrIncompatibleSchema = errors.New("the database was created by an incompatible schema version")

// Migrator handles database schema migrations for different database engines.
type Migrator struct {
	db     *sql.DB
	engine string
	logger *slog.Logger
}

// migrationLockKey is the PostgreSQL advisory lock held while migrating
// ("machinarchy migrations").
const migrationLockKey int64 = 0x6d61636869_6d67

// NewMigrator creates a new Migrator for the given database connection and engine type.
func NewMigrator(db *sql.DB, engine string, logger *slog.Logger) *Migrator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Migrator{db: db, engine: engine, logger: logger}
}

// Up applies all pending migrations for the configured database engine.
func (m *Migrator) Up(ctx context.Context) error {
	if m.engine == "postgres" {
		// Replicas start together on one database: they take turns, and each
		// reads what is applied only after the previous one finished
		// (AUDIT/20 F20-01). The session lock lives on one dedicated
		// connection and is released when it closes, even on a crash.
		conn, err := m.db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("reserve migration lock connection: %w", err)
		}
		defer func() { _ = conn.Close() }()
		if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
			return fmt.Errorf("wait for the migration lock: %w", err)
		}
		defer func() {
			unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if _, err := conn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationLockKey); err != nil {
				m.logger.Warn("release migration lock failed; it is released when the connection closes", "error", err)
			}
		}()
	}
	// 1. Ensure schema_migrations tracking table exists
	if err := m.ensureMigrationTable(ctx); err != nil {
		return err
	}

	// 2. Read embedded migrations for the specific engine driver
	dirPath := fmt.Sprintf("migrations/%s", m.engine)
	entries, err := fs.ReadDir(storageModule, dirPath)
	if err != nil {
		return fmt.Errorf("failed to read embedded migrations dir %s: %w", dirPath, err)
	}

	var versions []int
	migrationFiles := make(map[int]string)

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".up.sql") {
			continue
		}

		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			return fmt.Errorf("invalid migration filename %q: expected <version>_<name>.up.sql", name)
		}
		ver, err := strconv.Atoi(prefix)
		if err != nil {
			return fmt.Errorf("invalid migration version in filename %q: %w", name, err)
		}
		if _, exists := migrationFiles[ver]; exists {
			return fmt.Errorf("duplicate migration version %d for engine %q", ver, m.engine)
		}
		versions = append(versions, ver)
		migrationFiles[ver] = fmt.Sprintf("%s/%s", dirPath, name)
	}

	sort.Ints(versions)

	// 3. Fetch already applied versions
	applied, err := m.getAppliedVersions(ctx)
	if err != nil {
		return err
	}
	// A version this binary does not ship means the database was built by an
	// older migration chain; the schema was squashed into one baseline during
	// development (AUDIT/05 B8c), so such a database must be recreated.
	for version := range applied {
		if _, known := migrationFiles[version]; !known {
			return fmt.Errorf("%w: it records schema version %d, which this release does not ship; "+
				"the migrations were squashed into one baseline, so delete the SQLite file or drop the PostgreSQL database and start again",
				ErrIncompatibleSchema, version)
		}
	}

	// 4. Run pending migrations atomically
	for _, ver := range versions {
		if applied[ver] {
			continue
		}

		filePath := migrationFiles[ver]
		content, err := storageModule.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("failed to read migration file %s: %w", filePath, err)
		}

		slog.Info("Applying database migration", "version", ver, "engine", m.engine)

		tx, err := m.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin migration tx: %w", err)
		}
		defer sqlx.Rollback(tx, m.logger, "migration")
		if m.engine == "postgres" {
			// Migrations are bounded by MigrationTimeout, not by the
			// per-statement limit for requests (AUDIT Q-20).
			if _, err := tx.ExecContext(ctx, "SET LOCAL statement_timeout = 0"); err != nil {
				return fmt.Errorf("lift statement timeout for migration %d: %w", ver, err)
			}
		}

		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			return fmt.Errorf("failed to execute migration %d: %w", ver, err)
		}

		placeholder := "?"
		if m.engine == "postgres" {
			placeholder = "$1"
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES ("+placeholder+")", ver); err != nil {
			return fmt.Errorf("failed to record migration version %d: %w", ver, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit migration %d: %w", ver, err)
		}
	}

	slog.Info("Database migrations completed successfully")
	return nil
}

func (m *Migrator) ensureMigrationTable(ctx context.Context) error {
	query := `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INT PRIMARY KEY,
			applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
	`
	_, err := m.db.ExecContext(ctx, query)
	return err
}

func (m *Migrator) getAppliedVersions(ctx context.Context) (map[int]bool, error) {
	rows, err := m.db.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer sqlx.CloseRows(rows, m.logger, "query schema_migrations")

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied migration versions: %w", err)
	}
	return applied, nil
}
