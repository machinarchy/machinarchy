package agentgateway

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
	"github.com/machinarchy/machinarchy/internal/agentgateway/bus"
)

// Config configures the gateway.
type Config struct {
	// Bus is the controller-internal command and result bus (ADR-059).
	Bus bus.Bus
	// Signer signs everything the gateway sends: a local Ed25519 key or a
	// key held by a KMS provider (Phase 16 / K-phases). Agents pin its public
	// key.
	Signer crypto.Signer
	// EnrollmentToken is an optional shared token accepted from any host, for
	// development only (configuration refuses it in production). Hosts
	// normally enrol with owner-issued, single-use tokens (TokenStore).
	EnrollmentToken string
	LeaseTTL        time.Duration
	Heartbeat       time.Duration
	CommandLifetime time.Duration
	ConnectTimeout  time.Duration
	Logger          *slog.Logger
	Registerer      prometheus.Registerer
	// NATSIssuer, when set, gives every enrolled agent its own NATS user JWT
	// in register and renew responses (operator mode).
	NATSIssuer *NATSIssuer
}

// Defaults applied by New when a field is zero.
const (
	DefaultLeaseTTL        = time.Minute
	DefaultHeartbeat       = 15 * time.Second
	DefaultCommandLifetime = 5 * time.Minute
)

// ErrUnavailable reports that the gateway is not connected or not running.
var ErrUnavailable = bus.ErrUnavailable

// Gateway serves registration, lease renewal and results, and sends commands.
type Gateway struct {
	cfg     Config
	store   Store
	logger  *slog.Logger
	metrics *gatewayMetrics

	bus bus.Bus

	results atomic.Pointer[ResultHandler]
}

// ResultHandler persists an agent's result (ADR-057). The gateway sends
// result.ack for a terminal result only after it returns nil, so a result
// that could not be stored is republished by the agent and none is lost.
type ResultHandler func(ctx context.Context, result agentv1.Result) error

// SetResultHandler installs the handler; without one, results are dropped
// unacknowledged.
func (g *Gateway) SetResultHandler(handler ResultHandler) { g.results.Store(&handler) }

type gatewayMetrics struct {
	registrations *prometheus.CounterVec
	renewals      *prometheus.CounterVec
	results       *prometheus.CounterVec
	commands      *prometheus.CounterVec
}

// snapshotSigner is a Signer whose key can change (agentkeys.Manager):
// Snapshot fixes the key for one message.
type snapshotSigner interface {
	Snapshot() crypto.Signer
}

// signer returns the key to seal one message with.
func (g *Gateway) signer() crypto.Signer {
	if s, ok := g.cfg.Signer.(snapshotSigner); ok {
		return s.Snapshot()
	}
	return g.cfg.Signer
}

// New validates cfg and creates a Gateway; Start connects it.
func New(store Store, cfg Config) (*Gateway, error) {
	if store == nil || cfg.Bus == nil || cfg.Signer == nil {
		return nil, errors.New("agent gateway requires a store, a bus and an Ed25519 signer")
	}
	if public, ok := cfg.Signer.Public().(ed25519.PublicKey); !ok || len(public) != ed25519.PublicKeySize {
		return nil, errors.New("the agent gateway signer must hold an Ed25519 key")
	}
	if cfg.EnrollmentToken != "" && (len(cfg.EnrollmentToken) < agentv1.MinEnrollmentToken || len(cfg.EnrollmentToken) > agentv1.MaxEnrollmentToken) {
		return nil, fmt.Errorf("the shared agent enrollment token must be %d to %d characters", agentv1.MinEnrollmentToken, agentv1.MaxEnrollmentToken)
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = DefaultLeaseTTL
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = DefaultHeartbeat
	}
	if cfg.Heartbeat < agentv1.MinHeartbeatInterval || cfg.Heartbeat > agentv1.MaxHeartbeatInterval || cfg.Heartbeat*2 > cfg.LeaseTTL {
		return nil, errors.New("agent heartbeat must be within protocol bounds and at most half the lease TTL")
	}
	if cfg.CommandLifetime <= 0 || cfg.CommandLifetime > agentv1.MaxCommandLifetime {
		cfg.CommandLifetime = DefaultCommandLifetime
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 5 * time.Second
	}
	if cfg.NATSIssuer != nil {
		if cfg.NATSIssuer.TTL == 0 {
			cfg.NATSIssuer.TTL = DefaultAgentJWTTTL
		}
		if err := cfg.NATSIssuer.validate(cfg.Heartbeat); err != nil {
			return nil, err
		}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	g := &Gateway{cfg: cfg, store: store, bus: cfg.Bus, logger: cfg.Logger.With("component", "agent-gateway"),
		metrics: newMetrics(cfg.Registerer)}
	return g, nil
}

func newMetrics(registerer prometheus.Registerer) *gatewayMetrics {
	m := &gatewayMetrics{
		registrations: prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "machinarchy", Subsystem: "agent_gateway",
			Name: "registrations_total", Help: "Agent registration requests by outcome."}, []string{"outcome"}),
		renewals: prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "machinarchy", Subsystem: "agent_gateway",
			Name: "lease_renewals_total", Help: "Agent lease renewals by outcome."}, []string{"outcome"}),
		results: prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "machinarchy", Subsystem: "agent_gateway",
			Name: "results_total", Help: "Verified agent results by status."}, []string{"status"}),
		commands: prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "machinarchy", Subsystem: "agent_gateway",
			Name: "commands_total", Help: "Commands published to agents by type and outcome."}, []string{"type", "outcome"}),
	}
	if registerer != nil {
		for _, c := range []prometheus.Collector{m.registrations, m.renewals, m.results, m.commands} {
			if err := registerer.Register(c); err != nil {
				var already prometheus.AlreadyRegisteredError
				if !errors.As(err, &already) {
					panic(err) // programming error: conflicting metric definitions
				}
			}
		}
	}
	return m
}

