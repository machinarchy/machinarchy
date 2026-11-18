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
	// ListHosts returns one page of Hosts ordered by ID (see HostListFilter).
	ListHosts(context.Context, HostListFilter) ([]Host, error)
	ListHostKeys(context.Context, HostKeyCursor) ([]HostKey, error)
	UpdateHostSpec(context.Context, int, ID, uint64, uint64, HostSpec, time.Time) error
	UpdateHostMetadata(context.Context, int, ID, uint64, string, int, map[string]string, time.Time) error
	// ObserveHost records a reconcile result; nil keys keep the stored
	// observed host keys.
	ObserveHost(context.Context, int, ID, uint64, HostPhase, []Condition, []ObservedHostKey, time.Time) error
	// TrustHostKeys pins host key fingerprints (see Host.TrustHostKeys) and
	// audits who trusted them: an owner, or the TOFU policy.
	TrustHostKeys(ctx context.Context, workspaceID int, id ID, expectedRevision uint64, fingerprints []string, replace bool, source string, now time.Time) error
	DeleteHost(context.Context, int, ID, uint64) error
}

// OperationRepository persists immutable accepted intent and separately
// versioned execution progress. UpdateOperation must reject stale revisions
// and must not rewrite the accepted operation spec or target snapshots.
type OperationRepository interface {
	CreateOperation(context.Context, *Operation) error
	GetOperation(context.Context, int, ID) (*Operation, error)
	// ListOperations returns one page of summaries, newest first.
	ListOperations(context.Context, OperationListFilter) ([]OperationSummary, error)
	UpdateOperation(context.Context, *Operation, uint64) error
}

// OperationTargetRepository records per-target progress without rewriting the
// whole Operation. Target writes therefore never conflict with each other or
// with operation-level updates such as cancellation, and their cost does not
// grow with the number of targets (AUDIT Q-02, Q-03).
//
// UpdateOperationTarget moves one Pending or Running target forward while the
// Operation is Running or CancelRequested. It returns ErrOperationFinished when
// the target or the Operation is already terminal, and ErrOperationNotFound
// when the Operation does not exist in the workspace.
type OperationTargetRepository interface {
	UpdateOperationTarget(ctx context.Context, workspaceID int, operationID, hostID ID, state TargetState, result *TargetResult) error
	GetOperationState(ctx context.Context, workspaceID int, operationID ID) (OperationState, error)
}

// OperationStore is the full Operation persistence port used by the worker.
type OperationStore interface {
	OperationRepository
	OperationTargetRepository
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
	// ListAccessProfiles returns live (not deleted) profiles ordered by ID,
	// starting after the given ID.
	ListAccessProfiles(ctx context.Context, workspaceID int, after ID, limit int) ([]AccessProfile, error)
	GetAccessProfileRevision(context.Context, int, ID, uint64) (*AccessProfileRevision, error)
	UpdateAccessProfile(context.Context, int, ID, uint64, AccessProfileSpec, int, time.Time) (*AccessProfileRevision, error)
	ListHostsByAccessProfile(context.Context, int, ID) ([]HostKey, error)
	DisableAccessProfile(context.Context, int, ID, uint64, int, time.Time) (*AccessProfileRevision, error)
	DeleteAccessProfile(context.Context, int, ID, int, time.Time) error
}
