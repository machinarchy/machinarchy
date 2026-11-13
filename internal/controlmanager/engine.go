// Package controlmanager runs the level-triggered, edge-driven resource
// reconciliation pipeline. Imperative Operations use a separate worker path.
package controlmanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/eventing"
	"golang.org/x/time/rate"
)

var (
	ErrDuplicateController      = errors.New("a controller is already registered for this resource kind")
	ErrNoControllers            = errors.New("at least one controller must be registered before startup")
	ErrHostConnectorUnavailable = errors.New("no Host convergence adapter is configured")
)

type ReconcileResult struct{ RequeueAfter time.Duration }

type Controller interface {
	Name() string
	Kind() ResourceKind
	Reconcile(context.Context, ResourceKey) (ReconcileResult, error)
}

type Config struct {
	Workers             int
	QueueCapacity       int
	RetryBaseDelay      time.Duration
	RetryMaxDelay       time.Duration
	ResyncInterval      time.Duration
	OutboxPollInterval  time.Duration
	OutboxLeaseDuration time.Duration
	ResourceLeaseTTL    time.Duration
	ReconcileTimeout    time.Duration
	ShutdownTimeout     time.Duration
	PageSize            int
	RateLimitPerSecond  rate.Limit
	RateBurst           int
}

func DefaultConfig() Config {
	return Config{Workers: 4, QueueCapacity: 1024, RetryBaseDelay: time.Second, RetryMaxDelay: 5 * time.Minute, ResyncInterval: 5 * time.Minute,
		OutboxPollInterval: 250 * time.Millisecond, OutboxLeaseDuration: 30 * time.Second,
		ResourceLeaseTTL: 2 * time.Minute, ReconcileTimeout: 30 * time.Second,
		ShutdownTimeout: 30 * time.Second, PageSize: 200, RateLimitPerSecond: 20, RateBurst: 20}
}

type Dependencies struct {
	Hosts    resource.HostRepository
	Profiles interface {
		ListHostsByAccessProfile(context.Context, int, resource.ID) ([]resource.HostKey, error)
	}
	Outbox eventing.OutboxStore
	Leases resource.LeaseStore // nil for the SQLite single-manager profile
	Logger *slog.Logger
}

type Engine struct {
	deps           Dependencies
	cfg            Config
	queue          *ResourceQueue
	limiter        *rate.Limiter
	controllers    map[ResourceKind]Controller
	ownerID        string
	mu             sync.Mutex
	resyncLease    resource.Lease
	hasResyncLease bool
	started        bool
	ready          atomic.Bool
	initialScan    atomic.Bool
}