// Start serves registration, renewal and results over the bus until ctx
// ends.
func (g *Gateway) Start(ctx context.Context) error {
	return g.bus.Serve(ctx, bus.Handlers{Register: g.handleRegister, Renew: g.handleRenew, Result: g.handleResult})
}

// ReadinessName names the gateway's readiness check.
func (g *Gateway) ReadinessName() string { return "agent-gateway" }

// CheckReadiness reports whether the bus is serving.
func (g *Gateway) CheckReadiness(context.Context) error {
	if !g.bus.Ready() {
		return ErrUnavailable
	}
	return nil
}

// seal signs body for an agent; nil means the message could not be sealed.
func (g *Gateway) seal(kind agentv1.Kind, body agentv1.Body) []byte {
	payload, err := agentv1.SealWith(g.signer(), kind, body)
	if err != nil {
		g.logger.Error("seal agent reply", "kind", kind, "error", err)
		return nil
	}
	return payload
}

func (g *Gateway) newLease(now time.Time) (*agentv1.Lease, error) {
	id, err := randomID("lease-")
	if err != nil {
		return nil, err
	}
	return &agentv1.Lease{LeaseID: id, ExpiresAt: now.Add(g.cfg.LeaseTTL).UTC(), HeartbeatIntervalMillis: g.cfg.Heartbeat.Milliseconds()}, nil
}

func randomID(prefix string) (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw), nil
}

