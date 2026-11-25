package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Config holds the configuration for connecting to a PostgreSQL database.
type Config struct {
	PostgresURL     string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnectTimeout  time.Duration
	// StatementTimeout, when positive, is set as statement_timeout on every
	// connection (AUDIT Q-20).
	StatementTimeout time.Duration
}

// OpenDB opens a PostgreSQL database connection using the provided configuration.
func OpenDB(cfg Config) (*sql.DB, error) {
	dsn := strings.TrimSpace(cfg.PostgresURL)
	if dsn == "" {
		return nil, errors.New("PostgreSQL URL is required")
	}
	connConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL URL: %w", err)
	}
	if cfg.StatementTimeout > 0 {
		// A runtime parameter of the startup message: it holds for every
		// connection the pool opens, unlike a SET on one of them.
		connConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(cfg.StatementTimeout.Milliseconds(), 10)
	}
	db := stdlib.OpenDB(*connConfig)
	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 20
	}
	maxIdle := cfg.MaxIdleConns
	if maxIdle < 0 || maxIdle > maxOpen {
		_ = db.Close()
		return nil, errors.New("PostgreSQL max idle connections must be between 0 and max open connections")
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}
	return db, nil
}
