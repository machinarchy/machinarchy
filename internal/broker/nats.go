package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type EventBroker struct {
	nc *nats.Conn
	js nats.JetStreamContext
}

func (b *EventBroker) ReadinessName() string { return "event-broker" }

func (b *EventBroker) CheckReadiness(ctx context.Context) error {
	if b == nil || b.nc == nil || b.js == nil {
		return errors.New("NATS JetStream is not initialized")
	}
	if ctx == nil {
		return errors.New("readiness context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !b.nc.IsConnected() {
		return errors.New("NATS connection is not connected")
	}
	if err := b.nc.FlushWithContext(ctx); err != nil {
		return fmt.Errorf("flush NATS connection: %w", err)
	}
	if _, err := b.js.AccountInfo(nats.Context(ctx)); err != nil {
		return fmt.Errorf("query JetStream account: %w", err)
	}
	if _, err := b.js.StreamInfo("MACHINARCHY", nats.Context(ctx)); err != nil {
		return fmt.Errorf("inspect MACHINARCHY stream: %w", err)
	}
	return nil
}

func NewEventBroker(natsURL string) (*EventBroker, error) {
	nc, err := nats.Connect(natsURL,
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("nats connection failed: %w", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("nats jetstream context failed: %w", err)
	}

	// Bound retention so an idle consumer cannot fill JetStream storage forever.
	streamConfig := &nats.StreamConfig{
		Name:      "MACHINARCHY",
		Subjects:  []string{"machinarchy.>"},
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
		Discard:   nats.DiscardOld,
		MaxAge:    7 * 24 * time.Hour,
		MaxBytes:  10 * 1024 * 1024 * 1024,
		MaxMsgs:   10_000_000,
	}
	_, err = js.AddStream(streamConfig)
	if errors.Is(err, nats.ErrStreamNameAlreadyInUse) {
		info, infoErr := js.StreamInfo(streamConfig.Name)
		if infoErr != nil {
			nc.Close()
			return nil, fmt.Errorf("inspect existing JetStream stream: %w", infoErr)
		}
		if !compatibleBoundedStream(info.Config, *streamConfig) {
			nc.Close()
			return nil, errors.New("existing MACHINARCHY stream does not match the required subjects and bounded retention policy")
		}
	} else if err != nil {
		nc.Close()
		return nil, fmt.Errorf("failed to create jetstream stream: %w", err)
	}

	return &EventBroker{nc: nc, js: js}, nil
}

func compatibleBoundedStream(existing, required nats.StreamConfig) bool {
	return existing.Name == required.Name &&
		slices.Equal(existing.Subjects, required.Subjects) &&
		existing.Storage == required.Storage &&
		existing.Retention == required.Retention &&
		existing.Discard == required.Discard &&
		existing.MaxAge > 0 && existing.MaxAge <= required.MaxAge &&
		existing.MaxBytes > 0 && existing.MaxBytes <= required.MaxBytes &&
		existing.MaxMsgs > 0 && existing.MaxMsgs <= required.MaxMsgs
}

func (b *EventBroker) Publish(ctx context.Context, subject string, payload interface{}) error {
	if b == nil || b.js == nil {
		return errors.New("NATS event broker is not initialized")
	}
	if ctx == nil {
		return errors.New("publish context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, span := otel.Tracer("machinarchy/internal/broker").Start(ctx, "nats.publish",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(attribute.String("messaging.destination.name", subject)),
	)
	defer span.End()
	data, err := json.Marshal(payload)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "marshal event")
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	message := nats.NewMsg(subject)
	message.Data = data
	message.Header = make(nats.Header)
	otel.GetTextMapPropagator().Inject(ctx, natsHeaderCarrier{headers: message.Header})
	_, err = b.js.PublishMsg(message, nats.Context(ctx))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "publish event")
		return fmt.Errorf("failed to publish event to subject %s: %w", subject, err)
	}
	return nil
}

func (b *EventBroker) Subscribe(ctx context.Context, subject string, handler func(context.Context, []byte) error) error {
	if b == nil || b.js == nil {
		return errors.New("NATS event broker is not initialized")
	}
	if handler == nil {
		return errors.New("NATS event handler is required")
	}
	if ctx == nil {
		return errors.New("subscription context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(subject) == "" {
		return errors.New("NATS subject is required")
	}
	subscription, err := b.js.Subscribe(subject, func(message *nats.Msg) {
		messageCtx := otel.GetTextMapPropagator().Extract(ctx, natsHeaderCarrier{headers: message.Header})
		messageCtx, span := otel.Tracer("machinarchy/internal/broker").Start(messageCtx, "nats.consume",
			trace.WithSpanKind(trace.SpanKindConsumer),
			trace.WithAttributes(attribute.String("messaging.destination.name", message.Subject)),
		)
		defer span.End()
		if err := handler(messageCtx, message.Data); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "handle event")
			slog.Error("NATS event handler failed", "subject", message.Subject, "error", err)
			if nakErr := message.Nak(); nakErr != nil {
				slog.Error("NATS event negative acknowledgment failed", "subject", message.Subject, "error", nakErr)
			}
			return
		}
		if err := message.Ack(); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "acknowledge event")
			slog.Error("NATS event acknowledgment failed", "subject", message.Subject, "error", err)
		}
	}, nats.ManualAck(), nats.Durable("machinarchy-control-manager"),
		nats.AckWait(30*time.Second), nats.MaxDeliver(5), nats.MaxAckPending(100))
	if err != nil {
		return fmt.Errorf("subscribe to NATS subject %s: %w", subject, err)
	}
	context.AfterFunc(ctx, func() {
		if err := subscription.Unsubscribe(); err != nil {
			slog.Warn("unsubscribe from NATS subject", "subject", subject, "error", err)
		}
	})
	return nil
}

func (b *EventBroker) Close() {
	if b != nil && b.nc != nil {
		b.nc.Close()
	}
}

type natsHeaderCarrier struct {
	headers nats.Header
}

func (c natsHeaderCarrier) Get(key string) string {
	return c.headers.Get(key)
}

func (c natsHeaderCarrier) Set(key, value string) {
	if c.headers != nil {
		c.headers.Set(key, value)
	}
}

func (c natsHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c.headers))
	for key := range c.headers {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

var _ propagation.TextMapCarrier = natsHeaderCarrier{}
