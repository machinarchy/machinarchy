package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/pkg/netpolicy"
	"github.com/machinarchy/machinarchy/pkg/sshclient"
)

const (
	defaultHeartbeatInterval = 5 * time.Second
	defaultStaleAfter        = 30 * time.Second
	defaultMaxActiveRuns     = 4
	persistTimeout           = 10 * time.Second
	sshSlotPoll              = 50 * time.Millisecond
)

// CommandRunner runs one command on one host. The default implementation uses
// SSH with the pinned host key; tests substitute their own.
type CommandRunner interface {
	Run(ctx context.Context, params sshclient.DialParams, command string) (string, error)
}

type sshCommandRunner struct{}

func (sshCommandRunner) Run(ctx context.Context, params sshclient.DialParams, command string) (string, error) {
	return sshclient.ExecuteRemoteCommand(ctx, params, command)
}

// RunManager is the run service together with the lifecycle the process must
// drive: recovery at startup and an orderly stop at shutdown.
type RunManager interface {
	domain.RunService
	// Recover marks runs whose executor stopped heartbeating as interrupted. It
	// also runs periodically in the background.
	Recover(ctx context.Context) (int, error)
	// Close stops accepting runs, interrupts the ones in flight, and waits for
	// their executors to record the outcome (or for ctx to end).
	Close(ctx context.Context) error
}

type runService struct {
	runs              domain.RunStore
	hosts             domain.HostStore
	environments      domain.EnvironmentStore
	sshLimit          *SSHCapacity
	execution         domain.ExecutionPolicy
	policy            netpolicy.Policy
	policyProvider    func() netpolicy.Policy
	executionProvider func() domain.ExecutionPolicy
	runner            CommandRunner

	heartbeatInterval time.Duration
	staleAfter        time.Duration
	maxActive         int32
	defaultTimeout    int
	instance          string

	baseCtx    context.Context
	baseCancel context.CancelFunc
	wg         sync.WaitGroup
	active     atomic.Int32
	mu         sync.Mutex
	closed     bool
}

var _ RunManager = (*runService)(nil)

// NewRunService creates the run service and starts its recovery sweeper; call
// Close to stop it.
func NewRunService(runs domain.RunStore, hosts domain.HostStore, environments domain.EnvironmentStore,
	sshLimit *SSHCapacity, execution domain.ExecutionPolicy, opts ...Option) (RunManager, error) {
	if runs == nil || hosts == nil || environments == nil || sshLimit == nil {
		return nil, errors.New("run service requires run, host, and environment stores and an SSH capacity limiter")
	}
	options := applyOptions(opts)
	if options.heartbeatInterval <= 0 ||
		options.staleAfter <= options.heartbeatInterval ||
		options.maxActiveRuns < 1 {
		return nil, errors.New("run timing must have a positive heartbeat interval shorter than the stale timeout and allow at least one active run")
	}
	if int64(options.maxActiveRuns) > 1<<31-1 {
		return nil, fmt.Errorf(
			"max active runs exceeds int32 limit: %d",
			options.maxActiveRuns,
		)
	}
	runner := options.commandRunner
	if runner == nil {
		runner = sshCommandRunner{}
	}
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, fmt.Errorf("generate executor instance ID: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &runService{
		runs: runs, hosts: hosts, environments: environments, sshLimit: sshLimit,
		execution: execution, policy: options.targetPolicy, policyProvider: options.targetPolicyProvider, executionProvider: options.executionPolicyProvider, runner: runner,
		heartbeatInterval: options.heartbeatInterval, staleAfter: options.staleAfter,
		maxActive: int32(options.maxActiveRuns), defaultTimeout: options.defaultRunTimeout, instance: hex.EncodeToString(idBytes),
		baseCtx: ctx, baseCancel: cancel,
	}
	s.wg.Add(1)
	go s.sweep()
	return s, nil
}

