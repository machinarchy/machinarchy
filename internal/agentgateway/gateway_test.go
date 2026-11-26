package agentgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
	"github.com/machinarchy/machinarchy/internal/agentgateway/bus/memory"
	natsbus "github.com/machinarchy/machinarchy/internal/agentgateway/bus/nats"
)

// memStore is an in-memory Store and TokenStore with the same rules as the
// SQL store (internal/store/agents).
type memStore struct {
	mu       sync.Mutex
	agents   map[string]*Agent
	requests map[string]string // agent ID -> registration request ID
	tokens   map[string]*EnrollmentToken
}

func newMemStore() *memStore {
	return &memStore{agents: map[string]*Agent{}, requests: map[string]string{}, tokens: map[string]*EnrollmentToken{}}
}

func (s *memStore) RegisterAgent(_ context.Context, agent Agent, requestID string, enrollment Enrollment) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.agents {
		if !existing.PublicKey.Equal(agent.PublicKey) {
			continue
		}
		switch {
		case existing.RevokedAt != nil:
			return Agent{}, ErrAgentRevoked
		case s.requests[existing.ID] == requestID:
			return *existing, nil
		}
		if err := s.consume(enrollment, existing.ID, agent.RegisteredAt); err != nil {
			return Agent{}, err
		}
		return *existing, nil
	}
	if err := s.consume(enrollment, agent.ID, agent.RegisteredAt); err != nil {
		return Agent{}, err
	}
	s.requests[agent.ID] = requestID
	copied := agent
	s.agents[agent.ID] = &copied
	return agent, nil
}

func (s *memStore) consume(enrollment Enrollment, agentID string, now time.Time) error {
	if enrollment.Shared {
		return nil
	}
	for _, token := range s.tokens {
		if token.Hash == enrollment.TokenHash && token.Usable(now) && token.Matches(enrollment.Hostname) {
			used := now
			token.UsedAt, token.UsedByAgent = &used, agentID
			return nil
		}
	}
	return ErrEnrollmentRejected
}

func (s *memStore) CreateEnrollmentToken(_ context.Context, token EnrollmentToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := token
	s.tokens[token.ID] = &copied
	return nil
}

func (s *memStore) ListEnrollmentTokens(context.Context) ([]EnrollmentToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []EnrollmentToken
	for _, token := range s.tokens {
		list = append(list, *token)
	}
	return list, nil
}

func (s *memStore) RevokeEnrollmentToken(_ context.Context, id string, _ int, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	token, ok := s.tokens[id]
	if !ok || token.UsedAt != nil || token.RevokedAt != nil {
		return ErrEnrollmentTokenNotFound
	}
	token.RevokedAt = &now
	return nil
}

func (s *memStore) GetAgent(_ context.Context, id string) (*Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[id]
	if !ok {
		return nil, ErrAgentNotFound
	}
	copied := *agent
	return &copied, nil
}

func (s *memStore) RenewAgentLease(_ context.Context, id, leaseID string, expiresAt, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[id]
	switch {
	case !ok:
		return ErrAgentNotFound
	case agent.RevokedAt != nil:
		return ErrAgentRevoked
	}
	agent.LeaseID, agent.LeaseExpiresAt, agent.LastSeenAt = leaseID, expiresAt, now
	return nil
}

func (s *memStore) ListAgents(context.Context) ([]Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []Agent
	for _, agent := range s.agents {
		list = append(list, *agent)
	}
	return list, nil
}

func (s *memStore) HostAgent(context.Context, int, string) (*Agent, error) {
	return nil, ErrHostNotBound
}

func (s *memStore) RevokeAgent(_ context.Context, id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[id]
	if !ok {
		return ErrAgentNotFound
	}
	agent.RevokedAt = &now
	return nil
}

const enrollmentToken = "enrollment-token-0123456789abcdef0123"

func key(seed byte) ed25519.PrivateKey { return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32)) }

type fixture struct {
	t       *testing.T
	gateway *Gateway
	store   *memStore
	nc      *nats.Conn
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	server, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go server.Start()
	if !server.ReadyForConnections(10 * time.Second) {
		t.Fatal("NATS did not start")
	}
	t.Cleanup(server.Shutdown)
	return startFixture(t, server.ClientURL(), nil, nil, nil)
}

