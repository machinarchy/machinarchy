// Package bootstrap assembles the infrastructure adapters shared by Machinarchy
// process entry points.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/machinarchy/machinarchy/internal/broker"
	"github.com/machinarchy/machinarchy/internal/config"
	"github.com/machinarchy/machinarchy/internal/controlmanager"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/hostaccess"
	"github.com/machinarchy/machinarchy/internal/operationworker"
	"github.com/machinarchy/machinarchy/internal/runtimeconfig"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/service"
	"github.com/machinarchy/machinarchy/internal/store"
	httpserver "github.com/machinarchy/machinarchy/internal/transport/http"
	httpv1 "github.com/machinarchy/machinarchy/internal/transport/http/v1"
	"github.com/machinarchy/machinarchy/pkg/crypto"
	"github.com/machinarchy/machinarchy/pkg/netpolicy"
	"golang.org/x/time/rate"
)

// Runtime owns the process-local database and event broker connections.
// Close it only after all servers and workers using it have stopped.
type Runtime struct {
	stores           *store.Stores
	eventBroker      *broker.EventBroker
	runManager       service.RunManager
	configuration    *runtimeconfig.Service
	snapshots        *runtimeconfig.SnapshotManager
	secrets          *secrets.Service
	operationSecrets *secrets.Service
	configCancel     context.CancelFunc
	configWG         sync.WaitGroup
}

// Open initializes the shared persistence and event infrastructure.
func Open(ctx context.Context, cfg config.Config) (*Runtime, error) {
	return open(ctx, cfg, true, "application")
}

// OpenControlManager initializes only database-backed control-loop
// infrastructure. It does not require or connect to NATS.
func OpenControlManager(ctx context.Context, cfg config.Config) (*Runtime, error) {
	return open(ctx, cfg, false, "control-manager")
}

func open(ctx context.Context, cfg config.Config, connectLegacyBroker bool, role string) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("runtime context is required")
	}
	if ctx.Done() == nil {
		return nil, errors.New("runtime context must be cancellable")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("runtime startup canceled: %w", err)
	}

	stores, err := store.Open(ctx, store.Config{
		Engine: string(cfg.Database.Engine), SQLitePath: cfg.Database.Path,
		PostgresURL: cfg.Database.URL, MaxOpenConns: cfg.Database.MaxOpenConns,
		MaxIdleConns: cfg.Database.MaxIdleConns, ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
		ConnectTimeout: cfg.Database.ConnectTimeout, EncryptionSecret: cfg.EncryptionSecret,
	})
	if err != nil {
		return nil, fmt.Errorf("open datastore: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(fmt.Errorf("runtime startup canceled: %w", err),
			wrapCloseError("close datastore after canceled startup", stores.Close()))
	}

	var eventBroker *broker.EventBroker
	if connectLegacyBroker {
		eventBroker, err = broker.NewEventBroker(cfg.EventBroker.URL)
		if err != nil {
			closeErr := stores.Close()
			return nil, errors.Join(
				fmt.Errorf("connect legacy API event broker: %w", err),
				wrapCloseError("close datastore after broker startup failure", closeErr),
			)
		}
		if err := ctx.Err(); err != nil {
			eventBroker.Close()
			return nil, errors.Join(fmt.Errorf("runtime startup canceled: %w", err),
				wrapCloseError("close datastore after canceled startup", stores.Close()))
		}
	}

	runtime, err := newConfigurationRuntime(ctx, stores, cfg, role)
	if err != nil {
		if eventBroker != nil {
			eventBroker.Close()
		}
		return nil, errors.Join(err, wrapCloseError("close datastore after configuration startup failure", stores.Close()))
	}
	runtime.eventBroker = eventBroker
	return runtime, nil
}

