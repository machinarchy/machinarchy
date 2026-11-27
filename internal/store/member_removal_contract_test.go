package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

func testMemberRemoval(t *testing.T, f *fixture) {
	ctx := context.Background()
	alice := f.member(t, "alice", domain.RoleOwner)
	bob := f.member(t, "bob", domain.RoleOperator)
	carol := f.member(t, "carol", domain.RoleReader)
	ws := domain.DefaultWorkspaceID

	// Unknown member and a member of another workspace are both "not found".
	if err := f.stores.Users.RemoveWorkspaceMember(ctx, ws, 9999); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("unknown user error = %v, want ErrUserNotFound", err)
	}
	if err := f.stores.Users.RemoveWorkspaceMember(ctx, 4242, alice.ID); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("wrong workspace error = %v, want ErrUserNotFound", err)
	}

	// The last owner cannot be removed, even by themselves.
	if err := f.stores.Users.RemoveWorkspaceMember(ctx, ws, alice.ID); !errors.Is(err, domain.ErrLastOwner) {
		t.Fatalf("remove last owner error = %v, want ErrLastOwner", err)
	}

	// Removing a member drops the membership and the member's API tokens.
	expires := time.Now().Add(time.Hour)
	hash := strings.Repeat("b", 64)
	token := &domain.APIToken{UserID: bob.ID, WorkspaceID: ws, Name: "ci", Role: domain.RoleOperator,
		TokenHash: hash, CreatedAt: time.Now().UTC(), ExpiresAt: &expires}
	if err := f.stores.Access.CreateAPIToken(ctx, token); err != nil {
		t.Fatal(err)
	}
	sessionID, err := domain.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := f.stores.Sessions.CreateSession(ctx, domain.Session{ID: sessionID, UserID: bob.ID, WorkspaceID: ws,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), UserAgent: "ua", ClientIP: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.stores.Sessions.GetSessionRole(ctx, sessionID, bob.ID, ws, now); err != nil {
		t.Fatalf("session before removal: %v", err)
	}
	before := len(auditEvents(t, f, ws))
	if err := f.stores.Users.RemoveWorkspaceMember(ctx, ws, bob.ID); err != nil {
		t.Fatalf("remove operator: %v", err)
	}
	if _, err := f.stores.Access.ResolveAPIToken(ctx, hash); err == nil {
		t.Error("a removed member's API token still resolves")
	}
	if _, err := f.stores.Sessions.GetSessionRole(ctx, sessionID, bob.ID, ws, now); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Errorf("a removed member's login session error = %v, want ErrSessionNotFound", err)
	}
	if _, err := f.stores.Access.GetWorkspaceRole(ctx, bob.ID, ws); err == nil {
		t.Error("a removed member still has a workspace role")
	}
	if tokens, err := f.stores.Access.ListAPITokens(ctx, bob.ID, ws); err != nil || len(tokens) != 0 {
		t.Errorf("tokens after removal = %v, %v; want none", tokens, err)
	}
	var accounts int
	if err := f.raw(t).QueryRowContext(ctx, f.pick(`SELECT COUNT(*) FROM users WHERE id = ?`, `SELECT COUNT(*) FROM users WHERE id = $1`), bob.ID).Scan(&accounts); err != nil || accounts != 1 {
		t.Errorf("accounts after removal = %d, %v; the account must survive", accounts, err)
	}
	if err := f.stores.Users.RemoveWorkspaceMember(ctx, ws, bob.ID); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("second removal error = %v, want ErrUserNotFound", err)
	}

	// One audit event for the success; refused and failed removals add none.
	events := auditEvents(t, f, ws)
	if len(events) != before+1 || events[len(events)-1].Action != domain.AuditMemberRemoved ||
		events[len(events)-1].Details["previous_role"] != string(domain.RoleOperator) {
		t.Fatalf("audit events = %+v, want one trailing %s", events, domain.AuditMemberRemoved)
	}

	// With a second owner, an owner may remove themselves.
	if err := f.stores.Users.UpdateWorkspaceMemberRole(ctx, ws, carol.ID, domain.RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := f.stores.Users.RemoveWorkspaceMember(ctx, ws, alice.ID); err != nil {
		t.Fatalf("owner self-removal with another owner: %v", err)
	}
	if owners, err := f.stores.Users.CountWorkspaceOwners(ctx, ws); err != nil || owners != 1 {
		t.Fatalf("owners = %d, %v; want 1", owners, err)
	}
	if v, err := f.stores.Audit.VerifyAuditChain(ctx, ws); err != nil || !v.Valid {
		t.Errorf("audit chain after removals = %+v, %v; want valid", v, err)
	}
}

func testConcurrentOwnerRemoval(t *testing.T, f *fixture) {
	ctx := context.Background()
	alice := f.member(t, "alice", domain.RoleOwner)
	bob := f.member(t, "bob", domain.RoleOwner)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []int{alice.ID, bob.ID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = f.stores.Users.RemoveWorkspaceMember(ctx, domain.DefaultWorkspaceID, id)
		}()
	}
	wg.Wait()

	succeeded, refused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, domain.ErrLastOwner):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 || refused != 1 {
		t.Fatalf("succeeded=%d refused=%d; exactly one of two concurrent removals may win", succeeded, refused)
	}
	if owners, err := f.stores.Users.CountWorkspaceOwners(ctx, domain.DefaultWorkspaceID); err != nil || owners != 1 {
		t.Fatalf("owners = %d, %v; want exactly 1", owners, err)
	}
}
