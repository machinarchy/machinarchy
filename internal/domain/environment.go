// internal/domain/environment.go
package domain

import (
	"context"
	"errors"
	"time"
)

var ErrEnvironmentNotFound = errors.New("environment not found")

var ErrEnvironmentAlreadyExists = errors.New("environment name is already registered")

type Environment struct {
	ID          int       `json:"id"`
	WorkspaceID int       `json:"-"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
}

type EnvironmentStore interface {
	CreateEnvironment(ctx context.Context, env *Environment) error
	GetEnvironmentByID(ctx context.Context, workspaceID, id int) (*Environment, error)
	ListEnvironments(ctx context.Context, workspaceID int) ([]*Environment, error)
	UpdateEnvironment(ctx context.Context, env *Environment) error
	DeleteEnvironment(ctx context.Context, workspaceID, id int) error
}
