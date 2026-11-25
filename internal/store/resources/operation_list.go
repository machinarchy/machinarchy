package resources

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/store/sqlx"
)

// ListOperations returns one keyset page of Operation summaries, newest first.
// It reads neither the encrypted spec nor target output, and it uses the
// (workspace_id, created_at DESC, id DESC) index.
func (s *Store) ListOperations(ctx context.Context, filter resource.OperationListFilter) ([]resource.OperationSummary, error) {
	if filter.WorkspaceID <= 0 {
		return nil, fmt.Errorf("%w: workspace ID must be positive", resource.ErrInvalidResource)
	}
	query := `SELECT o.id, o.kind, o.state, o.actor_user_id, o.actor_token_id, o.cancel_asked, o.created_at, o.started_at, o.finished_at
		FROM operations o WHERE o.workspace_id = ?`
	args := []any{filter.WorkspaceID}
	if filter.State != "" {
		query += ` AND o.state = ?`
		args = append(args, string(filter.State))
	}
	if filter.HostID != "" {
		query += ` AND EXISTS (SELECT 1 FROM operation_targets t WHERE t.operation_id = o.id AND t.host_id = ?)`
		args = append(args, string(filter.HostID))
	}
	if !filter.CreatedAfter.IsZero() {
		query += ` AND o.created_at >= ?`
		args = append(args, sqlx.Time(filter.CreatedAfter))
	}
	if !filter.CreatedBefore.IsZero() {
		query += ` AND o.created_at < ?`
		args = append(args, sqlx.Time(filter.CreatedBefore))
	}
	if filter.After != "" {
		created, id, err := resource.ParseOperationCursor(filter.After)
		if err != nil {
			return nil, err
		}
		query += ` AND (o.created_at < ? OR (o.created_at = ? AND o.id < ?))`
		args = append(args, created, created, string(id))
	}
	query += ` ORDER BY o.created_at DESC, o.id DESC LIMIT ?`
	args = append(args, resource.PageLimit(filter.Limit))

	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("list Operations: %w", err)
	}
	defer sqlx.CloseRows(rows, s.logger, "query list operations")
	summaries := make([]resource.OperationSummary, 0)
	index := map[resource.ID]int{}
	for rows.Next() {
		var summary resource.OperationSummary
		var kind, state, created string
		var started, finished sql.NullString
		if err := rows.Scan(&summary.ID, &kind, &state, &summary.CreatedBy.UserID, &summary.CreatedBy.APITokenID,
			&summary.CancelAsked, &created, &started, &finished); err != nil {
			return nil, fmt.Errorf("read Operation summary: %w", err)
		}
		summary.Kind, summary.State = resource.OperationKind(kind), resource.OperationState(state)
		if summary.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, fmt.Errorf("parse Operation creation time: %w", err)
		}
		if summary.StartedAt, err = sqlx.ParseNullTime(started); err != nil {
			return nil, err
		}
		if summary.FinishedAt, err = sqlx.ParseNullTime(finished); err != nil {
			return nil, err
		}
		summary.Cursor = resource.OperationCursor(created, summary.ID)
		summary.Targets = map[resource.TargetState]int{}
		index[summary.ID] = len(summaries)
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Operations: %w", err)
	}
	if len(summaries) == 0 {
		return summaries, nil
	}
	return summaries, s.countTargets(ctx, summaries, index)
}

func (s *Store) countTargets(ctx context.Context, summaries []resource.OperationSummary, index map[resource.ID]int) error {
	args := make([]any, len(summaries))
	for i, summary := range summaries {
		args[i] = string(summary.ID)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(summaries)), ",")
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT operation_id, state, COUNT(*) FROM operation_targets
		WHERE operation_id IN (`+placeholders+`) GROUP BY operation_id, state`), args...)
	if err != nil {
		return fmt.Errorf("count Operation targets: %w", err)
	}
	defer sqlx.CloseRows(rows, s.logger, "query count operation targets")
	for rows.Next() {
		var id, state string
		var count int
		if err := rows.Scan(&id, &state, &count); err != nil {
			return fmt.Errorf("read Operation target count: %w", err)
		}
		summaries[index[resource.ID(id)]].Targets[resource.TargetState(state)] = count
	}
	return rows.Err()
}
