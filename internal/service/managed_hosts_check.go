package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

// ErrHostCheckUnavailable is returned when this process has no host checker.
var ErrHostCheckUnavailable = errors.New("host check is not available")

// ErrHostCheckInProgress is returned when a check of the same Host is running.
var ErrHostCheckInProgress = errors.New("a check of this host is already running")

// hostCheckTimeout bounds one check, whatever the connector's own budget is.
const hostCheckTimeout = 30 * time.Second

// HostChecker verifies one Host the way the controller does. The service
// only reads its answer: the observation is never stored and TrustHostKeys is
// ignored, so a check cannot change what a Host trusts.
type HostChecker interface {
	ReconcileHost(context.Context, *resource.Host) (resource.HostObservation, error)
}

// UseHostChecker enables CheckHost.
func (s *ManagedResourceService) UseHostChecker(checker HostChecker) { s.checker = checker }

// HostCheck is the answer of an on-demand check. Reason is the Ready reason
// (docs/runbook.md, "Host condition reasons"); Message never holds credentials.
type HostCheck struct {
	HostID    resource.ID `json:"hostId"`
	Ready     bool        `json:"ready"`
	Reachable bool        `json:"reachable"`
	Reason    string      `json:"reason"`
	Message   string      `json:"message,omitempty"`
	CheckedAt time.Time   `json:"checkedAt"`
}

// CheckHost verifies a Host now: address policy, host key against the trusted
// keys, and authentication. It sends no command, trusts nothing and writes
// nothing. Callers need inventory write, because a check opens an outbound
// SSH connection.
func (s *ManagedResourceService) CheckHost(ctx context.Context, principal domain.Principal, id resource.ID) (*HostCheck, error) {
	if err := authorizeManagedHost(principal); err != nil {
		return nil, err
	}
	if s.checker == nil {
		return nil, ErrHostCheckUnavailable
	}
	host, err := s.hosts.GetHost(ctx, principal.WorkspaceID, id)
	if err != nil {
		return nil, err
	}
	if _, running := s.checking.LoadOrStore(id, struct{}{}); running {
		return nil, ErrHostCheckInProgress
	}
	defer s.checking.Delete(id)
	ctx, cancel := context.WithTimeout(ctx, hostCheckTimeout)
	defer cancel()
	observed, err := s.checker.ReconcileHost(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("check host: %w", err)
	}
	result := &HostCheck{HostID: id, Reason: "AccessNotVerified", CheckedAt: time.Now().UTC()}
	for _, condition := range observed.Conditions {
		switch condition.Type {
		case "Ready":
			result.Ready = condition.Status == resource.ConditionTrue
			result.Reason, result.Message = condition.Reason, condition.Message
		case "Reachable":
			result.Reachable = condition.Status == resource.ConditionTrue
		}
	}
	return result, nil
}
