package bootstrap

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/agentkeys"
	"github.com/machinarchy/machinarchy/internal/config"
	"github.com/machinarchy/machinarchy/internal/store"
	"github.com/machinarchy/machinarchy/internal/transport/http/v1/agents"
)

// newAgentGateway builds the gateway when it is configured, or returns nil.
func newAgentGateway(ctx context.Context, cfg config.AgentGatewayConfig, stores *store.Stores, secretStore agentkeys.SecretStore,
	registry prometheus.Registerer) (*agentgateway.Gateway, *agentkeys.Manager, error) {
	if !cfg.Enabled() {
		return nil, nil, nil
	}
	// The signing key lives in the Secret Store (Phase 16). The key file, if
	// still configured, is imported once so enrolled agents keep working.
	keys, err := agentkeys.New(stores.Agents, secretStore, slog.Default())
	if err != nil {
		return nil, nil, err
	}
	var importSeed []byte
	if len(cfg.SigningKeySeed) == ed25519.SeedSize {
		importSeed = cfg.SigningKeySeed
	}
	if err := keys.Ensure(ctx, importSeed); err != nil {
		return nil, nil, fmt.Errorf("agent gateway signing key: %w", err)
	}
	publicKeys := keys.PublicKeys()
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
	var issuer *agentgateway.NATSIssuer
	if cfg.NATSAccount != "" {
		signer, err := nkeys.FromSeed(cfg.NATSAccountSigningKey)
		if err != nil {
			return nil, nil, fmt.Errorf("load the NATS account signing key: %w", err)
		}
		issuer = &agentgateway.NATSIssuer{Account: cfg.NATSAccount, Signer: signer, TTL: cfg.AgentJWTTTL}
	}
	gateway, err := agentgateway.New(stores.Agents, agentgateway.Config{
		NATSURL: cfg.NATSURL, NATSOptions: options, Signer: keys.Signer(), EnrollmentToken: cfg.EnrollmentToken,
		LeaseTTL: cfg.LeaseTTL, Heartbeat: cfg.Heartbeat, Logger: slog.Default(), Registerer: registry,
		NATSIssuer: issuer,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("configure agent gateway: %w", err)
	}
	attrs := []any{"controller_public_key", publicKeys[0], "per_agent_nats_credentials", issuer != nil}
	if len(publicKeys) > 1 {
		attrs = append(attrs, "next_controller_public_key", publicKeys[1])
	}
	slog.Info("agent gateway configured", attrs...)
	return gateway, keys, nil
}

// agentKeyAPI adapts the manager for the HTTP API; nil when the gateway is
// off, so the endpoints answer 503.
func agentKeyAPI(keys *agentkeys.Manager) agents.SigningKeys {
	if keys == nil {
		return nil
	}
	return keys
}