func NewEngine(deps Dependencies, cfg Config) (*Engine, error) {
	if deps.Hosts == nil || deps.Outbox == nil {
		return nil, errors.New("control manager requires Host and outbox repositories")
	}
	defaults := DefaultConfig()
	if cfg.Workers <= 0 {
		cfg.Workers = defaults.Workers
	}
	if cfg.QueueCapacity <= 0 {
		cfg.QueueCapacity = defaults.QueueCapacity
	}
	if cfg.RetryBaseDelay <= 0 {
		cfg.RetryBaseDelay = defaults.RetryBaseDelay
	}
	if cfg.RetryMaxDelay < cfg.RetryBaseDelay {
		cfg.RetryMaxDelay = defaults.RetryMaxDelay
	}
	if cfg.ResyncInterval <= 0 {
		cfg.ResyncInterval = defaults.ResyncInterval
	}
	if cfg.OutboxPollInterval <= 0 {
		cfg.OutboxPollInterval = defaults.OutboxPollInterval
	}
	if cfg.OutboxLeaseDuration <= 0 {
		cfg.OutboxLeaseDuration = defaults.OutboxLeaseDuration
	}
	if cfg.ResourceLeaseTTL <= 0 {
		cfg.ResourceLeaseTTL = defaults.ResourceLeaseTTL
	}
	if cfg.ReconcileTimeout <= 0 {
		cfg.ReconcileTimeout = defaults.ReconcileTimeout
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = defaults.ShutdownTimeout
	}
	if cfg.PageSize <= 0 || cfg.PageSize > 500 {
		cfg.PageSize = defaults.PageSize
	}
	if cfg.RateLimitPerSecond <= 0 {
		cfg.RateLimitPerSecond = defaults.RateLimitPerSecond
	}
	if cfg.RateBurst <= 0 {
		cfg.RateBurst = defaults.RateBurst
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	id, err := resource.NewResourceID()
	if err != nil {
		return nil, fmt.Errorf("generate Control Manager instance ID: %w", err)
	}
	return &Engine{deps: deps, cfg: cfg, limiter: rate.NewLimiter(cfg.RateLimitPerSecond, cfg.RateBurst),
		controllers: make(map[ResourceKind]Controller), ownerID: string(id)}, nil
}

func (e *Engine) Register(controller Controller) error {
	if e == nil || controller == nil || controller.Kind() == "" || controller.Name() == "" {
		return errors.New("valid controller is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return errors.New("controllers cannot be registered after startup")
	}
	if _, ok := e.controllers[controller.Kind()]; ok {
		return ErrDuplicateController
	}
	e.controllers[controller.Kind()] = controller
	return nil
}

func (e *Engine) ReadinessName() string { return "control-manager" }
func (e *Engine) CheckReadiness(ctx context.Context) error {
	if ctx == nil {
		return errors.New("readiness context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if e == nil || !e.ready.Load() || !e.initialScan.Load() {
		return errors.New("control manager recovery scan is incomplete")
	}
	return nil
}

// Start owns the bounded dispatcher, scanner, and worker lifecycles until ctx
// is canceled. Shutdown stops admissions first and drains queued/current work
// up to ShutdownTimeout, then cancels remaining reconcile contexts.
func (e *Engine) Start(ctx context.Context) error {
	if e == nil || ctx == nil {
		return errors.New("control manager and context are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return errors.New("control manager already started")
	}
	if len(e.controllers) == 0 {
		e.mu.Unlock()
		return ErrNoControllers
	}
	e.started = true
	e.queue = NewResourceQueue(e.cfg.QueueCapacity, e.cfg.RetryBaseDelay, e.cfg.RetryMaxDelay)
	e.mu.Unlock()

	admissionCtx, stopAdmission := context.WithCancel(ctx)
	workCtx, cancelWork := context.WithCancel(context.Background())
	var background sync.WaitGroup
	var workers sync.WaitGroup
	for i := 0; i < e.cfg.Workers; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); e.worker(workCtx) }()
	}
	background.Add(1)
	go func() { defer background.Done(); e.dispatchLoop(admissionCtx) }()

	if err := e.resync(admissionCtx); err != nil {
		e.deps.Logger.Error("Control Manager startup resync failed", "error_class", errorClass(err))
		stopAdmission()
		e.queue.Shutdown()
		cancelWork()
		workers.Wait()
		background.Wait()
		e.releaseResyncLease()
		return fmt.Errorf("startup resync: %w", err)
	}
	e.initialScan.Store(true)
	e.ready.Store(true)
	background.Add(1)
	go func() { defer background.Done(); e.periodicResync(admissionCtx) }()
	<-ctx.Done()
	e.ready.Store(false)
	stopAdmission()
	e.queue.Shutdown()
	background.Wait()

	finished := make(chan struct{})
	go func() { workers.Wait(); close(finished) }()
	drain := e.cfg.ShutdownTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < drain {
			drain = remaining
		}
	}
	timer := time.NewTimer(drain)
	defer timer.Stop()
	select {
	case <-finished:
	case <-timer.C:
		e.deps.Logger.Warn("Control Manager drain deadline reached; canceling active reconciliations")
		cancelWork()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			e.deps.Logger.Error("Control Manager workers did not stop after cancellation")
		}
	}
	cancelWork()
	e.releaseResyncLease()
	return nil
}

