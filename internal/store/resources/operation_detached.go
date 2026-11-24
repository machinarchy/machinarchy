package resources

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/store/sqlx"
)

// Detached execution (AUDIT/10b §1.3). The launch handle is plain JSON (it
// names a mode, unit, pid, state root and endpoint, nothing secret); the
// live progress holds command output and is encrypted like target output.

func (s *Store) decodeDetached(ctx context.Context, workspaceID int, operationID, hostID, launchText, liveText string) (*resource.LaunchHandle, *resource.LiveOutput, error) {
	var launch *resource.LaunchHandle
	if launchText != "" {
		launch = &resource.LaunchHandle{}
		if err := json.Unmarshal([]byte(launchText), launch); err != nil {
			return nil, nil, fmt.Errorf("decode Operation target launch: %w", err)
		}
	}
	var live *resource.LiveOutput
	if liveText != "" {
		plain, err := s.cipher.Open(ctx, liveText, dataAAD(columnLive, workspaceID, operationID, hostID))
		if err != nil {
			return nil, nil, fmt.Errorf("decrypt Operation target progress: %w", err)
		}
		live = &resource.LiveOutput{}
		if err := json.Unmarshal(plain, live); err != nil {
			return nil, nil, fmt.Errorf("decode Operation target progress: %w", err)
		}
	}
	return launch, live, nil
}