func (g *Gateway) handleRegister(_ context.Context, data []byte) []byte {
	request, err := agentv1.OpenRegisterRequest(data)
	if err != nil {
		g.metrics.registrations.WithLabelValues("invalid").Inc()
		return nil // an unauthenticated message gets no answer
	}
	response := &agentv1.RegisterResponse{ProtocolVersion: agentv1.ProtocolVersion, RequestID: request.RequestID}
	// Per-host tokens are checked and consumed by the store, atomically with
	// the registration; the shared development token is checked here.
	enrollment := Enrollment{Hostname: request.Hostname, TokenHash: HashEnrollmentToken(request.EnrollmentToken)}
	if g.cfg.EnrollmentToken != "" && subtle.ConstantTimeCompare([]byte(request.EnrollmentToken), []byte(g.cfg.EnrollmentToken)) == 1 {
		enrollment = Enrollment{Shared: true, Hostname: request.Hostname}
	}
	now := time.Now().UTC()
	lease, err := g.newLease(now)
	if err == nil {
		var id string
		if id, err = randomID("agent-"); err == nil {
			var agent Agent
			agent, err = g.store.RegisterAgent(context.Background(), Agent{ID: id, Hostname: request.Hostname,
				AgentVersion: request.AgentVersion, OS: request.OS, Architecture: request.Architecture,
				Capabilities: request.Capabilities, PublicKey: ed25519.PublicKey(request.AgentPublicKey),
				LeaseID: lease.LeaseID, LeaseExpiresAt: lease.ExpiresAt, RegisteredAt: now, LastSeenAt: now}, request.RequestID, enrollment)
			if err == nil && agent.ID != id {
				// A known key re-enrolled: record the lease it is about to get.
				err = g.store.RenewAgentLease(context.Background(), agent.ID, lease.LeaseID, lease.ExpiresAt, now)
			}
			if err == nil {
				err = g.provision(agent.ID)
				response.AgentID, response.HostID = agent.ID, agent.ID
			}
			if err == nil {
				response.NATSUserJWT, err = g.natsCredential(ed25519.PublicKey(request.AgentPublicKey), agent.ID, now)
			}
		}
	}
	// Both refusals look the same to the agent, so a client cannot tell a
	// bad token from a revoked identity; the log and metric tell them apart.
	refuse := func(outcome, message string) []byte {
		g.metrics.registrations.WithLabelValues(outcome).Inc()
		g.logger.Warn(message, "hostname", request.Hostname)
		response.AgentID, response.HostID, response.NATSUserJWT = "", "", ""
		response.Error = &agentv1.Error{Code: agentv1.ErrorEnrollmentRejected}
		return g.seal(agentv1.KindRegisterResponse, response)
	}
	if errors.Is(err, ErrEnrollmentRejected) {
		return refuse("rejected", "agent enrollment rejected")
	}
	if errors.Is(err, ErrAgentRevoked) {
		return refuse("revoked", "revoked agent tried to enrol again")
	}
	if err != nil {
		g.metrics.registrations.WithLabelValues("error").Inc()
		g.logger.Error("agent registration failed", "hostname", request.Hostname, "error", err)
		response.AgentID, response.HostID, response.NATSUserJWT = "", "", ""
		response.Error = &agentv1.Error{Code: agentv1.ErrorUnavailable, Retryable: true}
		return g.seal(agentv1.KindRegisterResponse, response)
	}
	response.Lease = lease
	g.metrics.registrations.WithLabelValues("registered").Inc()
	g.logger.Info("agent registered", "agent_id", response.AgentID, "hostname", request.Hostname, "version", request.AgentVersion)
	return g.seal(agentv1.KindRegisterResponse, response)
}

// natsCredential returns the agent's NATS user JWT, or "" when the gateway
// does not issue them.
func (g *Gateway) natsCredential(agentKey ed25519.PublicKey, agentID string, now time.Time) (string, error) {
	if g.cfg.NATSIssuer == nil {
		return "", nil
	}
	return g.cfg.NATSIssuer.mint(agentKey, agentID, now)
}

// provision creates the agent's durable command queue (M-6).
func (g *Gateway) provision(agentID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), g.cfg.ConnectTimeout)
	defer cancel()
	return g.bus.EnsureAgent(ctx, agentID)
}

// keyFor returns a live agent's public key for signature checks.
func (g *Gateway) keyFor(agentID string) (ed25519.PublicKey, bool) {
	agent, err := g.store.GetAgent(context.Background(), agentID)
	if err != nil || agent.RevokedAt != nil {
		return nil, false
	}
	return agent.PublicKey, true
}

func (g *Gateway) handleRenew(_ context.Context, data []byte) []byte {
	var request agentv1.RenewRequest
	var agentKey ed25519.PublicKey
	keyFor := func(agentID string) (ed25519.PublicKey, bool) {
		key, ok := g.keyForRenewal(agentID)
		agentKey = key
		return key, ok
	}
	if err := agentv1.OpenFromAgent(agentv1.KindRenewRequest, data, agentv1.MaxControlMessageBytes, keyFor, &request); err != nil {
		g.metrics.renewals.WithLabelValues("invalid").Inc()
		return nil
	}
	response := &agentv1.RenewResponse{ProtocolVersion: agentv1.ProtocolVersion, RequestID: request.RequestID, AgentID: request.AgentID}
	now := time.Now().UTC()
	lease, err := g.newLease(now)
	if err == nil {
		err = g.store.RenewAgentLease(context.Background(), request.AgentID, lease.LeaseID, lease.ExpiresAt, now)
	}
	if err == nil {
		// A fresh credential on every renewal: revocation stops renewals, so a
		// revoked agent keeps its NATS access only until this JWT expires.
		response.NATSUserJWT, err = g.natsCredential(agentKey, request.AgentID, now)
	}
	switch {
	case err == nil:
		response.Lease = lease
		g.metrics.renewals.WithLabelValues("renewed").Inc()
	case errors.Is(err, ErrAgentRevoked):
		response.Error = &agentv1.Error{Code: agentv1.ErrorLeaseRevoked}
		g.metrics.renewals.WithLabelValues("revoked").Inc()
	case errors.Is(err, ErrAgentNotFound):
		response.Error = &agentv1.Error{Code: agentv1.ErrorUnknownAgent}
		g.metrics.renewals.WithLabelValues("unknown").Inc()
	default:
		g.logger.Error("agent lease renewal failed", "agent_id", request.AgentID, "error", err)
		response.NATSUserJWT = ""
		response.Error = &agentv1.Error{Code: agentv1.ErrorUnavailable, Retryable: true}
		g.metrics.renewals.WithLabelValues("error").Inc()
	}
	return g.seal(agentv1.KindRenewResponse, response)
}

