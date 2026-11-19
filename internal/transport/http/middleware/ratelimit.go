package middleware

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/httperr"

	"github.com/labstack/echo/v4"
	"golang.org/x/time/rate"
)

type clientTracker struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// Config sizes each client's token bucket (AUDIT S-10). Limits are kept in
// memory, so each replica enforces them separately.
type Config struct {
	RequestsPerSec float64
	Burst          int
}

// Validate checks configuration parameters for logical errors or out-of-bounds values
func (c *Config) Validate() error {
	if c.RequestsPerSec <= 0 {
		return errors.New("rate limit RequestsPerSec must be greater than 0")
	}
	if c.Burst <= 0 {
		return errors.New("rate limit Burst must be greater than 0")
	}
	return nil
}

// RateLimiter enforces request rate limits for incoming HTTP traffic.
type RateLimiter struct {
	shards   [clientShardCount]clientShard
	seed     uint64
	config   Config
	quit     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

type clientShard struct {
	mu      sync.Mutex
	clients map[string]*clientTracker
}

const (
	maxTrackedClients  = 10000 // Bound client state to prevent memory bloat.
	clientShardCount   = 64    // Reduce lock contention across independent client IPs.
	maxClientsPerShard = (maxTrackedClients + clientShardCount - 1) / clientShardCount
	idleClientTTL      = 10 * time.Minute // Forget clients idle this long; their bucket is full again anyway.
)

// NewRateLimiter creates an encapsulated RateLimiter instance and validates config.
func NewRateLimiter(cfg Config) (*RateLimiter, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	var seedBytes [8]byte
	if _, err := rand.Read(seedBytes[:]); err != nil {
		return nil, fmt.Errorf("initialize rate limiter shard hash: %w", err)
	}
	rl := &RateLimiter{
		config: cfg,
		seed:   binary.LittleEndian.Uint64(seedBytes[:]),
		quit:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	for i := range rl.shards {
		rl.shards[i].clients = make(map[string]*clientTracker)
	}

	rl.startCleanupWorker()
	return rl, nil
}

// startCleanupWorker runs a background ticker to prune old inactive IP entries, stoppable via quit channel.
func (rl *RateLimiter) startCleanupWorker() {
	ticker := time.NewTicker(5 * time.Minute)
	go func() {
		defer close(rl.done)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				now := time.Now()
				for i := range rl.shards {
					shard := &rl.shards[i]
					shard.mu.Lock()
					for ip, tracker := range shard.clients {
						if now.Sub(tracker.lastSeen) > idleClientTTL {
							delete(shard.clients, ip)
						}
					}
					shard.mu.Unlock()
				}
			case <-rl.quit:
				return
			}
		}
	}()
}

// Stop gracefully shuts down the background cleanup goroutine.
func (rl *RateLimiter) Stop() {
	rl.stopOnce.Do(func() {
		close(rl.quit)
	})
	<-rl.done
}

// Middleware returns the Echo middleware function using this RateLimiter
// instance. Each client network (domain.ClientKey of the client address, so
// an IPv6 /64) has one token bucket. A request without a token is refused
// with 429 and a Retry-After of when the next token arrives; there is no
// escalating ban, so a client that slows down is served again at once.
func (rl *RateLimiter) Middleware() echo.MiddlewareFunc {
	rLimit := rate.Limit(rl.config.RequestsPerSec)
	burst := rl.config.Burst

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ip := c.RealIP()
			if ip == "" {
				host, _, err := net.SplitHostPort(c.Request().RemoteAddr)
				if err == nil {
					ip = host
				} else {
					ip = c.Request().RemoteAddr
				}
			}
			key := domain.ClientKey(ip)

			shard := rl.shardFor(key)
			shard.mu.Lock()
			now := time.Now()
			tracker, exists := shard.clients[key]
			if !exists {
				// Bound memory: past capacity, forget the longest-idle client.
				if len(shard.clients) >= maxClientsPerShard {
					var oldestKey string
					var oldestTime time.Time
					for existing, t := range shard.clients {
						if oldestKey == "" || t.lastSeen.Before(oldestTime) {
							oldestTime = t.lastSeen
							oldestKey = existing
						}
					}
					delete(shard.clients, oldestKey)
				}
				tracker = &clientTracker{limiter: rate.NewLimiter(rLimit, burst)}
				shard.clients[key] = tracker
			}
			tracker.lastSeen = now
			reservation := tracker.limiter.ReserveN(now, 1)
			wait := reservation.DelayFrom(now)
			if wait > 0 {
				reservation.CancelAt(now) // a refused request costs no token
			}
			shard.mu.Unlock()

			if wait > 0 {
				c.Response().Header().Set("Retry-After", strconv.FormatInt(int64(math.Ceil(wait.Seconds())), 10))
				return httperr.Status(c, http.StatusTooManyRequests, "Rate limit exceeded; retry after the time in Retry-After.")
			}
			return next(c)
		}
	}
}

func (rl *RateLimiter) shardFor(key string) *clientShard {
	const offset = uint64(14695981039346656037)
	const prime = uint64(1099511628211)
	hash := rl.seed ^ offset
	for i := 0; i < len(key); i++ {
		hash ^= uint64(key[i])
		hash *= prime
	}
	return &rl.shards[hash%clientShardCount]
}
