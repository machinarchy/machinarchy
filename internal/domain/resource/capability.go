package resource

import (
	"context"
	"errors"
	"time"
)

// Agent commands for capability Operations (ADR-057). Each target of a
// capability Operation gets one request command, written in the same
// transaction that marks the target Running. The relay publishes it to the
// agent's JetStream subject with Nats-Msg-Id = CommandID and marks it
// published only after the PubAck; cancellation adds a cancel command.

// AgentCommandType is the command sent to the agent.
type AgentCommandType string

// Command types (agentv1 command types).
const (
	AgentCommandRequest AgentCommandType = "operation.request"
	AgentCommandCancel  AgentCommandType = "operation.cancel"
)

// AgentCommandState is where a command is in the relay.
type AgentCommandState string

// Command states.
const (
	AgentCommandPending   AgentCommandState = "pending"
	AgentCommandPublished AgentCommandState = "published"
	// AgentCommandExpired: never published before its lifetime ended.
	AgentCommandExpired AgentCommandState = "expired"
)

// AgentCommand is one durable command to an agent.
type AgentCommand struct {
	CommandID   string
	OperationID ID
	HostID      ID
	WorkspaceID int
	AgentID     string
	// AgentOperationID is the agent's ID for this target's call; results
	// carry it.
	AgentOperationID string
	Type             AgentCommandType
	State            AgentCommandState
	Attempts         int
	NextAttemptAt    time.Time
	CreatedAt        time.Time
	ExpiresAt        time.Time
	// ResultDueAt: a request target with no terminal result by then is Lost.
	ResultDueAt time.Time
	PublishedAt time.Time
}

// ErrAgentCommandNotFound reports an unknown agent operation or command.
var ErrAgentCommandNotFound = errors.New("agent command not found")

// CapabilityRepository persists agent commands and capability targets.
type CapabilityRepository interface {
	// DispatchCapabilityTarget records the request command and moves the
	// target from Pending to Running, in one transaction, while the
	// Operation is active. ErrOperationFinished otherwise.
	DispatchCapabilityTarget(ctx context.Context, command AgentCommand) error
	// AddCancelCommands records a cancel command for every Running target
	// of a capability Operation that has a request command.
	AddCancelCommands(ctx context.Context, workspaceID int, operationID ID, now time.Time, lifetime time.Duration) (int, error)
	// ListDueAgentCommands returns pending commands due at now.
	ListDueAgentCommands(ctx context.Context, now time.Time, limit int) ([]AgentCommand, error)
	// MarkAgentCommandPublished records the PubAck.
	MarkAgentCommandPublished(ctx context.Context, commandID string, now time.Time) error
	// RetryAgentCommand counts a failed publish and sets the next attempt.
	RetryAgentCommand(ctx context.Context, commandID string, next time.Time) error
	// ExpireAgentCommand marks a command that was never published in time.
	ExpireAgentCommand(ctx context.Context, commandID string) error
	// GetAgentRequest returns the request command for an agent operation ID.
	GetAgentRequest(ctx context.Context, agentOperationID string) (AgentCommand, error)
	// ListOverdueCapabilityTargets returns request commands whose target is
	// still Running after its ResultDueAt.
	ListOverdueCapabilityTargets(ctx context.Context, now time.Time, limit int) ([]AgentCommand, error)
}
