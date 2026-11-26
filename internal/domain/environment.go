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
	// ErrEnvironmentUnknown: a request names an environment that does not exist
	// in the caller's workspace (answered 422; the environment is a body field).
	ErrEnvironmentUnknown = errors.New("environment does not exist in this workspace")
	// ErrEnvironmentInUse: the environment still has hosts.
	ErrEnvironmentInUse = errors.New("environment still has hosts; move or remove them first")
	// ErrDefaultEnvironment: the default environment cannot be deleted or renamed.
	ErrDefaultEnvironment = errors.New("the default environment cannot be deleted or renamed")
)

// DefaultEnvironmentName names the environment every workspace has and every
// host without an explicit environment joins (like the Kubernetes "default"
// namespace). Migration 000002 creates it.
const DefaultEnvironmentName = "default"

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