func (s *runService) targetPolicy() netpolicy.Policy {
	if s.policyProvider != nil {
		return s.policyProvider()
	}
	return s.policy
}
func (s *runService) executionPolicy() domain.ExecutionPolicy {
	if s.executionProvider != nil {
		return s.executionProvider()
	}
	return s.execution
}

func authorizeRunExecute(principal domain.Principal) error {
	if principal.UserID <= 0 || principal.WorkspaceID <= 0 || !principal.Role.Allows(domain.PermissionRunsExecute) {
		return domain.ErrPermissionDenied
	}
	return nil
}

func authorizeRunRead(principal domain.Principal) error {
	if principal.UserID <= 0 || principal.WorkspaceID <= 0 || !principal.Role.Allows(domain.PermissionOperationsRead) {
		return domain.ErrPermissionDenied
	}
	return nil
}

// begin reserves an executor slot, or reports that the service is stopping or full.
func (s *runService) begin() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("%w: the server is shutting down", domain.ErrServiceBusy)
	}
	if s.active.Add(1) > s.maxActive {
		s.active.Add(-1)
		return fmt.Errorf("%w: too many runs are executing; retry shortly", domain.ErrServiceBusy)
	}
	s.wg.Add(1)
	return nil
}

func (s *runService) end() {
	s.active.Add(-1)
	s.wg.Done()
}

func (s *runService) Submit(ctx context.Context, principal domain.Principal, request domain.RunRequest) (*domain.Run, error) {
	if ctx == nil {
		return nil, errors.New("run context is required")
	}
	if err := authorizeRunExecute(principal); err != nil {
		return nil, err
	}
	// Policy and shape are checked before the inventory is touched, so a server
	// with execution disabled reveals nothing about its hosts.
	if err := domain.ValidateArgv(request.Argv); err != nil {
		return nil, err
	}
	if err := s.executionPolicy().Check(request.Argv[0]); err != nil {
		return nil, err
	}
	timeout, concurrency, err := normalizeRunLimits(request, s.defaultTimeout)
	if err != nil {
		return nil, err
	}
	if err := request.Rule.Validate(); err != nil {
		return nil, err
	}

	hosts, err := s.hosts.ListAllHosts(ctx, principal.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("list inventory for run: %w", err)
	}
	environments, err := s.environments.ListEnvironments(ctx, principal.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("list environments for run: %w", err)
	}
	names := make(map[int]string, len(environments))
	for _, environment := range environments {
		names[environment.ID] = environment.Name
	}
	set, err := domain.ResolveTargets(request.Rule, hosts, names)
	if err != nil {
		return nil, err
	}
	switch {
	case len(set.UnknownIncludes) > 0:
		return nil, fmt.Errorf("%w: include entries match no host: %s", domain.ErrInvalidInput, strings.Join(set.UnknownIncludes, ", "))
	case len(set.Matched) == 0:
		return nil, fmt.Errorf("%w: the target rule matches no hosts", domain.ErrInvalidInput)
	case len(set.Matched) > domain.MaxRunHosts:
		return nil, fmt.Errorf("%w: the target rule matches %d hosts; a run may target at most %d", domain.ErrInvalidInput, len(set.Matched), domain.MaxRunHosts)
	}
	targetsHash := domain.TargetsHash(set.Matched)
	if request.ExpectTargetsHash != "" && request.ExpectTargetsHash != targetsHash {
		return nil, &domain.TargetsChangedError{TargetsHash: targetsHash, Total: len(set.Matched)}
	}

	run := &domain.Run{
		WorkspaceID: principal.WorkspaceID, Status: domain.RunPlanned,
		CreatedByUserID: principal.UserID, CreatedByUsername: principal.Username, CreatedByTokenID: principal.APITokenID,
		Program: request.Argv[0], ArgCount: len(request.Argv) - 1, Argv: append([]string(nil), request.Argv...),
		ArgvSHA256: domain.ArgvDigest(request.Argv), TimeoutSeconds: timeout, Concurrency: concurrency,
		TargetsHash: targetsHash, Rule: request.Rule,
	}
	for _, host := range set.Matched {
		if host.SSH == nil || host.IPAddress == "" {
			return nil, fmt.Errorf("%w: host %q has no SSH connection settings", domain.ErrInvalidInput, host.Name)
		}
		run.Hosts = append(run.Hosts, domain.RunHost{
			HostID: host.ID, Name: host.Name, Address: host.IPAddress, Port: host.SSH.Port, Status: domain.RunHostPending,
		})
	}
	run.Counts.Pending = len(run.Hosts)
	if request.DryRun {
		return run, nil
	}

	if err := s.begin(); err != nil {
		return nil, err
	}
	run.Status = domain.RunQueued
	if err := s.runs.CreateRun(ctx, run); err != nil {
		s.end()
		return nil, err
	}
	creator := domain.AuditActor{
		UserID: principal.UserID, Username: principal.Username, APITokenID: principal.APITokenID, Source: "executor",
	}
	if actor, ok := domain.AuditActorFrom(ctx); ok {
		creator.RequestID = actor.RequestID
	}
	go s.execute(run, creator)
	return run, nil
}

