package bootstrap

import (
	"context"
	"errors"
	"sync"
	"time"
)

// errComponentsStillRunning reports that a background worker outlived the
// shutdown budget. The caller must not close the datastore under it.
var errComponentsStillRunning = errors.New("background worker did not stop within the shutdown budget")

// shutdownBudget is the single time budget for stopping a process (AUDIT
// Q-01). The clock starts on the first call to context, so every later phase
// (HTTP drain, worker drain, runtime close, telemetry flush) shares one
// deadline instead of each adding its own timeout.
type shutdownBudget struct {
	total    time.Duration
	once     sync.Once
	deadline time.Time
}

func newShutdownBudget(total time.Duration) *shutdownBudget {
	if total <= 0 {
		total = defaultShutdownTimeout
	}
	return &shutdownBudget{total: total}
}

func (b *shutdownBudget) context() (context.Context, context.CancelFunc) {
	b.once.Do(func() { b.deadline = time.Now().Add(b.total) })
	return context.WithDeadline(context.Background(), b.deadline)
}

// engineDrainTimeout is how long the Control Manager waits for active
// reconciles before cancelling them. It leaves half the budget for the
// engine's post-cancel grace, the Operation worker, and closing the runtime.
func engineDrainTimeout(total time.Duration) time.Duration {
	if total <= 0 {
		total = defaultShutdownTimeout
	}
	return total / 2
}