// RecordTargetLaunch implements resource.DetachedTargetRepository. It is a
// single-row write, audited, and never moves a terminal target.
func (s *Store) RecordTargetLaunch(ctx context.Context, workspaceID int, operationID, hostID resource.ID, handle resource.LaunchHandle) error {
	if workspaceID <= 0 || operationID.Validate() != nil || hostID.Validate() != nil || handle.LaunchedAt.IsZero() || handle.Deadline.IsZero() {
		return fmt.Errorf("%w: workspace, Operation, host and a launch handle are required", resource.ErrInvalidResource)
	}
	launch, err := json.Marshal(handle)
	if err != nil {
		return fmt.Errorf("encode Operation target launch: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Operation target launch: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "Operation target launch")
	if fence, ok := resource.LeaseFenceFromContext(ctx); ok {
		if err := s.verifyLeaseFence(ctx, tx, fence, workspaceID, operationID); err != nil {
			return err
		}
	}
	updated, err := tx.ExecContext(ctx, s.bind(`UPDATE operation_targets SET state = ?, launch = ?
		WHERE operation_id = ? AND host_id = ? AND state IN (?, ?)
		AND EXISTS (SELECT 1 FROM operations WHERE id = ? AND workspace_id = ? AND state IN (?, ?))`),
		string(resource.TargetRunning), string(launch), string(operationID), string(hostID),
		string(resource.TargetPending), string(resource.TargetRunning),
		string(operationID), workspaceID, string(resource.OperationRunning), string(resource.OperationCancelRequested))
	if err != nil {
		return fmt.Errorf("record Operation target launch: %w", err)
	}
	if err := s.finishedUnlessUpdated(ctx, tx, updated, workspaceID, operationID); err != nil {
		return err
	}
	// System-driven progress is not audited (AUDIT/20 F20-05, owner
	// decision): the audit trail records intents and outcomes, and every
	// append serializes on the workspace's chain lock. The state is in the
	// row itself.
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Operation target launch: %w", err)
	}
	return nil
}

// UpdateTargetLive stores a Running target's progress. Progress is status,
// not a change anyone made, so it is not audited (the launch and the result
// are).
func (s *Store) UpdateTargetLive(ctx context.Context, workspaceID int, operationID, hostID resource.ID, live resource.LiveOutput) error {
	if workspaceID <= 0 || operationID.Validate() != nil || hostID.Validate() != nil {
		return fmt.Errorf("%w: workspace, Operation and host are required", resource.ErrInvalidResource)
	}
	plain, err := json.Marshal(live)
	if err != nil {
		return fmt.Errorf("encode Operation target progress: %w", err)
	}
	encrypted, err := s.cipher.Seal(ctx, plain, dataAAD(columnLive, workspaceID, string(operationID), string(hostID)))
	if err != nil {
		return fmt.Errorf("encrypt Operation target progress: %w", err)
	}
	updated, err := s.db.ExecContext(ctx, s.bind(`UPDATE operation_targets SET live = ?
		WHERE operation_id = ? AND host_id = ? AND state = ?
		AND EXISTS (SELECT 1 FROM operations WHERE id = ? AND workspace_id = ? AND state IN (?, ?))`),
		encrypted, string(operationID), string(hostID), string(resource.TargetRunning),
		string(operationID), workspaceID, string(resource.OperationRunning), string(resource.OperationCancelRequested))
	if err != nil {
		return fmt.Errorf("update Operation target progress: %w", err)
	}
	rows, err := updated.RowsAffected()
	if err != nil {
		return fmt.Errorf("read Operation target progress update: %w", err)
	}
	if rows == 0 {
		return resource.ErrOperationFinished
	}
	return nil
}

// ListDetachedTargets implements resource.DetachedTargetRepository.
func (s *Store) ListDetachedTargets(ctx context.Context, limit int) ([]resource.DetachedTarget, error) {
	if limit < 1 || limit > 10000 {
		return nil, fmt.Errorf("%w: limit must be 1..10000", resource.ErrInvalidResource)
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT o.workspace_id, t.operation_id, t.host_id, t.host_generation,
		t.access_profile_id, t.access_profile_revision, o.state, t.launch, t.live
		FROM operation_targets t JOIN operations o ON o.id = t.operation_id
		WHERE t.state = ? AND t.launch <> '' AND o.state IN (?, ?)
		ORDER BY t.operation_id, t.host_id LIMIT ?`),
		string(resource.TargetRunning), string(resource.OperationRunning), string(resource.OperationCancelRequested), limit)
	if err != nil {
		return nil, fmt.Errorf("list detached Operation targets: %w", err)
	}
	defer sqlx.CloseRows(rows, s.logger, "list detached Operation targets")
	var targets []resource.DetachedTarget
	for rows.Next() {
		var target resource.DetachedTarget
		var operationID, hostID, profileID, state, launchText, liveText string
		if err := rows.Scan(&target.WorkspaceID, &operationID, &hostID, &target.HostGeneration, &profileID,
			&target.AccessProfileRevision, &state, &launchText, &liveText); err != nil {
			return nil, fmt.Errorf("scan detached Operation target: %w", err)
		}
		launch, live, err := s.decodeDetached(ctx, target.WorkspaceID, operationID, hostID, launchText, liveText)
		if err != nil {
			return nil, err
		}
		target.OperationID, target.HostID, target.AccessProfileID = resource.ID(operationID), resource.ID(hostID), resource.ID(profileID)
		target.OperationState, target.Launch = resource.OperationState(state), *launch
		if live != nil {
			target.Live = *live
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate detached Operation targets: %w", err)
	}
	return targets, nil
}

// finishedUnlessUpdated maps a write that matched no row to
// ErrOperationNotFound or ErrOperationFinished.
func (s *Store) finishedUnlessUpdated(ctx context.Context, tx *sql.Tx, updated sql.Result, workspaceID int, operationID resource.ID) error {
	rows, err := updated.RowsAffected()
	if err != nil {
		return fmt.Errorf("read Operation target update result: %w", err)
	}
	if rows > 0 {
		return nil
	}
	var exists int
	err = tx.QueryRowContext(ctx, s.bind(`SELECT 1 FROM operations WHERE id = ? AND workspace_id = ?`), string(operationID), workspaceID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return resource.ErrOperationNotFound
	}
	if err != nil {
		return fmt.Errorf("inspect Operation for target update: %w", err)
	}
	return resource.ErrOperationFinished
}
