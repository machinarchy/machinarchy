// Package agents persists enrolled host agents for both SQL dialects.
package agents

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/store/sqlx"
)

// AuditAppender writes an audit event inside a transaction.
type AuditAppender func(context.Context, *sql.Tx, domain.AuditEvent) error

// Store implements agentgateway.Store.
type Store struct {
	db      *sql.DB
	dialect sqlx.Dialect
	audit   AuditAppender
	logger  *slog.Logger
}

var _ agentgateway.Store = (*Store)(nil)

// New creates a Store.
func New(db *sql.DB, dialect sqlx.Dialect, audit AuditAppender, logger *slog.Logger) (*Store, error) {
	if db == nil || !dialect.Valid() || audit == nil {
		return nil, errors.New("agent store requires a database, a supported dialect and an audit appender")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{db: db, dialect: dialect, audit: audit, logger: logger}, nil
}

const agentColumns = `id, public_key, hostname, agent_version, os, architecture, capabilities,
	lease_id, lease_expires_at, registered_at, last_seen_at, revoked_at`

func scanAgent(row interface{ Scan(...any) error }) (*agentgateway.Agent, error) {
	var agent agentgateway.Agent
	var key, capabilities, leaseExpires, registered, lastSeen string
	var revoked sql.NullString
	if err := row.Scan(&agent.ID, &key, &agent.Hostname, &agent.AgentVersion, &agent.OS, &agent.Architecture,
		&capabilities, &agent.LeaseID, &leaseExpires, &registered, &lastSeen, &revoked); err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("stored agent %s has an invalid public key", agent.ID)
	}
	agent.PublicKey = ed25519.PublicKey(raw)
	if err := json.Unmarshal([]byte(capabilities), &agent.Capabilities); err != nil {
		return nil, fmt.Errorf("decode agent capabilities: %w", err)
	}
	for target, text := range map[*time.Time]string{&agent.LeaseExpiresAt: leaseExpires, &agent.RegisteredAt: registered, &agent.LastSeenAt: lastSeen} {
		if *target, err = sqlx.ParseTime(text); err != nil {
			return nil, err
		}
	}
	if agent.RevokedAt, err = sqlx.ParseNullTime(revoked); err != nil {
		return nil, err
	}
	return &agent, nil
}

// RegisterAgent stores a new agent, or returns the one an earlier attempt with
// the same requestID created.
func (s *Store) RegisterAgent(ctx context.Context, agent agentgateway.Agent, requestID string) (agentgateway.Agent, error) {
	capabilities, err := json.Marshal(agent.Capabilities)
	if err != nil {
		return agentgateway.Agent{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return agentgateway.Agent{}, fmt.Errorf("begin agent registration: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "agent registration")
	existing, err := scanAgent(tx.QueryRowContext(ctx, s.dialect.Bind(`SELECT `+agentColumns+` FROM agents WHERE request_id = ? OR public_key = ?`),
		requestID, base64.StdEncoding.EncodeToString(agent.PublicKey)))
	if err == nil {
		// A retried request, or a known key enrolling again (an agent that lost
		// its identity). A revoked key is never re-admitted.
		if existing.RevokedAt != nil {
			return agentgateway.Agent{}, agentgateway.ErrAgentRevoked
		}
		return *existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return agentgateway.Agent{}, fmt.Errorf("read agent registration: %w", err)
	}
	_, err = tx.ExecContext(ctx, s.dialect.Bind(`INSERT INTO agents (id, workspace_id, request_id, public_key, hostname,
		agent_version, os, architecture, capabilities, lease_id, lease_expires_at, registered_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		agent.ID, domain.DefaultWorkspaceID, requestID, base64.StdEncoding.EncodeToString(agent.PublicKey), agent.Hostname,
		agent.AgentVersion, agent.OS, agent.Architecture, string(capabilities), agent.LeaseID, sqlx.Time(agent.LeaseExpiresAt),
		sqlx.Time(agent.RegisteredAt), sqlx.Time(agent.LastSeenAt))
	if err != nil {
		return agentgateway.Agent{}, fmt.Errorf("insert agent: %w", err)
	}
	if err := s.audit(ctx, tx, domain.AuditEvent{WorkspaceID: domain.DefaultWorkspaceID, Action: domain.AuditAgentRegistered,
		TargetType: domain.AuditTargetAgent, TargetID: agent.ID,
		Details: map[string]string{"hostname": agent.Hostname, "agent_version": agent.AgentVersion}}); err != nil {
		return agentgateway.Agent{}, err
	}
	if err := tx.Commit(); err != nil {
		return agentgateway.Agent{}, fmt.Errorf("commit agent registration: %w", err)
	}
	return agent, nil
}

// GetAgent returns one agent.
func (s *Store) GetAgent(ctx context.Context, id string) (*agentgateway.Agent, error) {
	agent, err := scanAgent(s.db.QueryRowContext(ctx, s.dialect.Bind(`SELECT `+agentColumns+` FROM agents WHERE id = ?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agentgateway.ErrAgentNotFound
	}
	return agent, err
}

// RenewAgentLease extends a live agent's lease.
func (s *Store) RenewAgentLease(ctx context.Context, id, leaseID string, expiresAt, now time.Time) error {
	result, err := s.db.ExecContext(ctx, s.dialect.Bind(`UPDATE agents SET lease_id = ?, lease_expires_at = ?, last_seen_at = ?
		WHERE id = ? AND revoked_at IS NULL`), leaseID, sqlx.Time(expiresAt), sqlx.Time(now), id)
	if err != nil {
		return fmt.Errorf("renew agent lease: %w", err)
	}
	if err := sqlx.RowsAffected(result, agentgateway.ErrAgentNotFound); err == nil {
		return nil
	}
	agent, err := s.GetAgent(ctx, id)
	if err != nil {
		return err
	}
	if agent.RevokedAt != nil {
		return agentgateway.ErrAgentRevoked
	}
	return agentgateway.ErrAgentNotFound
}

// ListAgents returns every agent, oldest first.
func (s *Store) ListAgents(ctx context.Context) ([]agentgateway.Agent, error) {
	rows, err := s.db.QueryContext(ctx, s.dialect.Bind(`SELECT `+agentColumns+` FROM agents WHERE workspace_id = ? ORDER BY registered_at, id`),
		domain.DefaultWorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer sqlx.CloseRows(rows, s.logger, "list agents")
	agents := make([]agentgateway.Agent, 0)
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, *agent)
	}
	return agents, rows.Err()
}

// RevokeAgent stops an agent's lease renewals and command delivery.
func (s *Store) RevokeAgent(ctx context.Context, id string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin agent revocation: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "agent revocation")
	result, err := tx.ExecContext(ctx, s.dialect.Bind(`UPDATE agents SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`), sqlx.Time(now), id)
	if err != nil {
		return fmt.Errorf("revoke agent: %w", err)
	}
	if err := sqlx.RowsAffected(result, agentgateway.ErrAgentNotFound); err != nil {
		return err
	}
	if err := s.audit(ctx, tx, domain.AuditEvent{WorkspaceID: domain.DefaultWorkspaceID, Action: domain.AuditAgentRevoked,
		TargetType: domain.AuditTargetAgent, TargetID: id}); err != nil {
		return err
	}
	return tx.Commit()
}