func newConfigurationRuntime(ctx context.Context, stores *store.Stores, cfg config.Config, role string) (*Runtime, error) {
	if stores == nil || stores.Management == nil {
		return nil, errors.New("configuration repositories are unavailable")
	}
	processID, err := resource.NewResourceID()
	if err != nil {
		return nil, err
	}
	snapshots, err := runtimeconfig.NewSnapshotManager(stores.Management, string(processID), role, logLevelApplier{level: processLogLevel})
	if err != nil {
		return nil, err
	}
	configuration, err := runtimeconfig.NewService(stores.Management, snapshots)
	if err != nil {
		return nil, err
	}
	defaults := runtimeconfig.Defaults()
	defaults.Features.SignupEnabled = cfg.SignupEnabled
	if cfg.LogLevel != "" {
		defaults.Logging.Level = cfg.LogLevel
	}
	defaults.SSH.AllowPrivate = cfg.SSHTargetPolicy.AllowPrivate
	defaults.SSH.AllowLoopback = cfg.SSHTargetPolicy.AllowLoopback
	for _, prefix := range cfg.SSHTargetPolicy.Allow {
		defaults.SSH.AllowedCIDRs = append(defaults.SSH.AllowedCIDRs, prefix.String())
	}
	for _, prefix := range cfg.SSHTargetPolicy.Deny {
		defaults.SSH.DeniedCIDRs = append(defaults.SSH.DeniedCIDRs, prefix.String())
	}
	for _, port := range cfg.SSHTargetPolicy.AllowedPorts {
		defaults.SSH.AllowedPorts = append(defaults.SSH.AllowedPorts, int(port))
	}
	defaults.Operations.Enabled = cfg.Execution.Enabled
	for program := range cfg.Execution.Programs {
		defaults.Operations.AllowedPrograms = append(defaults.Operations.AllowedPrograms, program)
	}
	if cfg.Execution.AllowAny {
		defaults.Operations.AllowedPrograms = []string{"*"}
	}
	if err = configuration.Bootstrap(ctx, defaults); err != nil {
		return nil, fmt.Errorf("initialize runtime configuration: %w", err)
	}
	keyID := cfg.EncryptionKeyID
	if keyID == "" {
		keyID = "bootstrap-v1"
	}
	root, err := crypto.DeriveKey(cfg.EncryptionSecret, "machinarchy/secret-kek/"+keyID)
	if err != nil {
		return nil, fmt.Errorf("initialize Secret Store key provider: %w", err)
	}
	previousKeys := map[string][]byte{}
	for oldID, oldSecret := range cfg.EncryptionPreviousKeys {
		oldKey, e := crypto.DeriveKey(oldSecret, "machinarchy/secret-kek/"+oldID)
		if e != nil {
			return nil, fmt.Errorf("initialize previous Secret Store key %s: %w", oldID, e)
		}
		previousKeys[oldID] = oldKey
	}
	bindings := runtimeProfileBindings{profiles: stores.ResourceProfiles, hosts: stores.ResourceHosts, operations: stores.Operations}
	cmAuthorizer, err := secrets.NewProfileAuthorizer(bindings, secrets.WorkloadControlManager)
	if err != nil {
		return nil, fmt.Errorf("initialize Control Manager Secret Store authorization: %w", err)
	}
	operationAuthorizer, err := secrets.NewProfileAuthorizer(bindings, secrets.WorkloadOperationWorker)
	if err != nil {
		return nil, fmt.Errorf("initialize Operation worker Secret Store authorization: %w", err)
	}
	keyProvider := &secrets.StaticKeyProvider{KeyID: keyID, Key: root, Previous: previousKeys}
	secretService, err := secrets.New(stores.Management, keyProvider, cmAuthorizer)
	if err != nil {
		return nil, err
	}
	operationSecretService, err := secrets.New(stores.Management, keyProvider, operationAuthorizer)
	if err != nil {
		return nil, err
	}
	watchCtx, cancel := context.WithCancel(ctx)
	runtime := &Runtime{stores: stores, configuration: configuration, snapshots: snapshots, secrets: secretService,
		operationSecrets: operationSecretService, configCancel: cancel}
	runtime.configWG.Add(1)
	go func() {
		defer runtime.configWG.Done()
		if e := configuration.Watch(watchCtx, 30*time.Second); e != nil && watchCtx.Err() == nil {
			slog.Error("runtime configuration watcher stopped", "error_class", "watcher_failed")
		}
	}()
	return runtime, nil
}