func normalizeRunLimits(request domain.RunRequest, defaultTimeout int) (timeout, concurrency int, err error) {
	timeout, concurrency = request.TimeoutSeconds, request.Concurrency
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if concurrency == 0 {
		concurrency = domain.DefaultRunConcurrency
	}
	if timeout < 1 || timeout > domain.MaxRunTimeout {
		return 0, 0, fmt.Errorf("%w: timeout must be between 1 and %d seconds", domain.ErrInvalidInput, domain.MaxRunTimeout)
	}
	if concurrency < 1 || concurrency > domain.MaxRunConcurrency {
		return 0, 0, fmt.Errorf("%w: concurrency must be between 1 and %d", domain.ErrInvalidInput, domain.MaxRunConcurrency)
	}
	return timeout, concurrency, nil
}

func (s *runService) Get(ctx context.Context, principal domain.Principal, id int64) (*domain.Run, error) {
	if ctx == nil {
		return nil, errors.New("run context is required")
	}
	if err := authorizeRunRead(principal); err != nil {
		return nil, err
	}
	run, err := s.runs.GetRun(ctx, principal.WorkspaceID, id)
	if err != nil {
		return nil, err
	}
	if !principal.Role.Allows(domain.PermissionRunsExecute) {
		redactRun(run)
	}
	return run, nil
}

func (s *runService) List(ctx context.Context, principal domain.Principal, filter domain.RunFilter) ([]domain.Run, error) {
	if ctx == nil {
		return nil, errors.New("run context is required")
	}
	if err := authorizeRunRead(principal); err != nil {
		return nil, err
	}
	filter, err := domain.NormalizeRunFilter(filter)
	if err != nil {
		return nil, err
	}
	runs, err := s.runs.ListRuns(ctx, principal.WorkspaceID, filter)
	if err != nil {
		return nil, err
	}
	for i := range runs {
		runs[i].Argv, runs[i].Hosts = nil, nil
	}
	return runs, nil
}

// redactRun removes what only executors may see: the arguments, which may hold
// sensitive values, and the command output, which may too.
func redactRun(run *domain.Run) {
	run.Argv = nil
	for i := range run.Hosts {
		run.Hosts[i].Output = ""
	}
}

func (s *runService) Cancel(ctx context.Context, principal domain.Principal, id int64) (*domain.Run, error) {
	if ctx == nil {
		return nil, errors.New("run context is required")
	}
	if err := authorizeRunExecute(principal); err != nil {
		return nil, err
	}
	if err := s.runs.RequestCancel(ctx, principal.WorkspaceID, id); err != nil {
		return nil, err
	}
	return s.runs.GetRun(ctx, principal.WorkspaceID, id)
}

