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

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// Config configures the gateway. NATSOptions carries authentication and TLS.
type Config struct {
	NATSURL     string
	NATSOptions []nats.Option
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
	streamDuplicateWindow  = 15 * time.Minute
)

// ErrUnavailable reports that the gateway is not connected or not running.
var ErrUnavailable = errors.New("agent gateway is not available")

// Gateway serves registration, lease renewal and results, and sends commands.
type Gateway struct {
	cfg     Config
	store   Store
	logger  *slog.Logger
	metrics *gatewayMetrics

	js        jetstream.JetStream
	connected atomic.Bool

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
	if store == nil || cfg.NATSURL == "" || cfg.Signer == nil {
		return nil, errors.New("agent gateway requires a store, a NATS URL and an Ed25519 signer")
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
	g := &Gateway{cfg: cfg, store: store, logger: cfg.Logger.With("component", "agent-gateway"),
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

// Start connects to NATS, provisions the command stream and serves until ctx
// ends; it then drains the connection.
func (g *Gateway) Start(ctx context.Context) error {
	options := append([]nats.Option{nats.Name("machinarchy-agent-gateway"), nats.Timeout(g.cfg.ConnectTimeout),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			g.connected.Store(false)
			g.logger.Warn("agent gateway disconnected from NATS", "error", err)
		}),
		nats.ReconnectHandler(func(*nats.Conn) {
			g.connected.Store(true)
			g.logger.Info("agent gateway reconnected to NATS")
		}),
		// Keep replies to the controller (JetStream API answers included) out
		// of the _INBOX namespace that enrolling agents may read.
		nats.CustomInboxPrefix(agentv1.ControllerInboxPrefix)}, g.cfg.NATSOptions...)
	nc, err := nats.Connect(g.cfg.NATSURL, options...)
	if err != nil {
		return fmt.Errorf("connect agent gateway to NATS: %w", err)
	}
	if g.js, err = jetstream.New(nc); err != nil {
		nc.Close()
		return fmt.Errorf("open JetStream: %w", err)
	}
	setupCtx, cancel := context.WithTimeout(ctx, g.cfg.ConnectTimeout)
	_, err = g.js.CreateOrUpdateStream(setupCtx, jetstream.StreamConfig{
		Name: agentv1.CommandStream, Subjects: []string{agentv1.SubjectAllCommands}, Storage: jetstream.FileStorage,
		MaxAge: 24 * time.Hour, Duplicates: streamDuplicateWindow,
	})
	cancel()
	if err != nil {
		nc.Close()
		return fmt.Errorf("provision agent command stream: %w", err)
	}
	var subs []*nats.Subscription
	for subject, handler := range map[string]nats.MsgHandler{
		agentv1.SubjectRegister:   g.onRegister,
		agentv1.SubjectLeaseRenew: g.onRenew,
		agentv1.SubjectAllResults: g.onResult,
	} {
		sub, err := nc.QueueSubscribe(subject, "machinarchy-agent-gateway", handler)
		if err != nil {
			nc.Close()
			return fmt.Errorf("subscribe %s: %w", subject, err)
		}
		subs = append(subs, sub)
	}
	if err := nc.Flush(); err != nil {
		nc.Close()
		return fmt.Errorf("flush agent gateway subscriptions: %w", err)
	}
	g.connected.Store(true)
	g.logger.Info("agent gateway serving", "nats_url", nc.ConnectedUrlRedacted())

	<-ctx.Done()
	g.connected.Store(false)
	for _, sub := range subs {
		if err := sub.Unsubscribe(); err != nil {
			g.logger.Debug("unsubscribe agent gateway subject", "subject", sub.Subject, "error", err)
		}
	}
	if err := nc.Drain(); err != nil {
		g.logger.Warn("drain agent gateway connection", "error", err)
		nc.Close()
	}
	return nil
}

// ReadinessName names the gateway's readiness check.
func (g *Gateway) ReadinessName() string { return "agent-gateway" }

// CheckReadiness reports whether the gateway is connected to NATS.
func (g *Gateway) CheckReadiness(context.Context) error {
	if !g.connected.Load() {
		return ErrUnavailable
	}
	return nil
}

func (g *Gateway) reply(msg *nats.Msg, kind agentv1.Kind, body agentv1.Body) {
	payload, err := agentv1.SealWith(g.signer(), kind, body)
	if err != nil {
		g.logger.Error("seal agent reply", "kind", kind, "error", err)
		return
	}
	if err := msg.Respond(payload); err != nil {
		g.logger.Warn("send agent reply", "kind", kind, "error", err)
	}
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

func (g *Gateway) onRegister(msg *nats.Msg) {
	request, err := agentv1.OpenRegisterRequest(msg.Data)
	if err != nil {
		g.metrics.registrations.WithLabelValues("invalid").Inc()
		return // an unauthenticated message gets no answer
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
	if errors.Is(err, ErrEnrollmentRejected) {
		g.metrics.registrations.WithLabelValues("rejected").Inc()
		g.logger.Warn("agent enrollment rejected", "hostname", request.Hostname)
		response.AgentID, response.HostID, response.NATSUserJWT = "", "", ""
		response.Error = &agentv1.Error{Code: agentv1.ErrorEnrollmentRejected}
		g.reply(msg, agentv1.KindRegisterResponse, response)
		return
	}
	if errors.Is(err, ErrAgentRevoked) {
		g.metrics.registrations.WithLabelValues("revoked").Inc()
		g.logger.Warn("revoked agent tried to enrol again", "hostname", request.Hostname)
		response.AgentID, response.HostID, response.NATSUserJWT = "", "", ""
		response.Error = &agentv1.Error{Code: agentv1.ErrorEnrollmentRejected}
		g.reply(msg, agentv1.KindRegisterResponse, response)
		return
	}
	if err != nil {
		g.metrics.registrations.WithLabelValues("error").Inc()
		g.logger.Error("agent registration failed", "hostname", request.Hostname, "error", err)
		response.AgentID, response.HostID, response.NATSUserJWT = "", "", ""
		response.Error = &agentv1.Error{Code: agentv1.ErrorUnavailable, Retryable: true}
		g.reply(msg, agentv1.KindRegisterResponse, response)
		return
	}
	response.Lease = lease
	g.metrics.registrations.WithLabelValues("registered").Inc()
	g.logger.Info("agent registered", "agent_id", response.AgentID, "hostname", request.Hostname, "version", request.AgentVersion)
	g.reply(msg, agentv1.KindRegisterResponse, response)
}

// natsCredential returns the agent's NATS user JWT, or "" when the gateway
// does not issue them.
func (g *Gateway) natsCredential(agentKey ed25519.PublicKey, agentID string, now time.Time) (string, error) {
	if g.cfg.NATSIssuer == nil {
		return "", nil
	}
	return g.cfg.NATSIssuer.mint(agentKey, agentID, now)
}

// provision creates the agent's durable pull consumer (M-6).
func (g *Gateway) provision(agentID string) error {
	subject, err := agentv1.CommandSubject(agentID)
	if err != nil {
		return err
	}
	name, err := agentv1.CommandConsumer(agentID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.cfg.ConnectTimeout)
	defer cancel()
	_, err = g.js.CreateOrUpdateConsumer(ctx, agentv1.CommandStream, jetstream.ConsumerConfig{
		Durable: name, FilterSubject: subject, AckPolicy: jetstream.AckExplicitPolicy,
		AckWait: 30 * time.Second, MaxDeliver: 20, DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	return err
}

// keyFor returns a live agent's public key for signature checks.
func (g *Gateway) keyFor(agentID string) (ed25519.PublicKey, bool) {
	agent, err := g.store.GetAgent(context.Background(), agentID)
	if err != nil || agent.RevokedAt != nil {
		return nil, false
	}
	return agent.PublicKey, true
}

func (g *Gateway) onRenew(msg *nats.Msg) {
	var request agentv1.RenewRequest
	var agentKey ed25519.PublicKey
	keyFor := func(agentID string) (ed25519.PublicKey, bool) {
		key, ok := g.keyForRenewal(agentID)
		agentKey = key
		return key, ok
	}
	if err := agentv1.OpenFromAgent(agentv1.KindRenewRequest, msg.Data, agentv1.MaxControlMessageBytes, keyFor, &request); err != nil {
		g.metrics.renewals.WithLabelValues("invalid").Inc()
		return
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
	g.reply(msg, agentv1.KindRenewResponse, response)
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

func (g *Gateway) onResult(msg *nats.Msg) {
	var result agentv1.Result
	if err := agentv1.OpenFromAgent(agentv1.KindResult, msg.Data, agentv1.MaxResultMessageBytes, g.keyFor, &result); err != nil {
		g.metrics.results.WithLabelValues("invalid").Inc()
		return
	}
	// The subject must match the signer, so one agent cannot report for another.
	if expected, err := agentv1.ResultSubject(result.AgentID); err != nil || msg.Subject != expected {
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
	if !g.connected.Load() || g.js == nil {
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
	if !g.connected.Load() || g.js == nil {
		return ErrUnavailable
	}
	cmd.ProtocolVersion = agentv1.ProtocolVersion
	payload, err := agentv1.SealWith(g.signer(), agentv1.KindCommand, &cmd)
	if err != nil {
		return err
	}
	subject, err := agentv1.CommandSubject(cmd.AgentID)
	if err != nil {
		return err
	}
	_, err = g.js.Publish(ctx, subject, payload, jetstream.WithMsgID(cmd.CommandID))
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
