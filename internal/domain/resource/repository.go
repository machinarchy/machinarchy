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
	GetHost(context.Context, int, ID) (*Host, error)
	ListHosts(context.Context, int, int) ([]Host, error)
	ListHostKeys(context.Context, HostKeyCursor) ([]HostKey, error)
	UpdateHostSpec(context.Context, int, ID, uint64, uint64, HostSpec, time.Time) error
	UpdateHostMetadata(context.Context, int, ID, uint64, string, int, map[string]string, time.Time) error
	ObserveHost(context.Context, int, ID, uint64, HostPhase, []Condition, time.Time) error
	DeleteHost(context.Context, int, ID, uint64) error
}

// OperationRepository persists immutable accepted intent and separately
// versioned execution progress. UpdateOperation must reject stale revisions
// and must not rewrite the accepted operation spec or target snapshots.
type OperationRepository interface {
	CreateOperation(context.Context, *Operation) error
	GetOperation(context.Context, int, ID) (*Operation, error)
	UpdateOperation(context.Context, *Operation, uint64) error
}

type OperationKey struct {
	WorkspaceID int
	ID          ID
}

// OperationQueueRepository supplies durable level-triggered discovery for
// queued Operations. Notifications may accelerate this scan but are not relied
// on for recovery.
type OperationQueueRepository interface {
	ListQueuedOperations(context.Context, int) ([]OperationKey, error)
}

type OperationHostReferenceRepository interface {
	ListActiveOperationsForHost(context.Context, int, ID) ([]OperationKey, error)
}

type OperationRecoveryRepository interface {
	ListRunningOperations(context.Context, int) ([]OperationKey, error)
}

type AccessProfileRepository interface {
	CreateAccessProfile(context.Context, *AccessProfile, AccessProfileRevision) error
	GetAccessProfile(context.Context, int, ID) (*AccessProfile, error)
	GetAccessProfileRevision(context.Context, int, ID, uint64) (*AccessProfileRevision, error)
	UpdateAccessProfile(context.Context, int, ID, uint64, AccessProfileSpec, int, time.Time) (*AccessProfileRevision, error)
	ListHostsByAccessProfile(context.Context, int, ID) ([]HostKey, error)
	DisableAccessProfile(context.Context, int, ID, uint64, int, time.Time) (*AccessProfileRevision, error)
	DeleteAccessProfile(context.Context, int, ID, int, time.Time) error
}
