// Package agentgateway is the controller side of protocol/agentv1: it enrols
// host agents, renews their leases, sends signed capability commands over
// JetStream and verifies their results (AUDIT/04 M-1 to M-8).
//
// It is deliberately minimal: one shared enrollment token, synchronous
// capability calls, and no Operation integration yet (M-9).
package agentgateway

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"
)

// Agent is an enrolled host agent.
type Agent struct {
	ID             string            `json:"id"`
	Hostname       string            `json:"hostname"`
	AgentVersion   string            `json:"agentVersion"`
	OS             string            `json:"os"`
	Architecture   string            `json:"architecture"`
	Capabilities   []string          `json:"capabilities"`
	PublicKey      ed25519.PublicKey `json:"-"`
	LeaseID        string            `json:"-"`
	LeaseExpiresAt time.Time         `json:"leaseExpiresAt"`
	RegisteredAt   time.Time         `json:"registeredAt"`
	LastSeenAt     time.Time         `json:"lastSeenAt"`
	RevokedAt      *time.Time        `json:"revokedAt,omitempty"`
}

// Connected reports whether the agent holds an unexpired, unrevoked lease.
func (a Agent) Connected(now time.Time) bool {
	return a.RevokedAt == nil && now.Before(a.LeaseExpiresAt)
}

// Errors returned by Store implementations.
var (
	ErrAgentNotFound = errors.New("agent not found")
	ErrAgentRevoked  = errors.New("agent has been revoked")
)

// Store persists agents. RegisterAgent is idempotent on requestID: a retried
// registration returns the agent created by the first one.
type Store interface {
	RegisterAgent(ctx context.Context, agent Agent, requestID string) (Agent, error)
	GetAgent(ctx context.Context, id string) (*Agent, error)
	RenewAgentLease(ctx context.Context, id, leaseID string, expiresAt, now time.Time) error
	ListAgents(ctx context.Context) ([]Agent, error)
	RevokeAgent(ctx context.Context, id string, now time.Time) error
}
