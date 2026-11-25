package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/machinarchy/machinarchy/internal/domain"
)

// testSessions covers the user_sessions table and the session store (AUDIT S-08)
// on both engines.
func testSessions(t *testing.T, f *fixture) {
	ctx := context.Background()
	alice := f.member(t, "alice", domain.RoleOperator)
	bob := f.member(t, "bob", domain.RoleReader)
	now := time.Now().UTC().Truncate(time.Second)
	newSession := func(user *domain.User, created time.Time, ttl time.Duration) string {
		t.Helper()
		id, err := domain.NewSessionID()
		if err != nil {
			t.Fatal(err)
		}
		if err := f.stores.Sessions.CreateSession(ctx, domain.Session{ID: id, UserID: user.ID, WorkspaceID: user.WorkspaceID,
			CreatedAt: created, ExpiresAt: created.Add(ttl), UserAgent: "ua", ClientIP: "192.0.2.1"}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	a1, a2 := newSession(alice, now, time.Hour), newSession(alice, now.Add(time.Second), time.Hour)
	b1 := newSession(bob, now, time.Hour)

	// The per-request check returns the current role and nothing for others.
	if role, err := f.stores.Sessions.GetSessionRole(ctx, a1, alice.ID, alice.WorkspaceID, now); err != nil || role != domain.RoleOperator {
		t.Fatalf("active session role = %q, %v", role, err)
	}
	for name, check := range map[string]func() error{
		"another user's session": func() error {
			_, err := f.stores.Sessions.GetSessionRole(ctx, b1, alice.ID, alice.WorkspaceID, now)
			return err
		},
		"unknown session": func() error {
			_, err := f.stores.Sessions.GetSessionRole(ctx, "ffffffffffffffffffffffffffffffff", alice.ID, alice.WorkspaceID, now)
			return err
		},
		"after expiry": func() error {
			_, err := f.stores.Sessions.GetSessionRole(ctx, a1, alice.ID, alice.WorkspaceID, now.Add(2*time.Hour))
			return err
		},
		"malformed id": func() error {
			_, err := f.stores.Sessions.GetSessionRole(ctx, "' OR 1=1 --", alice.ID, alice.WorkspaceID, now)
			return err
		},
	} {
		if err := check(); !errors.Is(err, domain.ErrSessionNotFound) {
			t.Errorf("%s: %v, want ErrSessionNotFound", name, err)
		}
	}
	list, err := f.stores.Sessions.ListSessions(ctx, alice.ID, now)
	if err != nil || len(list) != 2 || list[0].ID != a2 || list[0].ClientIP != "192.0.2.1" {
		t.Fatalf("alice's sessions = %+v, %v (newest first)", list, err)
	}

	// Revoking one session ends exactly that one, once, and is audited.
	if err := f.stores.Sessions.RevokeSession(ctx, a1, bob.ID, domain.SessionRevokedByUser, now); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("revoking another user's session = %v", err)
	}
	if err := f.stores.Sessions.RevokeSession(ctx, a1, alice.ID, domain.SessionRevokedLogout, now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.stores.Sessions.GetSessionRole(ctx, a1, alice.ID, alice.WorkspaceID, now); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("a revoked session still works: %v", err)
	}
	if err := f.stores.Sessions.RevokeSession(ctx, a1, alice.ID, domain.SessionRevokedLogout, now); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("revoking twice = %v", err)
	}
	if _, err := f.stores.Sessions.GetSessionRole(ctx, a2, alice.ID, alice.WorkspaceID, now); err != nil {
		t.Fatalf("revoking one session ended another: %v", err)
	}
	countAudit := func(action string) int {
		var n int
		if err := f.stores.DB.QueryRowContext(ctx, f.pick(`SELECT COUNT(*) FROM audit_events WHERE action = ?`,
			`SELECT COUNT(*) FROM audit_events WHERE action = $1`), action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if countAudit(domain.AuditSessionRevoked) != 1 {
		t.Fatal("session revocation was not audited once")
	}

	// Active sessions are bounded: the oldest are revoked past the limit.
	for i := range domain.MaxActiveSessionsPerUser {
		newSession(bob, now.Add(time.Duration(i+1)*time.Second), time.Hour)
	}
	if list, _ := f.stores.Sessions.ListSessions(ctx, bob.ID, now); len(list) != domain.MaxActiveSessionsPerUser {
		t.Fatalf("bob has %d active sessions, want the limit %d", len(list), domain.MaxActiveSessionsPerUser)
	}
	if _, err := f.stores.Sessions.GetSessionRole(ctx, b1, bob.ID, bob.WorkspaceID, now); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("the oldest session survived the limit: %v", err)
	}

	// Log out everywhere.
	if n, err := f.stores.Sessions.RevokeUserSessions(ctx, bob.ID, domain.SessionRevokedAll, now); err != nil || n != domain.MaxActiveSessionsPerUser {
		t.Fatalf("log out everywhere = %d, %v", n, err)
	}
	if countAudit(domain.AuditSessionsRevoked) != 1 {
		t.Fatal("log out everywhere was not audited")
	}

	// A password change stores the hash and ends every session at once.
	a3 := newSession(alice, now.Add(2*time.Second), time.Hour)
	n, err := f.stores.Sessions.ChangePassword(ctx, alice.ID, "new-hash", now)
	if err != nil || n != 2 {
		t.Fatalf("password change = %d, %v; want alice's 2 active sessions revoked", n, err)
	}
	for _, id := range []string{a2, a3} {
		if _, err := f.stores.Sessions.GetSessionRole(ctx, id, alice.ID, alice.WorkspaceID, now); !errors.Is(err, domain.ErrSessionNotFound) {
			t.Fatalf("session %s survived the password change", id[:8])
		}
	}
	if stored, err := f.stores.Users.GetUserByUsername(ctx, "alice"); err != nil || stored.PasswordHash != "new-hash" {
		t.Fatalf("stored hash = %v", err)
	}
	if countAudit(domain.AuditPasswordChanged) != 1 {
		t.Fatal("the password change was not audited")
	}
	if _, err := f.stores.Sessions.ChangePassword(ctx, 999999, "x", now); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("password change for no user = %v", err)
	}

	// A long multi-byte user agent is cut without splitting a character
	// (PostgreSQL rejects invalid UTF-8).
	long := strings.Repeat("é", 300)
	id, _ := domain.NewSessionID()
	if err := f.stores.Sessions.CreateSession(ctx, domain.Session{ID: id, UserID: alice.ID, WorkspaceID: alice.WorkspaceID,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), UserAgent: long}); err != nil {
		t.Fatalf("long user agent: %v", err)
	}
	list, _ = f.stores.Sessions.ListSessions(ctx, alice.ID, now)
	if len(list) != 1 || len(list[0].UserAgent) > 256 || !utf8.ValidString(list[0].UserAgent) {
		t.Fatalf("stored user agent = %q", fmt.Sprint(len(list[0].UserAgent)))
	}
}