func (e *Engine) dispatchLoop(ctx context.Context) {
	ticker := time.NewTicker(e.cfg.OutboxPollInterval)
	defer ticker.Stop()
	nextPrune := time.Now().Add(24 * time.Hour)
	for {
		if err := e.dispatchBatch(ctx); err != nil && !errors.Is(err, context.Canceled) {
			e.deps.Logger.Warn("outbox dispatch failed", "error_class", errorClass(err))
		}
		if time.Now().After(nextPrune) {
			if _, err := e.deps.Outbox.Prune(ctx, time.Now()); err != nil {
				e.deps.Logger.Warn("outbox retention pass failed", "error_class", errorClass(err))
			}
			nextPrune = time.Now().Add(24 * time.Hour)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (e *Engine) dispatchBatch(ctx context.Context) error {
	claimed, err := e.deps.Outbox.Claim(ctx, eventing.ConsumerControlManager, e.ownerID, time.Now().UTC(), e.cfg.OutboxLeaseDuration, 32)
	if err != nil {
		return err
	}
	for _, delivery := range claimed {
		if err := ctx.Err(); err != nil {
			//nolint:errcheck // Best-effort shutdown retry; the delivery can be reclaimed after its lease expires.
			_ = e.deps.Outbox.Retry(context.Background(), eventing.ConsumerControlManager, e.ownerID, delivery.Envelope.ID, time.Now().Add(time.Second), "dispatcher_stopping")
			continue
		}
		envelope := delivery.Envelope
		if err := envelope.Validate(); err != nil {
			if quarantineErr := e.deps.Outbox.Quarantine(ctx, eventing.ConsumerControlManager, e.ownerID, envelope.ID, time.Now(), "unsupported_or_invalid_event"); quarantineErr != nil {
				return quarantineErr
			}
			continue
		}
		if envelope.Type == eventing.AccessProfileChanged {
			if e.deps.Profiles == nil {
				if retryErr := e.deps.Outbox.Retry(ctx, eventing.ConsumerControlManager, e.ownerID, envelope.ID, time.Now().Add(outboxRetryDelay(delivery.Attempt)), "profile_repository_unavailable"); retryErr != nil {
					return retryErr
				}
				continue
			}
			profileID := envelope.Subject.ID
			hosts, listErr := e.deps.Profiles.ListHostsByAccessProfile(ctx, envelope.WorkspaceID, profileID)
			if listErr != nil {
				if retryErr := e.deps.Outbox.Retry(ctx, eventing.ConsumerControlManager, e.ownerID, envelope.ID, time.Now().Add(outboxRetryDelay(delivery.Attempt)), "profile_host_lookup_failed"); retryErr != nil {
					return retryErr
				}
				continue
			}
			backpressured := false
			for _, host := range hosts {
				admission := e.queue.Add(ResourceKey{Kind: HostResourceKind, WorkspaceID: host.WorkspaceID, ID: string(host.ID)})
				if admission == QueueBackpressured || admission == QueueShuttingDown {
					backpressured = true
					break
				}
			}
			if backpressured {
				if retryErr := e.deps.Outbox.Retry(ctx, eventing.ConsumerControlManager, e.ownerID, envelope.ID, time.Now().Add(outboxRetryDelay(delivery.Attempt)), "profile_fanout_backpressured"); retryErr != nil {
					return retryErr
				}
				continue
			}
			if err := e.deps.Outbox.Acknowledge(ctx, eventing.ConsumerControlManager, e.ownerID, envelope.ID, time.Now()); err != nil {
				return err
			}
			continue
		}
		key := ResourceKey{Kind: ResourceKind(envelope.Subject.Kind), WorkspaceID: envelope.WorkspaceID, ID: string(envelope.Subject.ID)}
		if _, ok := e.controllers[key.Kind]; !ok {
			if quarantineErr := e.deps.Outbox.Quarantine(ctx, eventing.ConsumerControlManager, e.ownerID, envelope.ID, time.Now(), "no_registered_controller"); quarantineErr != nil {
				return quarantineErr
			}
			continue
		}
		admission := e.queue.Add(key)
		switch admission {
		case QueueAccepted, QueueCoalesced:
			if err := e.deps.Outbox.Acknowledge(ctx, eventing.ConsumerControlManager, e.ownerID, envelope.ID, time.Now()); err != nil {
				return err
			}
		case QueueBackpressured:
			if err := e.deps.Outbox.Retry(ctx, eventing.ConsumerControlManager, e.ownerID, envelope.ID, time.Now().Add(outboxRetryDelay(delivery.Attempt)), "queue_backpressured"); err != nil {
				return err
			}
		case QueueShuttingDown:
			if err := e.deps.Outbox.Retry(ctx, eventing.ConsumerControlManager, e.ownerID, envelope.ID, time.Now().Add(time.Second), "dispatcher_stopping"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) periodicResync(ctx context.Context) {
	for {
		delay := jitterInterval(e.cfg.ResyncInterval)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := e.resync(ctx); err != nil && !errors.Is(err, context.Canceled) {
			e.deps.Logger.Warn("periodic Control Manager resync failed", "error_class", errorClass(err))
		}
	}
}

func (e *Engine) resync(ctx context.Context) error {
	var scanLease resource.Lease
	if e.deps.Leases != nil {
		var acquired bool
		var err error
		scanLease, acquired, err = e.deps.Leases.AcquireResourceLease(ctx, resource.Lease{
			Kind: "resync", WorkspaceID: 1, ID: "managed-hosts", Owner: e.ownerID,
		}, time.Now(), e.resyncLeaseTTL())
		if err != nil {
			return err
		}
		if !acquired {
			return nil
		} // Another PostgreSQL manager owns the full scan.
		e.mu.Lock()
		e.resyncLease, e.hasResyncLease = scanLease, true
		e.mu.Unlock()
	}
	cursor := resource.HostKeyCursor{Limit: e.cfg.PageSize}
	for {
		if e.deps.Leases != nil {
			renewed, ok, err := e.deps.Leases.RenewResourceLease(ctx, scanLease, time.Now(), e.resyncLeaseTTL())
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("resync leadership lease lost")
			}
			scanLease = renewed
			e.mu.Lock()
			e.resyncLease = renewed
			e.mu.Unlock()
		}
		page, err := e.deps.Hosts.ListHostKeys(ctx, cursor)
		if err != nil {
			return err
		}
		for _, host := range page {
			key := ResourceKey{Kind: HostResourceKind, WorkspaceID: host.WorkspaceID, ID: string(host.ID)}
			if _, ok := e.controllers[key.Kind]; !ok {
				continue
			}
			for {
				admission := e.queue.Add(key)
				if admission == QueueAccepted || admission == QueueCoalesced {
					break
				}
				if admission == QueueShuttingDown {
					return context.Canceled
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(25 * time.Millisecond):
				}
			}
			cursor.AfterWorkspaceID, cursor.AfterID = host.WorkspaceID, string(host.ID)
		}
		if len(page) < cursor.Limit {
			return nil
		}
	}
}

func (e *Engine) resyncLeaseTTL() time.Duration {
	ttl := 2 * e.cfg.ResyncInterval
	if ttl < 2*time.Minute {
		ttl = 2 * time.Minute
	}
	if ttl > time.Hour {
		ttl = time.Hour
	}
	return ttl
}

func (e *Engine) releaseResyncLease() {
	if e.deps.Leases == nil {
		return
	}
	e.mu.Lock()
	lease, ok := e.resyncLease, e.hasResyncLease
	e.hasResyncLease = false
	e.mu.Unlock()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := e.deps.Leases.ReleaseResourceLease(ctx, lease); err != nil {
		e.deps.Logger.Warn("resync lease release failed", "error_class", errorClass(err))
	}
}

func (e *Engine) worker(ctx context.Context) {
	for {
		key, ok := e.queue.Get(ctx)
		if !ok {
			return
		}
		controller := e.controllers[key.Kind]
		if controller == nil {
			e.queue.Forget(key)
			e.queue.Done(key)
			continue
		}
		result, err := e.reconcile(ctx, controller, key)
		if err != nil {
			e.deps.Logger.Warn("resource reconciliation failed", "controller", controller.Name(), "error_class", errorClass(err))
			if !isPermanent(err) {
				_ = e.queue.AddRateLimited(key)
			} else {
				e.queue.Forget(key)
			}
		} else {
			e.queue.Forget(key)
			if result.RequeueAfter > 0 {
				_ = e.queue.AddAfter(key, result.RequeueAfter)
			}
		}
		e.queue.Done(key)
	}
}

func (e *Engine) reconcile(parent context.Context, controller Controller, key ResourceKey) (result ReconcileResult, retErr error) {
	ctx, cancel := context.WithTimeout(parent, e.cfg.ReconcileTimeout)
	defer cancel()
	if err := e.limiter.Wait(ctx); err != nil {
		return result, err
	}
	var lease resource.Lease
	if e.deps.Leases != nil {
		var acquired bool
		lease, acquired, retErr = e.deps.Leases.AcquireResourceLease(ctx, resource.Lease{
			Kind: string(key.Kind), WorkspaceID: key.WorkspaceID, ID: key.ID, Owner: e.ownerID,
		}, time.Now(), e.cfg.ResourceLeaseTTL)
		if retErr != nil {
			return result, retErr
		}
		if !acquired {
			_ = e.queue.AddAfter(key, time.Second)
			return result, nil
		}
		ctx = resource.WithLeaseFence(ctx, resource.LeaseFence{Kind: lease.Kind, WorkspaceID: lease.WorkspaceID, ID: lease.ID, Token: lease.Token})
		leaseCtx, stopLease := context.WithCancel(ctx)
		ctx = leaseCtx
		lost := make(chan struct{})
		var renewer sync.WaitGroup
		renewer.Add(1)
		go func() {
			defer renewer.Done()
			ticker := time.NewTicker(e.cfg.ResourceLeaseTTL / 3)
			defer ticker.Stop()
			for {
				select {
				case <-leaseCtx.Done():
					return
				case <-ticker.C:
					_, ok, err := e.deps.Leases.RenewResourceLease(leaseCtx, lease, time.Now(), e.cfg.ResourceLeaseTTL)
					if err != nil || !ok {
						close(lost)
						stopLease()
						return
					}
				}
			}
		}()
		defer func() {
			stopLease()
			renewer.Wait()
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 2*time.Second)
			if err := e.deps.Leases.ReleaseResourceLease(releaseCtx, lease); err != nil && !errors.Is(err, context.Canceled) {
				e.deps.Logger.Warn("resource lease release failed", "error_class", errorClass(err))
			}
			releaseCancel()
			select {
			case <-lost:
				retErr = errors.Join(retErr, errors.New("resource lease lost"))
			default:
			}
		}()
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			retErr = errors.New("controller panic")
		}
	}()
	return controller.Reconcile(ctx, key)
}

func outboxRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second
	for i := 1; i < attempt && delay < 5*time.Minute; i++ {
		delay *= 2
	}
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	delay = time.Duration(float64(delay) * (0.8 + rand.Float64()*0.4)) // #nosec G404 -- non-security retry jitter
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	return delay
}

func jitterInterval(interval time.Duration) time.Duration {
	return time.Duration(float64(interval) * (0.9 + rand.Float64()*0.2)) // #nosec G404 -- non-security retry jitter
}

type permanentError struct{ error }

func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}
func isPermanent(err error) bool { var target permanentError; return errors.As(err, &target) }
func errorClass(err error) string {
	if err == nil {
		return "none"
	}
	if isPermanent(err) {
		return "permanent"
	}
	return "transient"
}