func (s *runService) Recover(ctx context.Context) (int, error) {
	now := time.Now().UTC()
	count, err := s.runs.InterruptStaleRuns(ctx, now.Add(-s.staleAfter), now)
	if err != nil {
		return 0, fmt.Errorf("recover interrupted runs: %w", err)
	}
	if count > 0 {
		slog.Warn("marked abandoned runs as interrupted", "runs", count)
	}
	return count, nil
}

// sweep periodically interrupts runs abandoned by a crashed process, including
// ones started by another instance.
func (s *runService) sweep() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.staleAfter)
	defer ticker.Stop()
	for {
		select {
		case <-s.baseCtx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(s.baseCtx, persistTimeout)
			if _, err := s.Recover(ctx); err != nil && s.baseCtx.Err() == nil {
				slog.Error("run recovery sweep failed", "error", err)
			}
			cancel()
		}
	}
}

func (s *runService) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.baseCancel()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("stop run executors: %w", ctx.Err())
	}
}

// persistContext returns a context for recording results that survives the
// cancellation of the run itself, and attributes any audit event to creator.
func persistContext(actor domain.AuditActor) (context.Context, context.CancelFunc) {
	return context.WithTimeout(domain.WithAuditActor(context.Background(), actor), persistTimeout)
}

func (s *runService) execute(run *domain.Run, creator domain.AuditActor) {
	defer s.end()
	runCtx, cancelRun := context.WithCancel(s.baseCtx)
	defer cancelRun()

	pctx, cancel := persistContext(creator)
	err := s.runs.StartRun(pctx, run.ID, s.instance, time.Now().UTC())
	cancel()
	if err != nil {
		slog.Error("cannot start run", "run", run.ID, "error", err)
		return
	}

	var userCancelled atomic.Bool
	heartbeatDone := make(chan struct{})
	go s.heartbeatLoop(runCtx, cancelRun, run.ID, &userCancelled, heartbeatDone)

	command := domain.QuoteArgv(run.Argv)
	timeout := time.Duration(run.TimeoutSeconds) * time.Second
	workers := run.Concurrency
	if workers > len(run.Hosts) {
		workers = len(run.Hosts)
	}

	var mu sync.Mutex
	outcomes := make(map[string]domain.RunHostStatus, len(run.Hosts))
	record := func(hostID string, status domain.RunHostStatus) {
		mu.Lock()
		outcomes[hostID] = status
		mu.Unlock()
	}

	jobs := make(chan domain.RunHost)
	var workerWG sync.WaitGroup
	for i := 0; i < workers; i++ {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			for host := range jobs {
				record(host.HostID, s.runHost(runCtx, run, host, command, timeout, creator))
			}
		}()
	}
feed:
	for _, host := range run.Hosts {
		select {
		case jobs <- host:
		case <-runCtx.Done():
			break feed
		}
	}
	close(jobs)
	workerWG.Wait()
	cancelRun()
	<-heartbeatDone

	// Hosts that were never started because the run stopped early.
	shuttingDown := s.baseCtx.Err() != nil
	for _, host := range run.Hosts {
		mu.Lock()
		_, done := outcomes[host.HostID]
		mu.Unlock()
		if done {
			continue
		}
		status, message := domain.RunHostCancelled, "the run was cancelled before this host was started"
		if shuttingDown && !userCancelled.Load() {
			status, message = domain.RunHostInterrupted, "the server stopped before this host was started"
		}
		pctx, cancel := persistContext(creator)
		if err := s.runs.UpdateRunHost(pctx, run.ID, host.HostID, domain.RunHostUpdate{Status: status, Error: message}); err != nil {
			slog.Error("cannot record unstarted host", "run", run.ID, "host", host.HostID, "error", err)
		}
		cancel()
		record(host.HostID, status)
	}

	// A run is cancelled or interrupted only if that actually cost it a host; a
	// request that arrived after every host had finished does not rewrite the
	// outcome.
	statuses := make([]domain.RunHostStatus, 0, len(run.Hosts))
	anyCancelled, anyInterrupted := false, false
	for _, host := range run.Hosts {
		status := outcomes[host.HostID]
		statuses = append(statuses, status)
		anyCancelled = anyCancelled || status == domain.RunHostCancelled
		anyInterrupted = anyInterrupted || status == domain.RunHostInterrupted
	}
	final := domain.AggregateRunStatus(statuses, userCancelled.Load() && anyCancelled, anyInterrupted)
	if !final.Final() {
		final = domain.RunInterrupted
	}
	pctx, cancel = persistContext(creator)
	defer cancel()
	if err := s.runs.FinishRun(pctx, run.ID, final, time.Now().UTC()); err != nil {
		slog.Error("cannot record the final run status", "run", run.ID, "status", final, "error", err)
	}
}

