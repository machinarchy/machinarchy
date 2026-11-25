package operationworker_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/hostaccess"
	"github.com/machinarchy/machinarchy/internal/operationworker"
	"github.com/machinarchy/machinarchy/internal/store"
)

// scriptedConnector answers by host name: "ok" succeeds, "fail" exits 2,
// "broken" cannot connect, "hang" blocks until cancelled.
type scriptedConnector struct{}

func (scriptedConnector) Verify(context.Context, *resource.Host, *resource.AccessProfileRevision) error {
	return nil
}

func (scriptedConnector) Execute(ctx context.Context, host *resource.Host, _ *resource.AccessProfileRevision, _ string, argv []string) (hostaccess.Result, error) {
	switch {
	case strings.HasPrefix(host.Metadata.Name, "ok"):
		return hostaccess.Result{Stdout: "ran " + strings.Join(argv, " "), ExitCode: 0}, nil
	case strings.HasPrefix(host.Metadata.Name, "fail"):
		return hostaccess.Result{Stderr: "boom", ExitCode: 2}, nil
	case strings.HasPrefix(host.Metadata.Name, "broken"):
		return hostaccess.Result{}, errors.New("connection refused")
	case strings.HasPrefix(host.Metadata.Name, "rekeyed"):
		return hostaccess.Result{}, hostaccess.ErrHostKey
	default:
		<-ctx.Done()
		return hostaccess.Result{}, ctx.Err()
	}
}

type env struct {
	t       *testing.T
	stores  *store.Stores
	profile *resource.AccessProfile
	rev     resource.AccessProfileRevision
	policy  domain.ExecutionPolicy
	// rechecker, when set, receives the worker's Host recheck requests.
	rechecker operationworker.HostRechecker
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stores, err := store.Open(context.Background(), store.Config{Engine: store.EngineSQLite, SQLitePath: filepath.Join(dir, "w.db"),
		EncryptionSecret: "worker-test-key-material-that-is-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	profile, rev, err := resource.NewAccessProfile(1, "deploy", "", resource.AccessProfileSpec{Connector: resource.ConnectorSSH, Username: "deploy",
		Authentication: resource.AuthenticationPassword, Credential: resource.SecretReference{ID: "s", Version: 1}, Enabled: true}, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := stores.ResourceProfiles.CreateAccessProfile(context.Background(), profile, rev); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, stores: stores, profile: profile, rev: rev, policy: domain.ExecutionPolicy{Enabled: true, AllowAny: true}}
}

func (e *env) submit(timeout int, names ...string) *resource.Operation {
	e.t.Helper()
	var targets []resource.OperationTargetSpec
	for _, name := range names {
		host, err := resource.NewHost(1, 0, name, nil, resource.HostSpec{AccessProfileID: e.profile.ID,
			AccessTarget: resource.HostAccessTarget{Endpoint: name + ":22"}}, time.Now())
		if err != nil {
			e.t.Fatal(err)
		}
		if err := e.stores.ResourceHosts.CreateHost(context.Background(), host); err != nil {
			e.t.Fatal(err)
		}
		targets = append(targets, resource.OperationTargetSpec{HostID: host.Metadata.ID, HostGeneration: host.Metadata.Generation,
			DisplayName: name, AccessProfileID: e.profile.ID, AccessProfileRevision: e.rev.Revision})
	}
	op, err := resource.NewRunOperation(1, resource.ActorRef{UserID: 1}, resource.OperationSpec{Targets: targets,
		Run: &resource.RunSpec{Argv: []string{"uptime"}, TimeoutSeconds: timeout, Concurrency: 1}}, time.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.stores.Operations.CreateOperation(context.Background(), op); err != nil {
		e.t.Fatal(err)
	}
	return op
}

func (e *env) run() (*operationworker.Worker, func()) {
	e.t.Helper()
	queue := e.stores.Operations.(resource.OperationQueueRepository)
	worker, err := operationworker.New(operationworker.Dependencies{Operations: e.stores.Operations, Queue: queue,
		Hosts: e.stores.ResourceHosts, Profiles: e.stores.ResourceProfiles, Outbox: e.stores.Outbox, Connector: scriptedConnector{},
		ExecutionPolicy: func() domain.ExecutionPolicy { return e.policy }, HostRechecker: e.rechecker,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, 2)
	if err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Start(ctx) }()
	return worker, func() {
		cancel()
		if err := <-done; err != nil {
			e.t.Errorf("worker stopped with %v", err)
		}
	}
}

func (e *env) await(id resource.ID, state resource.OperationState) *resource.Operation {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		op, err := e.stores.Operations.GetOperation(context.Background(), 1, id)
		if err == nil && op.Status.State == state {
			return op
		}
		time.Sleep(50 * time.Millisecond)
	}
	op, _ := e.stores.Operations.GetOperation(context.Background(), 1, id)
	e.t.Fatalf("operation %s did not reach %s: %+v", id, state, op.Status)
	return nil
}

