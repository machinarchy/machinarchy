package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

func (s *SQLiteStore) CreateJob(ctx context.Context, job *domain.Job) error {
	if job == nil || job.WorkspaceID <= 0 || job.HostID <= 0 {
		return fmt.Errorf("%w: job, workspace, and positive host ID are required", domain.ErrInvalidInput)
	}
	var hostExists bool
	if err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hosts
		WHERE id = ? AND workspace_id = ?)`, strconv.Itoa(job.HostID), job.WorkspaceID).Scan(&hostExists); err != nil {
		return fmt.Errorf("validate job host scope: %w", err)
	}
	if !hostExists {
		return domain.ErrHostNotFound
	}
	status := job.Status
	if status == "" {
		status = domain.JobStatusPending
	}
	var startedAt any
	if !job.StartedAt.IsZero() {
		startedAt = job.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	result, err := s.DB.ExecContext(ctx, `INSERT INTO jobs
		(workspace_id, host_id, command, status, triggered_by, started_at)
		VALUES (?, ?, ?, ?, ?, ?)`, job.WorkspaceID, job.HostID, job.Command, string(status), job.TriggeredBy, startedAt)
	if err != nil {
		return fmt.Errorf("insert job: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("read inserted job ID: %w", err)
	}
	job.ID, job.Status = int(id), status
	return nil
}

func (s *SQLiteStore) UpdateJobResult(ctx context.Context, id int, status, resultLog string) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE jobs SET status = ?, result_log = ?,
		finished_at = ? WHERE id = ?`, status, resultLog,
		time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("update job %d result: %w", id, err)
	}
	return requireRowsAffected(result, sql.ErrNoRows)
}

func (s *SQLiteStore) GetJobByID(ctx context.Context, workspaceID, id int) (*domain.Job, error) {
	if workspaceID <= 0 {
		return nil, fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	var job domain.Job
	var startedAt, finishedAt sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT id, workspace_id, host_id, command, status, result_log,
		triggered_by, started_at, finished_at FROM jobs WHERE id = ? AND workspace_id = ?`, id, workspaceID).Scan(
		&job.ID, &job.WorkspaceID, &job.HostID, &job.Command, &job.Status, &job.ResultLog,
		&job.TriggeredBy, &startedAt, &finishedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("query job %d: %w", id, err)
	}
	if job.StartedAt, err = parseOptionalTime(startedAt); err != nil {
		return nil, fmt.Errorf("parse start time for job %d: %w", id, err)
	}
	if job.FinishedAt, err = parseOptionalTime(finishedAt); err != nil {
		return nil, fmt.Errorf("parse finish time for job %d: %w", id, err)
	}
	return &job, nil
}

func (s *SQLiteStore) ListJobs(ctx context.Context, filter domain.JobFilter) ([]*domain.Job, int, error) {
	if filter.WorkspaceID <= 0 || filter.Limit < 1 || filter.Limit > 100 || filter.Offset < 0 || filter.HostID < 0 {
		return nil, 0, fmt.Errorf("%w: invalid job pagination filter", domain.ErrInvalidInput)
	}
	var total int
	var err error
	if filter.HostID == 0 {
		err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE workspace_id = ?`, filter.WorkspaceID).Scan(&total)
	} else {
		err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE workspace_id = ? AND host_id = ?`, filter.WorkspaceID, filter.HostID).Scan(&total)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("count jobs: %w", err)
	}

	var rows *sql.Rows
	if filter.HostID == 0 {
		rows, err = s.DB.QueryContext(ctx, `SELECT id, workspace_id, host_id, command, status, result_log,
			triggered_by, started_at, finished_at FROM jobs WHERE workspace_id = ? ORDER BY id DESC LIMIT ? OFFSET ?`,
			filter.WorkspaceID, filter.Limit, filter.Offset)
	} else {
		rows, err = s.DB.QueryContext(ctx, `SELECT id, workspace_id, host_id, command, status, result_log,
			triggered_by, started_at, finished_at FROM jobs WHERE workspace_id = ? AND host_id = ?
			ORDER BY id DESC LIMIT ? OFFSET ?`, filter.WorkspaceID, filter.HostID, filter.Limit, filter.Offset)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]*domain.Job, 0)
	for rows.Next() {
		job := new(domain.Job)
		var startedAt, finishedAt sql.NullString
		if err := rows.Scan(&job.ID, &job.WorkspaceID, &job.HostID, &job.Command, &job.Status,
			&job.ResultLog, &job.TriggeredBy, &startedAt, &finishedAt); err != nil {
			return nil, 0, fmt.Errorf("scan job row: %w", err)
		}
		if job.StartedAt, err = parseOptionalTime(startedAt); err != nil {
			return nil, 0, fmt.Errorf("parse start time for job %d: %w", job.ID, err)
		}
		if job.FinishedAt, err = parseOptionalTime(finishedAt); err != nil {
			return nil, 0, fmt.Errorf("parse finish time for job %d: %w", job.ID, err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate job rows: %w", err)
	}
	return jobs, total, nil
}

func parseOptionalTime(value sql.NullString) (time.Time, error) {
	if !value.Valid || value.String == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, value.String)
}
