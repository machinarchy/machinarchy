package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/machinarchy/machinarchy/internal/bootstrap"
	"github.com/machinarchy/machinarchy/internal/config"
	"github.com/machinarchy/machinarchy/internal/telemetry"
)

func main() {
	if err := run(); err != nil {
		slog.Error("control manager stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	bootstrap.ConfigureLogging(cfg.LogLevel)

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()
	telemetryShutdown, err := telemetry.Setup(ctx, "machinarchy-controlmanager")
	if err != nil {
		return fmt.Errorf("configure telemetry: %w", err)
	}
	defer shutdownTelemetry(telemetryShutdown)

	runtime, err := bootstrap.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			slog.Error("close runtime", "error", err)
		}
	}()

	engine, err := runtime.NewControlManager()
	if err != nil {
		return fmt.Errorf("construct control manager: %w", err)
	}
	server, err := runtime.NewControlManagerOperationsServer(engine)
	if err != nil {
		return fmt.Errorf("construct control manager operations server: %w", err)
	}

	engineErrors := make(chan error, 1)
	go func() { engineErrors <- engine.Start(ctx) }()
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Start(cfg.ListenAddr) }()
	slog.Info("control manager started", "health_listen_addr", cfg.ListenAddr,
		"database_engine", cfg.Database.Engine)

	var (
		componentErr   error
		engineFinished bool
	)
	select {
	case <-signalCtx.Done():
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			componentErr = fmt.Errorf("serve control manager operations: %w", err)
		} else {
			componentErr = errors.New("control manager operations server stopped unexpectedly")
		}
	case err := <-engineErrors:
		engineFinished = true
		if err != nil {
			componentErr = fmt.Errorf("run control manager engine: %w", err)
		} else if signalCtx.Err() == nil {
			componentErr = errors.New("control manager engine stopped unexpectedly")
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		componentErr = errors.Join(componentErr, fmt.Errorf("shutdown operations server: %w", err))
	}
	cancel()
	if !engineFinished {
		select {
		case err := <-engineErrors:
			if err != nil {
				componentErr = errors.Join(componentErr, fmt.Errorf("stop control manager engine: %w", err))
			}
		case <-shutdownCtx.Done():
			componentErr = errors.Join(componentErr,
				fmt.Errorf("control manager engine did not stop: %w", shutdownCtx.Err()))
		}
	}
	return componentErr
}

func shutdownTelemetry(shutdown func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		slog.Error("shutdown telemetry", "error", err)
	}
}
