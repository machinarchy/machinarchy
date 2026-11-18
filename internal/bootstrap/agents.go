package bootstrap

import (
	"crypto/ed25519"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"

	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/config"
	"github.com/machinarchy/machinarchy/internal/store"
)

// newAgentGateway builds the gateway when it is configured, or returns nil.
func newAgentGateway(cfg config.AgentGatewayConfig, stores *store.Stores, registry prometheus.Registerer) (*agentgateway.Gateway, string, error) {
	if !cfg.Enabled() {
		return nil, "", nil
	}
	key := ed25519.NewKeyFromSeed(cfg.SigningKeySeed)
	var options []nats.Option
	if cfg.NATSCredentialsFile != "" {
		options = append(options, nats.UserCredentials(cfg.NATSCredentialsFile))
	}
	if cfg.NATSCAFile != "" {
		options = append(options, nats.RootCAs(cfg.NATSCAFile))
	}
	if cfg.NATSCertFile != "" {
		options = append(options, nats.ClientCert(cfg.NATSCertFile, cfg.NATSKeyFile))
	}
	gateway, err := agentgateway.New(stores.Agents, agentgateway.Config{
		NATSURL: cfg.NATSURL, NATSOptions: options, SigningKey: key, EnrollmentToken: cfg.EnrollmentToken,
		LeaseTTL: cfg.LeaseTTL, Heartbeat: cfg.Heartbeat, Logger: slog.Default(), Registerer: registry,
	})
	if err != nil {
		return nil, "", fmt.Errorf("configure agent gateway: %w", err)
	}
	publicKey := agentv1.EncodePublicKey(key.Public().(ed25519.PublicKey))
	slog.Info("agent gateway configured", "controller_public_key", publicKey)
	return gateway, publicKey, nil
}