func TestTargetsRecordTheirOwnOutcome(t *testing.T) {
	e := newEnv(t)
	all := e.submit(30, "ok-1", "ok-2")
	mixed := e.submit(30, "ok-3", "fail-1", "broken-1")
	_, stop := e.run()
	defer stop()

	done := e.await(all.ID, resource.OperationSucceeded)
	for _, target := range done.Status.Targets {
		if target.State != resource.TargetSucceeded || target.Result == nil || !strings.Contains(target.Result.Stdout+target.Result.Output, "ran uptime") {
			t.Fatalf("target %s = %+v", target.Spec.DisplayName, target)
		}
	}
	partial := e.await(mixed.ID, resource.OperationPartiallySucceeded)
	states := map[string]resource.TargetState{}
	for _, target := range partial.Status.Targets {
		states[target.Spec.DisplayName] = target.State
	}
	if states["ok-3"] != resource.TargetSucceeded || states["fail-1"] != resource.TargetFailed || states["broken-1"] != resource.TargetFailed {
		t.Fatalf("target states = %v", states)
	}
}

func TestTimeoutAndCancellation(t *testing.T) {
	e := newEnv(t)
	slow := e.submit(1, "hang-1")
	worker, stop := e.run()
	defer stop()
	// A per-host timeout fails the Operation and is reported as a timeout.
	timedOut := e.await(slow.ID, resource.OperationFailed)
	if target := timedOut.Status.Targets[0]; target.State != resource.TargetTimedOut || target.Result == nil || target.Result.ErrorClass != "timeout" {
		t.Fatalf("hung target = %+v %+v", target, target.Result)
	}

	held := e.submit(60, "hang-2")
	deadline := time.Now().Add(10 * time.Second)
	for {
		op, err := e.stores.Operations.GetOperation(context.Background(), 1, held.ID)
		if err == nil && op.Status.State == resource.OperationRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("operation never started")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := worker.Cancel(context.Background(), 1, held.ID); err != nil {
		t.Fatal(err)
	}
	e.await(held.ID, resource.OperationCancelled)
}

func TestExecutionPolicyIsCheckedAtRunTime(t *testing.T) {
	e := newEnv(t)
	e.policy = domain.ExecutionPolicy{} // disabled after submission
	op := e.submit(30, "ok-4")
	_, stop := e.run()
	defer stop()
	failed := e.await(op.ID, resource.OperationFailed)
	if failed.Status.Targets[0].State == resource.TargetSucceeded {
		t.Fatalf("a target ran with execution disabled: %+v", failed.Status.Targets[0])
	}
}

type recheckRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recheckRecorder) RequestHostRecheck(_ context.Context, _ int, id resource.ID, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, string(id)+" "+reason)
	return nil
}

// AUDIT G-13C-01: a run that fails because the host's SSH keys changed asks
// for the Host to be reconciled, so it stops showing Ready; other failures do
// not.
func TestHostKeyFailureQueuesAHostRecheck(t *testing.T) {
	e := newEnv(t)
	recorder := &recheckRecorder{}
	e.rechecker = recorder
	op := e.submit(30, "rekeyed-1", "broken-1", "ok-1")
	_, stop := e.run()
	defer stop()
	done := e.await(op.ID, resource.OperationPartiallySucceeded)
	var rekeyed string
	for _, target := range done.Status.Targets {
		if target.Spec.DisplayName == "rekeyed-1" {
			rekeyed = string(target.Spec.HostID)
			if target.State != resource.TargetFailed {
				t.Fatalf("rekeyed target = %+v", target)
			}
		}
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.calls) != 1 || recorder.calls[0] != rekeyed+" host-key-rejected" {
		t.Fatalf("rechecks = %v, want one for %s", recorder.calls, rekeyed)
	}
}
