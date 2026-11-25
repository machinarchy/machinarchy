package operationworker_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/hostaccess"
	"github.com/machinarchy/machinarchy/internal/operationworker"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/service"
	"github.com/machinarchy/machinarchy/internal/store"
	"github.com/machinarchy/machinarchy/internal/testutil/pgtest"
	"github.com/machinarchy/machinarchy/internal/testutil/sshtest"
	"github.com/machinarchy/machinarchy/pkg/netpolicy"
)

// Detached execution end to end (AUDIT/10b §1.5): real SQLite store, real
// secret resolution and SSH connector, and an SSH server whose commands run
// in a real shell, so the embedded runner really supervises processes.

type bindings struct{ stores *store.Stores }

func (b bindings) GetAccessProfile(c context.Context, w int, id resource.ID) (*resource.AccessProfile, error) {
	return b.stores.ResourceProfiles.GetAccessProfile(c, w, id)
}
func (b bindings) GetAccessProfileRevision(c context.Context, w int, id resource.ID, n uint64) (*resource.AccessProfileRevision, error) {
	return b.stores.ResourceProfiles.GetAccessProfileRevision(c, w, id, n)
}
func (b bindings) GetHost(c context.Context, w int, id resource.ID) (*resource.Host, error) {
	return b.stores.ResourceHosts.GetHost(c, w, id)
}
func (b bindings) GetOperation(c context.Context, w int, id resource.ID) (*resource.Operation, error) {
	return b.stores.Operations.GetOperation(c, w, id)
}
func (b bindings) GetOperationTarget(c context.Context, w int, op, host resource.ID) (resource.OperationState, resource.OperationTargetSpec, error) {
	return b.stores.Operations.(resource.OperationTargetReader).GetOperationTarget(c, w, op, host)
}

// fastPolls keeps tests quick. Each poll runs the runner script on the
// "host" (this machine): about 20 short processes per target, so the
// 50-target tests poll at a slower, production-like pace.
var fastPolls = operationworker.PollConfig{MinInterval: 300 * time.Millisecond, MaxInterval: time.Second, Tick: 50 * time.Millisecond,
	CallTimeout: 15 * time.Second, LaunchConfirmGrace: 2 * time.Second, TimeoutGrace: time.Second, UnreachableGrace: time.Second}

type detachedEnv struct {
	t         *testing.T
	ctx       context.Context
	stores    *store.Stores
	app       *service.ManagedResourceService
	secrets   *secrets.Service
	owner     domain.Principal
	server    *sshtest.Server
	home      string
	profile   resource.ID
	connector operationworker.RunnerConnector
	poll      operationworker.PollConfig
	metrics   prometheus.Registerer
	stop      func()
	worker    *operationworker.Worker
}

func newDetachedEnv(t *testing.T) *detachedEnv { return newDetachedEnvOn(t, store.EngineSQLite) }

// envSlots bounds detached environments running at once: each spawns real
// shells, and the full race suite runs every package in parallel.
var envSlots = make(chan struct{}, 4)