type runtimeProfileBindings struct {
	profiles   resource.AccessProfileRepository
	hosts      resource.HostRepository
	operations resource.OperationRepository
}

func (b runtimeProfileBindings) GetAccessProfile(ctx context.Context, workspace int, id resource.ID) (*resource.AccessProfile, error) {
	return b.profiles.GetAccessProfile(ctx, workspace, id)
}
func (b runtimeProfileBindings) GetAccessProfileRevision(ctx context.Context, workspace int, id resource.ID, revision uint64) (*resource.AccessProfileRevision, error) {
	return b.profiles.GetAccessProfileRevision(ctx, workspace, id, revision)
}
func (b runtimeProfileBindings) GetHost(ctx context.Context, workspace int, id resource.ID) (*resource.Host, error) {
	return b.hosts.GetHost(ctx, workspace, id)
}
func (b runtimeProfileBindings) GetOperation(ctx context.Context, workspace int, id resource.ID) (*resource.Operation, error) {
	return b.operations.GetOperation(ctx, workspace, id)
}

// Close releases shared infrastructure. Stop servers and workers before calling it.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	var closeErrors []error
	if r.runManager != nil {
		// Let executing runs record their outcome before the database closes.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := r.runManager.Close(ctx); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close run executors: %w", err))
		}
		cancel()
	}
	if r.configCancel != nil {
		r.configCancel()
		r.configWG.Wait()
	}
	if r.eventBroker != nil {
		r.eventBroker.Close()
	}
	if r.stores != nil {
		if err := r.stores.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close datastore: %w", err))
		}
	}
	return errors.Join(closeErrors...)
}

// NewControlManager creates the background event processor over this runtime's
// host store and event connection.
func (r *Runtime) NewControlManager() (*controlmanager.Engine, error) {
	if r == nil || r.stores == nil || r.stores.ResourceHosts == nil || r.stores.Outbox == nil {
		return nil, errors.New("runtime is not initialized")
	}
	var leases = r.stores.ResourceLeases
	if leases != nil && r.stores.Engine() == store.EngineSQLite {
		leases = nil
	}
	engineConfig := controlmanager.DefaultConfig()
	if snap, ok := r.configuration.Snapshot(); ok {
		c := snap.Revision.Config
		engineConfig.Workers = c.ControlManager.Workers
		engineConfig.QueueCapacity = c.WorkQueue.Capacity
		engineConfig.RetryBaseDelay = time.Duration(c.WorkQueue.RetryBaseSeconds) * time.Second
		engineConfig.RetryMaxDelay = time.Duration(c.WorkQueue.RetryMaxSeconds) * time.Second
		engineConfig.ResyncInterval = time.Duration(c.ControlManager.ResyncSeconds) * time.Second
		engineConfig.ReconcileTimeout = time.Duration(c.Reconciliation.TimeoutSeconds) * time.Second
		engineConfig.RateLimitPerSecond = rate.Limit(c.WorkQueue.RatePerSecond)
	}
	engine, err := controlmanager.NewEngine(controlmanager.Dependencies{
		Hosts: r.stores.ResourceHosts, Profiles: r.stores.ResourceProfiles, Outbox: r.stores.Outbox, Leases: leases,
	}, engineConfig)
	if err != nil {
		return nil, err
	}
	hostResync := 5 * time.Minute
	if snap, ok := r.configuration.Snapshot(); ok {
		hostResync = time.Duration(snap.Revision.Config.Reconciliation.ManagedHostResyncSeconds) * time.Second
	}
	sshLimit := service.DefaultSSHConcurrency
	if snap, ok := r.configuration.Snapshot(); ok {
		sshLimit = snap.Revision.Config.SSH.MaxConcurrent
	}
	connector, err := hostaccess.NewSSHConnectorWithPolicyProvider(r.secrets, r.targetPolicyProvider(netpolicy.Default()), sshLimit,
		30*time.Second, secrets.WorkloadControlManager)
	if err != nil {
		return nil, fmt.Errorf("construct managed Host SSH connector: %w", err)
	}
	converger, err := hostaccess.NewHostReconciler(r.stores.ResourceProfiles, connector)
	if err != nil {
		return nil, fmt.Errorf("construct managed Host reconciler: %w", err)
	}
	hostController, err := controlmanager.NewHostControllerWithOperations(r.stores.ResourceHosts, converger, hostResync, r.stores.Operations)
	if err != nil {
		return nil, err
	}
	if err := engine.Register(hostController); err != nil {
		return nil, err
	}
	return engine, nil
}

