package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

type Config struct {
	PostgresURL     string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnectTimeout  time.Duration
}

func OpenDB(cfg Config) (*sql.DB, error) {
	dsn := strings.TrimSpace(cfg.PostgresURL)
	if dsn == "" {
		return nil, errors.New("PostgreSQL URL is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL database: %w", err)
	}
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
