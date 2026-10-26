package service

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/machinarchy/machinarchy/internal/domain"
)

// DefaultSSHConcurrency is the process-local limit for concurrent SSH work.
const DefaultSSHConcurrency = 32

// SSHCapacity bounds concurrent outbound SSH work across services in one
// process. It rejects excess work immediately, so overload does not turn into
// an unbounded queue of waiting requests.
type SSHCapacity struct {
	slots    chan struct{}
	rejected atomic.Uint64
}

// NewSSHCapacity constructs a process-local SSH bulkhead with the given limit.
func NewSSHCapacity(limit int) (*SSHCapacity, error) {
	if limit < 1 {
		return nil, errors.New("SSH concurrency limit must be positive")
	}
	return &SSHCapacity{slots: make(chan struct{}, limit)}, nil
}

func newDefaultSSHCapacity() *SSHCapacity {
	return &SSHCapacity{slots: make(chan struct{}, DefaultSSHConcurrency)}
}

// Acquire obtains one SSH slot without spawning a goroutine or accumulating a
// queue. The caller must call the returned release function exactly once.
func (c *SSHCapacity) Acquire(ctx context.Context) (func(), error) {
	if c == nil || c.slots == nil {
		return nil, errors.New("SSH capacity limiter is not initialized")
	}
	if ctx == nil {
		return nil, errors.New("SSH capacity context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case c.slots <- struct{}{}:
		return func() { <-c.slots }, nil
	default:
		c.rejected.Add(1)
		return nil, domain.ErrServiceBusy
	}
}

// InUse reports the number of active SSH operations.
func (c *SSHCapacity) InUse() int {
	if c == nil {
		return 0
	}
	return len(c.slots)
}

// Limit reports the configured maximum number of active SSH operations.
func (c *SSHCapacity) Limit() int {
	if c == nil {
		return 0
	}
	return cap(c.slots)
}

// Rejected reports cumulative operations rejected because all SSH slots were occupied.
func (c *SSHCapacity) Rejected() uint64 {
	if c == nil {
		return 0
	}
	return c.rejected.Load()
}