// NewOperationWorker constructs the independently bounded imperative execution
// pool. It shares persistence with the Control Manager but not its queue.
func (r *Runtime) NewOperationWorker() (*operationworker.Worker, error) {
	if r == nil || r.stores == nil || r.stores.Operations == nil || r.stores.ResourceHosts == nil || r.stores.ResourceProfiles == nil || r.stores.Outbox == nil {
		return nil, errors.New("runtime is not initialized for Operation execution")
	}
	concurrency := service.DefaultSSHConcurrency
	workers := 4
	if snap, ok := r.configuration.Snapshot(); ok {
		concurrency = snap.Revision.Config.SSH.MaxConcurrent
		workers = snap.Revision.Config.ControlManager.Workers
	}
	connector, err := hostaccess.NewSSHConnectorWithPolicyProvider(r.operationSecrets, r.targetPolicyProvider(netpolicy.Default()),
		concurrency, 15*time.Minute, secrets.WorkloadOperationWorker)
	if err != nil {
		return nil, fmt.Errorf("construct Operation SSH connector: %w", err)
	}
	queue, ok := r.stores.Operations.(resource.OperationQueueRepository)
	if !ok {
		return nil, errors.New("operation repository does not support durable queue discovery")
	}
	recovery, ok := r.stores.Operations.(resource.OperationRecoveryRepository)
	if !ok {
		return nil, errors.New("operation repository does not support startup recovery")
	}
	operationLeases := r.stores.ResourceLeases
	if r.stores.Engine() == store.EngineSQLite {
		operationLeases = nil
	}
	return operationworker.New(operationworker.Dependencies{Operations: r.stores.Operations, Queue: queue, Hosts: r.stores.ResourceHosts,
		Profiles: r.stores.ResourceProfiles, Outbox: r.stores.Outbox, Connector: connector,
		ExecutionPolicy: r.executionPolicyProvider(domain.ExecutionPolicy{}), Logger: slog.Default(), Leases: operationLeases,
		Recovery: recovery}, workers)
}

