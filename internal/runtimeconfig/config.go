// Package runtimeconfig defines the persisted, global runtime configuration
// model and its application-level contracts.
package runtimeconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

const SchemaVersion = 1

var (
	ErrConflict        = errors.New("runtime configuration revision conflict")
	ErrInvalid         = errors.New("invalid runtime configuration")
	ErrNotFound        = errors.New("runtime configuration revision not found")
	ErrRestartRequired = errors.New("runtime configuration contains restart-required settings")
	ErrApplyPending    = errors.New("runtime configuration persisted but application is pending")
)

type ApplyClass string

const (
	HotReloadable           ApplyClass = "hot-reloadable"
	ReconciliationTriggered ApplyClass = "reconciliation-triggered"
	RestartRequired         ApplyClass = "restart-required"
)

// Config contains only non-secret values and SecretRef-like identifiers.
// Fields are materialized in every immutable revision.
type Config struct {
	SchemaVersion  int            `json:"schemaVersion"`
	ControlManager ControlManager `json:"controlManager"`
	WorkQueue      WorkQueue      `json:"workQueue"`
	Reconciliation Reconciliation `json:"reconciliation"`
	SSH            SSH            `json:"ssh"`
	Operations     Operations     `json:"operations"`
	Logging        Logging        `json:"logging"`
	Features       Features       `json:"features"`
}

type ControlManager struct {
	Workers       int `json:"workers"`
	ResyncSeconds int `json:"resyncSeconds"`
}
type WorkQueue struct {
	Capacity         int `json:"capacity"`
	RetryBaseSeconds int `json:"retryBaseSeconds"`
	RetryMaxSeconds  int `json:"retryMaxSeconds"`
	RatePerSecond    int `json:"ratePerSecond"`
}
type Reconciliation struct {
	TimeoutSeconds           int `json:"timeoutSeconds"`
	ManagedHostResyncSeconds int `json:"managedHostResyncSeconds"`
}
type SSH struct {
	ConnectTimeoutSeconds int      `json:"connectTimeoutSeconds"`
	MaxConcurrent         int      `json:"maxConcurrent"`
	AllowedCIDRs          []string `json:"allowedCIDRs"`
	DeniedCIDRs           []string `json:"deniedCIDRs"`
	AllowPrivate          bool     `json:"allowPrivate"`
	AllowLoopback         bool     `json:"allowLoopback"`
}
type Operations struct {
	Enabled         bool     `json:"enabled"`
	Workers         int      `json:"workers"`
	TimeoutSeconds  int      `json:"timeoutSeconds"`
	AllowedPrograms []string `json:"allowedPrograms"`
}
type Logging struct {
	Level string `json:"level"`
}
type Features struct {
	SignupEnabled bool `json:"signupEnabled"`
}

type Revision struct {
	Revision      uint64    `json:"revision"`
	SchemaVersion int       `json:"schemaVersion"`
	Config        Config    `json:"config"`
	Checksum      string    `json:"checksum"`
	CreatedAt     time.Time `json:"createdAt"`
	CreatedBy     int       `json:"createdBy,omitempty"`
	ChangeNote    string    `json:"changeNote,omitempty"`
}

type ProcessStatus struct {
	ProcessID         string     `json:"processId"`
	Role              string     `json:"role"`
	AppliedRevision   uint64     `json:"appliedRevision"`
	AttemptedRevision uint64     `json:"attemptedRevision"`
	LastSuccess       *time.Time `json:"lastSuccess,omitempty"`
	LastErrorClass    string     `json:"lastErrorClass,omitempty"`
	Heartbeat         time.Time  `json:"heartbeat"`
}

type Repository interface {
	Seed(context.Context, Config) (Revision, error)
	Current(context.Context) (Revision, error)
	GetRevision(context.Context, uint64) (Revision, error)
	ListRevisions(context.Context, int, uint64) ([]Revision, error)
	Publish(context.Context, uint64, Config, int, string) (Revision, error)
	RecordProcessStatus(context.Context, ProcessStatus) error
	ProcessStatuses(context.Context, time.Time) ([]ProcessStatus, error)
	ConfigChangedAfter(context.Context, uint64) (bool, error)
}

