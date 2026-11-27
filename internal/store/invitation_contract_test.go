package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

func newInvite(ws int, email string, by int, hash string, ttl time.Duration) domain.NewInvitation {
	return domain.NewInvitation{WorkspaceID: ws, Email: email, Role: domain.RoleOperator, InvitedBy: by,
		TokenHash: hash, ExpiresAt: time.Now().UTC().Add(ttl)}
}

func testInvitations(t *testing.T, f *fixture) {
	ctx := context.Background()
	owner := f.member(t, "alice", domain.RoleOwner)
	ws := domain.DefaultWorkspaceID
	inv := f.stores.Invitations
	hashA, hashB := strings.Repeat("a", 64), strings.Repeat("b", 64)

	// Owner role and bad input are refused by the store too.
	bad := newInvite(ws, "x@example.test", owner.ID, hashA, time.Hour)
	bad.Role = domain.RoleOwner
	if _, err := inv.CreateInvitation(ctx, bad); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("owner invitation error = %v, want ErrInvalidInput", err)
	}

	created, err := inv.CreateInvitation(ctx, newInvite(ws, "New.Person@Example.test", owner.ID, hashA, time.Hour))
	if err != nil || created.Email != "new.person@example.test" || created.ID == 0 {
		t.Fatalf("create = %+v, %v", created, err)
	}
	list, err := inv.ListInvitations(ctx, ws)
	if err != nil || len(list) != 1 || list[0].InvitedBy != "alice" || list[0].Role != domain.RoleOperator {
		t.Fatalf("list = %+v, %v", list, err)
	}

	// A second invitation for the same email (any case) replaces the first.
	if _, err := inv.CreateInvitation(ctx, newInvite(ws, "NEW.person@example.test", owner.ID, hashB, time.Hour)); err != nil {
		t.Fatal(err)
	}
	if list, _ := inv.ListInvitations(ctx, ws); len(list) != 1 {
		t.Fatalf("replace left %d invitations, want 1", len(list))
	}
	if _, err := inv.AcceptInvitation(ctx, hashA, "newbie", "h", time.Now()); !errors.Is(err, domain.ErrInvitationInvalid) {
		t.Fatalf("replaced token error = %v, want ErrInvitationInvalid", err)
	}

	// Wrong workspace cannot revoke; unknown id is not found.
	list, _ = inv.ListInvitations(ctx, ws)
	if err := inv.RevokeInvitation(ctx, 4242, list[0].ID); !errors.Is(err, domain.ErrInvitationNotFound) {
		t.Errorf("wrong workspace revoke = %v, want ErrInvitationNotFound", err)
	}
	if err := inv.RevokeInvitation(ctx, ws, 9999); !errors.Is(err, domain.ErrInvitationNotFound) {
		t.Errorf("unknown revoke = %v, want ErrInvitationNotFound", err)
	}

	// A taken username fails and leaves the invitation usable.
	if _, err := inv.AcceptInvitation(ctx, hashB, "alice", "h", time.Now()); !errors.Is(err, domain.ErrUserAlreadyExists) {
		t.Fatalf("taken username error = %v, want ErrUserAlreadyExists", err)
	}
	if list, _ := inv.ListInvitations(ctx, ws); len(list) != 1 {
		t.Fatalf("invitation lost after a failed accept: %+v", list)
	}

	// Accepting creates the account with exactly the invited role; the token works once.
	user, err := inv.AcceptInvitation(ctx, hashB, "newbie", "bcrypt-hash", time.Now())
	if err != nil || user.Role != domain.RoleOperator || user.Email != "new.person@example.test" {
		t.Fatalf("accept = %+v, %v", user, err)
	}
	if role, err := f.stores.Access.GetWorkspaceRole(ctx, user.ID, ws); err != nil || role != domain.RoleOperator {
		t.Fatalf("role = %q, %v; want operator", role, err)
	}
	if _, err := inv.AcceptInvitation(ctx, hashB, "other", "h", time.Now()); !errors.Is(err, domain.ErrInvitationInvalid) {
		t.Fatalf("reused token error = %v, want ErrInvitationInvalid", err)
	}
	if list, _ := inv.ListInvitations(ctx, ws); len(list) != 0 {
		t.Fatalf("accepted invitation still listed: %+v", list)
	}

	// Revoked and expired tokens are invalid; expired ones are not listed.
	hashC, hashD := strings.Repeat("c", 64), strings.Repeat("d", 64)
	revoked, err := inv.CreateInvitation(ctx, newInvite(ws, "r@example.test", owner.ID, hashC, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := inv.RevokeInvitation(ctx, ws, revoked.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := inv.AcceptInvitation(ctx, hashC, "r", "h", time.Now()); !errors.Is(err, domain.ErrInvitationInvalid) {
		t.Errorf("revoked token error = %v, want ErrInvitationInvalid", err)
	}
	if _, err := inv.CreateInvitation(ctx, newInvite(ws, "e@example.test", owner.ID, hashD, time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := inv.AcceptInvitation(ctx, hashD, "e", "h", time.Now().Add(2*time.Hour)); !errors.Is(err, domain.ErrInvitationInvalid) {
		t.Errorf("expired token error = %v, want ErrInvitationInvalid", err)
	}
	if _, err := inv.AcceptInvitation(ctx, "", "e", "h", time.Now()); !errors.Is(err, domain.ErrInvitationInvalid) {
		t.Errorf("empty token error = %v, want ErrInvitationInvalid", err)
	}

	// Audit: invited, invite_revoked, joined; none carries a token hash; the chain verifies.
	var actions []string
	for _, e := range auditEvents(t, f, ws) {
		switch e.Action {
		case domain.AuditMemberInvited, domain.AuditMemberInviteRevoked, domain.AuditMemberJoined:
			actions = append(actions, e.Action)
			for _, v := range e.Details {
				if strings.Contains(v, hashA) || strings.Contains(v, hashB) || strings.Contains(v, hashC) {
					t.Errorf("%s audit detail leaks a token hash: %v", e.Action, e.Details)
				}
			}
			if e.Action == domain.AuditMemberJoined && (e.ActorUsername != "newbie" || e.Source != "invitation") {
				t.Errorf("joined event actor = %q source %q", e.ActorUsername, e.Source)
			}
		}
	}
	want := []string{domain.AuditMemberInvited, domain.AuditMemberInvited, domain.AuditMemberJoined,
		domain.AuditMemberInvited, domain.AuditMemberInviteRevoked, domain.AuditMemberInvited}
	if fmt.Sprint(actions) != fmt.Sprint(want) {
		t.Errorf("audit actions = %v, want %v", actions, want)
	}
	if v, err := f.stores.Audit.VerifyAuditChain(ctx, ws); err != nil || !v.Valid {
		t.Errorf("audit chain = %+v, %v; want valid", v, err)
	}
}

func testInvitationLimitAndConcurrency(t *testing.T, f *fixture) {
	ctx := context.Background()
	owner := f.member(t, "alice", domain.RoleOwner)
	ws := domain.DefaultWorkspaceID
	inv := f.stores.Invitations

	// Two redemptions of one token: exactly one account is created.
	hash := strings.Repeat("e", 64)
	if _, err := inv.CreateInvitation(ctx, newInvite(ws, "race@example.test", owner.ID, hash, time.Hour)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, name := range []string{"racer1", "racer2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = inv.AcceptInvitation(ctx, hash, name, "h", time.Now())
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrInvitationInvalid):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d concurrent redemptions succeeded, want exactly 1 (%v)", ok, errs)
	}

	// The workspace is capped.
	for i := 0; i < domain.MaxPendingInvitations; i++ {
		if _, err := inv.CreateInvitation(ctx, newInvite(ws, fmt.Sprintf("u%d@example.test", i), owner.ID, fmt.Sprintf("%064d", i), time.Hour)); err != nil {
			t.Fatalf("invitation %d: %v", i, err)
		}
	}
	if _, err := inv.CreateInvitation(ctx, newInvite(ws, "over@example.test", owner.ID, strings.Repeat("f", 64), time.Hour)); !errors.Is(err, domain.ErrInvitationLimit) {
		t.Fatalf("over the cap error = %v, want ErrInvitationLimit", err)
	}
	// Re-inviting an address already pending replaces it instead of counting twice.
	if _, err := inv.CreateInvitation(ctx, newInvite(ws, "u0@example.test", owner.ID, strings.Repeat("9", 64), time.Hour)); err != nil {
		t.Fatalf("replacing a pending invitation at the cap: %v", err)
	}
}