// NewAPIServer constructs the API adapter. Additional readiness checks are
// included when the API and a worker run together in one process.
func (r *Runtime) NewAPIServer(
	ctx context.Context,
	cfg config.Config,
	additionalReadiness ...httpserver.ReadinessChecker,
) (*httpserver.Server, error) {
	if ctx == nil {
		return nil, errors.New("API startup context is required")
	}
	if r == nil || r.stores == nil || r.eventBroker == nil {
		return nil, errors.New("runtime is not initialized")
	}

	sshConcurrency := service.DefaultSSHConcurrency
	if snap, ok := r.configuration.Snapshot(); ok {
		sshConcurrency = snap.Revision.Config.SSH.MaxConcurrent
	}
	sshCapacity, err := service.NewSSHCapacity(sshConcurrency)
	if err != nil {
		return nil, fmt.Errorf("configure SSH concurrency limit: %w", err)
	}
	authService := service.NewAuthService(r.stores.Users, cfg.OperatorToken, 24*time.Hour)
	if err := r.ensureOwner(ctx, cfg); err != nil {
		return nil, err
	}

	hostService, err := service.NewHostServiceWithCapacity(r.stores.Hosts, r.eventBroker, sshCapacity,
		service.WithTargetPolicyProvider(r.targetPolicyProvider(cfg.SSHTargetPolicy)))
	if err != nil {
		return nil, fmt.Errorf("construct host service: %w", err)
	}
	metricsService, err := service.NewMetricsServiceWithCapacity(r.stores.Metrics, r.stores.Hosts, sshCapacity,
		service.WithTargetPolicyProvider(r.targetPolicyProvider(cfg.SSHTargetPolicy)))
	if err != nil {
		return nil, fmt.Errorf("construct metrics service: %w", err)
	}
	runManager, err := service.NewRunService(r.stores.Runs, r.stores.Hosts, r.stores.Environments, sshCapacity,
		cfg.Execution, service.WithTargetPolicyProvider(r.targetPolicyProvider(cfg.SSHTargetPolicy)), service.WithExecutionPolicyProvider(r.executionPolicyProvider(cfg.Execution)), service.WithMaxActiveRuns(runtimeRunWorkers(r.configuration)), service.WithDefaultRunTimeout(runtimeRunTimeout(r.configuration)))
	if err != nil {
		return nil, fmt.Errorf("construct run service: %w", err)
	}
	recoverCtx, cancelRecover := context.WithTimeout(ctx, 15*time.Second)
	_, err = runManager.Recover(recoverCtx)
	cancelRecover()
	if err != nil {
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 10*time.Second)
		closeErr := runManager.Close(closeCtx)
		cancelClose()
		return nil, errors.Join(err, wrapCloseError("close run manager after recovery failure", closeErr))
	}
	r.runManager = runManager
	managedResources, err := service.NewManagedResourceService(r.stores.ResourceHosts, r.stores.ResourceProfiles, r.stores.Operations,
		r.executionPolicyProvider(domain.ExecutionPolicy{}), r.secrets)
	if err != nil {
		return nil, fmt.Errorf("construct managed resource application service: %w", err)
	}
	repositories := httpv1.Repositories{
		Users: r.stores.Users, Metrics: r.stores.Metrics, Hosts: r.stores.Hosts,
		Jobs: r.stores.Jobs, Environments: r.stores.Environments, Access: r.stores.Access,
		Audit: r.stores.Audit, Configuration: r.configuration, Secrets: r.secrets, ManagedResources: managedResources,
		SignupEnabled: func() bool {
			if snapshot, ok := r.configuration.Snapshot(); ok {
				return snapshot.Revision.Config.Features.SignupEnabled
			}
			return cfg.SignupEnabled
		},
	}

	readiness := make([]httpserver.ReadinessChecker, 0, 1+len(additionalReadiness))
	readiness = append(readiness, r.eventBroker)
	readiness = append(readiness, additionalReadiness...)
	return httpserver.NewServer(cfg, authService, hostService, metricsService,
		sshCapacity, runManager, repositories, r.stores, readiness...)
}

// NewControlManagerOperationsServer creates the HTTP surface used for worker
// health probes and database/process metrics without registering API routes.
func (r *Runtime) NewControlManagerOperationsServer(
	engine *controlmanager.Engine,
) (*httpserver.Server, error) {
	if r == nil || r.stores == nil {
		return nil, errors.New("runtime is not initialized")
	}
	return httpserver.NewOperationsServer(r.stores, engine)
}

// ConfigureLogging installs the common structured JSON logger.
func ConfigureLogging(level string) {
	processLogLevel.Set(parseLogLevel(level))
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout,
		&slog.HandlerOptions{Level: processLogLevel})))
}

var processLogLevel = &slog.LevelVar{}

func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type logLevelApplier struct{ level *slog.LevelVar }

