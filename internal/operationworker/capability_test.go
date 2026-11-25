package operationworker_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"

	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/hostaccess"
	"github.com/machinarchy/machinarchy/internal/operationworker"
	"github.com/machinarchy/machinarchy/internal/store"
)

// fakeAgents stands in for the agent gateway: it records what the relay
// publishes and can refuse publishes to simulate an unavailable JetStream.
type fakeAgents struct {
	mu       sync.Mutex
	lifetime time.Duration
	failing  int  // publishes still to refuse
	down     bool // refuse every publish
	panics   int  // publishes that panic first (a gateway bug)
	commands []agentv1.Command
}

func (f *fakeAgents) PublishCommand(_ context.Context, cmd agentv1.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.panics > 0 {
		f.panics--
		panic("gateway bug")
	}
	if f.down || f.failing > 0 {
		f.failing--
		return errors.New("jetstream unavailable")
	}
	f.commands = append(f.commands, cmd)
	return nil
}

func (f *fakeAgents) CommandLifetime() time.Duration { return f.lifetime }

func (f *fakeAgents) published(kind agentv1.CommandType) []agentv1.Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []agentv1.Command
	for _, c := range f.commands {
		if c.Type == kind {
			out = append(out, c)
		}
	}
	return out
}

// fakeBindings maps host IDs to the agents enrolled for them.
type fakeBindings map[string]string

func (b fakeBindings) HostAgent(_ context.Context, _ int, hostID string) (*agentgateway.Agent, error) {
	id, ok := b[hostID]
	if !ok {
		return nil, agentgateway.ErrHostNotBound
	}
	return &agentgateway.Agent{ID: id}, nil
}

// noSSH fails any SSH use: capability Operations must never reach it.
type noSSH struct{}

func (noSSH) Verify(context.Context, *resource.Host, *resource.AccessProfileRevision) error {
	return errors.New("ssh used by a capability Operation")
}
func (noSSH) Execute(context.Context, *resource.Host, *resource.AccessProfileRevision, string, []string) (hostaccess.Result, error) {
	return hostaccess.Result{}, errors.New("ssh used by a capability Operation")
}

type capabilityEnv struct {
	t        *testing.T
	ctx      context.Context
	stores   *store.Stores
	agents   *fakeAgents
	bindings fakeBindings
	grace    time.Duration
	profile  *resource.AccessProfile
	revision resource.AccessProfileRevision
	worker   *operationworker.Worker
	stop     func()
	// connector runs SSH commands; nil fails any use (capability tests).
	connector hostaccess.HostConnector
}

