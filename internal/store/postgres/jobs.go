package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

func (s *Store) CreateJob(ctx context.Context, job *domain.Job) error {
	if job == nil || job.WorkspaceID <= 0 || job.HostID <= 0 {
		return fmt.Errorf("%w: job, workspace, and positive host ID are required", domain.ErrInvalidInput)
	}
	var hostExists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hosts
		WHERE id = $1 AND workspace_id = $2)`, strconv.Itoa(job.HostID), job.WorkspaceID).Scan(&hostExists); err != nil {
		return fmt.Errorf("validate job host scope: %w", err)
	}
	if !hostExists {
		return domain.ErrHostNotFound
	}
	var startedAt sql.NullTime
	if !job.StartedAt.IsZero() {
		startedAt = sql.NullTime{Time: job.StartedAt, Valid: true}
	}
	err := s.db.QueryRowContext(ctx, `INSERT INTO jobs
		(workspace_id, host_id, command, status, triggered_by, started_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		job.WorkspaceID, job.HostID, job.Command, string(job.Status), job.TriggeredBy, startedAt,
	).Scan(&job.ID)
	if err != nil {
		return fmt.Errorf("insert job: %w", err)
	}
	return nil
}

func (s *Store) UpdateJobResult(ctx context.Context, id int, status, resultLog string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE jobs SET status = $1, result_log = $2,
		finished_at = $3 WHERE id = $4`, status, resultLog, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("update job %d result: %w", id, err)
	}
	return affected(result, sql.ErrNoRows)
}

func (s *Store) GetJobByID(ctx context.Context, workspaceID, id int) (*domain.Job, error) {
	if workspaceID <= 0 {
		return nil, fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	job, err := scanJob(s.db.QueryRowContext(ctx, `SELECT id, workspace_id, host_id, command, status,
		result_log, triggered_by, started_at, finished_at FROM jobs WHERE id = $1 AND workspace_id = $2`, id, workspaceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("query job %d: %w", id, err)
	}
	return job, nil
}

func (s *Store) ListJobs(ctx context.Context, filter domain.JobFilter) ([]*domain.Job, int, error) {
	if filter.WorkspaceID <= 0 {
		return nil, 0, fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	var total int
	var countErr error
	if filter.HostID == 0 {
		countErr = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE workspace_id = $1`, filter.WorkspaceID).Scan(&total)
	} else {
		countErr = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE workspace_id = $1 AND host_id = $2`, filter.WorkspaceID, filter.HostID).Scan(&total)
	}
	if countErr != nil {
		return nil, 0, fmt.Errorf("count jobs: %w", countErr)
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		return nil, 0, fmt.Errorf("%w: job limit must be between 1 and 100", domain.ErrInvalidInput)
	}
	if filter.Offset < 0 {
		return nil, 0, fmt.Errorf("%w: job offset must be non-negative", domain.ErrInvalidInput)
	}

	var rows *sql.Rows
	var err error
	if filter.HostID == 0 {
		rows, err = s.db.QueryContext(ctx, `SELECT id, workspace_id, host_id, command, status, result_log,
			triggered_by, started_at, finished_at FROM jobs WHERE workspace_id = $1 ORDER BY id DESC LIMIT $2 OFFSET $3`,
			filter.WorkspaceID, filter.Limit, filter.Offset)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT id, workspace_id, host_id, command, status, result_log,
			triggered_by, started_at, finished_at FROM jobs WHERE workspace_id = $1 AND host_id = $2
			ORDER BY id DESC LIMIT $3 OFFSET $4`, filter.WorkspaceID, filter.HostID, filter.Limit, filter.Offset)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]*domain.Job, 0)
	for rows.Next() {
		job := new(domain.Job)
		var startedAt, finishedAt sql.NullTime
		if err := rows.Scan(&job.ID, &job.WorkspaceID, &job.HostID, &job.Command, &job.Status,
			&job.ResultLog, &job.TriggeredBy, &startedAt, &finishedAt); err != nil {
			return nil, 0, fmt.Errorf("scan job row: %w", err)
		}
		if startedAt.Valid {
			job.StartedAt = startedAt.Time
		}
		if finishedAt.Valid {
			job.FinishedAt = finishedAt.Time
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate job rows: %w", err)
	}
	return jobs, total, nil
}

func scanJob(row *sql.Row) (*domain.Job, error) {
	job := new(domain.Job)
	var startedAt, finishedAt sql.NullTime
	if err := row.Scan(&job.ID, &job.WorkspaceID, &job.HostID, &job.Command, &job.Status,
		&job.ResultLog, &job.TriggeredBy, &startedAt, &finishedAt); err != nil {
		return nil, err
	}
	if startedAt.Valid {
		job.StartedAt = startedAt.Time
	}
	if finishedAt.Valid {
		job.FinishedAt = finishedAt.Time
	}
	return job, nil
}
