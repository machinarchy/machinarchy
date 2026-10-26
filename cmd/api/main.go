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

// @title           Machinarchy API
// @version         1.0
// @description     HTTP API for managing Machinarchy environments, SSH hosts, and job history.
// @termsOfService  https://github.com/machinarchy/machinarchy
// @BasePath        /
// @securityDefinitions.apikey BearerAuth
// @in              header
// @name            Authorization
// @description     Supply a JWT as `Bearer <token>`. The API also accepts the auth_token cookie.
func main() {
	if err := run(); err != nil {
		slog.Error("API server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	bootstrap.ConfigureLogging(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	telemetryShutdown, err := telemetry.Setup(ctx, "machinarchy-api")
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

	server, err := runtime.NewAPIServer(cfg)
	if err != nil {
		return fmt.Errorf("construct API server: %w", err)
	}

	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Start(cfg.ListenAddr) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown API server: %w", err)
		}
		return nil
	case err := <-serverErrors:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
			err = errors.Join(err, fmt.Errorf("shutdown API server: %w", shutdownErr))
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve API: %w", err)
		}
		return errors.New("API server stopped unexpectedly")
	}
}

func shutdownTelemetry(shutdown func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		slog.Error("shutdown telemetry", "error", err)
	}
}
