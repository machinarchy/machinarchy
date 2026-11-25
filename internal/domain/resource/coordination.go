package resource

import (
	"context"
	"errors"
	"time"
)

var ErrLeaseUnsupported = errors.New("database does not support distributed resource leases")
var ErrLeaseRequired = errors.New("a valid resource lease fence is required")

type HostKeyCursor struct {
	AfterWorkspaceID int
	AfterID          string
	Limit            int
}

type HostKey struct {
	WorkspaceID int
	ID          ID
}

type Lease struct {
	Kind        string
	WorkspaceID int
	ID          string
	Owner       string
	Token       uint64
	ExpiresAt   time.Time
}

type HostKeyLister interface {
	ListHostKeys(context.Context, HostKeyCursor) ([]HostKey, error)
}

type LeaseStore interface {
	AcquireResourceLease(context.Context, Lease, time.Time, time.Duration) (Lease, bool, error)
	RenewResourceLease(context.Context, Lease, time.Time, time.Duration) (Lease, bool, error)
	ReleaseResourceLease(context.Context, Lease) error
}

type LeaseFence struct {
	Kind        string
	WorkspaceID int
	ID          string
	Token       uint64
}

type leaseFenceContextKey struct{}

func WithLeaseFence(ctx context.Context, fence LeaseFence) context.Context {
	return context.WithValue(ctx, leaseFenceContextKey{}, fence)
}

func LeaseFenceFromContext(ctx context.Context) (LeaseFence, bool) {
	if ctx == nil {
		return LeaseFence{}, false
	}
	fence, ok := ctx.Value(leaseFenceContextKey{}).(LeaseFence)
	return fence, ok
}
