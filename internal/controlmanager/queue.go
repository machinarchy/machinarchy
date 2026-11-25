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

// ResourceQueue is a de-duplicating work queue of resource keys.
//
// capacity bounds the keys that are ready to run. Keys waiting for a delayed
// retry are not counted: each is a distinct resource that already exists, and
// counting them would let a fleet with many not-Ready hosts block every new
// event (and the startup scan) once the retries alone filled the queue.
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
		// An immediate add overrides a pending delayed requeue: a new event
		// (a spec change, an approval) must not wait for a retry delay.
		if _, delayed := q.delayed[key]; delayed {
			delete(q.delayed, key)
			if !q.processing[key] {
				q.queue = append(q.queue, key)
				q.signal()
			}
		}
		return QueueCoalesced
	}
	if !q.processing[key] && len(q.queue) >= q.capacity {
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
	// A delayed retry is never refused: dropping it would leave the resource
	// unchecked until the next periodic scan.
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
		if _, delayed := q.delayed[key]; !delayed {
			if q.shutdown || len(q.queue) < q.capacity {
				q.queue = append(q.queue, key)
			} else {
				// The queue is full: keep the key as an immediate delayed
				// entry, which the scheduler moves into the queue as soon as
				// there is room. Leaving it dirty but in neither place would
				// make every later Add coalesce into it, and never run.
				q.delayed[key] = time.Now()
			}
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
		// Pending retries are dropped, not run: they exist only in memory and
		// the next start scans every resource anyway.
		for key := range q.delayed {
			delete(q.delayed, key)
			delete(q.dirty, key)
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
