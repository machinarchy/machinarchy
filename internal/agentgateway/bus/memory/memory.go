// Package memory is an in-process bus for tests and for checking the shared
// contract (bustest). It is not durable across restarts.
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/machinarchy/machinarchy/internal/agentgateway/bus"
)

// Delivery is one command handed to a consumer.
type Delivery struct {
	Payload []byte
	Ack     func()
	Nack    func()
}

type entry struct {
	id      string
	payload []byte
}

type queue struct {
	pending  []*entry
	inflight map[*entry]*time.Timer
	seen     map[string]bool
}

// Bus implements bus.Bus in memory.
type Bus struct {
	// AckWait redelivers an unacknowledged command; zero disables it.
	AckWait time.Duration

	mu     sync.Mutex
	cond   chan struct{}
	queues map[string]*queue
	h      bus.Handlers
	ready  bool
}

var _ bus.Bus = (*Bus)(nil)

// New returns a bus that is ready once Serve runs.
func New() *Bus { return &Bus{cond: make(chan struct{}, 1), queues: map[string]*queue{}} }

func (b *Bus) queue(agentID string) *queue {
	q := b.queues[agentID]
	if q == nil {
		q = &queue{inflight: map[*entry]*time.Timer{}, seen: map[string]bool{}}
		b.queues[agentID] = q
	}
	return q
}

// Serve marks the bus ready and blocks until ctx ends.
func (b *Bus) Serve(ctx context.Context, h bus.Handlers) error {
	b.mu.Lock()
	b.h, b.ready = h, true
	b.mu.Unlock()
	<-ctx.Done()
	b.mu.Lock()
	b.ready = false
	b.mu.Unlock()
	return nil
}

// Ready reports whether Serve is running.
func (b *Bus) Ready() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.ready }

// EnsureAgent creates the agent's queue.
func (b *Bus) EnsureAgent(_ context.Context, agentID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.queue(agentID)
	return nil
}

// Publish appends payload unless msgID was already published for the agent.
func (b *Bus) Publish(_ context.Context, agentID, msgID string, payload []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.ready {
		return bus.ErrUnavailable
	}
	q := b.queue(agentID)
	if msgID != "" {
		if q.seen[msgID] {
			return nil
		}
		q.seen[msgID] = true
	}
	q.pending = append(q.pending, &entry{id: msgID, payload: append([]byte(nil), payload...)})
	b.wake()
	return nil
}

func (b *Bus) wake() {
	select {
	case b.cond <- struct{}{}:
	default:
	}
}

// Next returns the agent's next command, waiting up to wait. A command that is
// neither acknowledged nor rejected is redelivered after AckWait.
func (b *Bus) Next(ctx context.Context, agentID string, wait time.Duration) (Delivery, bool) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		b.mu.Lock()
		q := b.queue(agentID)
		if len(q.pending) > 0 {
			e := q.pending[0]
			q.pending = q.pending[1:]
			if b.AckWait > 0 {
				q.inflight[e] = time.AfterFunc(b.AckWait, func() { b.requeue(agentID, e) })
			} else {
				q.inflight[e] = nil
			}
			b.mu.Unlock()
			return Delivery{Payload: e.payload, Ack: func() { b.settle(agentID, e, false) }, Nack: func() { b.settle(agentID, e, true) }}, true
		}
		b.mu.Unlock()
		select {
		case <-b.cond:
		case <-deadline.C:
			return Delivery{}, false
		case <-ctx.Done():
			return Delivery{}, false
		}
	}
}

func (b *Bus) settle(agentID string, e *entry, requeue bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.queue(agentID)
	timer, ok := q.inflight[e]
	if !ok {
		return
	}
	if timer != nil {
		timer.Stop()
	}
	delete(q.inflight, e)
	if requeue {
		q.pending = append([]*entry{e}, q.pending...) // keep per-agent order
		b.wake()
	}
}

func (b *Bus) requeue(agentID string, e *entry) { b.settle(agentID, e, true) }

// DeliverResult passes an agent result to the installed handler, as a
// transport that authenticated agentID would.
func (b *Bus) DeliverResult(ctx context.Context, agentID string, payload []byte) {
	b.mu.Lock()
	h := b.h
	b.mu.Unlock()
	if h.Result != nil {
		h.Result(ctx, agentID, payload)
	}
}
