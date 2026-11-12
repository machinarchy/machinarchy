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
func (s *Service) Status(ctx context.Context) ([]ProcessStatus, error) {
	return s.repo.ProcessStatuses(ctx, time.Now().Add(-2*time.Minute))
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
	if changedRestartFields(current.Config, c) {
		return Revision{}, ErrRestartRequired
	}
	r, err := s.repo.Publish(ctx, expected, c, actor.UserID, note)
	if err != nil {
		return Revision{}, err
	}
	if err = s.snapshots.Reload(ctx); err != nil {
		return r, ErrApplyPending
	}
	return r, nil
}

func changedRestartFields(a, b Config) bool {
	return a.ControlManager != b.ControlManager || a.WorkQueue != b.WorkQueue || a.Reconciliation != b.Reconciliation || a.SSH.ConnectTimeoutSeconds != b.SSH.ConnectTimeoutSeconds || a.SSH.MaxConcurrent != b.SSH.MaxConcurrent || a.Operations.Workers != b.Operations.Workers || a.Operations.TimeoutSeconds != b.Operations.TimeoutSeconds
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
