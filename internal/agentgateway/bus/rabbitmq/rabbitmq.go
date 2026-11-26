// Package rabbitmq is a spike adapter of the agent gateway bus for RabbitMQ
// (AMQP 0-9-1). It proves that bus.Bus is not NATS-shaped: the contract suite
// (bustest) runs against it. It is the only place that may import
// rabbitmq/amqp091-go; a test enforces that. It is not wired into
// bootstrap; see ChangeLogAI.md for the known gaps.
//
// Mapping: one durable classic queue per agent (default exchange, routing key
// = queue name) holds commands; a topic exchange routes agent results, whose
// routing key carries the agent identity, into one durable results queue.
package rabbitmq

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
	"github.com/machinarchy/machinarchy/internal/agentgateway/bus"
)

const (
	defaultAckWait      = 30 * time.Second
	defaultDedupeWindow = 15 * time.Minute
	resultExchange      = "machinarchy.agent.results"
	resultQueue         = "machinarchy.agent.results"
	pollInterval        = 50 * time.Millisecond
	reconnectDelay      = 2 * time.Second
)

// Config configures the adapter.
type Config struct {
	// URL is an amqp:// or amqps:// URL; credentials may be in it.
	URL string
	// TLS is used for amqps. Nil means the system roots; verification is
	// never disabled here.
	TLS *tls.Config
	// AckWait is how long an unacknowledged command stays with its consumer
	// before it is returned to the queue. Default 30s.
	AckWait time.Duration
	// DedupeWindow is how long a message ID is remembered. Default 15m.
	DedupeWindow   time.Duration
	ConnectTimeout time.Duration
	Logger         *slog.Logger
}

// Bus is the RabbitMQ adapter.
type Bus struct {
	cfg    Config
	logger *slog.Logger
	ready  atomic.Bool

	mu   sync.RWMutex
	conn *amqp.Connection

	pubMu    sync.Mutex // serialises publishes on the confirm channel
	pub      *amqp.Channel
	confirms chan amqp.Confirmation
	seen     map[string]time.Time // in-process dedupe: NOT shared between controllers
	ensured  sync.Map             // agentID -> struct{}, queues declared on this connection
}

var _ bus.Bus = (*Bus)(nil)

// CommandQueue returns the queue name of an agent's commands.
func CommandQueue(agentID string) (string, error) {
	if _, err := agentv1.CommandSubject(agentID); err != nil { // validates the ID
		return "", err
	}
	return "machinarchy.agent." + agentID + ".commands", nil
}

// ResultRoutingKey returns the routing key an agent's result is published with.
func ResultRoutingKey(agentID string) (string, error) { return agentv1.ResultSubject(agentID) }

// ResultExchange is the topic exchange results are published to.
const ResultExchange = resultExchange

// ResultQueue is the durable queue the controller consumes results from.
const ResultQueue = resultQueue

// New validates cfg.
func New(cfg Config) (*Bus, error) {
	if cfg.URL == "" {
		return nil, errors.New("the RabbitMQ bus requires a URL")
	}
	if cfg.AckWait <= 0 {
		cfg.AckWait = defaultAckWait
	}
	if cfg.DedupeWindow <= 0 {
		cfg.DedupeWindow = defaultDedupeWindow
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 5 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Bus{cfg: cfg, logger: cfg.Logger.With("component", "agent-gateway"), seen: map[string]time.Time{}}, nil
}

// Ready reports whether the connection is up and the topology declared.
func (b *Bus) Ready() bool { return b.ready.Load() }

func (b *Bus) dial() (*amqp.Connection, error) {
	return amqp.DialConfig(b.cfg.URL, amqp.Config{
		Dial: amqp.DefaultDial(b.cfg.ConnectTimeout), TLSClientConfig: b.cfg.TLS,
		Properties: amqp.Table{"connection_name": "machinarchy-agent-gateway"},
	})
}

// Serve connects, declares the topology and delivers agent results to
// h.Result until ctx ends. A lost connection is re-established; only the first
// connection failing is an error.
func (b *Bus) Serve(ctx context.Context, h bus.Handlers) error {
	err := b.session(ctx, h)
	for err == nil && ctx.Err() == nil {
		b.logger.Warn("agent gateway lost RabbitMQ, reconnecting")
		select {
		case <-ctx.Done():
		case <-time.After(reconnectDelay):
		}
		if ctx.Err() == nil {
			if err = b.session(ctx, h); err != nil {
				b.logger.Warn("agent gateway reconnect to RabbitMQ failed", "error", err)
				err = nil
			}
		}
	}
	return err
}

// session runs one connection until it drops or ctx ends (nil); a setup
// failure is returned.
func (b *Bus) session(ctx context.Context, h bus.Handlers) error {
	conn, err := b.dial()
	if err != nil {
		return fmt.Errorf("connect agent gateway to RabbitMQ: %w", err)
	}
	defer func() { _ = conn.Close() }()
	pub, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("open publish channel: %w", err)
	}
	if err := pub.Confirm(false); err != nil {
		return fmt.Errorf("enable publisher confirms: %w", err)
	}
	consume, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("open result channel: %w", err)
	}
	if err := declareResults(consume); err != nil {
		return err
	}
	if err := consume.Qos(16, 0, false); err != nil {
		return fmt.Errorf("set result prefetch: %w", err)
	}
	results, err := consume.Consume(resultQueue, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume results: %w", err)
	}

	b.mu.Lock()
	b.conn = conn
	b.mu.Unlock()
	b.pubMu.Lock()
	b.pub, b.confirms = pub, pub.NotifyPublish(make(chan amqp.Confirmation, 1))
	b.pubMu.Unlock()
	b.ensured.Clear()
	closed := conn.NotifyClose(make(chan *amqp.Error, 1))
	b.ready.Store(true)
	b.logger.Info("agent gateway serving", "broker", "rabbitmq")
	defer func() {
		b.ready.Store(false)
		b.mu.Lock()
		b.conn = nil
		b.mu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case cause := <-closed: // the reconnect loop handles a dropped connection
			b.logger.Warn("RabbitMQ connection closed", "cause", cause)
			return nil
		case msg, ok := <-results:
			if !ok {
				return nil
			}
			agentID, valid := agentIDFromRoutingKey(msg.RoutingKey)
			if valid && h.Result != nil {
				h.Result(context.Background(), agentID, msg.Body)
			}
			if !valid {
				b.logger.Warn("dropped result with an unexpected routing key")
			}
			if err := msg.Ack(false); err != nil {
				b.logger.Warn("acknowledge agent result", "error", err)
			}
		}
	}
}

