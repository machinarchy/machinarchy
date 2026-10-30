package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/store/postgres"
	"github.com/machinarchy/machinarchy/internal/store/sqlite"
)

const (
	EngineSQLite   = "sqlite"
	EnginePostgres = "postgres"
)

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

type Stores struct {
	DB           *sql.DB
	engine       string
	Users        domain.UserStore
	Access       domain.AccessStore
	Metrics      domain.MetricsStore
	Hosts        domain.HostStore
	Jobs         domain.JobStore
	Environments domain.EnvironmentStore
	Audit        domain.AuditStore
}

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
	migrator := NewMigrator(db, engine)
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
		storeInstance, err := sqlite.NewSQLiteStore(db, cfg.EncryptionSecret)
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
	case "postgres":
		storeInstance, err := postgres.NewStore(db, cfg.EncryptionSecret)
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
	}

	return stores, nil
}

func (s *Stores) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

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

func (s *Stores) CheckReadiness(ctx context.Context) error { return s.Ping(ctx) }

func (s *Stores) DatabaseStats() sql.DBStats {
	if s == nil || s.DB == nil {
		return sql.DBStats{}
	}
	return s.DB.Stats()
}