// keyForRenewal also accepts revoked agents, so they learn they are revoked
// from a signed answer rather than a timeout.
func (g *Gateway) keyForRenewal(agentID string) (ed25519.PublicKey, bool) {
	agent, err := g.store.GetAgent(context.Background(), agentID)
	if err != nil {
		return nil, false
	}
	return agent.PublicKey, true
}

func (g *Gateway) handleResult(_ context.Context, subjectAgentID string, data []byte) {
	var result agentv1.Result
	if err := agentv1.OpenFromAgent(agentv1.KindResult, data, agentv1.MaxResultMessageBytes, g.keyFor, &result); err != nil {
		g.metrics.results.WithLabelValues("invalid").Inc()
		return
	}
	// The transport-authenticated agent must match the signer, so one agent cannot report for another.
	if subjectAgentID != result.AgentID {
		g.metrics.results.WithLabelValues("invalid").Inc()
		return
	}
	g.metrics.results.WithLabelValues(string(result.Status)).Inc()
	handler := g.results.Load()
	if handler == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.cfg.ConnectTimeout)
	defer cancel()
	if err := (*handler)(ctx, result); err != nil {
		// Not acknowledged: the agent republishes and the result is retried.
		g.logger.Warn("agent result not stored; it will be redelivered", "agent_id", result.AgentID,
			"operation_id", result.OperationID, "error", err)
		return
	}
	if result.Status.Terminal() {
		if err := g.send(ctx, result.AgentID, agentv1.Command{Type: agentv1.CommandResultAck, OperationID: result.OperationID}); err != nil {
			g.logger.Warn("acknowledge agent result", "agent_id", result.AgentID, "operation_id", result.OperationID, "error", err)
		}
	}
}

func (g *Gateway) send(ctx context.Context, agentID string, cmd agentv1.Command) error {
	if !g.bus.Ready() {
		return ErrUnavailable
	}
	now := time.Now().UTC()
	id, err := randomID("cmd-")
	if err != nil {
		return err
	}
	cmd.CommandID, cmd.AgentID = id, agentID
	cmd.IssuedAt, cmd.ExpiresAt = now, now.Add(g.cfg.CommandLifetime)
	return g.PublishCommand(ctx, cmd)
}

// CommandLifetime is how long an agent accepts a command after issue.
func (g *Gateway) CommandLifetime() time.Duration { return g.cfg.CommandLifetime }

// PublishCommand signs cmd as given (command ID, agent, issue and expiry
// times set by the caller) and publishes it to the agent's subject with
// Nats-Msg-Id = command ID. It returns after the PubAck, so a nil error means
// JetStream stored it; publishing the same command again within the
// duplicate window is ignored by JetStream (ADR-057, M-7).
func (g *Gateway) PublishCommand(ctx context.Context, cmd agentv1.Command) error {
	if !g.bus.Ready() {
		return ErrUnavailable
	}
	cmd.ProtocolVersion = agentv1.ProtocolVersion
	payload, err := agentv1.SealWith(g.signer(), agentv1.KindCommand, &cmd)
	if err != nil {
		return err
	}
	err = g.bus.Publish(ctx, cmd.AgentID, cmd.CommandID, payload)
	outcome := "published"
	if err != nil {
		outcome = "error"
	}
	g.metrics.commands.WithLabelValues(string(cmd.Type), outcome).Inc()
	return err
}

// Agents lists enrolled agents.
func (g *Gateway) Agents(ctx context.Context) ([]Agent, error) { return g.store.ListAgents(ctx) }

// Revoke revokes an agent; its next renewal is refused.
func (g *Gateway) Revoke(ctx context.Context, agentID string) error {
	return g.store.RevokeAgent(ctx, agentID, time.Now().UTC())
}
