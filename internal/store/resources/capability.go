package resources

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/store/sqlx"
)

var _ resource.CapabilityRepository = (*Store)(nil)

const agentCommandColumns = `command_id, operation_id, host_id, workspace_id, agent_id, agent_operation_id, type, state,
	attempts, next_attempt_at_ns, created_at_ns, expires_at_ns, result_due_at_ns, published_at_ns`

func scanAgentCommand(row interface{ Scan(...any) error }) (resource.AgentCommand, error) {
	var c resource.AgentCommand
	var operationID, hostID, kind, state string
	var next, created, expires, due, published int64
	if err := row.Scan(&c.CommandID, &operationID, &hostID, &c.WorkspaceID, &c.AgentID, &c.AgentOperationID, &kind, &state,
		&c.Attempts, &next, &created, &expires, &due, &published); err != nil {
		return c, err
	}
	c.OperationID, c.HostID = resource.ID(operationID), resource.ID(hostID)
	c.Type, c.State = resource.AgentCommandType(kind), resource.AgentCommandState(state)
	c.NextAttemptAt, c.CreatedAt, c.ExpiresAt = time.Unix(0, next).UTC(), time.Unix(0, created).UTC(), time.Unix(0, expires).UTC()
	if due > 0 {
		c.ResultDueAt = time.Unix(0, due).UTC()
	}
	if published > 0 {
		c.PublishedAt = time.Unix(0, published).UTC()
	}
	return c, nil
}

func (s *Store) insertAgentCommand(ctx context.Context, tx *sql.Tx, c resource.AgentCommand) error {
	var due int64
	if !c.ResultDueAt.IsZero() {
		due = c.ResultDueAt.UnixNano()
	}
	_, err := tx.ExecContext(ctx, s.bind(`INSERT INTO operation_agent_commands (`+agentCommandColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, 0)`),
		c.CommandID, string(c.OperationID), string(c.HostID), c.WorkspaceID, c.AgentID, c.AgentOperationID, string(c.Type),
		string(resource.AgentCommandPending), c.CreatedAt.UnixNano(), c.CreatedAt.UnixNano(), c.ExpiresAt.UnixNano(), due)
	if err != nil {
		return fmt.Errorf("record agent command: %w", err)
	}
	return nil
}

// DispatchCapabilityTarget implements resource.CapabilityRepository.
func (s *Store) DispatchCapabilityTarget(ctx context.Context, c resource.AgentCommand) error {
	if c.WorkspaceID <= 0 || c.OperationID.Validate() != nil || c.HostID.Validate() != nil || c.CommandID == "" ||
		c.AgentID == "" || c.AgentOperationID == "" || c.Type != resource.AgentCommandRequest || c.ExpiresAt.IsZero() || c.ResultDueAt.IsZero() {
		return fmt.Errorf("%w: a complete request command is required", resource.ErrInvalidResource)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin capability dispatch: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "capability dispatch")
	if fence, ok := resource.LeaseFenceFromContext(ctx); ok {
		if err := s.verifyLeaseFence(ctx, tx, fence, c.WorkspaceID, c.OperationID); err != nil {
			return err
		}
	}
	updated, err := tx.ExecContext(ctx, s.bind(`UPDATE operation_targets SET state = ?
		WHERE operation_id = ? AND host_id = ? AND state = ?
		AND EXISTS (SELECT 1 FROM operations WHERE id = ? AND workspace_id = ? AND kind = ? AND state IN (?, ?))`),
		string(resource.TargetRunning), string(c.OperationID), string(c.HostID), string(resource.TargetPending),
		string(c.OperationID), c.WorkspaceID, string(resource.OperationKindCapability),
		string(resource.OperationRunning), string(resource.OperationCancelRequested))
	if err != nil {
		return fmt.Errorf("start capability target: %w", err)
	}
	if n, err := updated.RowsAffected(); err != nil {
		return fmt.Errorf("start capability target: %w", err)
	} else if n == 0 {
		return resource.ErrOperationFinished
	}
	if err := s.insertAgentCommand(ctx, tx, c); err != nil {
		return err
	}
	// System-driven progress is not audited (AUDIT/20 F20-05, owner
	// decision): the audit trail records intents and outcomes, and every
	// append serializes on the workspace's chain lock. The state is in the
	// row itself.
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit capability dispatch: %w", err)
	}
	return nil
}

// AddCancelCommands implements resource.CapabilityRepository.
func (s *Store) AddCancelCommands(ctx context.Context, workspaceID int, operationID resource.ID, now time.Time, lifetime time.Duration) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin capability cancel: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "capability cancel")
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT `+prefixed("c.", agentCommandColumns)+`
		FROM operation_agent_commands c JOIN operation_targets t ON t.operation_id = c.operation_id AND t.host_id = c.host_id
		WHERE c.operation_id = ? AND c.workspace_id = ? AND c.type = ? AND t.state = ?
		AND NOT EXISTS (SELECT 1 FROM operation_agent_commands x WHERE x.operation_id = c.operation_id AND x.host_id = c.host_id AND x.type = ?)`),
		string(operationID), workspaceID, string(resource.AgentCommandRequest), string(resource.TargetRunning), string(resource.AgentCommandCancel))
	if err != nil {
		return 0, fmt.Errorf("list capability targets to cancel: %w", err)
	}
	var requests []resource.AgentCommand
	for rows.Next() {
		c, err := scanAgentCommand(rows)
		if err != nil {
			sqlx.CloseRows(rows, s.logger, "capability cancel")
			return 0, err
		}
		requests = append(requests, c)
	}
	sqlx.CloseRows(rows, s.logger, "capability cancel")
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, request := range requests {
		id, err := NewAgentCommandID()
		if err != nil {
			return 0, err
		}
		if err := s.insertAgentCommand(ctx, tx, resource.AgentCommand{CommandID: id, OperationID: request.OperationID, HostID: request.HostID,
			WorkspaceID: request.WorkspaceID, AgentID: request.AgentID, AgentOperationID: request.AgentOperationID,
			Type: resource.AgentCommandCancel, CreatedAt: now, ExpiresAt: now.Add(lifetime)}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit capability cancel: %w", err)
	}
	return len(requests), nil
}

