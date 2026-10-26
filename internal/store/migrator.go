package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*
var storageModule embed.FS

type Migrator struct {
	db     *sql.DB
	engine string
}

func NewMigrator(db *sql.DB, engine string) *Migrator {
	return &Migrator{db: db, engine: engine}
}

func (m *Migrator) Up(ctx context.Context) error {
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
		defer tx.Rollback()

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
	defer rows.Close()

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
