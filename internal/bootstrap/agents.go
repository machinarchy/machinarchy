package bootstrap

import (
	"crypto/ed25519"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
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
	var issuer *agentgateway.NATSIssuer
	if cfg.NATSAccount != "" {
		signer, err := nkeys.FromSeed(cfg.NATSAccountSigningKey)
		if err != nil {
			return nil, "", fmt.Errorf("load the NATS account signing key: %w", err)
		}
		issuer = &agentgateway.NATSIssuer{Account: cfg.NATSAccount, Signer: signer, TTL: cfg.AgentJWTTTL}
	}
	gateway, err := agentgateway.New(stores.Agents, agentgateway.Config{
		NATSURL: cfg.NATSURL, NATSOptions: options, SigningKey: key, EnrollmentToken: cfg.EnrollmentToken,
		LeaseTTL: cfg.LeaseTTL, Heartbeat: cfg.Heartbeat, Logger: slog.Default(), Registerer: registry,
		NATSIssuer: issuer,
	})
	if err != nil {
		return nil, "", fmt.Errorf("configure agent gateway: %w", err)
	}
	publicKey := agentv1.EncodePublicKey(key.Public().(ed25519.PublicKey))
	slog.Info("agent gateway configured", "controller_public_key", publicKey, "per_agent_nats_credentials", issuer != nil)
	return gateway, publicKey, nil
}
