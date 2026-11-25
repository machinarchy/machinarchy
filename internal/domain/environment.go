// Package domain defines the core domain models and interfaces for the application.
package domain

import (
	"context"
	"errors"
	"time"
)

// Errors related to environment management.
var (
	ErrEnvironmentNotFound      = errors.New("environment not found")
	ErrEnvironmentAlreadyExists = errors.New("environment name is already registered")
)

// Environment represents a deployment environment within a workspace.
type Environment struct {
	ID          int       `json:"id"`
	WorkspaceID int       `json:"-"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
}

// EnvironmentStore defines the interface for managing environments in the data store.
type EnvironmentStore interface {
	CreateEnvironment(ctx context.Context, env *Environment) error
	GetEnvironmentByID(ctx context.Context, workspaceID, id int) (*Environment, error)
	ListEnvironments(ctx context.Context, workspaceID int) ([]*Environment, error)
	UpdateEnvironment(ctx context.Context, env *Environment) error
	DeleteEnvironment(ctx context.Context, workspaceID, id int) error
}