func Defaults() Config {
	return Config{SchemaVersion: SchemaVersion,
		ControlManager: ControlManager{Workers: 4, ResyncSeconds: 300},
		WorkQueue:      WorkQueue{Capacity: 1024, RetryBaseSeconds: 1, RetryMaxSeconds: 300, RatePerSecond: 20},
		Reconciliation: Reconciliation{TimeoutSeconds: 30, ManagedHostResyncSeconds: 300},
		SSH:            SSH{ConnectTimeoutSeconds: 10, MaxConcurrent: 32, AllowedCIDRs: []string{}, DeniedCIDRs: []string{}, AllowPrivate: true},
		Operations:     Operations{Enabled: false, Workers: 2, TimeoutSeconds: 300, AllowedPrograms: []string{}},
		Logging:        Logging{Level: "info"}, Features: Features{SignupEnabled: false}}
}

func (c Config) Validate() error {
	if c.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported schema version %d", ErrInvalid, c.SchemaVersion)
	}
	if c.ControlManager.Workers < 1 || c.ControlManager.Workers > 128 || c.ControlManager.ResyncSeconds < 10 || c.ControlManager.ResyncSeconds > 86400 {
		return fmt.Errorf("%w: Control Manager workers or resync interval out of bounds", ErrInvalid)
	}
	if c.WorkQueue.Capacity < 1 || c.WorkQueue.Capacity > 100000 || c.WorkQueue.RetryBaseSeconds < 1 || c.WorkQueue.RetryBaseSeconds > 3600 || c.WorkQueue.RetryMaxSeconds < c.WorkQueue.RetryBaseSeconds || c.WorkQueue.RetryMaxSeconds > 86400 || c.WorkQueue.RatePerSecond < 1 || c.WorkQueue.RatePerSecond > 10000 {
		return fmt.Errorf("%w: workqueue values out of bounds", ErrInvalid)
	}
	if c.Reconciliation.TimeoutSeconds < 1 || c.Reconciliation.TimeoutSeconds > 3600 || c.Reconciliation.ManagedHostResyncSeconds < 10 || c.Reconciliation.ManagedHostResyncSeconds > 86400 {
		return fmt.Errorf("%w: reconciliation values out of bounds", ErrInvalid)
	}
	if c.SSH.ConnectTimeoutSeconds < 1 || c.SSH.ConnectTimeoutSeconds > 300 || c.SSH.MaxConcurrent < 1 || c.SSH.MaxConcurrent > 10000 {
		return fmt.Errorf("%w: SSH values out of bounds", ErrInvalid)
	}
	if c.Operations.Workers < 1 || c.Operations.Workers > 256 || c.Operations.TimeoutSeconds < 1 || c.Operations.TimeoutSeconds > domain.MaxRunTimeout {
		return fmt.Errorf("%w: operation values out of bounds", ErrInvalid)
	}
	if c.Logging.Level != "debug" && c.Logging.Level != "info" && c.Logging.Level != "warn" && c.Logging.Level != "error" {
		return fmt.Errorf("%w: logging level must be debug, info, warn, or error", ErrInvalid)
	}
	for _, cidr := range append(append([]string{}, c.SSH.AllowedCIDRs...), c.SSH.DeniedCIDRs...) {
		if _, err := netip.ParsePrefix(strings.TrimSpace(cidr)); err != nil {
			return fmt.Errorf("%w: invalid SSH CIDR entry", ErrInvalid)
		}
	}
	for _, program := range c.Operations.AllowedPrograms {
		if strings.TrimSpace(program) == "" || strings.ContainsAny(program, " \t\r\n") {
			return fmt.Errorf("%w: operation program entries must be executable names", ErrInvalid)
		}
	}
	if _, err := domain.NewExecutionPolicy(c.Operations.Enabled, c.Operations.AllowedPrograms); err != nil {
		return fmt.Errorf("%w: operation policy is invalid", ErrInvalid)
	}
	return nil
}