func (a logLevelApplier) Prepare(_ context.Context, s runtimeconfig.Snapshot) (runtimeconfig.PreparedApply, error) {
	if a.level == nil {
		return runtimeconfig.PreparedApply{}, errors.New("logger level adapter is unavailable")
	}
	next := parseLogLevel(s.Revision.Config.Logging.Level)
	previous := a.level.Level()
	return runtimeconfig.PreparedApply{Commit: func() error { a.level.Set(next); return nil }, Rollback: func() { a.level.Set(previous) }}, nil
}

func (r *Runtime) targetPolicyProvider(fallback netpolicy.Policy) func() netpolicy.Policy {
	return func() netpolicy.Policy {
		snapshot, ok := r.snapshots.Current()
		if !ok {
			return fallback
		}
		cfg := snapshot.Revision.Config.SSH
		allowed, err := netpolicy.ParsePrefixes(strings.Join(cfg.AllowedCIDRs, ","))
		if err != nil {
			slog.Warn("parse allowed CIDRs", "error", err)
		}
		denied, err := netpolicy.ParsePrefixes(strings.Join(cfg.DeniedCIDRs, ","))
		if err != nil {
			slog.Warn("parse denied CIDRs", "error", err)
		}
		ports := make([]uint16, 0, len(cfg.AllowedPorts))
		for _, port := range cfg.AllowedPorts {
			if port < 1 || port > int(^uint16(0)) {
				slog.Warn("invalid allowed port: must be between 1 and 65535",
					"port", port,
				)
				continue
			}

			ports = append(ports, uint16(port))
		}
		return netpolicy.Policy{AllowPrivate: cfg.AllowPrivate, AllowLoopback: cfg.AllowLoopback, Allow: allowed, Deny: denied, AllowedPorts: ports, DialTimeout: time.Duration(cfg.ConnectTimeoutSeconds) * time.Second}
	}
}
func (r *Runtime) executionPolicyProvider(fallback domain.ExecutionPolicy) func() domain.ExecutionPolicy {
	return func() domain.ExecutionPolicy {
		snapshot, ok := r.snapshots.Current()
		if !ok {
			return fallback
		}
		policy, err := domain.NewExecutionPolicy(snapshot.Revision.Config.Operations.Enabled, snapshot.Revision.Config.Operations.AllowedPrograms)
		if err != nil {
			return domain.ExecutionPolicy{}
		}
		return policy
	}
}

func runtimeRunWorkers(service *runtimeconfig.Service) int {
	if service != nil {
		if snapshot, ok := service.Snapshot(); ok {
			return snapshot.Revision.Config.Operations.Workers
		}
	}
	return 2
}
func runtimeRunTimeout(service *runtimeconfig.Service) int {
	if service != nil {
		if snapshot, ok := service.Snapshot(); ok {
			return snapshot.Revision.Config.Operations.TimeoutSeconds
		}
	}
	return domain.DefaultRunTimeout
}

func wrapCloseError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// ensureOwner creates the configured bootstrap owner when the default workspace
// has none. Without bootstrap credentials it only warns, because every
// self-registered account starts as a read-only member.
func (r *Runtime) ensureOwner(parent context.Context, cfg config.Config) error {
	if parent == nil {
		return errors.New("owner bootstrap context is required")
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()

	admin := cfg.BootstrapAdmin
	if admin.Password != "" {
		created, err := service.BootstrapOwner(ctx, r.stores.Users, admin.Username, admin.Email, admin.Password)
		if err != nil {
			return fmt.Errorf("bootstrap workspace owner: %w", err)
		}
		if created {
			slog.Info("bootstrap workspace owner created", "username", admin.Username)
		}
		return nil
	}
	owners, err := r.stores.Users.CountWorkspaceOwners(ctx, domain.DefaultWorkspaceID)
	if err != nil {
		return fmt.Errorf("check workspace owners: %w", err)
	}
	if owners == 0 {
		slog.Warn("no workspace owner exists; set MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD to create one")
	}
	return nil
}
