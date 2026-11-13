// Package store provides a unified interface for accessing different database engines (SQLite and PostgreSQL) and their associated domain stores. It handles database connection, configuration, migrations, and readiness checks.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/eventing"
	"github.com/machinarchy/machinarchy/internal/store/management"
	"github.com/machinarchy/machinarchy/internal/store/postgres"
	"github.com/machinarchy/machinarchy/internal/store/sqlite"
)

// Database engine identifiers.
const (
	EngineSQLite   = "sqlite"
	EnginePostgres = "postgres"
)

// Config contains the configuration required to initialize the storage layer.
type Config struct {
	Engine           string
	SQLitePath       string
	PostgresURL      string
	MaxOpenConns     int
	MaxIdleConns     int
	ConnMaxLifetime  time.Duration
	ConnectTimeout   time.Duration
	EncryptionSecret string
}

// Stores provides access to the configured persistence backends and manages
// their lifecycle.
type Stores struct {
	DB               *sql.DB
	engine           string
	Users            domain.UserStore
	Access           domain.AccessStore
	Metrics          domain.MetricsStore
	Hosts            domain.HostStore
	Jobs             domain.JobStore
	Environments     domain.EnvironmentStore
	Audit            domain.AuditStore
	Runs             domain.RunStore
	ResourceHosts    resource.HostRepository
	ResourceProfiles resource.AccessProfileRepository
	Operations       resource.OperationRepository
	Outbox           eventing.OutboxStore
	ResourceLeases   resource.LeaseStore
	Management       *management.Store
	logger           *slog.Logger
}

// Open initializes the configured storage backends using the provided context
// and configuration.
// It returns an error if any backend cannot be initialized.
func Open(parent context.Context, cfg Config) (*Stores, error) {
	engine := strings.ToLower(strings.TrimSpace(cfg.Engine))
	var (
		db  *sql.DB
		err error
	)

	switch engine {
	case "sqlite":
		db, err = sqlite.OpenDB(sqlite.Config{
			DatabasePath:     cfg.SQLitePath,
			EncryptionSecret: cfg.EncryptionSecret,
		})

	case "postgres":
		db, err = postgres.OpenDB(postgres.Config{
			PostgresURL:     cfg.PostgresURL,
			MaxOpenConns:    cfg.MaxOpenConns,
			MaxIdleConns:    cfg.MaxIdleConns,
			ConnMaxLifetime: cfg.ConnMaxLifetime,
			ConnectTimeout:  cfg.ConnectTimeout,
		})
	default:
		return nil, fmt.Errorf("unsupported database engine %q: expected sqlite or postgres", cfg.Engine)
	}

	if err != nil {
		return nil, err
	}

	fail := func(err error) (*Stores, error) {
		if closeErr := db.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close database after startup failure: %w", closeErr))
		}
		return nil, err
	}

	timeout := cfg.ConnectTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return fail(fmt.Errorf("connect to %s database: %w", engine, err))
	}

	// Configure SQLite PRAGMAs before running migrations
	if engine == "sqlite" {
		if err := sqlite.Configure(ctx, db); err != nil {
			return fail(err)
		}
	}

	// Run embedded migrations
	migrator := NewMigrator(db, engine, slog.Default())
	if err := migrator.Up(ctx); err != nil {
		return fail(fmt.Errorf("migrate %s database: %w", engine, err))
	}

	// Secure file permissions post-migration/creation for SQLite
	if engine == "sqlite" {
		if err := sqlite.SecureFile(cfg.SQLitePath); err != nil {
			return fail(err)
		}
	}

	// Assemble the Store container
	stores := &Stores{
		DB:     db,
		engine: engine,
	}

	switch engine {
	case "sqlite":
		storeInstance, err := sqlite.NewStore(db, cfg.EncryptionSecret, slog.Default())
		if err != nil {
			return fail(err)
		}

		stores.Users = storeInstance
		stores.Access = storeInstance
		stores.Metrics = storeInstance
		stores.Hosts = storeInstance
		stores.Jobs = storeInstance
		stores.Environments = storeInstance
		stores.Audit = storeInstance
		stores.Runs = storeInstance
		stores.ResourceHosts = storeInstance.Resources
		stores.ResourceProfiles = storeInstance.Resources
		stores.Operations = storeInstance.Resources
		stores.Outbox = storeInstance.Resources
		stores.ResourceLeases = storeInstance.Resources
	case "postgres":
		storeInstance, err := postgres.NewStore(db, cfg.EncryptionSecret, slog.Default())
		if err != nil {
			return fail(err)
		}
		stores.Users = storeInstance
		stores.Access = storeInstance
		stores.Metrics = storeInstance
		stores.Hosts = storeInstance
		stores.Jobs = storeInstance
		stores.Environments = storeInstance
		stores.Audit = storeInstance
		stores.Runs = storeInstance
		stores.ResourceHosts = storeInstance.Resources
		stores.ResourceProfiles = storeInstance.Resources
		stores.Operations = storeInstance.Resources
		stores.Outbox = storeInstance.Resources
		stores.ResourceLeases = storeInstance.Resources
	}
	managementDialect := management.SQLite
	if engine == EnginePostgres {
		managementDialect = management.PostgreSQL
	}
	managementStore, err := management.New(db, managementDialect, slog.Default())
	if err != nil {
		return fail(fmt.Errorf("initialize configuration and Secret Store repositories: %w", err))
	}
	stores.Management = managementStore

	return stores, nil
}

// Close releases the resources held by all configured storage backends.
// It returns an error if any backend fails to close.
func (s *Stores) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

// Engine reports the configured database profile used for capability wiring.
func (s *Stores) Engine() string {
	if s == nil {
		return ""
	}
	return s.engine
}

// Ping verifies connectivity to all configured storage backends.
// It returns an error if any backend is unreachable.
func (s *Stores) Ping(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return errors.New("database is not initialized")
	}
	if ctx == nil {
		return errors.New("database ping context is required")
	}
	if err := s.DB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping %s database: %w", s.engine, err)
	}
	return nil
}

// CheckReadiness reports whether all configured storage backends are ready.
// It returns an error if any backend is unavailable.
func (s *Stores) CheckReadiness(ctx context.Context) error { return s.Ping(ctx) }

// DatabaseStats returns connection pool statistics for the configured database.
func (s *Stores) DatabaseStats() sql.DBStats {
	if s == nil || s.DB == nil {
		return sql.DBStats{}
	}
	return s.DB.Stats()
}
