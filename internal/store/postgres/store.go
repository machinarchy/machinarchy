// Package postgres implements the account, environment and audit stores for
// PostgreSQL; managed resources live in store/resources.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/lib/pq"
	"github.com/machinarchy/machinarchy/internal/domain"
	resourceStore "github.com/machinarchy/machinarchy/internal/store/resources"
	"github.com/machinarchy/machinarchy/pkg/crypto"
)

// Store implements the domain store interfaces for PostgreSQL
type Store struct {
	db        *sql.DB
	Resources *resourceStore.Store
	logger    *slog.Logger
}

var (
	_ domain.UserStore        = (*Store)(nil)
	_ domain.EnvironmentStore = (*Store)(nil)
)

// NewStore creates a new Store instance with the given PostgreSQL database handle, encryption secret, and logger. It derives an encryption key from the secret for encrypting sensitive data in the database.
func NewStore(db *sql.DB, secret string, logger *slog.Logger) (*Store, error) {
	if db == nil {
		return nil, errors.New("PostgreSQL database handle is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	operationKey, err := crypto.DeriveKey(secret, "machinarchy-operation-spec-v1")
	if err != nil {
		return nil, fmt.Errorf("derive PostgreSQL Operation spec encryption key: %w", err)
	}
	resources, err := resourceStore.New(db, resourceStore.PostgreSQL, operationKey, logger,
		func(ctx context.Context, tx *sql.Tx, event domain.AuditEvent) error {
			return appendAudit(ctx, tx, event)
		})
	if err != nil {
		return nil, fmt.Errorf("initialize PostgreSQL managed resource repositories: %w", err)
	}
	return &Store{
		db:        db,
		logger:    logger,
		Resources: resources,
	}, nil
}

func affected(result sql.Result, notFound error) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected row count: %w", err)
	}
	if count == 0 {
		return notFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var postgresErr *pq.Error
	return errors.As(err, &postgresErr) && postgresErr.Code == "23505"
}
