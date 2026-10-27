// Package bootstrap assembles the infrastructure adapters shared by Machinarchy
// process entry points.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/machinarchy/machinarchy/internal/broker"
	"github.com/machinarchy/machinarchy/internal/config"
	"github.com/machinarchy/machinarchy/internal/controlmanager"
	"github.com/machinarchy/machinarchy/internal/service"
	"github.com/machinarchy/machinarchy/internal/store"
	httpserver "github.com/machinarchy/machinarchy/internal/transport/http"
	httpv1 "github.com/machinarchy/machinarchy/internal/transport/http/v1"
)

// Runtime owns the process-local database and event broker connections.
// Close it only after all servers and workers using it have stopped.
type Runtime struct {
	stores      *store.Stores
	eventBroker *broker.EventBroker
}

// Open initializes the shared persistence and event infrastructure.
func Open(ctx context.Context, cfg config.Config) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("runtime context is required")
	}

	stores, err := store.Open(ctx, store.Config{
		Engine: string(cfg.Database.Engine), SQLitePath: cfg.Database.Path,
		PostgresURL: cfg.Database.URL, MaxOpenConns: cfg.Database.MaxOpenConns,
		MaxIdleConns: cfg.Database.MaxIdleConns, ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
		ConnectTimeout: cfg.Database.ConnectTimeout, EncryptionSecret: cfg.EncryptionSecret,
	})
	if err != nil {
		return nil, fmt.Errorf("open datastore: %w", err)
	}

	eventBroker, err := broker.NewEventBroker(cfg.EventBroker.URL)
	if err != nil {
		closeErr := stores.Close()
		return nil, errors.Join(
			fmt.Errorf("connect event broker: %w", err),
			wrapCloseError("close datastore after broker startup failure", closeErr),
		)
	}

	return &Runtime{stores: stores, eventBroker: eventBroker}, nil
}

// Close releases shared infrastructure. Stop servers and workers before calling it.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	if r.eventBroker != nil {
		r.eventBroker.Close()
	}
	if r.stores != nil {
		if err := r.stores.Close(); err != nil {
			return fmt.Errorf("close datastore: %w", err)
		}
	}
	return nil
}

// NewControlManager creates the background event processor over this runtime's
// host store and event connection.
func (r *Runtime) NewControlManager() (*controlmanager.Engine, error) {
	if r == nil || r.stores == nil || r.eventBroker == nil {
		return nil, errors.New("runtime is not initialized")
	}
	return controlmanager.NewEngine(r.stores.Hosts, r.eventBroker), nil
}

// NewAPIServer constructs the API adapter. Additional readiness checks are
// included when the API and a worker run together in one process.
func (r *Runtime) NewAPIServer(
	cfg config.Config,
	additionalReadiness ...httpserver.ReadinessChecker,
) (*httpserver.Server, error) {
	if r == nil || r.stores == nil || r.eventBroker == nil {
		return nil, errors.New("runtime is not initialized")
	}

	sshCapacity, err := service.NewSSHCapacity(service.DefaultSSHConcurrency)
	if err != nil {
		return nil, fmt.Errorf("configure SSH concurrency limit: %w", err)
	}
	authService := service.NewAuthService(r.stores.Users, cfg.OperatorToken, 24*time.Hour)
	hostService, err := service.NewHostServiceWithCapacity(r.stores.Hosts, r.eventBroker, sshCapacity)
	if err != nil {
		return nil, fmt.Errorf("construct host service: %w", err)
	}
	metricsService, err := service.NewMetricsServiceWithCapacity(r.stores.Metrics, r.stores.Hosts, sshCapacity)
	if err != nil {
		return nil, fmt.Errorf("construct metrics service: %w", err)
	}
	repositories := httpv1.Repositories{
		Users: r.stores.Users, Metrics: r.stores.Metrics, Hosts: r.stores.Hosts,
		Jobs: r.stores.Jobs, Environments: r.stores.Environments, Access: r.stores.Access,
	}

	readiness := make([]httpserver.ReadinessChecker, 0, 1+len(additionalReadiness))
	readiness = append(readiness, r.eventBroker)
	readiness = append(readiness, additionalReadiness...)
	return httpserver.NewServer(cfg, authService, hostService, metricsService,
		sshCapacity, repositories, r.stores, readiness...)
}

// NewControlManagerOperationsServer creates the HTTP surface used for worker
// health probes and database/process metrics without registering API routes.
func (r *Runtime) NewControlManagerOperationsServer(
	engine *controlmanager.Engine,
) (*httpserver.Server, error) {
	if r == nil || r.stores == nil || r.eventBroker == nil {
		return nil, errors.New("runtime is not initialized")
	}
	return httpserver.NewOperationsServer(r.stores, r.eventBroker, engine)
}

// ConfigureLogging installs the common structured JSON logger.
func ConfigureLogging(level string) {
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout,
		&slog.HandlerOptions{Level: logLevel})))
}

func wrapCloseError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
