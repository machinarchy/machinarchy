package runtimeconfig

import (
	"context"
	"errors"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

type Service struct {
	repo      Repository
	snapshots *SnapshotManager
}

func NewService(repo Repository, snapshots *SnapshotManager) (*Service, error) {
	if repo == nil || snapshots == nil {
		return nil, errors.New("configuration service requires repository and snapshot manager")
	}
	return &Service{repo: repo, snapshots: snapshots}, nil
}
func (s *Service) Bootstrap(ctx context.Context, defaults Config) error {
	if err := defaults.Validate(); err != nil {
		return err
	}
	if _, err := s.repo.Seed(ctx, defaults); err != nil {
		return err
	}
	return s.snapshots.Reload(ctx)
}
func (s *Service) Current(ctx context.Context) (Revision, error) { return s.repo.Current(ctx) }
func (s *Service) Get(ctx context.Context, revision uint64) (Revision, error) {
	return s.repo.GetRevision(ctx, revision)
}
func (s *Service) List(ctx context.Context, limit int, before uint64) ([]Revision, error) {
	return s.repo.ListRevisions(ctx, limit, before)
}

// Status lists the processes seen in the last two minutes. RestartPending is
// set for a process whose startup revision differs from the current one in a
// restart-required setting.
func (s *Service) Status(ctx context.Context) ([]ProcessStatus, error) {
	statuses, err := s.repo.ProcessStatuses(ctx, time.Now().Add(-2*time.Minute))
	if err != nil {
		return nil, err
	}
	current, err := s.repo.Current(ctx)
	if err != nil {
		return nil, err
	}
	startedConfigs := map[uint64]Config{}
	for i, st := range statuses {
		if st.StartedRevision == 0 || st.StartedRevision == current.Revision {
			continue
		}
		started, ok := startedConfigs[st.StartedRevision]
		if !ok {
			r, err := s.repo.GetRevision(ctx, st.StartedRevision)
			if err != nil {
				return nil, err
			}
			started = r.Config
			startedConfigs[st.StartedRevision] = started
		}
		statuses[i].RestartPending = len(RestartFields(started, current.Config)) > 0
	}
	return statuses, nil
}
func (s *Service) Publish(ctx context.Context, actor domain.Principal, expected uint64, c Config, note string) (Revision, error) {
	if err := authorize(actor); err != nil {
		return Revision{}, err
	}
	if err := c.Validate(); err != nil {
		return Revision{}, err
	}
	current, err := s.repo.Current(ctx)
	if err != nil {
		return Revision{}, err
	}
	if current.Revision != expected {
		return Revision{}, ErrConflict
	}
	// A change to a restart-required setting is stored like any other; it
	// takes effect when each process next starts (Status shows which have not).
	r, err := s.repo.Publish(ctx, expected, c, actor.UserID, note)
	if err != nil {
		return Revision{}, err
	}
	r.RestartRequired = RestartFields(current.Config, c)
	if err = s.snapshots.Reload(ctx); err != nil {
		return r, ErrApplyPending
	}
	return r, nil
}

// RestartFields names the restart-required settings (as in Classifications)
// that differ between a and b.
func RestartFields(a, b Config) []string {
	var fields []string
	add := func(changed bool, name string) {
		if changed {
			fields = append(fields, name)
		}
	}
	add(a.ControlManager.Workers != b.ControlManager.Workers, "controlManager.workers")
	add(a.ControlManager.ResyncSeconds != b.ControlManager.ResyncSeconds, "controlManager.resyncSeconds")
	add(a.WorkQueue.Capacity != b.WorkQueue.Capacity, "workQueue.capacity")
	add(a.WorkQueue.RetryBaseSeconds != b.WorkQueue.RetryBaseSeconds, "workQueue.retryBaseSeconds")
	add(a.WorkQueue.RetryMaxSeconds != b.WorkQueue.RetryMaxSeconds, "workQueue.retryMaxSeconds")
	add(a.WorkQueue.RatePerSecond != b.WorkQueue.RatePerSecond, "workQueue.ratePerSecond")
	add(a.Reconciliation.TimeoutSeconds != b.Reconciliation.TimeoutSeconds, "reconciliation.timeoutSeconds")
	add(a.Reconciliation.ManagedHostResyncSeconds != b.Reconciliation.ManagedHostResyncSeconds, "reconciliation.managedHostResyncSeconds")
	add(a.SSH.ConnectTimeoutSeconds != b.SSH.ConnectTimeoutSeconds, "ssh.connectTimeoutSeconds")
	add(a.SSH.MaxConcurrent != b.SSH.MaxConcurrent, "ssh.maxConcurrent")
	add(a.Operations.Workers != b.Operations.Workers, "operations.workers")
	add(a.Operations.TimeoutSeconds != b.Operations.TimeoutSeconds, "operations.timeoutSeconds")
	return fields
}
func (s *Service) Rollback(ctx context.Context, actor domain.Principal, expected, target uint64, note string) (Revision, error) {
	if err := authorize(actor); err != nil {
		return Revision{}, err
	}
	previous, err := s.repo.GetRevision(ctx, target)
	if err != nil {
		return Revision{}, err
	}
	return s.Publish(ctx, actor, expected, previous.Config, "rollback to revision "+itoa(target)+": "+note)
}
func (s *Service) Snapshot() (Snapshot, bool) { return s.snapshots.Current() }
func (s *Service) Watch(ctx context.Context, interval time.Duration) error {
	return s.snapshots.Run(ctx, interval)
}
func authorize(p domain.Principal) error {
	if p.UserID <= 0 || p.Role != domain.RoleOwner || p.APITokenID != 0 {
		return domain.ErrPermissionDenied
	}
	return nil
}
func itoa(v uint64) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = digits[v%10]
		v /= 10
	}
	return string(b[i:])
}
