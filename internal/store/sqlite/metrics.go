package sqlite

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/utils"
)

// SaveMetricPoint saves a new metric data point for a specific workspace, ensuring that the CPU and memory percentages are valid and within the range of 0 to 100. It also prunes older metric points to maintain a maximum of 1440 entries per workspace.
func (s *Store) SaveMetricPoint(ctx context.Context, workspaceID int, cpuPercent, memoryPercent float64) error {
	if workspaceID <= 0 {
		return fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	if math.IsNaN(cpuPercent) || math.IsInf(cpuPercent, 0) || cpuPercent < 0 || cpuPercent > 100 ||
		math.IsNaN(memoryPercent) || math.IsInf(memoryPercent, 0) || memoryPercent < 0 || memoryPercent > 100 {
		return fmt.Errorf("%w: CPU and memory percentages must be finite values from 0 to 100", domain.ErrInvalidInput)
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin save metric transaction: %w", err)
	}
	defer utils.RollbackTx(tx, s.logger, "save metric")

	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO metrics_history
		(workspace_id, timestamp, cpu_percent, mem_percent) VALUES (?, ?, ?, ?)`, workspaceID, timestamp, cpuPercent, memoryPercent); err != nil {
		return fmt.Errorf("insert metric point: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM metrics_history WHERE id NOT IN
		(SELECT id FROM metrics_history WHERE workspace_id = ? ORDER BY id DESC LIMIT 1440) AND workspace_id = ?`, workspaceID, workspaceID); err != nil {
		return fmt.Errorf("prune metric history: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit metric transaction: %w", err)
	}
	return nil
}

// GetStoredHistory retrieves historical data points for the specified workspace,
// returning at most limit records. It returns an error if the query fails.
func (s *Store) GetStoredHistory(ctx context.Context, workspaceID, limit int) ([]domain.DataPoint, error) {
	if workspaceID <= 0 {
		return nil, fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	if limit < 1 || limit > 10000 {
		return nil, fmt.Errorf("%w: metric history limit must be between 1 and 10000", domain.ErrInvalidInput)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT timestamp, cpu_percent, mem_percent FROM (
		SELECT id, timestamp, cpu_percent, mem_percent FROM metrics_history WHERE workspace_id = ?
		ORDER BY id DESC LIMIT ?
	) ORDER BY id ASC`, workspaceID, limit)
	if err != nil {
		return nil, fmt.Errorf("query metric history: %w", err)
	}
	defer utils.CloseRows(rows, s.logger, "query metric history")

	points := make([]domain.DataPoint, 0, limit)
	for rows.Next() {
		var point domain.DataPoint
		if err := rows.Scan(&point.Timestamp, &point.CPUPercent, &point.MemPercent); err != nil {
			return nil, fmt.Errorf("scan metric point: %w", err)
		}
		point.CreatedAt = point.Timestamp
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate metric history: %w", err)
	}
	return points, nil
}
