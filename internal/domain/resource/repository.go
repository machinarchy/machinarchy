package resource

import (
	"context"
	"time"
)

// HostRepository is the persistence port for connector-neutral managed Hosts.
// Updates use the generation/revision expected by the caller; status writes
// additionally carry the generation observed by the controller.
type HostRepository interface {
	CreateHost(context.Context, *Host) error
	GetHost(context.Context, int, ResourceID) (*Host, error)
	ListHosts(context.Context, int, int) ([]Host, error)
	ListHostKeys(context.Context, HostKeyCursor) ([]HostKey, error)
	UpdateHostSpec(context.Context, int, ResourceID, uint64, uint64, HostSpec, time.Time) error
	UpdateHostMetadata(context.Context, int, ResourceID, uint64, string, int, map[string]string, time.Time) error
	ObserveHost(context.Context, int, ResourceID, uint64, HostPhase, []Condition, time.Time) error
	DeleteHost(context.Context, int, ResourceID, uint64) error
}

// OperationRepository persists immutable accepted intent and separately
// versioned execution progress. UpdateOperation must reject stale revisions
// and must not rewrite the accepted operation spec or target snapshots.
type OperationRepository interface {
	CreateOperation(context.Context, *Operation) error
	GetOperation(context.Context, int, ResourceID) (*Operation, error)
	UpdateOperation(context.Context, *Operation, uint64) error
}