func newCapabilityEnv(t *testing.T) *capabilityEnv {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stores, err := store.Open(ctx, store.Config{Engine: store.EngineSQLite, SQLitePath: filepath.Join(dir, "c.db"),
		EncryptionSecret: "capability-test-key-material-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	now := time.Now()
	profile, revision, err := resource.NewAccessProfile(1, "agents", "", resource.AccessProfileSpec{Connector: resource.ConnectorAgent, Enabled: true}, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := stores.ResourceProfiles.CreateAccessProfile(ctx, profile, revision); err != nil {
		t.Fatal(err)
	}
	return &capabilityEnv{t: t, ctx: ctx, stores: stores, agents: &fakeAgents{lifetime: time.Minute}, bindings: fakeBindings{},
		grace: time.Minute, profile: profile, revision: revision}
}

// host creates a host on the agent profile, bound to agentID unless empty.
func (e *capabilityEnv) host(name, agentID string) *resource.Host {
	e.t.Helper()
	host, err := resource.NewHost(1, 0, name, nil, resource.HostSpec{AccessProfileID: e.profile.ID}, time.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.stores.ResourceHosts.CreateHost(e.ctx, host); err != nil {
		e.t.Fatal(err)
	}
	if agentID != "" {
		e.bindings[string(host.Metadata.ID)] = agentID
	}
	return host
}

func (e *capabilityEnv) submit(timeoutSeconds int, hosts ...*resource.Host) resource.ID {
	e.t.Helper()
	return e.submitOn(e.profile.ID, e.revision.Revision, timeoutSeconds, hosts...)
}

func (e *capabilityEnv) submitOn(profile resource.ID, revision uint64, timeoutSeconds int, hosts ...*resource.Host) resource.ID {
	e.t.Helper()
	var targets []resource.OperationTargetSpec
	for _, host := range hosts {
		targets = append(targets, resource.OperationTargetSpec{HostID: host.Metadata.ID, HostGeneration: host.Metadata.Generation,
			DisplayName: host.Metadata.Name, AccessProfileID: profile, AccessProfileRevision: revision})
	}
	op, err := resource.NewCapabilityOperation(1, resource.ActorRef{UserID: 1}, resource.OperationSpec{Targets: targets,
		Capability: &resource.CapabilitySpec{Name: "service.status", Input: json.RawMessage(`{"service":"nginx.service"}`), TimeoutSeconds: timeoutSeconds}}, time.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.stores.Operations.CreateOperation(e.ctx, op); err != nil {
		e.t.Fatal(err)
	}
	return op.ID
}

func (e *capabilityEnv) start() {
	e.t.Helper()
	worker, err := operationworker.New(operationworker.Dependencies{Operations: e.stores.Operations,
		Queue: e.stores.Operations.(resource.OperationQueueRepository), Hosts: e.stores.ResourceHosts, Profiles: e.stores.ResourceProfiles,
		Outbox: e.stores.Outbox, Connector: e.sshConnector(), Recovery: e.stores.Operations.(resource.OperationRecoveryRepository),
		ExecutionPolicy: func() domain.ExecutionPolicy { return domain.ExecutionPolicy{Enabled: true, AllowAny: true} },
		Logger:          testLogger(), Agents: e.agents, AgentBindings: e.bindings, CapabilityResultGrace: e.grace}, 2)
	if err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = worker.Start(ctx) }()
	e.worker = worker
	var once sync.Once
	e.stop = func() { once.Do(func() { cancel(); <-done }) }
	e.t.Cleanup(e.stop)
}

func (e *capabilityEnv) sshConnector() hostaccess.HostConnector {
	if e.connector == nil {
		return noSSH{}
	}
	return e.connector
}

func (e *capabilityEnv) op(id resource.ID) *resource.Operation {
	e.t.Helper()
	op, err := e.stores.Operations.GetOperation(e.ctx, 1, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return op
}

func (e *capabilityEnv) waitFor(what string, condition func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond) // poll interval of a deadline-bounded wait
	}
}

func (e *capabilityEnv) waitRequests(n int) []agentv1.Command {
	e.t.Helper()
	e.waitFor(fmt.Sprintf("%d published requests", n), func() bool { return len(e.agents.published(agentv1.CommandOperationRequest)) >= n })
	return e.agents.published(agentv1.CommandOperationRequest)
}

func (e *capabilityEnv) waitTerminal(id resource.ID) *resource.Operation {
	e.t.Helper()
	var op *resource.Operation
	e.waitFor("a terminal Operation", func() bool { op = e.op(id); return op.IsTerminal() })
	return op
}

func (e *capabilityEnv) result(cmd agentv1.Command, status agentv1.OperationStatus, code, output string) error {
	r := agentv1.Result{ProtocolVersion: 1, AgentID: cmd.AgentID, OperationID: cmd.OperationID, Capability: cmd.Capability,
		Status: status, Code: code, AcceptedAt: time.Now()}
	if output != "" {
		r.Output = json.RawMessage(output)
	}
	return e.worker.HandleAgentResult(e.ctx, r)
}

func target(op *resource.Operation, host *resource.Host) resource.OperationTarget {
	for _, t := range op.Status.Targets {
		if t.Spec.HostID == host.Metadata.ID {
			return t
		}
	}
	return resource.OperationTarget{}
}

func errorClass(t resource.OperationTarget) string {
	if t.Result == nil {
		return ""
	}
	return t.Result.ErrorClass
}

func TestCapabilityOperationRunsOnBoundAgents(t *testing.T) {
	t.Parallel()
	e := newCapabilityEnv(t)
	web, db := e.host("web", "agent-web"), e.host("db", "agent-db")
	e.agents.failing = 1 // the first publish is refused and retried
	id := e.submit(30, web, db)
	e.start()

	requests := e.waitRequests(2)
	byAgent := map[string]agentv1.Command{}
	for _, cmd := range requests {
		if cmd.Capability != "service.status" || string(cmd.Input) != `{"service":"nginx.service"}` || cmd.TimeoutMillis != 30000 ||
			cmd.CommandID == "" || cmd.OperationID == "" || !cmd.ExpiresAt.After(cmd.IssuedAt) {
			t.Fatalf("published request %+v", cmd)
		}
		byAgent[cmd.AgentID] = cmd
	}
	if len(byAgent) != 2 || byAgent["agent-web"].OperationID == byAgent["agent-db"].OperationID {
		t.Fatalf("requests did not go one to each bound agent: %+v", requests)
	}
	if op := e.op(id); op.Status.State != resource.OperationRunning || target(op, web).State != resource.TargetRunning {
		t.Fatalf("after dispatch: %s, targets %v", op.Status.State, targetStates(op))
	}

	// accepted is not terminal; the target stays Running.
	if err := e.result(byAgent["agent-web"], agentv1.StatusAccepted, "", ""); err != nil {
		t.Fatal(err)
	}
	// A result must come from the agent the command went to.
	stolen := byAgent["agent-web"]
	stolen.AgentID = "agent-db"
	if err := e.result(stolen, agentv1.StatusSucceeded, "", `{}`); err == nil {
		t.Fatal("a result from another agent was stored")
	}
	if err := e.result(byAgent["agent-web"], agentv1.StatusSucceeded, "", `{"active":true}`); err != nil {
		t.Fatal(err)
	}
	if err := e.result(byAgent["agent-db"], agentv1.StatusFailed, "unit_not_found", ""); err != nil {
		t.Fatal(err)
	}
	op := e.waitTerminal(id)
	if got := target(op, web); got.State != resource.TargetSucceeded || got.Result == nil || got.Result.Output != `{"active":true}` {
		t.Fatalf("web target %+v", got)
	}
	if got := target(op, db); got.State != resource.TargetFailed || errorClass(got) != "unit_not_found" {
		t.Fatalf("db target %+v", got)
	}

	// The agent republishes a result whose ack it missed: acknowledged, no change.
	if err := e.result(byAgent["agent-db"], agentv1.StatusSucceeded, "", `{}`); err != nil {
		t.Fatalf("duplicate result: %v", err)
	}
	if again := e.op(id); target(again, db).State != resource.TargetFailed || again.Status.State != op.Status.State {
		t.Fatalf("a duplicate result changed the Operation: %s %v", again.Status.State, targetStates(again))
	}
	// A result for an operation the controller never issued is acknowledged.
	if err := e.worker.HandleAgentResult(e.ctx, agentv1.Result{AgentID: "agent-web", OperationID: "op-unknown", Status: agentv1.StatusSucceeded}); err != nil {
		t.Fatalf("unknown operation result: %v", err)
	}
	if got := len(e.agents.published(agentv1.CommandOperationRequest)); got != 2 {
		t.Fatalf("published %d requests, want 2", got)
	}
}

func TestCapabilityOperationSurvivesControllerRestart(t *testing.T) {
	t.Parallel()
	e := newCapabilityEnv(t)
	web, db := e.host("web", "agent-web"), e.host("db", "agent-db")
	id := e.submit(30, web, db)
	e.start()
	requests := e.waitRequests(2)
	if err := e.result(requests[0], agentv1.StatusSucceeded, "", `{}`); err != nil {
		t.Fatal(err)
	}
	e.stop() // the controller restarts while one result is outstanding

	e.start()
	if err := e.result(requests[1], agentv1.StatusSucceeded, "", `{}`); err != nil {
		t.Fatal(err)
	}
	op := e.waitTerminal(id)
	if op.Status.State != resource.OperationSucceeded || targetStates(op)[resource.TargetSucceeded] != 2 {
		t.Fatalf("after restart: %s, targets %v", op.Status.State, targetStates(op))
	}
	time.Sleep(300 * time.Millisecond) // one more relay tick
	if got := len(e.agents.published(agentv1.CommandOperationRequest)); got != 2 {
		t.Fatalf("the restart republished requests: %d", got)
	}
}

func TestCapabilityResultStatusesMapToTargetStates(t *testing.T) {
	t.Parallel()
	e := newCapabilityEnv(t)
	hosts := []*resource.Host{e.host("a", "agent-a"), e.host("b", "agent-b"), e.host("c", "agent-c"), e.host("d", "agent-d")}
	id := e.submit(30, hosts...)
	e.start()
	byAgent := map[string]agentv1.Command{}
	for _, cmd := range e.waitRequests(4) {
		byAgent[cmd.AgentID] = cmd
	}
	cases := []struct {
		agent  string
		status agentv1.OperationStatus
		code   string
		state  resource.TargetState
		class  string
	}{
		{"agent-a", agentv1.StatusUnknown, "", resource.TargetInterrupted, "outcome_unknown"},
		{"agent-b", agentv1.StatusTimedOut, "", resource.TargetTimedOut, "timeout"},
		{"agent-c", agentv1.StatusRejected, "", resource.TargetFailed, "rejected"},
		{"agent-d", agentv1.StatusCancelled, "", resource.TargetCancelled, "cancelled"},
	}
	for _, c := range cases {
		if err := e.result(byAgent[c.agent], c.status, c.code, ""); err != nil {
			t.Fatal(err)
		}
	}
	op := e.waitTerminal(id)
	for i, c := range cases {
		if got := target(op, hosts[i]); got.State != c.state || errorClass(got) != c.class {
			t.Errorf("%s: target %s/%s, want %s/%s", c.status, got.State, errorClass(got), c.state, c.class)
		}
	}
}

func TestCapabilityTargetWithoutResultBecomesLost(t *testing.T) {
	t.Parallel()
	e := newCapabilityEnv(t)
	e.agents.lifetime = 200 * time.Millisecond
	e.grace = 50 * time.Millisecond
	web := e.host("web", "agent-web")
	id := e.submit(1, web)
	e.start()
	e.waitRequests(1)
	op := e.waitTerminal(id)
	if got := target(op, web); got.State != resource.TargetLost || errorClass(got) != "no_result" {
		t.Fatalf("target %+v", got)
	}
}

func TestCapabilityCommandNeverPublishedFailsTheTarget(t *testing.T) {
	t.Parallel()
	e := newCapabilityEnv(t)
	e.agents.lifetime = 500 * time.Millisecond
	e.agents.down = true
	web := e.host("web", "agent-web")
	id := e.submit(30, web)
	e.start()
	op := e.waitTerminal(id)
	if got := target(op, web); got.State != resource.TargetFailed || errorClass(got) != "agent_command_undelivered" {
		t.Fatalf("target %+v", got)
	}
}

func TestCapabilityCancelSendsCancelCommands(t *testing.T) {
	t.Parallel()
	e := newCapabilityEnv(t)
	web := e.host("web", "agent-web")
	id := e.submit(30, web)
	e.start()
	request := e.waitRequests(1)[0]
	if _, err := e.worker.Cancel(e.ctx, 1, id); err != nil {
		t.Fatal(err)
	}
	e.waitFor("a cancel command", func() bool { return len(e.agents.published(agentv1.CommandOperationCancel)) == 1 })
	cancel := e.agents.published(agentv1.CommandOperationCancel)[0]
	if cancel.AgentID != "agent-web" || cancel.OperationID != request.OperationID || cancel.CommandID == request.CommandID {
		t.Fatalf("cancel command %+v for request %+v", cancel, request)
	}
	// The target's final state comes from the agent.
	if op := e.op(id); op.IsTerminal() {
		t.Fatalf("Operation finished before the agent answered: %s", op.Status.State)
	}
	if err := e.result(request, agentv1.StatusCancelled, "", ""); err != nil {
		t.Fatal(err)
	}
	op := e.waitTerminal(id)
	if op.Status.State != resource.OperationCancelled || target(op, web).State != resource.TargetCancelled {
		t.Fatalf("after cancel: %s, targets %v", op.Status.State, targetStates(op))
	}
}

func TestCapabilityTargetsThatCannotBeDispatchedFail(t *testing.T) {
	t.Parallel()
	e := newCapabilityEnv(t)
	unbound := e.host("unbound", "")
	sshProfile, sshRevision, err := resource.NewAccessProfile(1, "ssh", "", resource.AccessProfileSpec{Connector: resource.ConnectorSSH,
		Username: "deploy", Authentication: resource.AuthenticationPassword, Enabled: true,
		Credential: resource.SecretReference{ID: "secret-1", Version: 1}}, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.stores.ResourceProfiles.CreateAccessProfile(e.ctx, sshProfile, sshRevision); err != nil {
		t.Fatal(err)
	}
	sshHost, err := resource.NewHost(1, 0, "ssh-host", nil, resource.HostSpec{AccessProfileID: sshProfile.ID}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.stores.ResourceHosts.CreateHost(e.ctx, sshHost); err != nil {
		t.Fatal(err)
	}
	unboundOp := e.submit(30, unbound)
	sshOp := e.submitOn(sshProfile.ID, sshRevision.Revision, 30, sshHost)
	e.start()
	if got := target(e.waitTerminal(unboundOp), unbound); got.State != resource.TargetFailed || errorClass(got) != "agent_not_bound" {
		t.Fatalf("unbound host target %+v", got)
	}
	if got := target(e.waitTerminal(sshOp), sshHost); got.State != resource.TargetFailed || errorClass(got) != "not_an_agent_host" {
		t.Fatalf("SSH host target %+v", got)
	}
	if got := len(e.agents.published(agentv1.CommandOperationRequest)); got != 0 {
		t.Fatalf("published %d requests for undispatchable targets", got)
	}
}

// A panic in the relay (here, the gateway's publish) is isolated: the next
// tick publishes the command.
func TestCapabilityRelaySurvivesAPanic(t *testing.T) {
	e := newCapabilityEnv(t)
	e.agents.panics = 2
	host := e.host("web-1", "agent-1")
	e.submit(60, host)
	e.start()
	if got := e.waitRequests(1); len(got) != 1 {
		t.Fatalf("requests = %v", got)
	}
}
