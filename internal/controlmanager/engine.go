package controlmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

type EventPublisher interface {
	Publish(ctx context.Context, subject string, payload interface{}) error
	Subscribe(ctx context.Context, subject string, handler func(context.Context, []byte) error) error
}

type Engine struct {
	hostStore domain.HostStore
	publisher EventPublisher
	ready     atomic.Bool
}

func NewEngine(hostStore domain.HostStore, publisher EventPublisher) *Engine {
	return &Engine{
		hostStore: hostStore,
		publisher: publisher,
	}
}

func (e *Engine) ReadinessName() string { return "control-manager" }

func (e *Engine) CheckReadiness(ctx context.Context) error {
	if ctx == nil {
		return errors.New("readiness context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if e == nil || !e.ready.Load() {
		return fmt.Errorf("control-manager subscriptions are not active")
	}
	return nil
}

// HandleHostHeartbeat processes incoming telemetry events from agents or push mechanisms.
func (e *Engine) HandleHostHeartbeat(ctx context.Context, hostID int) error {
	if e == nil || e.hostStore == nil {
		return errors.New("control-manager host store is not configured")
	}
	if ctx == nil {
		return errors.New("heartbeat context is required")
	}
	host, err := e.hostStore.GetHostByID(ctx, hostID)
	if err != nil {
		return err
	}
	if host == nil || host.SSH == nil {
		return domain.ErrHostNotFound
	}

	// If the host was disconnected, transition it back to Active via CAS
	if host.SSH.Status == domain.HostStatusDisconnected {
		err = e.hostStore.UpdateHostStatusWithGuard(ctx, hostID, domain.HostStatusDisconnected, domain.HostStatusActive)
		if err != nil {
			return err
		}

		// Publish event to the event mesh so other components react
		event := domain.HostReconnectedEvent{
			HostID:    hostID,
			Hostname:  host.Hostname,
			IP:        host.IPAddress,
			Timestamp: time.Now().UTC(),
		}
		if e.publisher == nil {
			return errors.New("control-manager event publisher is not configured")
		}
		if pubErr := e.publisher.Publish(ctx, "machinarchy.host.reconnected", event); pubErr != nil {
			slog.Error("Failed to publish host reconnected event", "error", pubErr)
		}

		slog.Info("Host successfully re-integrated into the mesh", "host_id", hostID)
	}

	return nil
}

func (e *Engine) Start(ctx context.Context) error {
	if e == nil || e.publisher == nil {
		return errors.New("control-manager event publisher is not configured")
	}
	if ctx == nil {
		return errors.New("control-manager context is required")
	}
	slog.Info("Control-manager engine initializing event subscriptions...")

	// Example: Subscribe to host reconnected events
	err := e.publisher.Subscribe(ctx, "machinarchy.host.reconnected", func(c context.Context, data []byte) error {
		var event domain.HostReconnectedEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("unmarshal host reconnected event: %w", err)
		}

		slog.Info("Received HostReconnectedEvent in background worker",
			"host_id", event.HostID,
			"hostname", event.Hostname,
		)

		// TODO: Add your background state machine or reconciliation logic here!
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to subscribe to host reconnected events: %w", err)
	}
	e.ready.Store(true)
	defer e.ready.Store(false)

	slog.Info("Control-manager background listeners active and waiting for signals.")

	// Block until the graceful shutdown signal cancels the context
	<-ctx.Done()

	slog.Info("Control-manager engine stopping background workers.")
	return nil
}
