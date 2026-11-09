// Package utils provides shared internal utility functions.
package utils

import (
	"database/sql"
	"errors"
	"log/slog"
)

// RollbackTx rolls back a transaction and logs unexpected rollback errors.
// sql.ErrTxDone is ignored because the transaction may already be committed.
func RollbackTx(tx *sql.Tx, logger *slog.Logger, operation string) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		logger.Warn(
			"failed to rollback transaction",
			"operation", operation,
			"error", err,
		)
	}
}

// CloseRows closes database rows and logs unexpected errors.
func CloseRows(rows *sql.Rows, logger *slog.Logger, operation string) {
	if err := rows.Close(); err != nil {
		logger.Warn(
			"failed to close database rows",
			"operation", operation,
			"error", err,
		)
	}
}
