package resources

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/store/sqlx"
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
	output, err := s.encryptTargetOutput(ctx, workspaceID, operationID, hostID, result)
	if err != nil {
		return fmt.Errorf("encrypt Operation target output: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Operation target update: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "Operation target update")
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
	// System-driven progress is not audited (AUDIT/20 F20-05, owner
	// decision): the audit trail records intents and outcomes, and every
	// append serializes on the workspace's chain lock. The state is in the
	// row itself.
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

// GetOperationTarget implements resource.OperationTargetReader.
func (s *Store) GetOperationTarget(ctx context.Context, workspaceID int, operationID, hostID resource.ID) (resource.OperationState, resource.OperationTargetSpec, error) {
	var state, profileID, displayName string
	spec := resource.OperationTargetSpec{HostID: hostID}
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT o.state, t.host_generation, t.display_name, t.access_profile_id, t.access_profile_revision
		FROM operations o JOIN operation_targets t ON t.operation_id = o.id
		WHERE o.workspace_id = ? AND o.id = ? AND t.host_id = ?`), workspaceID, string(operationID), string(hostID)).
		Scan(&state, &spec.HostGeneration, &displayName, &profileID, &spec.AccessProfileRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return "", resource.OperationTargetSpec{}, resource.ErrOperationNotFound
	}
	if err != nil {
		return "", resource.OperationTargetSpec{}, fmt.Errorf("read Operation target: %w", err)
	}
	spec.DisplayName, spec.AccessProfileID = displayName, resource.ID(profileID)
	return resource.OperationState(state), spec, nil
}
