package agentgateway

import (
	"context"
	"crypto/ed25519"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"

	"github.com/machinarchy/machinarchy/internal/testutil/natstest"
)

// In operator mode the gateway gives each agent a JWT for its own identity
// key, limited to agentv1.AgentPermissions; the agent can then work with it
// and nothing else (AUDIT M-10).
func TestGatewayIssuesScopedNATSCredentials(t *testing.T) {
	ns := natstest.Start(t)
	issuer := &NATSIssuer{Account: ns.Account, Signer: ns.Signer, TTL: 10 * time.Minute}
	f := startFixture(t, ns.URL, []nats.Option{nats.UserCredentials(ns.ControllerCredentials(t, ControllerPermissions()))}, issuer,
		[]nats.Option{nats.UserCredentials(ns.BootstrapCredentials(t))})

	agent := key(2)
	registered := f.register(agent, enrollmentToken, "req-1")
	if registered.Error != nil || registered.NATSUserJWT == "" {
		t.Fatalf("registration = %+v", registered)
	}
	claims, err := jwt.DecodeUserClaims(registered.NATSUserJWT)
	if err != nil {
		t.Fatal(err)
	}
	userKey, _ := nkeys.Encode(nkeys.PrefixByteUser, agent.Public().(ed25519.PublicKey))
	permissions, _ := agentv1.AgentPermissions(registered.AgentID)
	switch {
	case claims.Subject != string(userKey):
		t.Fatalf("JWT subject %s is not the agent's identity key", claims.Subject)
	case claims.IssuerAccount != ns.Account:
		t.Fatalf("issuer account = %s", claims.IssuerAccount)
	case !slices.Equal(claims.Pub.Allow, jwt.StringList(permissions.Publish)) || !slices.Equal(claims.Sub.Allow, jwt.StringList(permissions.Subscribe)):
		t.Fatalf("permissions = pub %v sub %v, want %+v", claims.Pub.Allow, claims.Sub.Allow, permissions)
	case time.Until(time.Unix(claims.Expires, 0)) > 10*time.Minute || time.Until(time.Unix(claims.Expires, 0)) < 9*time.Minute:
		t.Fatalf("expiry %v is not about 10 minutes away", time.Unix(claims.Expires, 0))
	}

	// Connect as the agent: the JWT plus the agent's own key.
	inbox, _ := agentv1.AgentInboxPrefix(registered.AgentID)
	violations := make(chan error, 8)
	signer, _ := nkeys.FromRawSeed(nkeys.PrefixByteUser, agent.Seed())
	agentConn, err := nats.Connect(ns.URL, nats.UserJWT(func() (string, error) { return registered.NATSUserJWT, nil }, signer.Sign),
		nats.CustomInboxPrefix(inbox), nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { violations <- err }))
	if err != nil {
		t.Fatalf("connect with the issued JWT: %v", err)
	}
	t.Cleanup(agentConn.Close)

	renewed := f.renewOn(agentConn, agent, registered.AgentID)
	if renewed == nil || renewed.Error != nil || renewed.NATSUserJWT == "" {
		t.Fatalf("renewal over the agent's own connection = %+v", renewed)
	}
	// A renewal in the same second can yield the same token; it must never
	// shorten the credential.
	if fresh, err := jwt.DecodeUserClaims(renewed.NATSUserJWT); err != nil || fresh.Expires < claims.Expires {
		t.Fatalf("renewed JWT expires %v (err %v), before the first one", fresh, err)
	}
	js, _ := jetstream.New(agentConn)
	consumer, _ := agentv1.CommandConsumer(registered.AgentID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := js.Consumer(ctx, agentv1.CommandStream, consumer); err != nil {
		t.Fatalf("binding its own consumer: %v", err)
	}
	if _, err := agentConn.SubscribeSync(agentv1.SubjectRegister); err != nil {
		t.Fatal(err)
	}
	_ = agentConn.Flush()
	select {
	case err := <-violations:
		if !strings.Contains(strings.ToLower(err.Error()), "permissions violation") {
			t.Fatalf("unexpected error %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the agent could subscribe to registration requests")
	}

	// A revoked agent gets no new credential.
	if err := f.gateway.Revoke(context.Background(), registered.AgentID); err != nil {
		t.Fatal(err)
	}
	if revoked := f.renewOn(agentConn, agent, registered.AgentID); revoked == nil || revoked.Error == nil || revoked.NATSUserJWT != "" {
		t.Fatalf("renewal after revocation = %+v", revoked)
	}
}

func TestNATSIssuerValidation(t *testing.T) {
	account, _ := nkeys.CreateAccount()
	accountPublic, _ := account.PublicKey()
	user, _ := nkeys.CreateUser()
	valid := Config{NATSURL: "nats://x:4222", SigningKey: key(1), EnrollmentToken: enrollmentToken}
	for name, tc := range map[string]struct {
		issuer *NATSIssuer
		ok     bool
	}{
		"default lifetime":   {&NATSIssuer{Account: accountPublic, Signer: account}, true},
		"user key signs":     {&NATSIssuer{Account: accountPublic, Signer: user}, false},
		"no signer":          {&NATSIssuer{Account: accountPublic}, false},
		"bad account":        {&NATSIssuer{Account: "UABC", Signer: account}, false},
		"lifetime too long":  {&NATSIssuer{Account: accountPublic, Signer: account, TTL: 48 * time.Hour}, false},
		"lifetime too short": {&NATSIssuer{Account: accountPublic, Signer: account, TTL: time.Minute}, false},
	} {
		cfg := valid
		cfg.NATSIssuer = tc.issuer
		if _, err := New(newMemStore(), cfg); (err == nil) != tc.ok {
			t.Errorf("%s: New() error = %v, want ok=%v", name, err, tc.ok)
		}
	}
}