func (c Config) Checksum() (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Classifications is the contract presented to API clients and operators.
// Fields requiring a component-level live adapter are not silently applied.
func Classifications() map[string]ApplyClass {
	return map[string]ApplyClass{
		"controlManager.workers": RestartRequired, "controlManager.resyncSeconds": RestartRequired,
		"workQueue.capacity": RestartRequired, "workQueue.retryBaseSeconds": RestartRequired, "workQueue.retryMaxSeconds": RestartRequired, "workQueue.ratePerSecond": RestartRequired,
		"reconciliation.timeoutSeconds": RestartRequired, "reconciliation.managedHostResyncSeconds": RestartRequired,
		"ssh.connectTimeoutSeconds": RestartRequired, "ssh.maxConcurrent": RestartRequired,
		"ssh.allowedCIDRs": HotReloadable, "ssh.deniedCIDRs": HotReloadable, "ssh.allowPrivate": HotReloadable, "ssh.allowLoopback": HotReloadable,
		"operations.enabled": HotReloadable, "operations.workers": RestartRequired, "operations.timeoutSeconds": RestartRequired, "operations.allowedPrograms": HotReloadable,
		"logging.level": HotReloadable, "features.signupEnabled": HotReloadable,
		"database.engine": RestartRequired, "database.path": RestartRequired, "database.url": RestartRequired,
		"listen.address": RestartRequired, "encryption.keyProvider": RestartRequired,
	}
}

type Snapshot struct {
	Revision  Revision
	AppliedAt time.Time
}
type PreparedApply struct {
	Commit   func() error
	Rollback func()
}
type Applier interface {
	Prepare(context.Context, Snapshot) (PreparedApply, error)
}

// SnapshotManager atomically publishes immutable snapshots after every
// registered participant has prepared successfully.
type SnapshotManager struct {
	current         atomic.Pointer[Snapshot]
	repo            Repository
	processID, role string
	appliers        []Applier
}

func NewSnapshotManager(repo Repository, processID, role string, appliers ...Applier) (*SnapshotManager, error) {
	if repo == nil || strings.TrimSpace(processID) == "" || strings.TrimSpace(role) == "" {
		return nil, errors.New("snapshot manager requires repository, process ID, and role")
	}
	return &SnapshotManager{repo: repo, processID: processID, role: role, appliers: append([]Applier(nil), appliers...)}, nil
}
func (m *SnapshotManager) Current() (Snapshot, bool) {
	if m == nil {
		return Snapshot{}, false
	}
	p := m.current.Load()
	if p == nil {
		return Snapshot{}, false
	}
	return cloneSnapshot(*p), true
}

func cloneSnapshot(s Snapshot) Snapshot {
	s.Revision.Config.SSH.AllowedCIDRs = append([]string(nil), s.Revision.Config.SSH.AllowedCIDRs...)
	s.Revision.Config.SSH.DeniedCIDRs = append([]string(nil), s.Revision.Config.SSH.DeniedCIDRs...)
	s.Revision.Config.Operations.AllowedPrograms = append([]string(nil), s.Revision.Config.Operations.AllowedPrograms...)
	return s
}
func (m *SnapshotManager) Reload(ctx context.Context) error {
	r, err := m.repo.Current(ctx)
	if err != nil {
		return err
	}
	if err = r.Config.Validate(); err != nil {
		return err
	}
	if old, ok := m.Current(); ok && old.Revision.Revision >= r.Revision {
		now := time.Now().UTC()
		return m.repo.RecordProcessStatus(ctx, ProcessStatus{ProcessID: m.processID, Role: m.role,
			AppliedRevision: old.Revision.Revision, AttemptedRevision: old.Revision.Revision, Heartbeat: now})
	}
	candidate := Snapshot{Revision: r, AppliedAt: time.Now().UTC()}
	prepared := make([]PreparedApply, 0, len(m.appliers))
	for _, a := range m.appliers {
		change, e := a.Prepare(ctx, cloneSnapshot(candidate))
		if e != nil {
			for i := len(prepared) - 1; i >= 0; i-- {
				if prepared[i].Rollback != nil {
					prepared[i].Rollback()
				}
			}
			m.record(ctx, r.Revision, "apply_failed")
			return e
		}
		prepared = append(prepared, change)
	}
	for i, change := range prepared {
		if change.Commit != nil {
			if e := change.Commit(); e != nil {
				for j := len(prepared) - 1; j >= 0; j-- {
					if prepared[j].Rollback != nil {
						prepared[j].Rollback()
					}
				}
				m.record(ctx, r.Revision, "apply_failed")
				return e
			}
		}
		_ = i
	}
	m.current.Store(&candidate)
	now := candidate.AppliedAt
	return m.repo.RecordProcessStatus(ctx, ProcessStatus{ProcessID: m.processID, Role: m.role, AppliedRevision: r.Revision, AttemptedRevision: r.Revision, LastSuccess: &now, Heartbeat: now})
}
func (m *SnapshotManager) Check(ctx context.Context) error { return m.Reload(ctx) }
func (m *SnapshotManager) Run(ctx context.Context, interval time.Duration) error {
	if ctx == nil {
		return errors.New("configuration watcher context is required")
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if err := m.Reload(ctx); err != nil {
		return err
	}
	heartbeat := time.NewTicker(interval)
	// Polling is only the cross-process wake-up fallback. Keep its steady-state
	// database cost bounded while still applying a committed revision promptly.
	const notificationPollInterval = time.Second
	events := time.NewTicker(notificationPollInterval)
	defer heartbeat.Stop()
	defer events.Stop()
	// A durable notification remains visible until a process has advanced its
	// snapshot. If applying a revision fails, avoid hammering the database and
	// the failing participant on every notification poll.
	retryDelay := notificationPollInterval
	var retryAfter time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-events.C:
			if time.Now().Before(retryAfter) {
				continue
			}
			current, _ := m.Current()
			changed, err := m.repo.ConfigChangedAfter(ctx, current.Revision.Revision)
			if err != nil {
				m.record(ctx, current.Revision.Revision, "database_unavailable")
				retryAfter = time.Now().Add(retryDelay)
				retryDelay = nextRetryDelay(retryDelay)
				continue
			}
			if changed {
				if err = m.Reload(ctx); err != nil {
					m.record(ctx, current.Revision.Revision, "reload_failed")
					retryAfter = time.Now().Add(retryDelay)
					retryDelay = nextRetryDelay(retryDelay)
				} else {
					retryDelay = notificationPollInterval
					retryAfter = time.Time{}
				}
			}
		case <-heartbeat.C:
			if err := m.Reload(ctx); err != nil {
				m.record(ctx, 0, "reload_failed")
				retryAfter = time.Now().Add(retryDelay)
				retryDelay = nextRetryDelay(retryDelay)
			} else {
				retryDelay = notificationPollInterval
				retryAfter = time.Time{}
			}
		}
	}
}

func nextRetryDelay(current time.Duration) time.Duration {
	const maxRetryDelay = 30 * time.Second
	if current >= maxRetryDelay/2 {
		return maxRetryDelay
	}
	return current * 2
}
func (m *SnapshotManager) record(ctx context.Context, attempted uint64, class string) {
	prev, _ := m.Current()
	status := ProcessStatus{
		ProcessID:         m.processID,
		Role:              m.role,
		AppliedRevision:   prev.Revision.Revision,
		AttemptedRevision: attempted,
		LastErrorClass:    class,
		Heartbeat:         time.Now().UTC(),
	}
	if err := m.repo.RecordProcessStatus(ctx, status); err != nil {
		slog.Warn("failed to record process status",
			"process_id", m.processID,
			"role", m.role,
			"attempted_revision", attempted,
			"error", err,
		)
	}
}
