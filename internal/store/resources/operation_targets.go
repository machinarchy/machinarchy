package resources

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

// UpdateOperationTarget moves one target forward with a single row update
// (AUDIT Q-02, Q-03). It does not bump the Operation revision, so it never
// conflicts with cancellation or with other targets, and it only touches the
// one target's row and output.
func (s *Store) UpdateOperationTarget(ctx context.Context, workspaceID int, operationID, hostID resource.ID,
	state resource.TargetState, result *resource.TargetResult) error {
	if workspaceID <= 0 || operationID.Validate() != nil || hostID.Validate() != nil ||
		!resource.ValidTargetState(state) || state == resource.TargetPending {
		return fmt.Errorf("%w: workspace, Operation, host and a forward target state are required", resource.ErrInvalidResource)
	}
	result = resource.NormalizeTargetResult(result)
	output, err := s.encryptTargetOutput(result)
	if err != nil {
		return fmt.Errorf("encrypt Operation target output: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Operation target update: %w", err)
	}
	defer rollback(tx, s.logger, "Operation target update")
	if fence, ok := resource.LeaseFenceFromContext(ctx); ok {
		if err := s.verifyLeaseFence(ctx, tx, fence, workspaceID, operationID); err != nil {
			return err
		}
	}
	updated, err := tx.ExecContext(ctx, s.bind(`UPDATE operation_targets SET state = ?, exit_code = ?, output = ?, error_class = ?
		WHERE operation_id = ? AND host_id = ? AND state IN (?, ?)
		AND EXISTS (SELECT 1 FROM operations WHERE id = ? AND workspace_id = ? AND state IN (?, ?))`),
		string(state), nullableInt(result, func(r *resource.TargetResult) *int { return r.ExitCode }), output, targetErrorClass(result),
		string(operationID), string(hostID), string(resource.TargetPending), string(resource.TargetRunning),
		string(operationID), workspaceID, string(resource.OperationRunning), string(resource.OperationCancelRequested))
	if err != nil {
		return fmt.Errorf("update Operation target: %w", err)
	}
	rows, err := updated.RowsAffected()
	if err != nil {
		return fmt.Errorf("read Operation target update result: %w", err)
	}
	if rows == 0 {
		var exists int
		err := tx.QueryRowContext(ctx, s.bind(`SELECT 1 FROM operations WHERE id = ? AND workspace_id = ?`), string(operationID), workspaceID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return resource.ErrOperationNotFound
		}
		if err != nil {
			return fmt.Errorf("inspect Operation for target update: %w", err)
		}
		return resource.ErrOperationFinished
	}
	if err := s.audit(ctx, tx, workspaceID, domain.AuditOperationUpdated, domain.AuditTargetOperation, string(operationID),
		map[string]string{"host_id": string(hostID), "target_state": string(state)}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Operation target update: %w", err)
	}
	return nil
}

// GetOperationState reads only the Operation state. Polling and per-target
// checks use it instead of GetOperation, which decrypts every target's output.
func (s *Store) GetOperationState(ctx context.Context, workspaceID int, operationID resource.ID) (resource.OperationState, error) {
	if workspaceID <= 0 || operationID.Validate() != nil {
		return "", fmt.Errorf("%w: workspace and Operation ID are required", resource.ErrInvalidResource)
	}
	var state string
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT state FROM operations WHERE workspace_id = ? AND id = ?`), workspaceID, string(operationID)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", resource.ErrOperationNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read Operation state: %w", err)
	}
	return resource.OperationState(state), nil
}
