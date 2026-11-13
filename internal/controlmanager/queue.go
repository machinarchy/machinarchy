package controlmanager

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

type ResourceKind string

const HostResourceKind ResourceKind = "host"

type ResourceKey struct {
	Kind        ResourceKind
	WorkspaceID int
	ID          string
}

type Admission uint8

const (
	QueueAccepted Admission = iota
	QueueCoalesced
	QueueBackpressured
	QueueShuttingDown
)

type Queue interface {
	Add(ResourceKey) Admission
	AddAfter(ResourceKey, time.Duration) Admission
	AddRateLimited(ResourceKey) Admission
	Get(context.Context) (ResourceKey, bool)
	Done(ResourceKey)
	Forget(ResourceKey)
	NumRequeues(ResourceKey) int
	Len() int
	Shutdown()
}

type ResourceQueue struct {
	mu          sync.Mutex
	capacity    int
	queue       []ResourceKey
	dirty       map[ResourceKey]bool
	processing  map[ResourceKey]bool
	delayed     map[ResourceKey]time.Time
	requeues    map[ResourceKey]int
	wake        chan struct{}
	shutdownCh  chan struct{}
	waiting     int
	waitChange  chan struct{}
	stop        chan struct{}
	stopped     chan struct{}
	shutdown    bool
	baseBackoff time.Duration
	maxBackoff  time.Duration
	closeOnce   sync.Once
}

func NewResourceQueue(capacity int, baseBackoff, maxBackoff time.Duration) *ResourceQueue {
	if capacity < 1 {
		capacity = 1024
	}
	if baseBackoff <= 0 {
		baseBackoff = time.Second
	}
	if maxBackoff < baseBackoff {
		maxBackoff = 5 * time.Minute
	}
	q := &ResourceQueue{capacity: capacity, dirty: make(map[ResourceKey]bool), processing: make(map[ResourceKey]bool),
		delayed: make(map[ResourceKey]time.Time), requeues: make(map[ResourceKey]int), wake: make(chan struct{}, 1), shutdownCh: make(chan struct{}),
		stop: make(chan struct{}), stopped: make(chan struct{}), waitChange: make(chan struct{}, 1), baseBackoff: baseBackoff, maxBackoff: maxBackoff}
	go q.scheduleDelayed()
	return q
}

func (q *ResourceQueue) Add(key ResourceKey) Admission {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.addLocked(key)
}

func (q *ResourceQueue) addLocked(key ResourceKey) Admission {
	if q.shutdown {
		return QueueShuttingDown
	}
	if !validKey(key) {
		return QueueBackpressured
	}
	if q.dirty[key] {
		return QueueCoalesced
	}
	if !q.processing[key] && len(q.queue)+len(q.delayed) >= q.capacity {
		return QueueBackpressured
	}
	q.dirty[key] = true
	if !q.processing[key] {
		q.queue = append(q.queue, key)
		q.signal()
	}
	return QueueAccepted
}

func (q *ResourceQueue) AddAfter(key ResourceKey, delay time.Duration) Admission {
	if delay <= 0 {
		return q.Add(key)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.shutdown {
		return QueueShuttingDown
	}
	if !validKey(key) {
		return QueueBackpressured
	}
	if q.dirty[key] {
		return QueueCoalesced
	}
	if q.processing[key] {
		q.dirty[key] = true
		q.delayed[key] = time.Now().Add(delay)
		q.signal()
		return QueueAccepted
	}
	if len(q.queue)+len(q.delayed) >= q.capacity {
		return QueueBackpressured
	}
	q.dirty[key] = true
	q.delayed[key] = time.Now().Add(delay)
	q.signal()
	return QueueAccepted
}

func (q *ResourceQueue) AddRateLimited(key ResourceKey) Admission {
	q.mu.Lock()
	if q.shutdown {
		q.mu.Unlock()
		return QueueShuttingDown
	}
	count := q.requeues[key] + 1
	q.requeues[key] = count
	backoff := q.baseBackoff
	for i := 1; i < count && backoff < q.maxBackoff; i++ {
		if backoff > q.maxBackoff/2 {
			backoff = q.maxBackoff
			break
		}
		backoff *= 2
	}
	if backoff > q.maxBackoff {
		backoff = q.maxBackoff
	}
	// Jitter avoids synchronized retry waves while retaining the configured cap.
	jittered := time.Duration(float64(backoff) * (0.8 + rand.Float64()*0.4)) // #nosec G404 -- non-security retry jitter
	if jittered > q.maxBackoff {
		jittered = q.maxBackoff
	}
	q.mu.Unlock()
	return q.AddAfter(key, jittered)
}

func (q *ResourceQueue) Get(ctx context.Context) (ResourceKey, bool) {
	if ctx == nil {
		return ResourceKey{}, false
	}
	for {
		q.mu.Lock()
		if len(q.queue) > 0 {
			key := q.queue[0]
			q.queue[0] = ResourceKey{}
			q.queue = q.queue[1:]
			delete(q.dirty, key)
			q.processing[key] = true
			q.mu.Unlock()
			return key, true
		}
		if q.shutdown {
			q.mu.Unlock()
			return ResourceKey{}, false
		}
		q.waiting++
		select {
		case q.waitChange <- struct{}{}:
		default:
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return ResourceKey{}, false
		case <-q.shutdownCh:
		case <-q.wake:
		}
		q.mu.Lock()
		q.waiting--
		q.mu.Unlock()
	}
}

func (q *ResourceQueue) Done(key ResourceKey) {
	q.mu.Lock()
	delete(q.processing, key)
	if q.dirty[key] {
		_, delayed := q.delayed[key]
		if !delayed && (q.shutdown || len(q.queue) < q.capacity) {
			q.queue = append(q.queue, key)
		}
	}
	q.mu.Unlock()
	q.signal()
}

func (q *ResourceQueue) Forget(key ResourceKey) { q.mu.Lock(); delete(q.requeues, key); q.mu.Unlock() }
func (q *ResourceQueue) NumRequeues(key ResourceKey) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.requeues[key]
}
func (q *ResourceQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.queue) + len(q.delayed)
}

func (q *ResourceQueue) Shutdown() {
	q.closeOnce.Do(func() {
		q.mu.Lock()
		q.shutdown = true
		for key := range q.delayed {
			delete(q.delayed, key)
			if !q.processing[key] && q.dirty[key] {
				q.queue = append(q.queue, key)
			}
		}
		q.mu.Unlock()
		close(q.shutdownCh)
		close(q.stop)
		q.signal()
		<-q.stopped
	})
}

func (q *ResourceQueue) scheduleDelayed() {
	defer close(q.stopped)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-q.stop:
			return
		case now := <-ticker.C:
			q.mu.Lock()
			for key, due := range q.delayed {
				if due.After(now) {
					continue
				}
				if q.processing[key] || len(q.queue) >= q.capacity {
					continue
				}
				delete(q.delayed, key)
				q.queue = append(q.queue, key)
			}
			q.mu.Unlock()
			q.signal()
		}
	}
}

func (q *ResourceQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func validKey(key ResourceKey) bool { return key.Kind != "" && key.WorkspaceID > 0 && key.ID != "" }