func (s *runService) heartbeatLoop(ctx context.Context, cancelRun context.CancelFunc, id int64, userCancelled *atomic.Bool, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(s.heartbeatInterval)
	defer ticker.Stop()
	for {
		hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
		cancelRequested, err := s.runs.Heartbeat(hctx, id, time.Now().UTC())
		cancel()
		switch {
		case errors.Is(err, domain.ErrRunFinished):
			cancelRun() // finalized elsewhere (recovery); stop working on it
			return
		case err != nil:
			slog.Warn("run heartbeat failed", "run", id, "error", err)
		case cancelRequested:
			userCancelled.Store(true)
			cancelRun()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runHost executes the command on one host and records the outcome.
func (s *runService) runHost(ctx context.Context, run *domain.Run, target domain.RunHost, command string,
	timeout time.Duration, creator domain.AuditActor) domain.RunHostStatus {
	finish := func(update domain.RunHostUpdate) domain.RunHostStatus {
		finished := time.Now().UTC()
		update.FinishedAt = &finished
		pctx, cancel := persistContext(creator)
		defer cancel()
		if err := s.runs.UpdateRunHost(pctx, run.ID, target.HostID, update); err != nil {
			slog.Error("cannot record host result", "run", run.ID, "host", target.HostID, "error", err)
		}
		return update.Status
	}
	stopped := func() domain.RunHostUpdate {
		if s.baseCtx.Err() != nil {
			return domain.RunHostUpdate{Status: domain.RunHostInterrupted, Error: "the server stopped; the command may or may not have run"}
		}
		return domain.RunHostUpdate{Status: domain.RunHostCancelled, Error: "the run was cancelled; the command may or may not have run"}
	}
	if ctx.Err() != nil {
		return finish(domain.RunHostUpdate{Status: stopped().Status, Error: "the run stopped before this host was started; no command was sent to it"})
	}

	started := time.Now().UTC()
	pctx, cancel := persistContext(creator)
	err := s.runs.UpdateRunHost(pctx, run.ID, target.HostID, domain.RunHostUpdate{Status: domain.RunHostRunning, StartedAt: &started})
	cancel()
	if err != nil {
		slog.Error("cannot mark host running", "run", run.ID, "host", target.HostID, "error", err)
	}

	id, err := strconv.Atoi(target.HostID)
	if err != nil {
		return finish(domain.RunHostUpdate{Status: domain.RunHostFailed, Error: "invalid host ID"})
	}
	lookup, cancel := persistContext(creator)
	host, err := s.hosts.GetHostByIDInWorkspace(lookup, run.WorkspaceID, id)
	cancel()
	switch {
	case errors.Is(err, domain.ErrHostNotFound):
		return finish(domain.RunHostUpdate{Status: domain.RunHostFailed, Error: "the host no longer exists"})
	case err != nil:
		slog.Error("cannot load host for run", "run", run.ID, "host", target.HostID, "error", err)
		return finish(domain.RunHostUpdate{Status: domain.RunHostFailed, Error: "the host could not be loaded"})
	}
	// The run is pinned to what was approved: if the host was re-pointed since
	// submission, do not run the command somewhere else.
	if host.SSH == nil || host.IPAddress != target.Address || host.SSH.Port != target.Port {
		return finish(domain.RunHostUpdate{Status: domain.RunHostFailed,
			Error: "the host's connection details changed after the run was submitted; submit the run again"})
	}
	if len(run.Argv) == 0 || s.executionPolicy().Check(run.Argv[0]) != nil {
		return finish(domain.RunHostUpdate{Status: domain.RunHostFailed, Error: "operation is no longer permitted by current runtime policy"})
	}
	policy := s.targetPolicy()
	params, err := dialParamsForHost(host, policy.DialContext)
	if err != nil {
		return finish(domain.RunHostUpdate{Status: domain.RunHostFailed, Error: sshFailureMessage(err, "the host has no usable SSH credentials")})
	}

	hostCtx, cancelHost := context.WithTimeout(ctx, timeout)
	defer cancelHost()
	release, err := s.acquireSSH(hostCtx)
	if err != nil {
		if ctx.Err() != nil {
			return finish(stopped())
		}
		return finish(domain.RunHostUpdate{Status: domain.RunHostTimedOut, Error: "no SSH capacity became available before the timeout; the command was not started"})
	}
	output, runErr := s.runner.Run(hostCtx, params, command)
	release()

	update := domain.RunHostUpdate{Output: domain.SanitizeOutput(output, domain.MaxRunHostOutputBytes)}
	switch runErr {
	case nil:
		zero := 0
		update.Status, update.ExitCode = domain.RunHostSucceeded, &zero

	default:
		if code, ok := sshclient.ExitStatus(runErr); ok {
			update.Status, update.ExitCode = domain.RunHostFailed, &code
			update.Error = fmt.Sprintf("the command exited with status %d", code)
		} else if ctx.Err() != nil {
			stop := stopped()
			update.Status, update.Error = stop.Status, stop.Error
		} else if errors.Is(hostCtx.Err(), context.DeadlineExceeded) ||
			errors.Is(runErr, context.DeadlineExceeded) {
			update.Status = domain.RunHostTimedOut
			update.Error = fmt.Sprintf(
				"the command did not finish within %d seconds; it may still be running on the host",
				run.TimeoutSeconds,
			)
		} else {
			update.Status = domain.RunHostFailed
			update.Error = sshFailureMessage(runErr, "execution failed")
		}
	}
	return finish(update)
}

// acquireSSH waits for an SSH slot until ctx ends. Runs queue for capacity
// rather than failing, but never longer than the per-host timeout.
func (s *runService) acquireSSH(ctx context.Context) (func(), error) {
	for {
		release, err := s.sshLimit.Acquire(ctx)
		if !errors.Is(err, domain.ErrServiceBusy) {
			return release, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sshSlotPoll):
		}
	}
}

// dialParamsForHost builds SSH parameters from a stored host.
func dialParamsForHost(host *domain.Host, dialer func(ctx context.Context, network, address string) (net.Conn, error)) (sshclient.DialParams, error) {
	var secret string
	switch host.SSH.AuthType.Method {
	case domain.AuthTypePrivateKey:
		secret = host.SSH.AuthType.PrivateKey
	case domain.AuthTypePassword:
		secret = host.SSH.AuthType.Password
	default:
		return sshclient.DialParams{}, fmt.Errorf("%w: host has no supported SSH authentication method", domain.ErrInvalidInput)
	}
	return sshclient.DialParams{
		IP:                  host.IPAddress,
		Port:                host.SSH.Port,
		Username:            host.SSH.Username,
		AuthMethod:          host.SSH.AuthType.Method,
		SecretData:          secret,
		Passphrase:          host.SSH.AuthType.Passphrase,
		ExpectedFingerprint: host.SSH.AuthType.ExpectedFingerprint,
		Dialer:              dialer,
	}, nil
}
