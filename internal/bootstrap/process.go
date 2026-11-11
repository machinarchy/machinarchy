package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/config"
	"github.com/machinarchy/machinarchy/internal/telemetry"
)

const (
	componentShutdownTimeout  = 10 * time.Second
	apiServiceName            = "machinarchy-api"
	controlManagerServiceName = "machinarchy-controlmanager"
	monolithServiceName       = "machinarchy-monolit"
)

type httpLifecycle interface {
	StartWithReady(address string, ready func()) error
	Shutdown(context.Context) error
}

type workerLifecycle interface {
	Start(context.Context) error
}

// RunAPI initializes and serves the standalone API process.
func RunAPI(ctx context.Context, cfg config.Config) error {
	return run(ctx, cfg, apiServiceName, func(runtime *Runtime) (httpLifecycle, workerLifecycle, error) {
		server, err := runtime.NewAPIServer(ctx, cfg)
		return server, nil, err
	})
}

// RunControlManager initializes the standalone Control Manager and its
// operations-only health server.
func RunControlManager(ctx context.Context, cfg config.Config) error {
	return run(ctx, cfg, controlManagerServiceName, func(runtime *Runtime) (httpLifecycle, workerLifecycle, error) {
		engine, err := runtime.NewControlManager()
		if err != nil {
			return nil, nil, fmt.Errorf("construct control manager: %w", err)
		}
		server, err := runtime.NewControlManagerOperationsServer(engine)
		if err != nil {
			return nil, nil, fmt.Errorf("construct control manager operations server: %w", err)
		}
		return server, engine, nil
	})
}

// RunMonolith composes the API and Control Manager over one Runtime. It does
// not duplicate their service or worker construction.
func RunMonolith(ctx context.Context, cfg config.Config) error {
	return run(ctx, cfg, monolithServiceName, func(runtime *Runtime) (httpLifecycle, workerLifecycle, error) {
		engine, err := runtime.NewControlManager()
		if err != nil {
			return nil, nil, fmt.Errorf("construct control manager: %w", err)
		}
		server, err := runtime.NewAPIServer(ctx, cfg, engine)
		if err != nil {
			return nil, nil, fmt.Errorf("construct API server: %w", err)
		}
		return server, engine, nil
	})
}

type componentFactory func(*Runtime) (httpLifecycle, workerLifecycle, error)

func run(ctx context.Context, cfg config.Config, serviceName string, factory componentFactory) (retErr error) {
	if ctx == nil {
		return errors.New("process context is required")
	}
	if factory == nil {
		return errors.New("process component factory is required")
	}
	if strings.TrimSpace(cfg.ListenAddr) == "" {
		return errors.New("process listen address is required")
	}

	shutdownTelemetry, err := initializeProcess(ctx, cfg, serviceName)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), componentShutdownTimeout)
		defer cancel()
		if err := shutdownTelemetry(shutdownCtx); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("shutdown telemetry: %w", err))
		}
	}()

	runtime, err := Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close runtime: %w", err))
		}
	}()

	server, worker, err := factory(runtime)
	if err != nil {
		return err
	}
	slog.Info("process components constructed", "service", serviceName,
		"listen_addr", cfg.ListenAddr, "database_engine", cfg.Database.Engine)
	return runComponents(ctx, cfg.ListenAddr, server, worker)
}

func initializeProcess(ctx context.Context, cfg config.Config, serviceName string) (func(context.Context) error, error) {
	if ctx == nil {
		return nil, errors.New("process context is required")
	}
	if strings.TrimSpace(serviceName) == "" {
		return nil, errors.New("telemetry service name is required")
	}
	ConfigureLogging(cfg.LogLevel)
	shutdown, err := telemetry.Setup(ctx, serviceName)
	if err != nil {
		return nil, fmt.Errorf("configure telemetry: %w", err)
	}
	return shutdown, nil
}

func runComponents(ctx context.Context, address string, server httpLifecycle, worker workerLifecycle) error {
	if ctx == nil {
		return errors.New("process context is required")
	}
	if strings.TrimSpace(address) == "" {
		return errors.New("process listen address is required")
	}
	if server == nil {
		return errors.New("process HTTP server is required")
	}

	processCtx, cancelProcess := context.WithCancel(ctx)
	defer cancelProcess()

	serverErrors := make(chan error, 1)
	serverReady := make(chan struct{})
	go func() { serverErrors <- server.StartWithReady(address, func() { close(serverReady) }) }()
	serverFinished := false
	workerFinished := false

	select {
	case <-serverReady:
	case err := <-serverErrors:
		serverFinished = true
		if ctx.Err() == nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), componentShutdownTimeout)
			defer cancel()
			return errors.Join(unexpectedComponentStop("HTTP server", err), server.Shutdown(shutdownCtx))
		}
		return nil
	case <-ctx.Done():
		// StartWithReady owns listener creation. Wait for it to either bind or
		// fail before shutdown so a late listener cannot escape cleanup.
		startupCtx, cancelStartup := context.WithTimeout(context.Background(), componentShutdownTimeout)
		defer cancelStartup()
		select {
		case <-serverReady:
		case err := <-serverErrors:
			serverFinished = true
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("start HTTP server during shutdown: %w", err)
			}
			return nil
		case <-startupCtx.Done():
			return fmt.Errorf("wait for HTTP listener startup during shutdown: %w", startupCtx.Err())
		}
	}

	var workerErrors chan error
	if worker != nil && ctx.Err() == nil {
		workerErrors = make(chan error, 1)
		go func() { workerErrors <- worker.Start(processCtx) }()
	}

	var componentErr error
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		serverFinished = true
		componentErr = unexpectedComponentStop("HTTP server", err)
	case err := <-workerErrors:
		workerFinished = true
		if ctx.Err() == nil {
			componentErr = unexpectedComponentStop("background worker", err)
		}
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), componentShutdownTimeout)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		componentErr = errors.Join(componentErr, fmt.Errorf("shutdown HTTP server: %w", err))
	}
	if !serverFinished {
		if err := waitForServer(shutdownCtx, serverErrors); err != nil {
			componentErr = errors.Join(componentErr, err)
		}
	}

	cancelProcess()
	if workerErrors != nil && !workerFinished {
		if err := waitForWorker(shutdownCtx, workerErrors); err != nil {
			componentErr = errors.Join(componentErr, err)
		}
	}
	return componentErr
}

func unexpectedComponentStop(name string, err error) error {
	if err == nil {
		return fmt.Errorf("%s stopped unexpectedly", name)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("%s stopped unexpectedly", name)
	}
	return fmt.Errorf("%s stopped unexpectedly: %w", name, err)
}

func waitForServer(ctx context.Context, errorsCh <-chan error) error {
	select {
	case err := <-errorsCh:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("HTTP server stopped: %w", err)
	case <-ctx.Done():
		return fmt.Errorf("wait for HTTP server shutdown: %w", ctx.Err())
	}
}

func waitForWorker(ctx context.Context, errorsCh <-chan error) error {
	select {
	case err := <-errorsCh:
		if err != nil {
			return fmt.Errorf("stop background worker: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for background worker shutdown: %w", ctx.Err())
	}
}