func newDetachedEnvOn(t *testing.T, engine string) *detachedEnv {
	t.Helper()
	envSlots <- struct{}{}
	t.Cleanup(func() { <-envSlots })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	home, err := os.MkdirTemp("", "mh") // short: it is part of every runner path
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	// Detached commands outlive the test unless stopped; kill them before
	// their home goes.
	t.Cleanup(func() { _ = exec.Command("pkill", "-KILL", "-f", home).Run() })
	server := sshtest.Start(t, sshtest.Options{Password: "pw", ExecStreams: sshtest.ShellExec("", home)})
	config := store.Config{Engine: store.EngineSQLite, SQLitePath: filepath.Join(dir, "d.db"), EncryptionSecret: "detached-test-key-material-long-enough"}
	if engine == store.EnginePostgres {
		config = store.Config{Engine: store.EnginePostgres, PostgresURL: pgtest.URL(t), EncryptionSecret: config.EncryptionSecret,
			MaxOpenConns: 16, MaxIdleConns: 16, ConnectTimeout: 15 * time.Second}
	}
	stores, err := store.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	authorizer, err := secrets.NewProfileAuthorizer(bindings{stores}, secrets.WorkloadOperationWorker)
	if err != nil {
		t.Fatal(err)
	}
	secretService, err := secrets.New(stores.Management, &secrets.LocalProvider{KeyID: "k", Key: []byte("0123456789abcdef0123456789abcdef")}, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	app, err := service.NewManagedResourceService(stores.ResourceHosts, stores.ResourceProfiles, stores.Operations,
		func() domain.ExecutionPolicy { return domain.ExecutionPolicy{Enabled: true, AllowAny: true} }, secretService)
	if err != nil {
		t.Fatal(err)
	}
	e := &detachedEnv{t: t, ctx: ctx, stores: stores, app: app, secrets: secretService, server: server, home: home, poll: fastPolls,
		owner: domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner}}
	secret, err := secretService.Create(ctx, e.owner, secrets.Input{Name: "pw", Purpose: secrets.PurposeSSHCredential, Value: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := app.CreateAccessProfile(ctx, e.owner, "detached", "", resource.AccessProfileSpec{Connector: resource.ConnectorSSH,
		Username: "deploy", Authentication: resource.AuthenticationPassword, Enabled: true,
		Credential: resource.SecretReference{ID: secret.ID, Version: secret.CurrentVersion},
		Execution:  &resource.ExecutionSettings{Supervisor: resource.SupervisorSetsid}})
	if err != nil {
		t.Fatal(err)
	}
	e.profile = profile.ID
	connector, err := hostaccess.NewSSHConnector(secretService, netpolicy.Policy{AllowLoopback: true, AllowedPorts: []uint16{uint16(server.Port)}},
		64, 30*time.Second, secrets.WorkloadOperationWorker)
	if err != nil {
		t.Fatal(err)
	}
	e.connector = connector
	return e
}

func (e *detachedEnv) hosts(n int) []*resource.Host {
	e.t.Helper()
	hosts := make([]*resource.Host, 0, n)
	for i := range n {
		host, err := e.app.CreateHost(e.ctx, e.owner, fmt.Sprintf("web-%d", i), nil, resource.HostSpec{AccessProfileID: e.profile,
			AccessTarget: resource.HostAccessTarget{Endpoint: e.server.Addr(), Trust: []string{e.server.Fingerprint}}})
		if err != nil {
			e.t.Fatal(err)
		}
		hosts = append(hosts, host)
	}
	return hosts
}

func (e *detachedEnv) submit(hosts []*resource.Host, timeout, concurrency int, argv ...string) resource.ID {
	e.t.Helper()
	profile, err := e.stores.ResourceProfiles.GetAccessProfile(e.ctx, 1, e.profile)
	if err != nil {
		e.t.Fatal(err)
	}
	var targets []resource.OperationTargetSpec
	for _, host := range hosts {
		targets = append(targets, resource.OperationTargetSpec{HostID: host.Metadata.ID, HostGeneration: host.Metadata.Generation,
			DisplayName: host.Metadata.Name, AccessProfileID: e.profile, AccessProfileRevision: profile.CurrentRevision})
	}
	op, err := resource.NewRunOperation(1, resource.ActorRef{UserID: 1}, resource.OperationSpec{Targets: targets,
		Run: &resource.RunSpec{Argv: argv, TimeoutSeconds: timeout, Concurrency: concurrency, Detach: true}}, time.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.stores.Operations.CreateOperation(e.ctx, op); err != nil {
		e.t.Fatal(err)
	}
	return op.ID
}

func (e *detachedEnv) start(connector hostaccess.HostConnector) {
	e.t.Helper()
	if connector == nil {
		connector = e.connector.(hostaccess.HostConnector)
	}
	queue := e.stores.Operations.(resource.OperationQueueRepository)
	recovery := e.stores.Operations.(resource.OperationRecoveryRepository)
	var leases resource.LeaseStore
	if e.stores.Engine() == store.EnginePostgres {
		leases = e.stores.ResourceLeases // as in production: several workers coordinate
	}
	worker, err := operationworker.New(operationworker.Dependencies{Operations: e.stores.Operations, Queue: queue, Hosts: e.stores.ResourceHosts,
		Profiles: e.stores.ResourceProfiles, Outbox: e.stores.Outbox, Connector: connector, Recovery: recovery, Leases: leases,
		ExecutionPolicy: func() domain.ExecutionPolicy { return domain.ExecutionPolicy{Enabled: true, AllowAny: true} },
		Logger:          testLogger(), Poll: e.poll, Registerer: e.metrics}, 4)
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

func (e *detachedEnv) op(id resource.ID) *resource.Operation {
	e.t.Helper()
	op, err := e.stores.Operations.GetOperation(e.ctx, 1, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return op
}

func (e *detachedEnv) waitFor(what string, timeout time.Duration, condition func(*resource.Operation) bool, id resource.ID) *resource.Operation {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		op := e.op(id)
		if condition(op) {
			return op
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s; Operation %s, targets %v", what, op.Status.State, targetStates(op))
		}
		time.Sleep(50 * time.Millisecond) // poll interval of a deadline-bounded wait
	}
}

func (e *detachedEnv) waitTerminal(id resource.ID, timeout time.Duration) *resource.Operation {
	e.t.Helper()
	return e.waitFor("a terminal Operation", timeout, func(op *resource.Operation) bool { return op.IsTerminal() }, id)
}

func allTargets(state resource.TargetState) func(*resource.Operation) bool {
	return func(op *resource.Operation) bool {
		for _, target := range op.Status.Targets {
			if target.State != state {
				return false
			}
		}
		return true
	}
}

func targetStates(op *resource.Operation) map[resource.TargetState]int {
	states := map[resource.TargetState]int{}
	for _, target := range op.Status.Targets {
		states[target.State]++
	}
	return states
}

func (e *detachedEnv) stateDirs() []string {
	dirs, _ := filepath.Glob(filepath.Join(e.home, ".local", "state", "machinarchy", "ops", "*", "*"))
	return dirs
}

// sleepFor returns a sleep duration unique to this test process and a
// pattern that matches it, and kills leftovers when the test ends.
func sleepFor(t *testing.T, base int) (string, string) {
	t.Helper()
	seconds := strconv.Itoa(base*1000 + os.Getpid()%1000)
	pattern := "sleep " + seconds
	t.Cleanup(func() { _ = exec.Command("pkill", "-f", pattern).Run() })
	return seconds, pattern
}

func processes(t *testing.T, pattern string) int {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", pattern).Output()
	if err != nil {
		return 0
	}
	return len(strings.Fields(string(out)))
}

func waitProcesses(t *testing.T, pattern string, want int) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for processes(t, pattern) != want {
		if time.Now().After(deadline) {
			t.Fatalf("%d processes match %q, want %d", processes(t, pattern), pattern, want)
		}
		time.Sleep(100 * time.Millisecond) // poll interval of a deadline-bounded wait
	}
}

func TestDetachedRunReportsOutputAndCleansUp(t *testing.T) {
	t.Parallel()
	e := newDetachedEnv(t)
	e.start(nil)
	id := e.submit(e.hosts(2), 600, 2, "sh", "-c", "echo line1; sleep 1; echo line2; echo warn >&2; exit 3")
	// While it runs, the first line is already visible.
	e.waitFor("live output", 90*time.Second, func(op *resource.Operation) bool {
		for _, target := range op.Status.Targets {
			if target.Live != nil && strings.Contains(string(target.Live.Stdout.Head), "line1") {
				return true
			}
		}
		return op.IsTerminal()
	}, id)
	op := e.waitTerminal(id, 90*time.Second)
	if op.Status.State != resource.OperationFailed {
		t.Fatalf("Operation = %s, want Failed (both exit 3)", op.Status.State)
	}
	for _, target := range op.Status.Targets {
		result := target.Result
		if target.State != resource.TargetFailed || result == nil || result.ExitCode == nil || *result.ExitCode != 3 ||
			result.Stdout != "line1\nline2\n" || result.Stderr != "warn\n" || result.StdoutBytes != 12 || result.Truncated {
			t.Fatalf("target = %s %+v", target.State, result)
		}
		if target.Launch == nil || target.Launch.Mode != "setsid" || target.Live == nil {
			t.Fatalf("target launch %+v, live %+v", target.Launch, target.Live)
		}
		// The final capture is kept: the output endpoint reads it by offset.
		if out := target.OutputOf(op.ID, resource.StreamStdout, 6); out.Text != "line2\n" || out.Next != 12 || out.Live {
			t.Fatalf("output from offset 6 = %+v", out)
		}
	}
	if dirs := e.stateDirs(); len(dirs) != 0 {
		t.Fatalf("state directories left on the host: %v", dirs)
	}
}

// AUDIT/10b §1.5: a restart while 50 targets run loses none, and no
// command runs twice, even when the launcher stops part-way through.
func TestRestartWith50RunningTargetsLosesNone(t *testing.T) {
	t.Parallel()
	e := newDetachedEnv(t)
	e.poll.MinInterval, e.poll.MaxInterval = time.Second, 2*time.Second
	e.start(nil)
	log := filepath.Join(e.home, "runs.log")
	id := e.submit(e.hosts(50), 600, 5, "sh", "-c", "echo run >> "+log+"; sleep 4; echo done")
	e.waitFor("some targets running", 90*time.Second, func(op *resource.Operation) bool {
		return targetStates(op)[resource.TargetRunning] >= 10
	}, id)
	e.stop() // the worker stops mid-launch
	before := targetStates(e.op(id))
	if before[resource.TargetPending] == 0 {
		t.Log("every target was launched before the stop; the resume path is not exercised this run")
	}
	e.start(nil)
	op := e.waitTerminal(id, 90*time.Second)
	if op.Status.State != resource.OperationSucceeded {
		classes := map[string]int{}
		for _, target := range op.Status.Targets {
			if target.Result != nil && target.Result.ErrorClass != "" {
				classes[target.Result.ErrorClass]++
			}
		}
		t.Fatalf("Operation = %s, targets %v, error classes %v (before the restart: %v)", op.Status.State, targetStates(op), classes, before)
	}
	runs, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(runs), "run\n"); n != 50 {
		t.Fatalf("the command ran %d times on 50 targets", n)
	}
	for _, target := range op.Status.Targets {
		if target.Result.Stdout != "done\n" {
			t.Fatalf("target output = %q", target.Result.Stdout)
		}
	}
}

func TestDetachedCancelStopsTheCommands(t *testing.T) {
	t.Parallel()
	e := newDetachedEnv(t)
	e.start(nil)
	first, pattern := sleepFor(t, 1)
	id := e.submit(e.hosts(3), 600, 3, "sh", "-c", "sleep "+first+" & sleep "+first)
	e.waitFor("running targets", 90*time.Second, allTargets(resource.TargetRunning), id)
	// Per target: the `sh -c` and its two sleeps.
	waitProcesses(t, pattern, 9)
	if _, err := e.worker.Cancel(e.ctx, 1, id); err != nil {
		t.Fatal(err)
	}
	op := e.waitTerminal(id, 90*time.Second)
	if op.Status.State != resource.OperationCancelled || !allTargets(resource.TargetCancelled)(op) {
		t.Fatalf("Operation = %s, targets %v", op.Status.State, targetStates(op))
	}
	waitProcesses(t, pattern, 0)
}

func TestDetachedTimeoutEnforcedOnTheHost(t *testing.T) {
	t.Parallel()
	e := newDetachedEnv(t)
	e.start(nil)
	seconds, pattern := sleepFor(t, 2)
	id := e.submit(e.hosts(1), 2, 1, "sleep", seconds)
	op := e.waitTerminal(id, 90*time.Second)
	target := op.Status.Targets[0]
	if op.Status.State != resource.OperationFailed || target.State != resource.TargetTimedOut || target.Result.ErrorClass != "timeout" {
		t.Fatalf("Operation = %s, target %s %+v", op.Status.State, target.State, target.Result)
	}
	waitProcesses(t, pattern, 0)
}

// The poller enforces the deadline itself when the host-side watchdog did
// not act (here: a handle whose deadline is already past).
func TestDetachedTimeoutEnforcedByThePoller(t *testing.T) {
	t.Parallel()
	e := newDetachedEnv(t)
	e.start(nil)
	host := e.hosts(1)[0]
	seconds, pattern := sleepFor(t, 3)
	id := e.submit([]*resource.Host{host}, 600, 1, "sleep", seconds)
	op := e.waitFor("a running target", 90*time.Second, allTargets(resource.TargetRunning), id)
	handle := *op.Status.Targets[0].Launch
	handle.Deadline = time.Now().Add(-time.Minute)
	if err := e.stores.Operations.(resource.DetachedTargetRepository).RecordTargetLaunch(e.ctx, 1, id, host.Metadata.ID, handle); err != nil {
		t.Fatal(err)
	}
	op = e.waitTerminal(id, 90*time.Second)
	if target := op.Status.Targets[0]; target.State != resource.TargetTimedOut {
		t.Fatalf("target = %s %+v", target.State, target.Result)
	}
	waitProcesses(t, pattern, 0)
}

func TestDetachedLostWhenTheSupervisorDies(t *testing.T) {
	t.Parallel()
	e := newDetachedEnv(t)
	e.start(nil)
	seconds, _ := sleepFor(t, 4)
	id := e.submit(e.hosts(1), 600, 1, "sleep", seconds)
	e.waitFor("a running target", 90*time.Second, allTargets(resource.TargetRunning), id)
	dirs := e.stateDirs()
	if len(dirs) != 1 {
		t.Fatalf("state directories = %v", dirs)
	}
	for _, name := range []string{"pid", "pgid"} {
		raw, err := os.ReadFile(filepath.Join(dirs[0], name))
		if err != nil {
			t.Fatal(err)
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
		if name == "pgid" {
			pid = -pid
		}
		_ = syscall.Kill(pid, syscall.SIGKILL) // what a reboot or the OOM killer leaves
	}
	op := e.waitTerminal(id, 90*time.Second)
	if target := op.Status.Targets[0]; target.State != resource.TargetLost || target.Result.ErrorClass != "supervisor_lost" {
		t.Fatalf("target = %s %+v", target.State, target.Result)
	}
	if op.Status.State != resource.OperationFailed {
		t.Fatalf("Operation = %s", op.Status.State)
	}
}

func TestDetachedUnreachableHostBecomesLost(t *testing.T) {
	t.Parallel()
	e := newDetachedEnv(t)
	e.start(nil)
	host := e.hosts(1)[0]
	seconds, _ := sleepFor(t, 5)
	id := e.submit([]*resource.Host{host}, 600, 1, "sleep", seconds)
	op := e.waitFor("a running target", 90*time.Second, allTargets(resource.TargetRunning), id)
	e.server.Close()
	handle := *op.Status.Targets[0].Launch
	handle.Deadline = time.Now().Add(-time.Minute) // past deadline + grace
	if err := e.stores.Operations.(resource.DetachedTargetRepository).RecordTargetLaunch(e.ctx, 1, id, host.Metadata.ID, handle); err != nil {
		t.Fatal(err)
	}
	op = e.waitTerminal(id, 90*time.Second)
	if target := op.Status.Targets[0]; target.State != resource.TargetLost || target.Result.ErrorClass != "unreachable" {
		t.Fatalf("target = %s %+v", target.State, target.Result)
	}
}

// lossyConnector loses the reply to every launch: with run=true the
// command starts on the host anyway, with run=false it never does.
type lossyConnector struct {
	*hostaccess.SSHConnector
	run bool
}

func (c lossyConnector) RunRunner(ctx context.Context, host *resource.Host, profile *resource.AccessProfileRevision, operationID, command string) (string, error) {
	if strings.Contains(command, " 'launch' ") {
		if c.run {
			_, _ = c.SSHConnector.RunRunner(ctx, host, profile, operationID, command)
		}
		return "", hostaccess.ErrConnection
	}
	return c.SSHConnector.RunRunner(ctx, host, profile, operationID, command)
}

func TestUnconfirmedLaunchIsAdoptedOrFailed(t *testing.T) {
	t.Parallel()
	t.Run("started", func(t *testing.T) {
		t.Parallel()
		e := newDetachedEnv(t)
		e.start(lossyConnector{SSHConnector: e.connector.(*hostaccess.SSHConnector), run: true})
		log := filepath.Join(e.home, "runs.log")
		id := e.submit(e.hosts(1), 600, 1, "sh", "-c", "echo run >> "+log)
		op := e.waitTerminal(id, 90*time.Second)
		if target := op.Status.Targets[0]; target.State != resource.TargetSucceeded || target.Launch.Mode != "adopted" {
			t.Fatalf("target = %s, launch %+v, result %+v", target.State, target.Launch, target.Result)
		}
		if runs, _ := os.ReadFile(log); string(runs) != "run\n" {
			t.Fatalf("runs = %q, want exactly one", runs)
		}
	})
	t.Run("never started", func(t *testing.T) {
		t.Parallel()
		e := newDetachedEnv(t)
		e.start(lossyConnector{SSHConnector: e.connector.(*hostaccess.SSHConnector), run: false})
		id := e.submit(e.hosts(1), 600, 1, "true")
		op := e.waitTerminal(id, 90*time.Second)
		if target := op.Status.Targets[0]; target.State != resource.TargetFailed || target.Result.ErrorClass != "launch_failed" {
			t.Fatalf("target = %s %+v", target.State, target.Result)
		}
	})
}

func TestProfileCanRefuseDetachedRuns(t *testing.T) {
	t.Parallel()
	e := newDetachedEnv(t)
	profile, err := e.stores.ResourceProfiles.GetAccessProfile(e.ctx, 1, e.profile)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := e.stores.ResourceProfiles.GetAccessProfileRevision(e.ctx, 1, e.profile, profile.CurrentRevision)
	if err != nil {
		t.Fatal(err)
	}
	spec := revision.Spec
	spec.Execution = &resource.ExecutionSettings{Supervisor: resource.SupervisorAttached}
	if _, err := e.app.UpdateAccessProfile(e.ctx, e.owner, e.profile, profile.CurrentRevision, spec); err != nil {
		t.Fatal(err)
	}
	e.start(nil)
	id := e.submit(e.hosts(1), 600, 1, "true")
	op := e.waitTerminal(id, 90*time.Second)
	if target := op.Status.Targets[0]; target.State != resource.TargetFailed || target.Result.ErrorClass != "detached_disabled_by_profile" {
		t.Fatalf("target = %s %+v", target.State, target.Result)
	}
}

// PostgreSQL deployments run several workers. Two run at once here, one is
// stopped mid-way, and the leases (Operation leases for launches, host
// leases for polls) must still run each of 50 commands exactly once.
func TestTwoWorkersOnPostgresRunEachCommandOnce(t *testing.T) {
	t.Parallel()
	e := newDetachedEnvOn(t, store.EnginePostgres)
	// Each PostgreSQL poll also takes a host lease and resolves a credential;
	// poll at a production-like pace rather than the SQLite tests' 150 ms.
	e.poll.MinInterval, e.poll.MaxInterval = time.Second, 2*time.Second
	e.start(nil)
	stopFirst := e.stop
	e.start(nil)
	log := filepath.Join(e.home, "runs.log")
	id := e.submit(e.hosts(50), 600, 5, "sh", "-c", "echo run >> "+log+"; sleep 3; echo done")
	e.waitFor("some targets running", 90*time.Second, func(op *resource.Operation) bool {
		return targetStates(op)[resource.TargetRunning] >= 10
	}, id)
	stopFirst()
	op := e.waitTerminal(id, 120*time.Second)
	if op.Status.State != resource.OperationSucceeded {
		t.Fatalf("Operation = %s, targets %v", op.Status.State, targetStates(op))
	}
	runs, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(runs), "run\n"); n != 50 {
		t.Fatalf("the command ran %d times on 50 targets", n)
	}
}

// testLogger discards worker logs unless MACHINARCHY_TEST_WORKER_LOGS is set.
func testLogger() *slog.Logger {
	if os.Getenv("MACHINARCHY_TEST_WORKER_LOGS") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// AUDIT/10b §1.5 benchmark: 200 targets × a 5-minute command with 4 workers
// and the production poll intervals finish in about 5 minutes (attached and
// sequential, the same work took 200 × 5 min ≈ 16.7 h). Opt-in: set
// MACHINARCHY_BENCH_DETACHED=1 (it takes about 6 minutes).
func TestBenchmark200TargetsFiveMinutes(t *testing.T) {
	if os.Getenv("MACHINARCHY_BENCH_DETACHED") == "" {
		t.Skip("set MACHINARCHY_BENCH_DETACHED=1 to run the 200 × 5 min benchmark")
	}
	e := newDetachedEnv(t)
	e.poll = operationworker.PollConfig{} // production defaults: 5 s, growing to 30 s
	registry := prometheus.NewRegistry()
	e.metrics = registry
	hosts := e.hosts(200)
	e.start(nil)
	t.Cleanup(func() { _ = exec.Command("pkill", "-f", "echo start; sleep 300; echo end").Run() })
	start := time.Now()
	id := e.submit(hosts, 600, 20, "sh", "-c", "echo start; sleep 300; echo end")
	launched := e.waitFor("every target launched", 5*time.Minute, func(op *resource.Operation) bool {
		return targetStates(op)[resource.TargetPending] == 0
	}, id)
	launchTime := time.Since(start)
	op := e.waitTerminal(id, 15*time.Minute)
	total := time.Since(start)
	families, _ := registry.Gather()
	metrics := map[string]float64{}
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			name := family.GetName()
			for _, label := range metric.GetLabel() {
				name += "{" + label.GetValue() + "}"
			}
			if metric.GetCounter() != nil {
				metrics[name] = metric.GetCounter().GetValue()
			}
		}
	}
	t.Logf("200 targets: all launched after %v (%v), Operation %s after %v; targets %v",
		launchTime.Round(time.Second), targetStates(launched), op.Status.State, total.Round(time.Second), targetStates(op))
	t.Logf("polls ok=%v unreachable=%v, launches confirmed=%v, lost=%v",
		metrics["machinarchy_operationworker_polls_total{ok}"], metrics["machinarchy_operationworker_polls_total{unreachable}"],
		metrics["machinarchy_operationworker_detached_launches_total{confirmed}"], metrics["machinarchy_operationworker_lost_targets_total"])
	if op.Status.State != resource.OperationSucceeded {
		t.Fatalf("Operation = %s, targets %v", op.Status.State, targetStates(op))
	}
	if total > 7*time.Minute {
		t.Fatalf("200 × 5 min took %v, want about 5 minutes", total)
	}
}
