package bootstrap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/agentgateway/server"
	"github.com/machinarchy/machinarchy/internal/config"
)

// agentGRPC serves the agent-facing gRPC service (ADR-059) on its own listener:
// agent streams are long-lived and must not share the API's write timeout.
type agentGRPC struct {
	addr     string
	certFile string
	keyFile  string
	server   *server.Server
	shutdown time.Duration
}

// newAgentGRPC returns nil when the gateway or the listener is not configured.
func newAgentGRPC(cfg config.AgentGatewayConfig, gateway *agentgateway.Gateway, shutdown time.Duration) (*agentGRPC, error) {
	if gateway == nil || cfg.GRPCAddr == "" {
		return nil, nil
	}
	srv, err := server.New(server.Config{Gateway: gateway, Logger: slog.Default()})
	if err != nil {
		return nil, fmt.Errorf("configure agent gRPC service: %w", err)
	}
	return &agentGRPC{addr: cfg.GRPCAddr, certFile: cfg.GRPCCertFile, keyFile: cfg.GRPCKeyFile, server: srv, shutdown: shutdown}, nil
}

// Start listens and serves until ctx ends, then stops gracefully within the
// shutdown budget.
func (a *agentGRPC) Start(ctx context.Context) error {
	listener, err := net.Listen("tcp", a.addr)
	if err != nil {
		return fmt.Errorf("listen for agents on %s: %w", a.addr, err)
	}
	if a.certFile != "" {
		certificate, err := tls.LoadX509KeyPair(a.certFile, a.keyFile)
		if err != nil {
			_ = listener.Close()
			return errors.New("load the agent gRPC TLS certificate: " + err.Error())
		}
		listener = tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13,
			NextProtos: []string{"h2"}})
	} else {
		slog.Warn("agent gRPC listener has no TLS (development only)", "addr", a.addr)
	}
	slog.Info("agent gRPC service listening", "addr", a.addr, "tls", a.certFile != "")
	done := make(chan error, 1)
	go func() { done <- a.server.Serve(listener) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	budget := a.shutdown
	if budget <= 0 {
		budget = 10 * time.Second
	}
	stopped := make(chan struct{})
	go func() { a.server.GracefulStop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(budget):
		a.server.Stop()
	}
	if err := <-done; err != nil {
		slog.Debug("agent gRPC service stopped", "error", err)
	}
	return nil
}
