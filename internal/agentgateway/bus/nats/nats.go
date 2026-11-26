// Package nats is the NATS JetStream adapter of the agent gateway bus. It is
// one of the two places that may import nats-io (the other is natsjwt.go in
// the gateway package); a test enforces that.
package nats

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
	"github.com/machinarchy/machinarchy/internal/agentgateway/bus"
)

const (
	streamDuplicateWindow = 15 * time.Minute
	ackWait               = 30 * time.Second
	maxDeliver            = 20
)

// Config configures the adapter. Options carries authentication and TLS.
type Config struct {
	URL            string
	Options        []natsgo.Option
	ConnectTimeout time.Duration
	Logger         *slog.Logger
}

// Bus is the NATS adapter.
type Bus struct {
	cfg       Config
	logger    *slog.Logger
	js        jetstream.JetStream
	connected atomic.Bool
}

var _ bus.Bus = (*Bus)(nil)

// New validates cfg.
func New(cfg Config) (*Bus, error) {
	if cfg.URL == "" {
		return nil, errors.New("the NATS bus requires a URL")
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 5 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Bus{cfg: cfg, logger: cfg.Logger.With("component", "agent-gateway")}, nil
}

// Ready reports whether the connection is up and the stream provisioned.
func (b *Bus) Ready() bool { return b.connected.Load() && b.js != nil }

// Serve connects to NATS, provisions the command stream and serves until ctx
// ends; it then drains the connection.
func (b *Bus) Serve(ctx context.Context, h bus.Handlers) error {
	options := append([]natsgo.Option{natsgo.Name("machinarchy-agent-gateway"), natsgo.Timeout(b.cfg.ConnectTimeout),
		natsgo.MaxReconnects(-1),
		natsgo.DisconnectErrHandler(func(_ *natsgo.Conn, err error) {
			b.connected.Store(false)
			b.logger.Warn("agent gateway disconnected from NATS", "error", err)
		}),
		natsgo.ReconnectHandler(func(*natsgo.Conn) {
			b.connected.Store(true)
			b.logger.Info("agent gateway reconnected to NATS")
		}),
		// Keep replies to the controller (JetStream API answers included) out
		// of the _INBOX namespace that enrolling agents may read.
		natsgo.CustomInboxPrefix(agentv1.ControllerInboxPrefix)}, b.cfg.Options...)
	nc, err := natsgo.Connect(b.cfg.URL, options...)
	if err != nil {
		return fmt.Errorf("connect agent gateway to NATS: %w", err)
	}
	if b.js, err = jetstream.New(nc); err != nil {
		nc.Close()
		return fmt.Errorf("open JetStream: %w", err)
	}
	setupCtx, cancel := context.WithTimeout(ctx, b.cfg.ConnectTimeout)
	_, err = b.js.CreateOrUpdateStream(setupCtx, jetstream.StreamConfig{
		Name: agentv1.CommandStream, Subjects: []string{agentv1.SubjectAllCommands}, Storage: jetstream.FileStorage,
		MaxAge: 24 * time.Hour, Duplicates: streamDuplicateWindow,
	})
	cancel()
	if err != nil {
		nc.Close()
		return fmt.Errorf("provision agent command stream: %w", err)
	}
	respond := func(handle func(context.Context, []byte) []byte) natsgo.MsgHandler {
		return func(msg *natsgo.Msg) {
			if handle == nil {
				return
			}
			if reply := handle(context.Background(), msg.Data); reply != nil {
				if err := msg.Respond(reply); err != nil {
					b.logger.Warn("send agent reply", "error", err)
				}
			}
		}
	}
	result := func(msg *natsgo.Msg) {
		if h.Result == nil {
			return
		}
		agentID, ok := agentIDFromResultSubject(msg.Subject)
		if !ok {
			return
		}
		h.Result(context.Background(), agentID, msg.Data)
	}
	var subs []*natsgo.Subscription
	for subject, handler := range map[string]natsgo.MsgHandler{
		agentv1.SubjectRegister:   respond(h.Register),
		agentv1.SubjectLeaseRenew: respond(h.Renew),
		agentv1.SubjectAllResults: result,
	} {
		sub, err := nc.QueueSubscribe(subject, "machinarchy-agent-gateway", handler)
		if err != nil {
			nc.Close()
			return fmt.Errorf("subscribe %s: %w", subject, err)
		}
		subs = append(subs, sub)
	}
	if err := nc.Flush(); err != nil {
		nc.Close()
		return fmt.Errorf("flush agent gateway subscriptions: %w", err)
	}
	b.connected.Store(true)
	b.logger.Info("agent gateway serving", "nats_url", nc.ConnectedUrlRedacted())

	<-ctx.Done()
	b.connected.Store(false)
	for _, sub := range subs {
		if err := sub.Unsubscribe(); err != nil {
			b.logger.Debug("unsubscribe agent gateway subject", "subject", sub.Subject, "error", err)
		}
	}
	if err := nc.Drain(); err != nil {
		b.logger.Warn("drain agent gateway connection", "error", err)
		nc.Close()
	}
	return nil
}

// EnsureAgent creates the agent's durable pull consumer (M-6).
func (b *Bus) EnsureAgent(ctx context.Context, agentID string) error {
	if b.js == nil {
		return bus.ErrUnavailable
	}
	subject, err := agentv1.CommandSubject(agentID)
	if err != nil {
		return err
	}
	name, err := agentv1.CommandConsumer(agentID)
	if err != nil {
		return err
	}
	_, err = b.js.CreateOrUpdateConsumer(ctx, agentv1.CommandStream, jetstream.ConsumerConfig{
		Durable: name, FilterSubject: subject, AckPolicy: jetstream.AckExplicitPolicy,
		AckWait: ackWait, MaxDeliver: maxDeliver, DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	return err
}

// Next pulls one command from the agent's durable consumer.
func (b *Bus) Next(ctx context.Context, agentID string, wait time.Duration) (bus.Delivery, bool, error) {
	if !b.Ready() {
		return bus.Delivery{}, false, bus.ErrUnavailable
	}
	name, err := agentv1.CommandConsumer(agentID)
	if err != nil {
		return bus.Delivery{}, false, err
	}
	consumer, err := b.js.Consumer(ctx, agentv1.CommandStream, name)
	if err != nil {
		return bus.Delivery{}, false, err
	}
	batch, err := consumer.Fetch(1, jetstream.FetchMaxWait(wait))
	if err != nil {
		return bus.Delivery{}, false, err
	}
	for msg := range batch.Messages() {
		settle := func(settle func() error, what string) func() {
			return func() {
				if err := settle(); err != nil {
					b.logger.Warn("settle agent command", "action", what, "error", err)
				}
			}
		}
		return bus.Delivery{Payload: msg.Data(), Ack: settle(msg.Ack, "ack"), Nack: settle(msg.Nak, "nack")}, true, nil
	}
	return bus.Delivery{}, false, batch.Error()
}

// Publish stores payload on the agent's subject with Nats-Msg-Id = msgID and
// returns after the PubAck; publishing the same ID again within the duplicate
// window is ignored by JetStream (ADR-057, M-7).
func (b *Bus) Publish(ctx context.Context, agentID, msgID string, payload []byte) error {
	if !b.Ready() {
		return bus.ErrUnavailable
	}
	subject, err := agentv1.CommandSubject(agentID)
	if err != nil {
		return err
	}
	_, err = b.js.Publish(ctx, subject, payload, jetstream.WithMsgID(msgID))
	return err
}

// agentIDFromResultSubject extracts the agent ID from a result subject. The
// subject is the transport-authenticated identity of the sender.
func agentIDFromResultSubject(subject string) (string, bool) {
	const prefix, suffix = agentv1.SubjectRoot + ".agent.", ".result"
	if !strings.HasPrefix(subject, prefix) || !strings.HasSuffix(subject, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(subject, prefix), suffix)
	if expected, err := agentv1.ResultSubject(id); err != nil || expected != subject {
		return "", false
	}
	return id, true
}
