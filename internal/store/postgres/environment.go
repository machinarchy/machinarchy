package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/machinarchy/machinarchy/internal/domain"
)

func (s *Store) CreateEnvironment(ctx context.Context, environment *domain.Environment) error {
	if environment == nil || environment.WorkspaceID <= 0 {
		return fmt.Errorf("%w: environment and workspace are required", domain.ErrInvalidInput)
	}
	err := s.db.QueryRowContext(ctx, `INSERT INTO environments (workspace_id, name, description)
		VALUES ($1, $2, $3) RETURNING id, created_at`, environment.WorkspaceID, environment.Name, environment.Description).
		Scan(&environment.ID, &environment.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: %v", domain.ErrEnvironmentAlreadyExists, err)
		}
		return fmt.Errorf("insert environment: %w", err)
	}
	return nil
}

func (s *Store) GetEnvironmentByID(ctx context.Context, workspaceID, id int) (*domain.Environment, error) {
	if workspaceID <= 0 {
		return nil, fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	environment := new(domain.Environment)
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, name, description, created_at
		FROM environments WHERE id = $1 AND workspace_id = $2`, id, workspaceID).Scan(
		&environment.ID, &environment.WorkspaceID, &environment.Name, &environment.Description, &environment.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrEnvironmentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query environment %d: %w", id, err)
	}
	return environment, nil
}

func (s *Store) ListEnvironments(ctx context.Context, workspaceID int) ([]*domain.Environment, error) {
	if workspaceID <= 0 {
		return nil, fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, name, description, created_at
		FROM environments WHERE workspace_id = $1 ORDER BY id`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	defer rows.Close()

	environments := make([]*domain.Environment, 0)
	for rows.Next() {
		environment := new(domain.Environment)
		if err := rows.Scan(&environment.ID, &environment.WorkspaceID, &environment.Name,
			&environment.Description, &environment.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan environment row: %w", err)
		}
		environments = append(environments, environment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate environments: %w", err)
	}
	return environments, nil
}

func (s *Store) UpdateEnvironment(ctx context.Context, environment *domain.Environment) error {
	if environment == nil || environment.WorkspaceID <= 0 {
		return fmt.Errorf("%w: environment and workspace are required", domain.ErrInvalidInput)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE environments SET name = $1,
		description = $2 WHERE id = $3 AND workspace_id = $4`,
		environment.Name, environment.Description, environment.ID, environment.WorkspaceID)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: %v", domain.ErrEnvironmentAlreadyExists, err)
		}
		return fmt.Errorf("update environment %d: %w", environment.ID, err)
	}
	return affected(result, domain.ErrEnvironmentNotFound)
}

func (s *Store) DeleteEnvironment(ctx context.Context, workspaceID, id int) error {
	if workspaceID <= 0 {
		return fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM environments WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if err != nil {
		return fmt.Errorf("delete environment %d: %w", id, err)
	}
	return affected(result, domain.ErrEnvironmentNotFound)
}
