package server_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/agentgateway/bus/memory"
	"github.com/machinarchy/machinarchy/internal/agentgateway/server"
)

const sharedToken = "shared-enrollment-token-0123456789abcdef"

func seedKey(seed byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32))
}

// store is an in-memory agentgateway.Store with the SQL store's token rules.
type store struct {
	mu     sync.Mutex
	agents map[string]*agentgateway.Agent
	tokens map[string]*agentgateway.EnrollmentToken
}

func newStore() *store {
	return &store{agents: map[string]*agentgateway.Agent{}, tokens: map[string]*agentgateway.EnrollmentToken{}}
}

func (s *store) addToken(secret, hostname string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash := agentgateway.HashEnrollmentToken(secret)
	s.tokens[hash] = &agentgateway.EnrollmentToken{ID: "enr-" + hash[:8], Hostname: hostname, Hash: hash,
		ExpiresAt: time.Now().Add(ttl)}
}

func (s *store) revokeToken(secret string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.tokens[agentgateway.HashEnrollmentToken(secret)].RevokedAt = &now
}

func (s *store) RegisterAgent(_ context.Context, agent agentgateway.Agent, _ string, e agentgateway.Enrollment) (agentgateway.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.agents {
		if existing.PublicKey.Equal(agent.PublicKey) {
			if existing.RevokedAt != nil {
				return agentgateway.Agent{}, agentgateway.ErrAgentRevoked
			}
			return *existing, nil
		}
	}
	if !e.Shared {
		token, ok := s.tokens[e.TokenHash]
		now := time.Now()
		if !ok || !token.Usable(now) || !token.Matches(e.Hostname) {
			return agentgateway.Agent{}, agentgateway.ErrEnrollmentRejected
		}
		token.UsedAt, token.UsedByAgent = &now, agent.ID
	}
	copied := agent
	s.agents[agent.ID] = &copied
	return agent, nil
}

func (s *store) GetAgent(_ context.Context, id string) (*agentgateway.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[id]
	if !ok {
		return nil, agentgateway.ErrAgentNotFound
	}
	copied := *agent
	return &copied, nil
}

func (s *store) RenewAgentLease(_ context.Context, id, leaseID string, expiresAt, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[id]
	switch {
	case !ok:
		return agentgateway.ErrAgentNotFound
	case agent.RevokedAt != nil:
		return agentgateway.ErrAgentRevoked
	}
	agent.LeaseID, agent.LeaseExpiresAt, agent.LastSeenAt = leaseID, expiresAt, now
	return nil
}

func (s *store) ListAgents(context.Context) ([]agentgateway.Agent, error) { return nil, nil }

func (s *store) RevokeAgent(_ context.Context, id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agents[id].RevokedAt = &now
	return nil
}

func (s *store) HostAgent(context.Context, int, string) (*agentgateway.Agent, error) {
	return nil, agentgateway.ErrHostNotBound
}

func (s *store) expireLease(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agents[id].LeaseExpiresAt = time.Now().Add(-time.Second)
}

type env struct {
	t       *testing.T
	store   *store
	gateway *agentgateway.Gateway
	server  *server.Server
	conn    *grpc.ClientConn
	logs    *syncBuffer
	signer  ed25519.PrivateKey
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newEnv(t *testing.T, mutate func(*server.Config)) *env {
	t.Helper()
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st := newStore()
	signer := seedKey(1)
	gw, err := agentgateway.New(st, agentgateway.Config{Bus: memory.New(), Signer: signer, EnrollmentToken: sharedToken,
		LeaseTTL: 20 * time.Second, Heartbeat: 5 * time.Second, Logger: logger, Registerer: prometheus.NewRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- gw.Start(ctx) }()
	for deadline := time.Now().Add(5 * time.Second); gw.CheckReadiness(ctx) != nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("gateway not ready")
		}
	}
	cfg := server.Config{Gateway: gw, Logger: logger, PollInterval: 50 * time.Millisecond, AckTimeout: 400 * time.Millisecond}
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := server.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(l) }()
	conn, err := grpc.NewClient(l.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(server.Codec()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop()
		cancel()
		<-done
	})
	return &env{t: t, store: st, gateway: gw, server: srv, conn: conn, logs: logs, signer: signer}
}

func (e *env) unary(method string, payload []byte) ([]byte, error) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := server.NewFrame(nil)
	err := e.conn.Invoke(ctx, "/"+server.ServiceName+"/"+method, server.NewFrame(payload), out)
	return out.Bytes(), err
}