// startFixture runs a gateway against url. controller carries the gateway's
// NATS credentials, client those of the test's own connection.
func startFixture(t *testing.T, url string, controller []nats.Option, issuer *NATSIssuer, client []nats.Option) *fixture {
	t.Helper()
	store := newMemStore()
	natsBus, err := natsbus.New(natsbus.Config{URL: url, Options: controller})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := New(store, Config{Bus: natsBus, Signer: key(1), EnrollmentToken: enrollmentToken,
		LeaseTTL: 20 * time.Second, Heartbeat: 5 * time.Second, Registerer: prometheus.NewRegistry(), NATSIssuer: issuer})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- gateway.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("gateway stopped with %v", err)
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for gateway.CheckReadiness(ctx) != nil {
		if time.Now().After(deadline) {
			t.Fatal("gateway not ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	nc, err := nats.Connect(url, client...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return &fixture{t: t, gateway: gateway, store: store, nc: nc}
}

func (f *fixture) register(agent ed25519.PrivateKey, token, requestID string) *agentv1.RegisterResponse {
	f.t.Helper()
	request := &agentv1.RegisterRequest{ProtocolVersion: agentv1.ProtocolVersion, RequestID: requestID, EnrollmentToken: token,
		AgentPublicKey: agent.Public().(ed25519.PublicKey), Hostname: "web-01", AgentVersion: "1.0.0", OS: "linux",
		Architecture: "amd64", Capabilities: []string{agentv1.CapabilityRuntimeInfo}, SentAt: time.Now().UTC()}
	raw, err := agentv1.Seal(agent, agentv1.KindRegisterRequest, request)
	if err != nil {
		f.t.Fatal(err)
	}
	reply, err := f.nc.Request(agentv1.SubjectRegister, raw, 5*time.Second)
	if err != nil {
		f.t.Fatal(err)
	}
	var response agentv1.RegisterResponse
	if err := agentv1.Open(key(1).Public().(ed25519.PublicKey), agentv1.KindRegisterResponse, reply.Data, agentv1.MaxControlMessageBytes, &response); err != nil {
		f.t.Fatalf("controller reply does not verify: %v", err)
	}
	return &response
}

func (f *fixture) renew(agent ed25519.PrivateKey, agentID string) *agentv1.RenewResponse {
	f.t.Helper()
	return f.renewOn(f.nc, agent, agentID)
}

func (f *fixture) renewOn(nc *nats.Conn, agent ed25519.PrivateKey, agentID string) *agentv1.RenewResponse {
	f.t.Helper()
	raw, err := agentv1.Seal(agent, agentv1.KindRenewRequest, &agentv1.RenewRequest{ProtocolVersion: agentv1.ProtocolVersion,
		RequestID: "renew-1", AgentID: agentID, LeaseID: "lease-x", AgentVersion: "1.0.0", SentAt: time.Now().UTC()})
	if err != nil {
		f.t.Fatal(err)
	}
	reply, err := nc.Request(agentv1.SubjectLeaseRenew, raw, 2*time.Second)
	if err != nil {
		return nil // no answer
	}
	var response agentv1.RenewResponse
	if err := agentv1.Open(key(1).Public().(ed25519.PublicKey), agentv1.KindRenewResponse, reply.Data, agentv1.MaxControlMessageBytes, &response); err != nil {
		f.t.Fatalf("controller reply does not verify: %v", err)
	}
	return &response
}

func TestEnrollmentAndLeases(t *testing.T) {
	f := newFixture(t)
	agent := key(2)

	if rejected := f.register(agent, strings.Repeat("x", 40), "req-bad"); rejected.Error == nil || rejected.Error.Code != agentv1.ErrorEnrollmentRejected || rejected.AgentID != "" {
		t.Fatalf("wrong token = %+v, want enrollment_rejected", rejected)
	}
	first := f.register(agent, enrollmentToken, "req-1")
	if first.Error != nil || first.AgentID == "" || first.Lease == nil {
		t.Fatalf("registration = %+v", first)
	}
	if again := f.register(agent, enrollmentToken, "req-1"); again.AgentID != first.AgentID {
		t.Fatalf("retried registration gave %s, want %s", again.AgentID, first.AgentID)
	}

	if renewed := f.renew(agent, first.AgentID); renewed == nil || renewed.Error != nil || renewed.Lease == nil {
		t.Fatalf("renewal = %+v", renewed)
	}
	// A renewal signed by another key is not answered at all.
	if forged := f.renew(key(9), first.AgentID); forged != nil {
		t.Fatalf("forged renewal was answered: %+v", forged)
	}
	if err := f.gateway.Revoke(context.Background(), first.AgentID); err != nil {
		t.Fatal(err)
	}
	if revoked := f.renew(agent, first.AgentID); revoked == nil || revoked.Error == nil || revoked.Error.Code != agentv1.ErrorLeaseRevoked {
		t.Fatalf("renewal after revocation = %+v, want lease_revoked", revoked)
	}
	// A revoked agent that clears its identity and enrols again with the
	// shared token is rejected, not given a fresh identity.
	if again := f.register(agent, enrollmentToken, "req-after-revoke"); again.Error == nil || again.Error.Code != agentv1.ErrorEnrollmentRejected || again.AgentID != "" {
		t.Fatalf("re-enrolment of a revoked key = %+v, want enrollment_rejected", again)
	}
}

// ADR-057: only a result signed by the agent it names, on that agent's
// subject, reaches the handler; it is acknowledged only after the handler
// stored it.
func TestResultsMustComeFromTheSigningAgentAndAreAckedAfterStorage(t *testing.T) {
	f := newFixture(t)
	agent, intruder := key(2), key(3)
	victim := f.register(agent, enrollmentToken, "req-a").AgentID
	other := f.register(intruder, enrollmentToken, "req-b").AgentID

	var mu sync.Mutex
	var stored []agentv1.Result
	failNext := true
	f.gateway.SetResultHandler(func(_ context.Context, result agentv1.Result) error {
		mu.Lock()
		defer mu.Unlock()
		if failNext {
			failNext = false
			return errors.New("database unavailable")
		}
		stored = append(stored, result)
		return nil
	})
	publish := func(signer ed25519.PrivateKey, agentID, subjectAgent, output string) {
		now := time.Now().UTC()
		raw, err := agentv1.Seal(signer, agentv1.KindResult, &agentv1.Result{ProtocolVersion: agentv1.ProtocolVersion,
			AgentID: agentID, OperationID: "op-0123456789", Capability: agentv1.CapabilityRuntimeInfo, Status: agentv1.StatusSucceeded,
			Output: json.RawMessage(output), AcceptedAt: now, StartedAt: &now, CompletedAt: &now})
		if err != nil {
			t.Fatal(err)
		}
		subject, _ := agentv1.ResultSubject(subjectAgent)
		if err := f.nc.Publish(subject, raw); err != nil {
			t.Fatal(err)
		}
		_ = f.nc.Flush()
	}
	acks := func() uint64 {
		js, err := jetstream.New(f.nc)
		if err != nil {
			t.Fatal(err)
		}
		subject, _ := agentv1.CommandSubject(victim)
		stream, err := js.Stream(context.Background(), agentv1.CommandStream)
		if err != nil {
			t.Fatal(err)
		}
		info, err := stream.Info(context.Background(), jetstream.WithSubjectFilter(subject))
		if err != nil {
			t.Fatal(err)
		}
		return info.State.Subjects[subject]
	}
	publish(intruder, victim, victim, `{"forged":"wrong key"}`)    // claims the victim's ID, signed by another agent
	publish(intruder, other, victim, `{"forged":"wrong subject"}`) // valid signer, but on the victim's subject
	publish(agent, victim, victim, `{"genuine":1}`)                // the handler fails: no ack
	time.Sleep(300 * time.Millisecond)
	if n := acks(); n != 0 {
		t.Fatalf("%d acks after the handler failed, want 0 (the agent must republish)", n)
	}
	publish(agent, victim, victim, `{"genuine":2}`) // the republished result is stored and acknowledged
	deadline := time.Now().Add(5 * time.Second)
	for acks() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(stored) != 1 || !strings.Contains(string(stored[0].Output), `"genuine":2`) {
		t.Fatalf("stored %+v, want only the genuine republished result", stored)
	}
	if n := acks(); n != 1 {
		t.Fatalf("%d acks, want 1", n)
	}
}

// M-7: a command published twice with the same ID is stored once.
func TestPublishCommandIsIdempotent(t *testing.T) {
	f := newFixture(t)
	agentID := f.register(key(2), enrollmentToken, "req-a").AgentID
	now := time.Now().UTC()
	cmd := agentv1.Command{Type: agentv1.CommandOperationRequest, CommandID: "cmd-0123456789", AgentID: agentID,
		OperationID: "op-0123456789", Capability: agentv1.CapabilityRuntimeInfo, Input: json.RawMessage(`{}`),
		TimeoutMillis: 5000, IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
	for range 3 {
		if err := f.gateway.PublishCommand(context.Background(), cmd); err != nil {
			t.Fatal(err)
		}
	}
	js, _ := jetstream.New(f.nc)
	stream, err := js.Stream(context.Background(), agentv1.CommandStream)
	if err != nil {
		t.Fatal(err)
	}
	info, err := stream.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != 1 {
		t.Fatalf("%d messages after 3 publishes of one command, want 1", info.State.Msgs)
	}
}

func TestNewValidatesConfiguration(t *testing.T) {
	valid := Config{Bus: memory.New(), Signer: key(1), EnrollmentToken: enrollmentToken}
	for name, mutate := range map[string]func(*Config){
		"no bus":            func(c *Config) { c.Bus = nil },
		"no key":            func(c *Config) { c.Signer = nil },
		"short token":       func(c *Config) { c.EnrollmentToken = "short" },
		"heartbeat > ttl/2": func(c *Config) { c.LeaseTTL, c.Heartbeat = 20*time.Second, 15*time.Second },
	} {
		cfg := valid
		mutate(&cfg)
		if _, err := New(newMemStore(), cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := New(newMemStore(), valid); err != nil {
		t.Fatalf("valid config: %v", err)
	}
}
