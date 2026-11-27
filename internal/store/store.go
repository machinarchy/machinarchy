// Package store opens the configured database (SQLite or PostgreSQL), applies
// the embedded schema, and assembles the dialect-neutral repositories:
// accounts, managed resources and management (configuration, secrets).
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
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/store/accounts"
	"github.com/machinarchy/machinarchy/internal/store/agents"
	"github.com/machinarchy/machinarchy/internal/store/management"
	"github.com/machinarchy/machinarchy/internal/store/postgres"
	"github.com/machinarchy/machinarchy/internal/store/resources"
	"github.com/machinarchy/machinarchy/internal/store/sqlite"
	"github.com/machinarchy/machinarchy/internal/store/sqlx"
	"github.com/machinarchy/machinarchy/pkg/crypto"
)

// Database engine identifiers.
const (
	EngineSQLite   = "sqlite"
	EnginePostgres = "postgres"
)

// Config contains the configuration required to initialize the storage layer.
type Config struct {
	Engine          string
	SQLitePath      string
	PostgresURL     string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnectTimeout  time.Duration
	// MigrationTimeout bounds schema migrations at startup (default 10 min);
	// ConnectTimeout covers only connecting and session setup.
	MigrationTimeout time.Duration
	// StatementTimeout ends any single statement that runs longer, in the
	// database (PostgreSQL statement_timeout; the SQLite busy timeout). Keep
	// it below the request deadline (AUDIT Q-20). Zero: no limit, and the
	// SQLite default busy timeout.
	StatementTimeout time.Duration
	EncryptionSecret string
	// Keys is the root of trust for Operation data (AUDIT S-07). Nil builds
	// the built-in local provider from EncryptionSecret, as bootstrap does.
	Keys secrets.KeyProvider
	// PreviousSecrets are earlier encryption secrets, to read Operation data
	// written before Phase 16 under a secret that has since been rotated.
	PreviousSecrets []string
}

// Stores provides access to the configured persistence backends and manages
// their lifecycle.
type Stores struct {
	DB     *sql.DB
	engine string
	// Keys is the key provider shared by the Secret Store and Operation data.
	Keys             secrets.KeyProvider
	Users            domain.UserStore
	Invitations      domain.InvitationStore
	Access           domain.AccessStore
	Sessions         domain.SessionStore
	LoginThrottle    domain.LoginThrottleStore
	Environments     domain.EnvironmentStore
	Audit            domain.AuditStore
	ResourceHosts    resource.HostRepository
	ResourceProfiles resource.AccessProfileRepository
	Operations       resource.OperationStore
	Outbox           eventing.OutboxStore
	ResourceLeases   resource.LeaseStore
	Management       *management.Store
	Agents           *agents.Store
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
		db, err = sqlite.OpenDB(sqlite.Config{DatabasePath: cfg.SQLitePath, BusyTimeout: cfg.StatementTimeout})

	case "postgres":
		db, err = postgres.OpenDB(postgres.Config{
			PostgresURL:      cfg.PostgresURL,
			MaxOpenConns:     cfg.MaxOpenConns,
			MaxIdleConns:     cfg.MaxIdleConns,
			ConnMaxLifetime:  cfg.ConnMaxLifetime,
			ConnectTimeout:   cfg.ConnectTimeout,
			StatementTimeout: cfg.StatementTimeout,
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

	// Run embedded migrations. They get their own deadline: a migration that
	// rebuilds a table (000004 on SQLite) is proportional to the data and can
	// take far longer than connecting, and failing it would stop the upgrade.
	migrationTimeout := cfg.MigrationTimeout
	if migrationTimeout <= 0 {
		migrationTimeout = 10 * time.Minute
	}
	migrationCtx, cancelMigrations := context.WithTimeout(parent, migrationTimeout)
	defer cancelMigrations()
	migrator := NewMigrator(db, engine, slog.Default())
	if err := migrator.Up(migrationCtx); err != nil {
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

	dialect := sqlx.SQLite
	if engine == EnginePostgres {
		dialect = sqlx.PostgreSQL
	}
	accountStore, err := accounts.New(db, dialect, slog.Default())
	if err != nil {
		return fail(err)
	}
	keys := cfg.Keys
	if keys == nil {
		local, err := secrets.NewLocalProvider("", cfg.EncryptionSecret, nil)
		if err != nil {
			return fail(fmt.Errorf("initialize key provider: %w", err))
		}
		keys = local
	}
	stores.Keys = keys
	// Operation data written before Phase 16 used one key derived from the
	// secret, without a key ID; keep every such key to read and rewrap it.
	var legacy [][]byte
	for _, secret := range append([]string{cfg.EncryptionSecret}, cfg.PreviousSecrets...) {
		key, err := crypto.DeriveKey(secret, "machinarchy-operation-spec-v1")
		if err != nil {
			return fail(fmt.Errorf("derive legacy Operation data key: %w", err))
		}
		legacy = append(legacy, key)
	}
	cipher, err := resources.NewDataCipher(keys, legacy)
	if err != nil {
		return fail(err)
	}
	resourceStore, err := resources.New(db, dialect, cipher, slog.Default(), accountStore.AppendAudit)
	if err != nil {
		return fail(fmt.Errorf("initialize managed resource repositories: %w", err))
	}
	stores.Users, stores.Access, stores.Environments, stores.Audit = accountStore, accountStore, accountStore, accountStore
	stores.Invitations = accountStore
	stores.Sessions, stores.LoginThrottle = accountStore, accountStore
	stores.ResourceHosts, stores.ResourceProfiles, stores.Operations = resourceStore, resourceStore, resourceStore
	stores.Outbox, stores.ResourceLeases = resourceStore, resourceStore
	if stores.Agents, err = agents.New(db, dialect, accountStore.AppendAudit, slog.Default()); err != nil {
		return fail(err)
	}
	managementStore, err := management.New(db, dialect, slog.Default())
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