type agent struct {
	key      ed25519.PrivateKey
	id       string
	leaseID  string
	hostname string
}

func (e *env) register(key ed25519.PrivateKey, hostname, token string) (*agent, *agentv1.RegisterResponse) {
	e.t.Helper()
	request := &agentv1.RegisterRequest{ProtocolVersion: agentv1.ProtocolVersion, RequestID: "req-" + hostname, EnrollmentToken: token,
		AgentPublicKey: key.Public().(ed25519.PublicKey), Hostname: hostname, AgentVersion: "1.0.0", OS: "linux",
		Architecture: "amd64", Capabilities: []string{agentv1.CapabilityRuntimeInfo}, SentAt: time.Now().UTC()}
	raw, err := agentv1.Seal(key, agentv1.KindRegisterRequest, request)
	if err != nil {
		e.t.Fatal(err)
	}
	reply, err := e.unary("Register", raw)
	if err != nil {
		e.t.Fatalf("register: %v", err)
	}
	var response agentv1.RegisterResponse
	if err := agentv1.Open(e.signer.Public().(ed25519.PublicKey), agentv1.KindRegisterResponse, reply, agentv1.MaxControlMessageBytes, &response); err != nil {
		e.t.Fatalf("register reply does not verify: %v", err)
	}
	if response.Error != nil {
		return nil, &response
	}
	return &agent{key: key, id: response.AgentID, leaseID: response.Lease.LeaseID, hostname: hostname}, &response
}

func (e *env) mustRegister(seed byte, hostname string) *agent {
	e.t.Helper()
	a, response := e.register(seedKey(seed), hostname, sharedToken)
	if a == nil {
		e.t.Fatalf("registration refused: %+v", response.Error)
	}
	return a
}

func (a *agent) openRequest(leaseID string, sentAt time.Time) []byte {
	raw, err := agentv1.Seal(a.key, agentv1.KindRenewRequest, &agentv1.RenewRequest{ProtocolVersion: agentv1.ProtocolVersion,
		RequestID: "open-1", AgentID: a.id, LeaseID: leaseID, AgentVersion: "1.0.0", SentAt: sentAt})
	if err != nil {
		panic(err)
	}
	return raw
}

func (a *agent) result(agentID, operation string, signer ed25519.PrivateKey) []byte {
	now := time.Now().UTC()
	raw, err := agentv1.Seal(signer, agentv1.KindResult, &agentv1.Result{ProtocolVersion: agentv1.ProtocolVersion, AgentID: agentID,
		OperationID: operation, Capability: agentv1.CapabilityRuntimeInfo, Status: agentv1.StatusSucceeded,
		Output: json.RawMessage(`{"ok":true}`), AcceptedAt: now, StartedAt: &now, CompletedAt: &now})
	if err != nil {
		panic(err)
	}
	return raw
}

// open starts the agent's command stream.
func (e *env) open(a *agent, leaseID string, sentAt time.Time) (grpc.ClientStream, context.CancelFunc, error) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := e.conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, "/"+server.ServiceName+"/Commands")
	if err != nil {
		cancel()
		return nil, nil, err
	}
	if err := stream.SendMsg(server.NewFrame(a.openRequest(leaseID, sentAt))); err != nil {
		cancel()
		return nil, nil, err
	}
	return stream, cancel, nil
}

