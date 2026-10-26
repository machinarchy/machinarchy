package middleware

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"golang.org/x/time/rate"
)

type clientTracker struct {
	limiter      *rate.Limiter
	blockedUntil time.Time
	banCount     int
	lastSeen     time.Time
	lastBannedAt time.Time
}

type Config struct {
	RequestsPerSec       float64
	Burst                int
	InitialBlockDuration time.Duration
	MaxBlockDuration     time.Duration
}

// Validate checks configuration parameters for logical errors or out-of-bounds values
func (c *Config) Validate() error {
	if c.RequestsPerSec <= 0 {
		return errors.New("rate limit RequestsPerSec must be greater than 0")
	}
	if c.Burst <= 0 {
		return errors.New("rate limit Burst must be greater than 0")
	}
	if c.InitialBlockDuration <= 0 {
		return errors.New("rate limit InitialBlockDuration must be greater than 0")
	}
	if c.MaxBlockDuration < c.InitialBlockDuration {
		return errors.New("rate limit MaxBlockDuration cannot be less than InitialBlockDuration")
	}
	return nil
}

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
	banDecayDuration   = 30 * time.Minute // Reset banCount if client is clean for 30 mins.
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
						if now.Sub(tracker.lastSeen) > 10*time.Minute && now.After(tracker.blockedUntil) {
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

// Middleware returns the Echo middleware function using this RateLimiter instance.
func (rl *RateLimiter) Middleware() echo.MiddlewareFunc {
	rLimit := rate.Limit(rl.config.RequestsPerSec)
	burst := rl.config.Burst
	initialBlockDuration := rl.config.InitialBlockDuration
	maxBlockDuration := rl.config.MaxBlockDuration

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

			shard := rl.shardFor(ip)
			shard.mu.Lock()
			now := time.Now()
			tracker, exists := shard.clients[ip]

			if !exists {
				// Eviction strategy: if map exceeds capacity, drop the oldest entry
				if len(shard.clients) >= maxClientsPerShard {
					var oldestIP string
					var oldestTime time.Time
					for existingIP, t := range shard.clients {
						if oldestIP == "" || t.lastSeen.Before(oldestTime) {
							oldestTime = t.lastSeen
							oldestIP = existingIP
						}
					}
					if oldestIP != "" {
						delete(shard.clients, oldestIP)
					}
				}

				tracker = &clientTracker{
					limiter:      rate.NewLimiter(rLimit, burst),
					blockedUntil: now,
					banCount:     0,
					lastSeen:     now,
				}
				shard.clients[ip] = tracker
			}

			tracker.lastSeen = now

			// Decay check: If the client has been unblocked and behaved well for 30+ minutes, reset banCount
			if now.After(tracker.blockedUntil) && !tracker.lastBannedAt.IsZero() && now.Sub(tracker.lastBannedAt) > banDecayDuration {
				tracker.banCount = 0
				tracker.lastBannedAt = time.Time{}
			}

			// 1. Check if client is currently blocked
			if now.Before(tracker.blockedUntil) {
				retryAfter := int(time.Until(tracker.blockedUntil).Seconds())
				if retryAfter < 1 {
					retryAfter = 1
				}
				c.Response().Header().Set("Retry-After", strconv.Itoa(retryAfter))

				shard.mu.Unlock()
				return c.JSON(http.StatusTooManyRequests, map[string]string{
					"error": "Rate limit exceeded. Your ban duration has been extended due to continued requests.",
				})
			}

			// 2. Token Bucket check (.Allow() consumes 1 token if available)
			if !tracker.limiter.Allow() {
				tracker.banCount++
				tracker.lastBannedAt = now

				// Safe exponential backoff calculation preventing overflow
				currentBlockDuration := initialBlockDuration
				for i := 1; i < tracker.banCount; i++ {
					if currentBlockDuration >= maxBlockDuration/2 {
						currentBlockDuration = maxBlockDuration
						break
					}
					currentBlockDuration *= 2
				}

				if currentBlockDuration > maxBlockDuration {
					currentBlockDuration = maxBlockDuration
				}

				tracker.blockedUntil = now.Add(currentBlockDuration)

				retryAfter := int(currentBlockDuration.Seconds())
				c.Response().Header().Set("Retry-After", strconv.Itoa(retryAfter))

				shard.mu.Unlock()

				return c.JSON(http.StatusTooManyRequests, map[string]string{
					"error": "Rate limit exceeded. You are temporarily blocked.",
				})
			}

			shard.mu.Unlock()
			return next(c)
		}
	}
}

func (rl *RateLimiter) shardFor(ip string) *clientShard {
	const offset = uint64(14695981039346656037)
	const prime = uint64(1099511628211)
	hash := rl.seed ^ offset
	for i := 0; i < len(ip); i++ {
		hash ^= uint64(ip[i])
		hash *= prime
	}
	return &rl.shards[hash%clientShardCount]
}
