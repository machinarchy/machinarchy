package agentgateway

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// NATSIssuer issues per-agent NATS user JWTs when NATS runs in operator (JWT)
// mode (AUDIT M-10). Signer is a signing key of Account; the account JWT must
// list it. Each JWT's subject is the agent's Ed25519 identity key as a NATS
// user nkey, so the agent proves possession with the key it already has, and
// its permissions are agentv1.AgentPermissions.
type NATSIssuer struct {
	Account string
	Signer  nkeys.KeyPair
	TTL     time.Duration
}

// Bounds for the agent JWT lifetime. Revocation at the broker takes effect
// when the current JWT expires, so shorter is safer; each expiry costs the
// agent one reconnect.
const (
	DefaultAgentJWTTTL = time.Hour
	MinAgentJWTTTL     = 5 * time.Minute
	MaxAgentJWTTTL     = 24 * time.Hour
)

func (i *NATSIssuer) validate(heartbeat time.Duration) error {
	if i.Signer == nil || !nkeys.IsValidPublicAccountKey(i.Account) {
		return errors.New("NATS JWT issuing needs an account public key and an account signing key")
	}
	if public, err := i.Signer.PublicKey(); err != nil || !nkeys.IsValidPublicAccountKey(public) {
		return errors.New("the NATS signing key must be an account key (seed starting with SA)")
	}
	if i.TTL < MinAgentJWTTTL || i.TTL > MaxAgentJWTTTL {
		return fmt.Errorf("the agent NATS JWT lifetime must be between %s and %s", MinAgentJWTTTL, MaxAgentJWTTTL)
	}
	if i.TTL < 2*heartbeat {
		return errors.New("the agent NATS JWT lifetime must be at least twice the heartbeat, so renewals refresh it in time")
	}
	return nil
}

// mint returns a NATS user JWT for the agent.
func (i *NATSIssuer) mint(agentKey ed25519.PublicKey, agentID string, now time.Time) (string, error) {
	if len(agentKey) != ed25519.PublicKeySize {
		return "", errors.New("agent key is not an Ed25519 public key")
	}
	userKey, err := nkeys.Encode(nkeys.PrefixByteUser, agentKey)
	if err != nil {
		return "", err
	}
	permissions, err := agentv1.AgentPermissions(agentID)
	if err != nil {
		return "", err
	}
	claims := jwt.NewUserClaims(string(userKey))
	claims.Name = agentID
	claims.IssuerAccount = i.Account
	claims.IssuedAt = now.Unix()
	claims.Expires = now.Add(i.TTL).Unix()
	claims.Pub.Allow.Add(permissions.Publish...)
	claims.Sub.Allow.Add(permissions.Subscribe...)
	return claims.Encode(i.Signer)
}

// ControllerPermissions are the NATS permissions of the gateway's own user:
// serve registration, renewal and results, publish commands and use the
// JetStream API, and read replies only under ControllerInboxPrefix. The user
// also needs "allow responses" to answer agent requests, whose reply subjects
// are in the agents' inboxes. deployments/nats/generate creates the
// controller user with exactly these permissions.
func ControllerPermissions() agentv1.Permissions {
	return agentv1.Permissions{
		Publish: []string{agentv1.SubjectRoot + ".>", "$JS.API.>"},
		Subscribe: []string{agentv1.SubjectRegister, agentv1.SubjectLeaseRenew, agentv1.SubjectAllResults,
			agentv1.ControllerInboxPrefix + ".>"},
	}
}