func (e *env) mustOpen(a *agent) (grpc.ClientStream, context.CancelFunc) {
	e.t.Helper()
	stream, cancel, err := e.open(a, a.leaseID, time.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(cancel)
	return stream, cancel
}

// recv waits for one command on the stream.
func recv(t *testing.T, stream grpc.ClientStream, within time.Duration) (agentv1.Command, error) {
	t.Helper()
	type outcome struct {
		cmd agentv1.Command
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		f := server.NewFrame(nil)
		if err := stream.RecvMsg(f); err != nil {
			ch <- outcome{err: err}
			return
		}
		var cmd agentv1.Command
		key := seedKey(1).Public().(ed25519.PublicKey)
		err := agentv1.Open(key, agentv1.KindCommand, f.Bytes(), agentv1.MaxControlMessageBytes, &cmd)
		ch <- outcome{cmd: cmd, err: err}
	}()
	select {
	case o := <-ch:
		return o.cmd, o.err
	case <-time.After(within):
		return agentv1.Command{}, errors.New("timeout")
	}
}

func ack(t *testing.T, stream grpc.ClientStream, id string) {
	t.Helper()
	if err := stream.SendMsg(server.NewFrame([]byte(id))); err != nil {
		t.Fatal(err)
	}
}

func (e *env) publish(agentID, operation string) string {
	e.t.Helper()
	now := time.Now().UTC()
	id := "cmd-" + operation
	err := e.gateway.PublishCommand(context.Background(), agentv1.Command{CommandID: id, AgentID: agentID, Type: agentv1.CommandOperationRequest,
		IssuedAt: now, ExpiresAt: now.Add(time.Minute), OperationID: operation, Capability: agentv1.CapabilityRuntimeInfo,
		Input: json.RawMessage(`{}`), TimeoutMillis: 1000})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func code(err error) codes.Code { return status.Code(err) }

func TestRegisterRenewStreamAndResult(t *testing.T) {
	e := newEnv(t, nil)
	var mu sync.Mutex
	var stored []agentv1.Result
	e.gateway.SetResultHandler(func(_ context.Context, r agentv1.Result) error {
		mu.Lock()
		defer mu.Unlock()
		stored = append(stored, r)
		return nil
	})
	a := e.mustRegister(2, "web-01")

	// Renew over gRPC, then use the renewed lease to open the stream.
	raw, _ := agentv1.Seal(a.key, agentv1.KindRenewRequest, &agentv1.RenewRequest{ProtocolVersion: agentv1.ProtocolVersion,
		RequestID: "renew-1", AgentID: a.id, LeaseID: a.leaseID, AgentVersion: "1.0.0", SentAt: time.Now().UTC()})
	reply, err := e.unary("Renew", raw)
	if err != nil {
		t.Fatal(err)
	}
	var renewed agentv1.RenewResponse
	if err := agentv1.Open(e.signer.Public().(ed25519.PublicKey), agentv1.KindRenewResponse, reply, agentv1.MaxControlMessageBytes, &renewed); err != nil || renewed.Error != nil {
		t.Fatalf("renew: %v %+v", err, renewed.Error)
	}
	if renewed.NATSUserJWT != "" {
		t.Fatal("a broker credential must never reach an agent")
	}
	a.leaseID = renewed.Lease.LeaseID

	stream, _ := e.mustOpen(a)
	id := e.publish(a.id, "op-0000000001")
	cmd, err := recv(t, stream, 3*time.Second)
	if err != nil || cmd.CommandID != id || cmd.AgentID != a.id {
		t.Fatalf("command = %+v, %v", cmd, err)
	}
	ack(t, stream, cmd.CommandID)

	if _, err := e.unary("SendResult", a.result(a.id, "op-0000000001", a.key)); err != nil {
		t.Fatalf("send result: %v", err)
	}
	// A terminal result is acknowledged with result.ack on the same stream.
	cmd, err = recv(t, stream, 3*time.Second)
	if err != nil || cmd.Type != agentv1.CommandResultAck || cmd.OperationID != "op-0000000001" {
		t.Fatalf("result ack = %+v, %v", cmd, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(stored) != 1 || stored[0].AgentID != a.id {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestOfflineQueueIsDeliveredOnReconnectAndUnackedIsRedelivered(t *testing.T) {
	e := newEnv(t, nil)
	a := e.mustRegister(2, "web-01")
	e.publish(a.id, "op-0000000001")
	e.publish(a.id, "op-0000000002")

	stream, cancel := e.mustOpen(a)
	first, err := recv(t, stream, 3*time.Second)
	if err != nil || first.OperationID != "op-0000000001" {
		t.Fatalf("first = %+v, %v", first, err)
	}
	cancel() // the connection drops before the agent acknowledged

	stream, _ = e.mustOpen(a)
	again, err := recv(t, stream, 3*time.Second)
	if err != nil || again.CommandID != first.CommandID {
		t.Fatalf("unacknowledged command was not redelivered first: %+v, %v", again, err)
	}
	ack(t, stream, again.CommandID)
	second, err := recv(t, stream, 3*time.Second)
	if err != nil || second.OperationID != "op-0000000002" {
		t.Fatalf("second = %+v, %v (per-agent order)", second, err)
	}
}

func TestRegistrationSecurity(t *testing.T) {
	e := newEnv(t, nil)
	e.store.addToken("good-token-0123456789abcdef0123456789", "web-01", time.Hour)
	e.store.addToken("other-host-0123456789abcdef0123456789", "db-01", time.Hour)
	e.store.addToken("expired-0123456789abcdef0123456789ab", "web-02", -time.Minute)
	e.store.addToken("revoked-0123456789abcdef0123456789ab", "web-03", time.Hour)
	e.store.revokeToken("revoked-0123456789abcdef0123456789ab")

	refused := func(name string, key ed25519.PrivateKey, host, token string) {
		t.Helper()
		a, response := e.register(key, host, token)
		if a != nil || response.Error == nil || response.Error.Code != agentv1.ErrorEnrollmentRejected {
			t.Errorf("%s: want enrollment_rejected, got agent=%v error=%+v", name, a, response.Error)
		}
		if response.AgentID != "" || response.Lease != nil {
			t.Errorf("%s: refusal must not carry an identity or lease", name)
		}
	}
	refused("unknown token", seedKey(10), "web-01", "unknown-token-0123456789abcdef0123456789")
	refused("wrong hostname", seedKey(11), "web-01", "other-host-0123456789abcdef0123456789")
	refused("expired", seedKey(12), "web-02", "expired-0123456789abcdef0123456789ab")
	refused("revoked", seedKey(13), "web-03", "revoked-0123456789abcdef0123456789ab")
	if a, _ := e.register(seedKey(14), "web-01", "good-token-0123456789abcdef0123456789"); a == nil {
		t.Fatal("a valid token must enrol")
	}
	refused("reused", seedKey(15), "web-01", "good-token-0123456789abcdef0123456789")

	// An unauthenticated or malformed request gets a generic error, no detail.
	if _, err := e.unary("Register", []byte("not an envelope")); code(err) != codes.InvalidArgument {
		t.Fatalf("garbage register = %v", err)
	}
}

func TestStreamRequiresALiveLeaseBoundToTheAgentKey(t *testing.T) {
	e := newEnv(t, nil)
	a := e.mustRegister(2, "web-01")
	intruder := e.mustRegister(3, "web-02")

	expectDenied := func(name string, stream grpc.ClientStream, cancel context.CancelFunc, err error) {
		t.Helper()
		if err == nil {
			defer cancel()
			_, err = recv(t, stream, 3*time.Second)
		}
		if code(err) != codes.Unauthenticated {
			t.Errorf("%s: got %v, want Unauthenticated", name, err)
		}
	}
	s, c, err := e.open(a, "lease-forged0123456789", time.Now())
	expectDenied("wrong lease ID", s, c, err)
	s, c, err = e.open(a, a.leaseID, time.Now().Add(-10*time.Minute))
	expectDenied("stale request", s, c, err)

	// A copied lease ID alone does nothing: the opening request must be signed
	// by the agent's own key.
	forged := &agent{key: intruder.key, id: a.id, leaseID: a.leaseID}
	s, c, err = e.open(forged, a.leaseID, time.Now())
	expectDenied("lease copied by another key", s, c, err)

	e.store.expireLease(a.id)
	s, c, err = e.open(a, a.leaseID, time.Now())
	expectDenied("expired lease", s, c, err)
	if _, err := e.unary("SendResult", a.result(a.id, "op-0000000001", a.key)); code(err) != codes.Unauthenticated {
		t.Errorf("result with an expired lease = %v", err)
	}
}

func TestRevokedAgentStreamIsClosed(t *testing.T) {
	e := newEnv(t, nil)
	a := e.mustRegister(2, "web-01")
	stream, _ := e.mustOpen(a)
	e.publish(a.id, "op-0000000001")
	if _, err := recv(t, stream, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := e.gateway.Revoke(context.Background(), a.id); err != nil {
		t.Fatal(err)
	}
	_, err := recv(t, stream, 3*time.Second)
	if code(err) != codes.PermissionDenied {
		t.Fatalf("revoked stream = %v, want PermissionDenied", err)
	}
	e.publish(a.id, "op-0000000002") // nothing more is delivered to a revoked agent
	s, c, err := e.open(a, a.leaseID, time.Now())
	if err == nil {
		defer c()
		_, err = recv(t, s, 2*time.Second)
	}
	if code(err) != codes.Unauthenticated {
		t.Fatalf("re-opening a revoked agent's stream = %v", err)
	}
}

func TestAgentsAreIsolated(t *testing.T) {
	e := newEnv(t, nil)
	var mu sync.Mutex
	var stored []agentv1.Result
	e.gateway.SetResultHandler(func(_ context.Context, r agentv1.Result) error {
		mu.Lock()
		defer mu.Unlock()
		stored = append(stored, r)
		return nil
	})
	a, b := e.mustRegister(2, "web-01"), e.mustRegister(3, "web-02")
	sa, _ := e.mustOpen(a)
	sb, _ := e.mustOpen(b)
	e.publish(b.id, "op-for-b-000001")
	if cmd, err := recv(t, sb, 3*time.Second); err != nil || cmd.AgentID != b.id {
		t.Fatalf("b got %+v, %v", cmd, err)
	}
	if cmd, err := recv(t, sa, 400*time.Millisecond); err == nil {
		t.Fatalf("agent A received %+v addressed to B", cmd)
	}
	// A cannot write results for B: signed by A's key but naming B.
	if _, err := e.unary("SendResult", a.result(b.id, "op-0000000009", a.key)); code(err) != codes.Unauthenticated {
		t.Fatalf("A reporting for B = %v, want Unauthenticated", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(stored) != 0 {
		t.Fatalf("a forged result was stored: %+v", stored)
	}
}

func TestLimits(t *testing.T) {
	t.Run("oversize message", func(t *testing.T) {
		e := newEnv(t, nil)
		_, err := e.unary("SendResult", make([]byte, agentv1.MaxResultMessageBytes+1))
		if code(err) != codes.ResourceExhausted {
			t.Fatalf("oversize = %v, want ResourceExhausted", err)
		}
	})
	t.Run("flood of unauthenticated calls", func(t *testing.T) {
		e := newEnv(t, func(c *server.Config) { c.CallsPerSecond, c.CallBurst = 1, 3 })
		limited := false
		for i := 0; i < 20; i++ {
			if _, err := e.unary("Register", []byte("x")); code(err) == codes.ResourceExhausted {
				limited = true
			}
		}
		if !limited {
			t.Fatal("no rate limit applied to a flood")
		}
	})
	t.Run("results are rate limited", func(t *testing.T) {
		e := newEnv(t, func(c *server.Config) { c.CallsPerSecond, c.CallBurst = 0.001, 2 })
		a := e.mustRegister(2, "web-01")
		limited := false
		for i := 0; i < 6; i++ {
			if _, err := e.unary("SendResult", a.result(a.id, "op-0000000001", a.key)); code(err) == codes.ResourceExhausted {
				limited = true
			}
		}
		if !limited {
			t.Fatal("results were not rate limited")
		}
	})
	t.Run("concurrent streams are bounded and a reconnect replaces the old stream", func(t *testing.T) {
		e := newEnv(t, func(c *server.Config) { c.MaxStreams = 1 })
		a, b := e.mustRegister(2, "web-01"), e.mustRegister(3, "web-02")
		first, _ := e.mustOpen(a)
		sb, _, err := e.open(b, b.leaseID, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := recv(t, sb, 3*time.Second); code(err) != codes.ResourceExhausted {
			t.Fatalf("second agent's stream = %v, want ResourceExhausted", err)
		}
		replacement, _ := e.mustOpen(a) // same agent reconnects
		if _, err := recv(t, first, 3*time.Second); err == nil {
			t.Fatal("the replaced stream stayed open")
		}
		e.publish(a.id, "op-0000000001")
		if cmd, err := recv(t, replacement, 3*time.Second); err != nil || cmd.AgentID != a.id {
			t.Fatalf("replacement stream got %+v, %v", cmd, err)
		}
	})
}

func TestNoCredentialsInLogs(t *testing.T) {
	e := newEnv(t, nil)
	const secret = "log-secret-token-0123456789abcdef0123456"
	e.store.addToken(secret, "web-01", time.Hour)
	a, _ := e.register(seedKey(2), "web-01", secret)
	e.register(seedKey(3), "web-01", secret) // reuse: logged as rejected
	e.register(seedKey(4), "web-01", sharedToken)
	stream, _ := e.mustOpen(a)
	e.publish(a.id, "op-0000000001")
	_, _ = recv(t, stream, 3*time.Second)
	logs := e.logs.String()
	for _, forbidden := range []string{secret, sharedToken, a.leaseID, string(a.key), agentv1.EncodePublicKey(a.key.Public().(ed25519.PublicKey))} {
		if forbidden != "" && strings.Contains(logs, forbidden) {
			t.Fatalf("logs contain a credential: %q", forbidden)
		}
	}
}
