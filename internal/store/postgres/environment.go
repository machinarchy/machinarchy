package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/machinarchy/machinarchy/internal/domain"
)

func (s *Store) CreateEnvironment(ctx context.Context, environment *domain.Environment) error {
	if environment == nil {
		return fmt.Errorf("%w: environment is required", domain.ErrInvalidInput)
	}
	err := s.db.QueryRowContext(ctx, `INSERT INTO environments (name, description)
		VALUES ($1, $2) RETURNING id, created_at`, environment.Name, environment.Description).
		Scan(&environment.ID, &environment.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: %v", domain.ErrEnvironmentAlreadyExists, err)
		}
		return fmt.Errorf("insert environment: %w", err)
	}
	return nil
}

func (s *Store) GetEnvironmentByID(ctx context.Context, id int) (*domain.Environment, error) {
	environment := new(domain.Environment)
	err := s.db.QueryRowContext(ctx, `SELECT id, name, description, created_at
		FROM environments WHERE id = $1`, id).Scan(
		&environment.ID, &environment.Name, &environment.Description, &environment.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrEnvironmentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query environment %d: %w", id, err)
	}
	return environment, nil
}

func (s *Store) ListEnvironments(ctx context.Context) ([]*domain.Environment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, created_at
		FROM environments ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	defer rows.Close()

	environments := make([]*domain.Environment, 0)
	for rows.Next() {
		environment := new(domain.Environment)
		if err := rows.Scan(&environment.ID, &environment.Name,
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
	if environment == nil {
		return fmt.Errorf("%w: environment is required", domain.ErrInvalidInput)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE environments SET name = $1,
		description = $2 WHERE id = $3`, environment.Name, environment.Description, environment.ID)
	if err != nil {
		return fmt.Errorf("update environment %d: %w", environment.ID, err)
	}
	return affected(result, domain.ErrEnvironmentNotFound)
}

func (s *Store) DeleteEnvironment(ctx context.Context, id int) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM environments WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete environment %d: %w", id, err)
	}
	return affected(result, domain.ErrEnvironmentNotFound)
}
