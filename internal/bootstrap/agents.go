package bootstrap

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"errors"
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
func newAgentGateway(ctx context.Context, cfg config.AgentGatewayConfig, stores *store.Stores, registry prometheus.Registerer) (*agentgateway.Gateway, []string, error) {
	if !cfg.Enabled() {
		return nil, nil, nil
	}
	signer, err := newControllerSigner(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	current := signer.Public().(ed25519.PublicKey)
	publicKeys := []string{agentv1.EncodePublicKey(current)}
	if cfg.NextPublicKey != nil {
		if current.Equal(ed25519.PublicKey(cfg.NextPublicKey)) {
			return nil, nil, errors.New("MACHINARCHY_AGENT_GATEWAY_NEXT_PUBLIC_KEY is the key in use; announce a different key")
		}
		publicKeys = append(publicKeys, agentv1.EncodePublicKey(ed25519.PublicKey(cfg.NextPublicKey)))
	}
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
		NATSURL: cfg.NATSURL, NATSOptions: options, Signer: signer, EnrollmentToken: cfg.EnrollmentToken,
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
	return gateway, publicKeys, nil
}

// newControllerSigner returns the key the gateway signs with (AUDIT M-3).
// Phase 16.3 moves it into the Secret Store; a KMS signer can plug in through
// the same crypto.Signer interface later (K-phases).
func newControllerSigner(_ context.Context, cfg config.AgentGatewayConfig) (crypto.Signer, error) {
	if len(cfg.SigningKeySeed) != ed25519.SeedSize {
		return nil, errors.New("agent gateway signer: the signing key seed is missing")
	}
	return ed25519.NewKeyFromSeed(cfg.SigningKeySeed), nil
}
