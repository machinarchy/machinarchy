package postgres

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

func (s *Store) SaveMetricPoint(ctx context.Context, cpuPercent, memoryPercent float64) error {
	if math.IsNaN(cpuPercent) || math.IsInf(cpuPercent, 0) || cpuPercent < 0 || cpuPercent > 100 ||
		math.IsNaN(memoryPercent) || math.IsInf(memoryPercent, 0) || memoryPercent < 0 || memoryPercent > 100 {
		return fmt.Errorf("%w: CPU and memory percentages must be finite values from 0 to 100", domain.ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin save metric transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO metrics_history
		(timestamp, cpu_percent, mem_percent) VALUES ($1, $2, $3)`,
		time.Now().UTC(), cpuPercent, memoryPercent); err != nil {
		return fmt.Errorf("save metric point: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM metrics_history WHERE id NOT IN
		(SELECT id FROM metrics_history ORDER BY id DESC LIMIT 1440)`); err != nil {
		return fmt.Errorf("prune metric history: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit metric transaction: %w", err)
	}
	return nil
}

func (s *Store) GetStoredHistory(ctx context.Context, limit int) ([]domain.DataPoint, error) {
	if limit < 1 || limit > 10000 {
		return nil, fmt.Errorf("%w: metric history limit must be between 1 and 10000", domain.ErrInvalidInput)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT timestamp, cpu_percent, mem_percent
		FROM metrics_history ORDER BY timestamp DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("query metric history: %w", err)
	}
	defer rows.Close()

	points := make([]domain.DataPoint, 0)
	for rows.Next() {
		var timestamp time.Time
		var point domain.DataPoint
		if err := rows.Scan(&timestamp, &point.CPUPercent, &point.MemPercent); err != nil {
			return nil, fmt.Errorf("scan metric point: %w", err)
		}
		point.Timestamp = timestamp.UTC().Format(time.RFC3339Nano)
		point.CreatedAt = point.Timestamp
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate metric history: %w", err)
	}
	return points, nil
}
