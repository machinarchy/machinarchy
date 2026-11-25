package agentgateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

func (f *fixture) issue(hostname string, mutate func(*EnrollmentToken)) string {
	f.t.Helper()
	secret, token, err := NewEnrollmentToken(hostname, time.Hour, 1, time.Now())
	if err != nil {
		f.t.Fatal(err)
	}
	if mutate != nil {
		mutate(&token)
	}
	if err := f.store.CreateEnrollmentToken(context.Background(), token); err != nil {
		f.t.Fatal(err)
	}
	return secret
}

func rejected(r *agentv1.RegisterResponse) bool {
	return r.Error != nil && r.Error.Code == agentv1.ErrorEnrollmentRejected && r.AgentID == ""
}

// Owner-issued tokens enrol one host each (AUDIT M-4).
func TestPerHostEnrollmentTokens(t *testing.T) {
	f := newFixture(t)

	secret := f.issue("", nil)
	first := f.register(key(2), secret, "req-1")
	if first.Error != nil || first.AgentID == "" {
		t.Fatalf("registration with a fresh token = %+v", first)
	}
	if again := f.register(key(2), secret, "req-1"); again.AgentID != first.AgentID {
		t.Fatalf("a retried registration must succeed without a new token: %+v", again)
	}
	if reuse := f.register(key(3), secret, "req-2"); !rejected(reuse) {
		t.Fatalf("a used token enrolled a second host: %+v", reuse)
	}

	if other := f.register(key(4), f.issue("db-01.example", nil), "req-3"); !rejected(other) {
		t.Fatalf("a token bound to db-01.example enrolled web-01: %+v", other)
	}
	if bound := f.register(key(4), f.issue("WEB-01.", nil), "req-4"); bound.Error != nil {
		t.Fatalf("a token bound to this host (case and trailing dot ignored) was refused: %+v", bound)
	}
	expired := f.issue("", func(token *EnrollmentToken) { token.ExpiresAt = time.Now().Add(-time.Second) })
	if late := f.register(key(5), expired, "req-5"); !rejected(late) {
		t.Fatalf("an expired token was accepted: %+v", late)
	}
	revokedSecret, revoked, err := NewEnrollmentToken("", time.Hour, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateEnrollmentToken(context.Background(), revoked); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RevokeEnrollmentToken(context.Background(), revoked.ID, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if cancelled := f.register(key(6), revokedSecret, "req-6"); !rejected(cancelled) {
		t.Fatalf("a revoked token was accepted: %+v", cancelled)
	}
	if unknown := f.register(key(7), "mae_"+strings.Repeat("x", 43), "req-7"); !rejected(unknown) {
		t.Fatalf("an unknown token was accepted: %+v", unknown)
	}
}

// Without a shared token configured (production), only per-host tokens work.
func TestNoSharedTokenAcceptsOnlyIssuedTokens(t *testing.T) {
	f := newFixture(t)
	f.gateway.cfg.EnrollmentToken = ""
	if shared := f.register(key(2), enrollmentToken, "req-1"); !rejected(shared) {
		t.Fatalf("the former shared token was accepted: %+v", shared)
	}
	if issued := f.register(key(2), f.issue("", nil), "req-2"); issued.Error != nil {
		t.Fatalf("an issued token was refused: %+v", issued)
	}
}

func TestNewEnrollmentToken(t *testing.T) {
	now := time.Now()
	secret, token, err := NewEnrollmentToken(" Web-01.Example. ", 0, 7, now)
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case !strings.HasPrefix(secret, "mae_") || len(secret) < agentv1.MinEnrollmentToken:
		t.Fatalf("secret %q", secret)
	case token.Hash != HashEnrollmentToken(secret) || strings.Contains(token.Hash, secret):
		t.Fatal("only the hash may be stored")
	case token.Hostname != "web-01.example" || token.CreatedBy != 7:
		t.Fatalf("token = %+v", token)
	case !token.ExpiresAt.Equal(now.UTC().Add(DefaultEnrollmentTokenTTL)):
		t.Fatalf("expiry %v, want the 1h default", token.ExpiresAt)
	}
	other, _, _ := NewEnrollmentToken("", time.Hour, 7, now)
	if other == secret {
		t.Fatal("two tokens are equal")
	}
	for name, call := range map[string]func() error{
		"too short":    func() error { _, _, err := NewEnrollmentToken("", 10*time.Second, 7, now); return err },
		"too long":     func() error { _, _, err := NewEnrollmentToken("", 48*time.Hour, 7, now); return err },
		"bad hostname": func() error { _, _, err := NewEnrollmentToken("web_01", time.Hour, 7, now); return err },
		"label too long": func() error {
			_, _, err := NewEnrollmentToken(strings.Repeat("a", 64)+".example", time.Hour, 7, now)
			return err
		},
		"no issuer": func() error { _, _, err := NewEnrollmentToken("", time.Hour, 0, now); return err },
	} {
		if call() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
