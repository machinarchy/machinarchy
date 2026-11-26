package agentgateway

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
	"github.com/machinarchy/machinarchy/internal/agentgateway/bus"
)

// This file is the surface the agent-facing transport (internal/agentgateway/
// server, ADR-059) uses. The transport authenticates the caller; the bus is
// never exposed to it beyond Next.

// ErrNotAuthorized reports a request whose signature, agent or lease does not
// check out. Callers answer it without detail.
var ErrNotAuthorized = errors.New("agent request is not authorized")

// ErrRateLimited reports an authenticated agent that sends too fast.
var ErrRateLimited = errors.New("agent is sending too fast")

// Register handles a signed registration; nil means "send no answer".
func (g *Gateway) Register(ctx context.Context, data []byte) []byte {
	return g.handleRegister(ctx, data)
}

// Renew handles a signed lease renewal; nil means "send no answer".
func (g *Gateway) Renew(ctx context.Context, data []byte) []byte { return g.handleRenew(ctx, data) }

// liveAgent returns the agent when it is enrolled, not revoked and holds an
// unexpired lease.
func (g *Gateway) liveAgent(ctx context.Context, agentID string) (*Agent, error) {
	agent, err := g.store.GetAgent(ctx, agentID)
	if err != nil || agent.RevokedAt != nil || !time.Now().Before(agent.LeaseExpiresAt) {
		return nil, ErrNotAuthorized
	}
	return agent, nil
}

// ReceiveResult verifies a result signed by an enrolled agent that holds a
// live lease and hands it to the result handler. The agent identity is the
// signed agent ID: a result signed by agent A that names agent B fails the
// signature check against B's key.
// allow, when set, is asked once the agent is authenticated and before the
// result is processed; false yields ErrRateLimited.
func (g *Gateway) ReceiveResult(ctx context.Context, data []byte, allow func(agentID string) bool) error {
	var result agentv1.Result
	if err := agentv1.OpenFromAgent(agentv1.KindResult, data, agentv1.MaxResultMessageBytes, g.keyFor, &result); err != nil {
		g.metrics.results.WithLabelValues("invalid").Inc()
		return ErrNotAuthorized
	}
	if _, err := g.liveAgent(ctx, result.AgentID); err != nil {
		g.metrics.results.WithLabelValues("invalid").Inc()
		return err
	}
	if allow != nil && !allow(result.AgentID) {
		return ErrRateLimited
	}
	g.processResult(result.AgentID, data)
	return nil
}

// AuthenticateStream checks the request that opens a command stream: a
// renewal-shaped message signed by the agent key that names the agent's
// current lease and was sent within the allowed clock skew. A lease ID copied
// from a response is useless without the agent key. It returns the agent ID
// the stream is bound to.
func (g *Gateway) AuthenticateStream(ctx context.Context, data []byte) (string, error) {
	var request agentv1.RenewRequest
	if err := agentv1.OpenFromAgent(agentv1.KindRenewRequest, data, agentv1.MaxControlMessageBytes, g.keyFor, &request); err != nil {
		return "", ErrNotAuthorized
	}
	now := time.Now()
	if d := now.Sub(request.SentAt); d > agentv1.MaxClockSkew || d < -agentv1.MaxClockSkew {
		return "", ErrNotAuthorized
	}
	agent, err := g.liveAgent(ctx, request.AgentID)
	if err != nil || agent.LeaseID != request.LeaseID {
		return "", ErrNotAuthorized
	}
	if err := g.provision(agent.ID); err != nil {
		return "", ErrUnavailable
	}
	return agent.ID, nil
}

// StreamValid reports whether an open stream may keep delivering: the agent
// is still enrolled, not revoked and its (renewed) lease has not expired.
func (g *Gateway) StreamValid(ctx context.Context, agentID string) bool {
	_, err := g.liveAgent(ctx, agentID)
	return err == nil
}

// NextCommand returns the agent's next queued command, waiting up to wait.
func (g *Gateway) NextCommand(ctx context.Context, agentID string, wait time.Duration) (bus.Delivery, bool, error) {
	return g.bus.Next(ctx, agentID, wait)
}

// ControllerPublicKey is the key agents pin.
func (g *Gateway) ControllerPublicKey() ed25519.PublicKey {
	if public, ok := g.signer().Public().(ed25519.PublicKey); ok {
		return public
	}
	return nil
}
