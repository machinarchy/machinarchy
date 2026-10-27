package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	sqliteDriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func (s *SQLiteStore) CreateEnvironment(ctx context.Context, environment *domain.Environment) error {
	if environment == nil || environment.WorkspaceID <= 0 {
		return fmt.Errorf("%w: environment and workspace are required", domain.ErrInvalidInput)
	}
	now := time.Now().UTC()
	result, err := s.DB.ExecContext(ctx, `INSERT INTO environments (workspace_id, name, description, created_at)
		VALUES (?, ?, ?, ?)`, environment.WorkspaceID, environment.Name, environment.Description, now.Format(time.RFC3339Nano))
	if err != nil {
		var sqliteErr *sqliteDriver.Error
		if errors.As(err, &sqliteErr) && (sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY) {
			return fmt.Errorf("%w: %v", domain.ErrEnvironmentAlreadyExists, err)
		}
		return fmt.Errorf("insert environment: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("read inserted environment ID: %w", err)
	}
	environment.ID, environment.CreatedAt = int(id), now
	return nil
}

func (s *SQLiteStore) GetEnvironmentByID(ctx context.Context, workspaceID, id int) (*domain.Environment, error) {
	if workspaceID <= 0 {
		return nil, fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	environment := new(domain.Environment)
	var createdAt string
	err := s.DB.QueryRowContext(ctx, `SELECT id, workspace_id, name, description, created_at
		FROM environments WHERE id = ? AND workspace_id = ?`, id, workspaceID).Scan(
		&environment.ID, &environment.WorkspaceID, &environment.Name, &environment.Description, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrEnvironmentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query environment %d: %w", id, err)
	}
	if environment.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return nil, fmt.Errorf("parse creation time for environment %d: %w", id, err)
	}
	return environment, nil
}

func (s *SQLiteStore) ListEnvironments(ctx context.Context, workspaceID int) ([]*domain.Environment, error) {
	if workspaceID <= 0 {
		return nil, fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, workspace_id, name, description, created_at
		FROM environments WHERE workspace_id = ? ORDER BY id ASC`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	defer rows.Close()

	environments := make([]*domain.Environment, 0)
	for rows.Next() {
		environment := new(domain.Environment)
		var createdAt string
		if err := rows.Scan(&environment.ID, &environment.WorkspaceID, &environment.Name,
			&environment.Description, &createdAt); err != nil {
			return nil, fmt.Errorf("scan environment row: %w", err)
		}
		if environment.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
			return nil, fmt.Errorf("parse creation time for environment %d: %w", environment.ID, err)
		}
		environments = append(environments, environment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate environments: %w", err)
	}
	return environments, nil
}

func (s *SQLiteStore) UpdateEnvironment(ctx context.Context, environment *domain.Environment) error {
	if environment == nil || environment.WorkspaceID <= 0 {
		return fmt.Errorf("%w: environment and workspace are required", domain.ErrInvalidInput)
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE environments SET name = ?, description = ?
		WHERE id = ? AND workspace_id = ?`,
		environment.Name, environment.Description, environment.ID, environment.WorkspaceID)
	if err != nil {
		var sqliteErr *sqliteDriver.Error
		if errors.As(err, &sqliteErr) && (sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY) {
			return fmt.Errorf("%w: %v", domain.ErrEnvironmentAlreadyExists, err)
		}
		return fmt.Errorf("update environment %d: %w", environment.ID, err)
	}
	return requireRowsAffected(result, domain.ErrEnvironmentNotFound)
}

func (s *SQLiteStore) DeleteEnvironment(ctx context.Context, workspaceID, id int) error {
	if workspaceID <= 0 {
		return fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	result, err := s.DB.ExecContext(ctx, `DELETE FROM environments WHERE id = ? AND workspace_id = ?`, id, workspaceID)
	if err != nil {
		return fmt.Errorf("delete environment %d: %w", id, err)
	}
	return requireRowsAffected(result, domain.ErrEnvironmentNotFound)
}
