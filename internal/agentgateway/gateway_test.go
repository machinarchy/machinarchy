package agentgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// memStore is an in-memory Store.
type memStore struct {
	mu       sync.Mutex
	agents   map[string]*Agent
	requests map[string]string
}

func newMemStore() *memStore {
	return &memStore{agents: map[string]*Agent{}, requests: map[string]string{}}
}

func (s *memStore) RegisterAgent(_ context.Context, agent Agent, requestID string) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.requests[requestID]; ok {
		return *s.agents[id], nil
	}
	for _, existing := range s.agents {
		if existing.PublicKey.Equal(agent.PublicKey) {
			if existing.RevokedAt != nil {
				return Agent{}, ErrAgentRevoked
			}
			return *existing, nil
		}
	}
	s.requests[requestID] = agent.ID
	copied := agent
	s.agents[agent.ID] = &copied
	return agent, nil
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
	gateway, err := New(store, Config{NATSURL: url, NATSOptions: controller, SigningKey: key(1), EnrollmentToken: enrollmentToken,
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

func TestResultsMustComeFromTheSigningAgent(t *testing.T) {
	f := newFixture(t)
	agent, intruder := key(2), key(3)
	victim := f.register(agent, enrollmentToken, "req-a").AgentID
	other := f.register(intruder, enrollmentToken, "req-b").AgentID

	// Capture the command the gateway publishes, then answer it in three ways.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type outcome struct {
		result *agentv1.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := f.gateway.Invoke(ctx, victim, agentv1.CapabilityRuntimeInfo, nil, 5*time.Second)
		done <- outcome{result, err}
	}()
	var operationID string
	for operationID == "" {
		f.gateway.mu.Lock()
		for id := range f.gateway.waiters {
			operationID = id
		}
		f.gateway.mu.Unlock()
		if ctx.Err() != nil {
			t.Fatal("no operation was started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	publish := func(signer ed25519.PrivateKey, agentID, subjectAgent string, output string) {
		now := time.Now().UTC()
		raw, err := agentv1.Seal(signer, agentv1.KindResult, &agentv1.Result{ProtocolVersion: agentv1.ProtocolVersion,
			AgentID: agentID, OperationID: operationID, Capability: agentv1.CapabilityRuntimeInfo, Status: agentv1.StatusSucceeded,
			Output: json.RawMessage(output), AcceptedAt: now, StartedAt: &now, CompletedAt: &now})
		if err != nil {
			t.Fatal(err)
		}
		subject, _ := agentv1.ResultSubject(subjectAgent)
		if err := f.nc.Publish(subject, raw); err != nil {
			t.Fatal(err)
		}
	}
	publish(intruder, victim, victim, `{"forged":"wrong key"}`)    // claims the victim's ID, signed by another agent
	publish(intruder, other, victim, `{"forged":"wrong subject"}`) // valid signer, but on the victim's subject
	publish(agent, victim, victim, `{"genuine":true}`)
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !strings.Contains(string(got.result.Output), "genuine") {
		t.Fatalf("accepted result %s, want only the genuine one", got.result.Output)
	}
}

func TestNewValidatesConfiguration(t *testing.T) {
	valid := Config{NATSURL: "nats://x:4222", SigningKey: key(1), EnrollmentToken: enrollmentToken}
	for name, mutate := range map[string]func(*Config){
		"no url":            func(c *Config) { c.NATSURL = "" },
		"no key":            func(c *Config) { c.SigningKey = nil },
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
	g, _ := New(newMemStore(), valid)
	if _, err := g.Invoke(context.Background(), "agent-x", "shell exec", nil, time.Second); err == nil {
		t.Fatal("invalid capability name accepted")
	}
}
