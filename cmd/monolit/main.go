// Package main is the entry point for the monolit application.
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

func main() {
	if err := run(); err != nil {
		slog.Error("Machinarchy monolith stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	return bootstrap.RunMonolith(signalCtx, cfg)
}
