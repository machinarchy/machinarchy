// Package domain defines the core domain models and interfaces for the application.
package domain

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	// ErrProtectedEnvironmentConfirmation: a guarded call touches a protected
	// environment whose name was not confirmed (answered 409).
	ErrProtectedEnvironmentConfirmation = errors.New("protected environment confirmation required")
	// ErrEnvironmentUnprotectOwnerOnly: only the workspace owner may unprotect.
	ErrEnvironmentUnprotectOwnerOnly = errors.New("only the workspace owner can unprotect an environment")
)

// DefaultEnvironmentName names the environment every workspace has and every
// host without an explicit environment joins (like the Kubernetes "default"
// namespace). Migration 000002 creates it.
const DefaultEnvironmentName = "default"

// Environment represents a deployment environment within a workspace.
type Environment struct {
	ID          int    `json:"id"`
	WorkspaceID int    `json:"-"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Protected environments need a confirmation (ConfirmedEnvironments) for
	// host changes and runs; the server enforces it for every client.
	Protected bool      `json:"protected"`
	CreatedAt time.Time `json:"createdAt"`
}

// EnvironmentStore defines the interface for managing environments in the data store.
type EnvironmentStore interface {
	CreateEnvironment(ctx context.Context, env *Environment) error
	GetEnvironmentByID(ctx context.Context, workspaceID, id int) (*Environment, error)
	ListEnvironments(ctx context.Context, workspaceID int) ([]*Environment, error)
	UpdateEnvironment(ctx context.Context, env *Environment) error
	DeleteEnvironment(ctx context.Context, workspaceID, id int) error
}

// ProtectedEnvironmentError names the protected environments a call touched
// without confirming them.
type ProtectedEnvironmentError struct{ Names []string }

func (e *ProtectedEnvironmentError) Error() string {
	return ErrProtectedEnvironmentConfirmation.Error() + ": " + strings.Join(e.Names, ", ")
}

// Is makes errors.Is(err, ErrProtectedEnvironmentConfirmation) true.
func (e *ProtectedEnvironmentError) Is(target error) bool {
	return target == ErrProtectedEnvironmentConfirmation
}

type confirmedEnvironmentsKey struct{}

// WithConfirmedEnvironments carries the environment names the caller typed
// (the request's confirmEnvironment) down to the services that enforce them.
func WithConfirmedEnvironments(ctx context.Context, names []string) context.Context {
	return context.WithValue(ctx, confirmedEnvironmentsKey{}, names)
}

// ConfirmedEnvironments returns the confirmed names carried by ctx.
func ConfirmedEnvironments(ctx context.Context) []string {
	names, _ := ctx.Value(confirmedEnvironmentsKey{}).([]string)
	return names
}

// ConfirmEnvironment is the request field confirmEnvironment: one name or a
// list of names (JSON string or array).
type ConfirmEnvironment []string

// UnmarshalJSON accepts "name" or ["a","b"].
func (c *ConfirmEnvironment) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*c = ConfirmEnvironment{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*c = many
	return nil
}