// ListDueAgentCommands implements resource.CapabilityRepository.
func (s *Store) ListDueAgentCommands(ctx context.Context, now time.Time, limit int) ([]resource.AgentCommand, error) {
	return s.listAgentCommands(ctx, `SELECT `+agentCommandColumns+` FROM operation_agent_commands
		WHERE state = ? AND next_attempt_at_ns <= ? ORDER BY next_attempt_at_ns, command_id LIMIT ?`,
		string(resource.AgentCommandPending), now.UnixNano(), limit)
}

// ListOverdueCapabilityTargets implements resource.CapabilityRepository.
func (s *Store) ListOverdueCapabilityTargets(ctx context.Context, now time.Time, limit int) ([]resource.AgentCommand, error) {
	return s.listAgentCommands(ctx, `SELECT `+prefixed("c.", agentCommandColumns)+`
		FROM operation_agent_commands c JOIN operation_targets t ON t.operation_id = c.operation_id AND t.host_id = c.host_id
		WHERE c.type = ? AND t.state = ? AND c.result_due_at_ns > 0 AND c.result_due_at_ns <= ?
		ORDER BY c.result_due_at_ns LIMIT ?`,
		string(resource.AgentCommandRequest), string(resource.TargetRunning), now.UnixNano(), limit)
}

func (s *Store) listAgentCommands(ctx context.Context, query string, args ...any) ([]resource.AgentCommand, error) {
	if limit, ok := args[len(args)-1].(int); !ok || limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("%w: limit must be 1..1000", resource.ErrInvalidResource)
	}
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("list agent commands: %w", err)
	}
	defer sqlx.CloseRows(rows, s.logger, "list agent commands")
	var commands []resource.AgentCommand
	for rows.Next() {
		c, err := scanAgentCommand(rows)
		if err != nil {
			return nil, fmt.Errorf("scan agent command: %w", err)
		}
		commands = append(commands, c)
	}
	return commands, rows.Err()
}

// MarkAgentCommandPublished implements resource.CapabilityRepository.
func (s *Store) MarkAgentCommandPublished(ctx context.Context, commandID string, now time.Time) error {
	return s.updateAgentCommand(ctx, `UPDATE operation_agent_commands SET state = ?, attempts = attempts + 1, published_at_ns = ?
		WHERE command_id = ? AND state = ?`, string(resource.AgentCommandPublished), now.UnixNano(), commandID, string(resource.AgentCommandPending))
}

// RetryAgentCommand implements resource.CapabilityRepository.
func (s *Store) RetryAgentCommand(ctx context.Context, commandID string, next time.Time) error {
	return s.updateAgentCommand(ctx, `UPDATE operation_agent_commands SET attempts = attempts + 1, next_attempt_at_ns = ?
		WHERE command_id = ? AND state = ?`, next.UnixNano(), commandID, string(resource.AgentCommandPending))
}

// ExpireAgentCommand implements resource.CapabilityRepository.
func (s *Store) ExpireAgentCommand(ctx context.Context, commandID string) error {
	return s.updateAgentCommand(ctx, `UPDATE operation_agent_commands SET state = ? WHERE command_id = ? AND state = ?`,
		string(resource.AgentCommandExpired), commandID, string(resource.AgentCommandPending))
}

func (s *Store) updateAgentCommand(ctx context.Context, query string, args ...any) error {
	updated, err := s.db.ExecContext(ctx, s.bind(query), args...)
	if err != nil {
		return fmt.Errorf("update agent command: %w", err)
	}
	if n, err := updated.RowsAffected(); err != nil {
		return fmt.Errorf("update agent command: %w", err)
	} else if n == 0 {
		return resource.ErrAgentCommandNotFound
	}
	return nil
}

// GetAgentRequest implements resource.CapabilityRepository.
func (s *Store) GetAgentRequest(ctx context.Context, agentOperationID string) (resource.AgentCommand, error) {
	c, err := scanAgentCommand(s.db.QueryRowContext(ctx, s.bind(`SELECT `+agentCommandColumns+` FROM operation_agent_commands
		WHERE agent_operation_id = ? AND type = ?`), agentOperationID, string(resource.AgentCommandRequest)))
	if errors.Is(err, sql.ErrNoRows) {
		return c, resource.ErrAgentCommandNotFound
	}
	if err != nil {
		return c, fmt.Errorf("read agent request: %w", err)
	}
	return c, nil
}

// NewAgentCommandID returns a command ID for the agent protocol.
func NewAgentCommandID() (string, error) { return randomAgentID("cmd-") }

// NewAgentOperationID returns the agent's ID for one target's call.
func NewAgentOperationID() (string, error) { return randomAgentID("op-") }

func randomAgentID(prefix string) (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw), nil
}

// prefixed qualifies a comma-separated column list with a table alias.
func prefixed(alias, columns string) string {
	fields := strings.Split(columns, ",")
	for i, field := range fields {
		fields[i] = alias + strings.TrimSpace(field)
	}
	return strings.Join(fields, ", ")
}