func declareResults(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(resultExchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare result exchange: %w", err)
	}
	if _, err := ch.QueueDeclare(resultQueue, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare result queue: %w", err)
	}
	if err := ch.QueueBind(resultQueue, agentv1.SubjectRoot+".agent.#", resultExchange, false, nil); err != nil {
		return fmt.Errorf("bind result queue: %w", err)
	}
	return nil
}

func (b *Bus) connection() (*amqp.Connection, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.conn == nil || b.conn.IsClosed() {
		return nil, bus.ErrUnavailable
	}
	return b.conn, nil
}

// EnsureAgent declares the agent's durable command queue. It is idempotent.
func (b *Bus) EnsureAgent(_ context.Context, agentID string) error {
	name, err := CommandQueue(agentID)
	if err != nil {
		return err
	}
	conn, err := b.connection()
	if err != nil {
		return err
	}
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer func() { _ = ch.Close() }()
	if _, err := ch.QueueDeclare(name, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare command queue: %w", err)
	}
	b.ensured.Store(agentID, struct{}{})
	return nil
}

// Publish stores payload in the agent's queue and returns after the broker
// confirmed it. The same msgID for the same agent within DedupeWindow is
// stored once (tracked in this process only).
func (b *Bus) Publish(ctx context.Context, agentID, msgID string, payload []byte) error {
	name, err := CommandQueue(agentID)
	if err != nil {
		return err
	}
	if !b.Ready() {
		return bus.ErrUnavailable
	}
	if _, ok := b.ensured.Load(agentID); !ok { // a queue must exist or the broker drops the message
		if err := b.EnsureAgent(ctx, agentID); err != nil {
			return err
		}
	}
	b.pubMu.Lock()
	defer b.pubMu.Unlock()
	if b.pub == nil {
		return bus.ErrUnavailable
	}
	now := time.Now()
	key := agentID + "\x00" + msgID
	for k, at := range b.seen {
		if now.Sub(at) > b.cfg.DedupeWindow {
			delete(b.seen, k)
		}
	}
	if msgID != "" {
		if _, dup := b.seen[key]; dup {
			return nil
		}
	}
	if err := b.pub.PublishWithContext(ctx, "", name, false, false, amqp.Publishing{
		DeliveryMode: amqp.Persistent, MessageId: msgID, Body: payload, Timestamp: now,
	}); err != nil {
		return fmt.Errorf("publish command: %w", err)
	}
	select {
	case c, ok := <-b.confirms:
		if !ok || !c.Ack {
			return errors.New("broker did not confirm the command")
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	if msgID != "" {
		b.seen[key] = now
	}
	return nil
}

// Next returns the agent's next command, polling the queue for up to wait.
// Each delivery lives on its own channel: acknowledging or rejecting it
// closes the channel, and so does AckWait, which makes the broker return an
// unacknowledged command to the queue.
func (b *Bus) Next(ctx context.Context, agentID string, wait time.Duration) (bus.Delivery, bool, error) {
	name, err := CommandQueue(agentID)
	if err != nil {
		return bus.Delivery{}, false, err
	}
	conn, err := b.connection()
	if err != nil {
		return bus.Delivery{}, false, err
	}
	ch, err := conn.Channel()
	if err != nil {
		return bus.Delivery{}, false, err
	}
	deadline := time.Now().Add(wait)
	for {
		msg, ok, err := ch.Get(name, false)
		if err != nil {
			_ = ch.Close()
			return bus.Delivery{}, false, err
		}
		if ok {
			return b.delivery(ch, msg), true, nil
		}
		if !time.Now().Before(deadline) {
			_ = ch.Close()
			return bus.Delivery{}, false, nil
		}
		select {
		case <-ctx.Done():
			_ = ch.Close()
			return bus.Delivery{}, false, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func (b *Bus) delivery(ch *amqp.Channel, msg amqp.Delivery) bus.Delivery {
	var once sync.Once
	timer := time.AfterFunc(b.cfg.AckWait, func() { once.Do(func() { _ = ch.Close() }) })
	settle := func(what string, op func() error) func() {
		return func() {
			once.Do(func() {
				timer.Stop()
				if err := op(); err != nil {
					b.logger.Warn("settle agent command", "action", what, "error", err)
				}
				_ = ch.Close() // returns anything still unacknowledged
			})
		}
	}
	return bus.Delivery{
		Payload: msg.Body,
		Ack:     settle("ack", func() error { return msg.Ack(false) }),
		Nack:    settle("nack", func() error { return msg.Nack(false, true) }),
	}
}

// agentIDFromRoutingKey extracts the agent ID from a result routing key.
func agentIDFromRoutingKey(key string) (string, bool) {
	const prefix, suffix = agentv1.SubjectRoot + ".agent.", ".result"
	if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(key, prefix), suffix)
	if expected, err := agentv1.ResultSubject(id); err != nil || expected != key {
		return "", false
	}
	return id, true
}
