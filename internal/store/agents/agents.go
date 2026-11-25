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
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/eventing"
	"github.com/machinarchy/machinarchy/internal/store/resources"
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

// RegisterAgent implements agentgateway.Store: agents are found by public key,
// a retry of the same request needs no new enrolment, and anything else
// consumes the enrolment in the same transaction as the registration.
func (s *Store) RegisterAgent(ctx context.Context, agent agentgateway.Agent, requestID string, enrollment agentgateway.Enrollment) (agentgateway.Agent, error) {
	capabilities, err := json.Marshal(agent.Capabilities)
	if err != nil {
		return agentgateway.Agent{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return agentgateway.Agent{}, fmt.Errorf("begin agent registration: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "agent registration")
	publicKey := base64.StdEncoding.EncodeToString(agent.PublicKey)
	var existingID, existingRequest string
	var revoked sql.NullString
	err = tx.QueryRowContext(ctx, s.dialect.Bind(`SELECT id, request_id, revoked_at FROM agents WHERE public_key = ?`), publicKey).
		Scan(&existingID, &existingRequest, &revoked)
	switch {
	case err == nil && revoked.Valid:
		return agentgateway.Agent{}, agentgateway.ErrAgentRevoked // a revoked key is never re-admitted
	case err == nil && existingRequest == requestID:
		existing, err := scanAgent(tx.QueryRowContext(ctx, s.dialect.Bind(`SELECT `+agentColumns+` FROM agents WHERE id = ?`), existingID))
		if err != nil {
			return agentgateway.Agent{}, fmt.Errorf("read agent registration: %w", err)
		}
		return *existing, nil // a retried request
	case err == nil:
		// A known key enrolling again (an agent that lost its identity) must
		// present a valid enrolment like a new one.
		consumed, err := s.consumeEnrollment(ctx, tx, enrollment, requestID, existingID, agent.RegisteredAt)
		if err != nil {
			return agentgateway.Agent{}, err
		}
		if err := s.bindHost(ctx, tx, consumed, existingID, agent.RegisteredAt); err != nil {
			return agentgateway.Agent{}, err
		}
		existing, err := scanAgent(tx.QueryRowContext(ctx, s.dialect.Bind(`SELECT `+agentColumns+` FROM agents WHERE id = ?`), existingID))
		if err != nil {
			return agentgateway.Agent{}, fmt.Errorf("read agent registration: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return agentgateway.Agent{}, fmt.Errorf("commit agent re-enrolment: %w", err)
		}
		return *existing, nil
	case !errors.Is(err, sql.ErrNoRows):
		return agentgateway.Agent{}, fmt.Errorf("read agent registration: %w", err)
	}
	consumed, err := s.consumeEnrollment(ctx, tx, enrollment, requestID, agent.ID, agent.RegisteredAt)
	if err != nil {
		return agentgateway.Agent{}, err
	}
	_, err = tx.ExecContext(ctx, s.dialect.Bind(`INSERT INTO agents (id, workspace_id, request_id, public_key, hostname,
		agent_version, os, architecture, capabilities, lease_id, lease_expires_at, registered_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		agent.ID, domain.DefaultWorkspaceID, requestID, publicKey, agent.Hostname,
		agent.AgentVersion, agent.OS, agent.Architecture, string(capabilities), agent.LeaseID, sqlx.Time(agent.LeaseExpiresAt),
		sqlx.Time(agent.RegisteredAt), sqlx.Time(agent.LastSeenAt))
	if err != nil {
		return agentgateway.Agent{}, fmt.Errorf("insert agent: %w", err)
	}
	if err := s.audit(ctx, tx, domain.AuditEvent{WorkspaceID: domain.DefaultWorkspaceID, Action: domain.AuditAgentRegistered,
		TargetType: domain.AuditTargetAgent, TargetID: agent.ID,
		Details: mergeDetails(map[string]string{"hostname": agent.Hostname, "agent_version": agent.AgentVersion}, consumed.details)}); err != nil {
		return agentgateway.Agent{}, err
	}
	if err := s.bindHost(ctx, tx, consumed, agent.ID, agent.RegisteredAt); err != nil {
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
	return collect(rows, scanAgent)
}

// collect scans every row with scan.
func collect[T any](rows *sql.Rows, scan func(interface{ Scan(...any) error }) (*T, error)) ([]T, error) {
	out := make([]T, 0)
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
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
	rows, err := tx.QueryContext(ctx, s.dialect.Bind(`SELECT host_id FROM agent_host_bindings WHERE agent_id = ?`), id)
	if err != nil {
		return fmt.Errorf("read the revoked agent's hosts: %w", err)
	}
	var hosts []string
	for rows.Next() {
		var host string
		if err := rows.Scan(&host); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read the revoked agent's hosts: %w", err)
		}
		hosts = append(hosts, host)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("read the revoked agent's hosts: %w", err)
	}
	for _, host := range hosts {
		if err := s.queueHostReconcile(ctx, tx, host, "agent-revoked", now); err != nil {
			return err
		}
	}
	if err := s.audit(ctx, tx, domain.AuditEvent{WorkspaceID: domain.DefaultWorkspaceID, Action: domain.AuditAgentRevoked,
		TargetType: domain.AuditTargetAgent, TargetID: id}); err != nil {
		return err
	}
	return tx.Commit()
}

// consumeEnrollment checks the registration's enrolment and, for a per-host
// token, marks it used. It returns audit details naming how the agent
// enrolled. The UPDATE re-checks used_at, so two registrations racing for one
// token cannot both succeed.
// consumedEnrollment is what a registration's enrolment allows.
type consumedEnrollment struct {
	details map[string]string
	tokenID string
	hostID  string // bind the agent to this managed host ("" for none)
}

func (s *Store) consumeEnrollment(ctx context.Context, tx *sql.Tx, enrollment agentgateway.Enrollment, requestID, agentID string, now time.Time) (consumedEnrollment, error) {
	if enrollment.Shared {
		return consumedEnrollment{details: map[string]string{"enrollment": "shared_token"}}, nil
	}
	if enrollment.TokenHash == "" {
		return consumedEnrollment{}, agentgateway.ErrEnrollmentRejected
	}
	token, err := scanToken(tx.QueryRowContext(ctx, s.dialect.Bind(`SELECT `+enrollmentColumns+` FROM agent_enrollment_tokens
		WHERE token_hash = ? AND workspace_id = ?`), enrollment.TokenHash, domain.DefaultWorkspaceID))
	if errors.Is(err, sql.ErrNoRows) {
		return consumedEnrollment{}, agentgateway.ErrEnrollmentRejected
	}
	if err != nil {
		return consumedEnrollment{}, fmt.Errorf("read enrollment token: %w", err)
	}
	if !token.Usable(now) || !token.Matches(enrollment.Hostname) {
		return consumedEnrollment{}, agentgateway.ErrEnrollmentRejected
	}
	result, err := tx.ExecContext(ctx, s.dialect.Bind(`UPDATE agent_enrollment_tokens SET used_at = ?, used_request_id = ?, used_by_agent = ?
		WHERE id = ? AND used_at IS NULL AND revoked_at IS NULL`), sqlx.Time(now), requestID, agentID, token.ID)
	if err != nil {
		return consumedEnrollment{}, fmt.Errorf("consume enrollment token: %w", err)
	}
	if err := sqlx.RowsAffected(result, agentgateway.ErrEnrollmentRejected); err != nil {
		return consumedEnrollment{}, err
	}
	details := map[string]string{"enrollment": "token", "token_id": token.ID}
	if token.HostID != "" {
		details["host_id"] = token.HostID
	}
	return consumedEnrollment{details: details, tokenID: token.ID, hostID: token.HostID}, nil
}

// bindHost binds the agent to the managed host its token names, replacing
// any earlier binding of that host or that agent (a replaced machine).
func (s *Store) bindHost(ctx context.Context, tx *sql.Tx, consumed consumedEnrollment, agentID string, now time.Time) error {
	if consumed.hostID == "" {
		return nil
	}
	var workspaceID int
	err := tx.QueryRowContext(ctx, s.dialect.Bind(`SELECT workspace_id FROM managed_hosts WHERE id = ?`), consumed.hostID).Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return agentgateway.ErrEnrollmentRejected // the host was deleted after the token was issued
	}
	if err != nil {
		return fmt.Errorf("read host to bind: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.dialect.Bind(`DELETE FROM agent_host_bindings WHERE host_id = ? OR agent_id = ?`), consumed.hostID, agentID); err != nil {
		return fmt.Errorf("replace host binding: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.dialect.Bind(`INSERT INTO agent_host_bindings (host_id, workspace_id, agent_id, token_id, bound_at_ns)
		VALUES (?, ?, ?, ?, ?)`), consumed.hostID, workspaceID, agentID, consumed.tokenID, now.UnixNano()); err != nil {
		return fmt.Errorf("bind host: %w", err)
	}
	if err := s.queueHostReconcile(ctx, tx, consumed.hostID, "agent-bound", now); err != nil {
		return err
	}
	return s.audit(ctx, tx, domain.AuditEvent{WorkspaceID: workspaceID, Action: domain.AuditAgentHostBound,
		TargetType: domain.AuditTargetAgent, TargetID: agentID,
		Details: map[string]string{"host_id": consumed.hostID, "token_id": consumed.tokenID}})
}

// HostAgent implements agentgateway.Store.
func (s *Store) HostAgent(ctx context.Context, workspaceID int, hostID string) (*agentgateway.Agent, error) {
	agent, err := scanAgent(s.db.QueryRowContext(ctx, s.dialect.Bind(`SELECT `+prefixedAgentColumns+` FROM agent_host_bindings b
		JOIN agents a ON a.id = b.agent_id WHERE b.workspace_id = ? AND b.host_id = ? AND a.revoked_at IS NULL`), workspaceID, hostID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agentgateway.ErrHostNotBound
	}
	if err != nil {
		return nil, fmt.Errorf("read host binding: %w", err)
	}
	return agent, nil
}

var prefixedAgentColumns = func() string {
	fields := strings.Split(agentColumns, ",")
	for i, field := range fields {
		fields[i] = "a." + strings.TrimSpace(field)
	}
	return strings.Join(fields, ", ")
}()

func mergeDetails(base, extra map[string]string) map[string]string {
	for key, value := range extra {
		base[key] = value
	}
	return base
}

const enrollmentColumns = `id, hostname, host_id, created_by, created_at, expires_at, used_at, used_by_agent, revoked_at`

func scanToken(row interface{ Scan(...any) error }) (*agentgateway.EnrollmentToken, error) {
	var token agentgateway.EnrollmentToken
	var hostname, usedAt, usedBy, revokedAt sql.NullString
	var created, expires string
	if err := row.Scan(&token.ID, &hostname, &token.HostID, &token.CreatedBy, &created, &expires, &usedAt, &usedBy, &revokedAt); err != nil {
		return nil, err
	}
	token.Hostname, token.UsedByAgent = hostname.String, usedBy.String
	var err error
	if token.CreatedAt, err = sqlx.ParseTime(created); err != nil {
		return nil, err
	}
	if token.ExpiresAt, err = sqlx.ParseTime(expires); err != nil {
		return nil, err
	}
	if token.UsedAt, err = sqlx.ParseNullTime(usedAt); err != nil {
		return nil, err
	}
	if token.RevokedAt, err = sqlx.ParseNullTime(revokedAt); err != nil {
		return nil, err
	}
	return &token, nil
}

var _ agentgateway.TokenStore = (*Store)(nil)

// CreateEnrollmentToken stores a new token (its hash only) and audits it.
func (s *Store) CreateEnrollmentToken(ctx context.Context, token agentgateway.EnrollmentToken) error {
	if token.ID == "" || len(token.Hash) != 64 || token.CreatedBy <= 0 {
		return fmt.Errorf("%w: incomplete enrollment token", domain.ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin enrollment token: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "enrollment token")
	var hostname any
	if token.Hostname != "" {
		hostname = token.Hostname
	}
	if token.HostID != "" {
		var exists int
		err := tx.QueryRowContext(ctx, s.dialect.Bind(`SELECT 1 FROM managed_hosts WHERE id = ? AND workspace_id = ?`), token.HostID, domain.DefaultWorkspaceID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: no managed host %s", domain.ErrInvalidInput, token.HostID)
		}
		if err != nil {
			return fmt.Errorf("read host for enrollment token: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, s.dialect.Bind(`INSERT INTO agent_enrollment_tokens (id, workspace_id, token_hash, hostname, host_id,
		created_by, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`), token.ID, domain.DefaultWorkspaceID, token.Hash, hostname,
		token.HostID, token.CreatedBy, sqlx.Time(token.CreatedAt), sqlx.Time(token.ExpiresAt)); err != nil {
		return fmt.Errorf("insert enrollment token: %w", err)
	}
	details := map[string]string{"expires_at": sqlx.Time(token.ExpiresAt)}
	if token.HostID != "" {
		details["host_id"] = token.HostID
	}
	if token.Hostname != "" {
		details["hostname"] = token.Hostname
	}
	if err := s.audit(ctx, tx, domain.AuditEvent{WorkspaceID: domain.DefaultWorkspaceID, ActorUserID: token.CreatedBy,
		Action: domain.AuditAgentEnrollmentTokenIssued, TargetType: domain.AuditTargetEnrollment, TargetID: token.ID,
		Details: details}); err != nil {
		return err
	}
	return tx.Commit()
}

// ListEnrollmentTokens returns the workspace's tokens, newest first.
func (s *Store) ListEnrollmentTokens(ctx context.Context) ([]agentgateway.EnrollmentToken, error) {
	rows, err := s.db.QueryContext(ctx, s.dialect.Bind(`SELECT `+enrollmentColumns+` FROM agent_enrollment_tokens
		WHERE workspace_id = ? ORDER BY created_at DESC, id`), domain.DefaultWorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("list enrollment tokens: %w", err)
	}
	defer sqlx.CloseRows(rows, s.logger, "list enrollment tokens")
	return collect(rows, scanToken)
}

// RevokeEnrollmentToken revokes an unused, unrevoked token.
func (s *Store) RevokeEnrollmentToken(ctx context.Context, id string, actorID int, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin enrollment token revocation: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "enrollment token revocation")
	result, err := tx.ExecContext(ctx, s.dialect.Bind(`UPDATE agent_enrollment_tokens SET revoked_at = ?
		WHERE id = ? AND workspace_id = ? AND used_at IS NULL AND revoked_at IS NULL`), sqlx.Time(now), id, domain.DefaultWorkspaceID)
	if err != nil {
		return fmt.Errorf("revoke enrollment token: %w", err)
	}
	if err := sqlx.RowsAffected(result, agentgateway.ErrEnrollmentTokenNotFound); err != nil {
		return err
	}
	if err := s.audit(ctx, tx, domain.AuditEvent{WorkspaceID: domain.DefaultWorkspaceID, ActorUserID: actorID,
		Action: domain.AuditAgentEnrollmentTokenRevoked, TargetType: domain.AuditTargetEnrollment, TargetID: id}); err != nil {
		return err
	}
	return tx.Commit()
}

// queueHostReconcile asks the Control Manager to reconcile a host whose agent
// binding changed, so its Ready condition follows at once rather than at the
// next periodic resync (AUDIT F17-04).
func (s *Store) queueHostReconcile(ctx context.Context, tx *sql.Tx, hostID, change string, now time.Time) error {
	var workspaceID int
	var generation, revision uint64
	err := tx.QueryRowContext(ctx, s.dialect.Bind(`SELECT workspace_id, generation, revision FROM managed_hosts WHERE id = ?`), hostID).
		Scan(&workspaceID, &generation, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // the host is gone; nothing to reconcile
	}
	if err != nil {
		return fmt.Errorf("read host to reconcile: %w", err)
	}
	event, err := eventing.NewHostEvent(eventing.HostSpecChanged, workspaceID, eventing.Subject{Kind: "host",
		ID: resource.ID(hostID), Generation: generation, Revision: revision}, change, now, "", "", nil)
	if err != nil {
		return fmt.Errorf("construct host reconcile event: %w", err)
	}
	return resources.InsertHostEvent(ctx, tx, s.dialect.Bind, event)
}
