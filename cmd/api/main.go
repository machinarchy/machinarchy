// Package main is the entry point for the api application.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/machinarchy/machinarchy/internal/bootstrap"
	"github.com/machinarchy/machinarchy/internal/config"
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
	cfg, err := config.LoadCommandLine(os.Args[1:], true)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return bootstrap.RunAPI(ctx, cfg)
}
