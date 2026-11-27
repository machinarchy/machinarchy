package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

func newMember(ws int, name, email string, by int, hash string, ttl time.Duration) domain.NewMember {
	return domain.NewMember{WorkspaceID: ws, Username: name, Email: email, Role: domain.RoleOperator, CreatedBy: by,
		TokenHash: hash, ExpiresAt: time.Now().UTC().Add(ttl)}
}

func testAddMember(t *testing.T, f *fixture) {
	ctx := context.Background()
	owner := f.member(t, "alice", domain.RoleOwner)
	active := f.member(t, "carol", domain.RoleOperator) // has a password hash, so it is "active"
	ws := domain.DefaultWorkspaceID
	inv := f.stores.Invitations
	h1, h2, h3 := strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64)

	bad := newMember(ws, "x", "", owner.ID, h1, time.Hour)
	bad.Role = domain.RoleOwner
	if _, err := inv.AddMember(ctx, bad); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("owner role error = %v, want ErrInvalidInput", err)
	}

	added, err := inv.AddMember(ctx, newMember(ws, "bob", "bob@example.test", owner.ID, h1, time.Hour))
	if err != nil || added.Role != domain.RoleOperator || added.ID == 0 {
		t.Fatalf("add = %+v, %v", added, err)
	}
	if _, err := inv.AddMember(ctx, newMember(ws, "BOB", "", owner.ID, h2, time.Hour)); !errors.Is(err, domain.ErrUserAlreadyExists) {
		t.Errorf("duplicate username error = %v, want ErrUserAlreadyExists", err)
	}
	if _, err := inv.AddMember(ctx, newMember(ws, "other", "bob@example.test", owner.ID, h2, time.Hour)); !errors.Is(err, domain.ErrUserAlreadyExists) {
		t.Errorf("duplicate email error = %v, want ErrUserAlreadyExists", err)
	}
	// A failed add leaves nothing behind (no account, no token).
	if _, err := f.stores.Users.GetUserByUsername(ctx, "other"); err == nil {
		t.Error("a failed add left an account")
	}

	// The account is a member but cannot sign in: its hash is the locked value.
	stored, err := f.stores.Users.GetUserByUsername(ctx, "bob")
	if err != nil || stored.PasswordHash != domain.LockedPasswordHash || stored.Role != domain.RoleOperator {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	// Add tokens are not invitations: they are not listed.
	if list, _ := inv.ListInvitations(ctx, ws); len(list) != 0 {
		t.Errorf("an activation token showed up as an invitation: %+v", list)
	}

	// The username must be the added one; a mismatch fails and keeps the token.
	if _, err := inv.AcceptInvitation(ctx, h1, "mallory", "newhash", time.Now()); !errors.Is(err, domain.ErrInvitationInvalid) {
		t.Fatalf("wrong username error = %v, want ErrInvitationInvalid", err)
	}
	if stored, _ := f.stores.Users.GetUserByUsername(ctx, "bob"); stored.PasswordHash != domain.LockedPasswordHash {
		t.Fatal("a wrong username changed the password")
	}

	// Reissue replaces the token: the old one stops working, the new one works once.
	if err := inv.ReissueMemberLink(ctx, ws, added.ID, owner.ID, h2, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := inv.AcceptInvitation(ctx, h1, "bob", "newhash", time.Now()); !errors.Is(err, domain.ErrInvitationInvalid) {
		t.Fatalf("old token error = %v, want ErrInvitationInvalid", err)
	}
	user, err := inv.AcceptInvitation(ctx, h2, "Bob", "newhash", time.Now())
	if err != nil || user.ID != added.ID || user.Role != domain.RoleOperator {
		t.Fatalf("activate = %+v, %v", user, err)
	}
	if stored, _ := f.stores.Users.GetUserByUsername(ctx, "bob"); stored.PasswordHash != "newhash" {
		t.Fatalf("password hash = %q, want the chosen one", stored.PasswordHash)
	}
	if _, err := inv.AcceptInvitation(ctx, h2, "bob", "evil", time.Now()); !errors.Is(err, domain.ErrInvitationInvalid) {
		t.Fatalf("reused token error = %v, want ErrInvitationInvalid", err)
	}

	// A link can never reach a working account, whoever asks: active accounts,
	// owners, other workspaces and unknown users.
	for name, c := range map[string]struct {
		ws, id int
		want   error
	}{
		"activated member": {ws, added.ID, domain.ErrMemberActive},
		"working member":   {ws, active.ID, domain.ErrMemberActive},
		"owner":            {ws, owner.ID, domain.ErrMemberActive},
		"other workspace":  {4242, added.ID, domain.ErrUserNotFound},
		"unknown":          {ws, 9999, domain.ErrUserNotFound},
	} {
		if err := inv.ReissueMemberLink(ctx, c.ws, c.id, owner.ID, h3, time.Now().UTC().Add(time.Hour)); !errors.Is(err, c.want) {
			t.Errorf("reissue for %s = %v, want %v", name, err, c.want)
		}
	}
	if _, err := inv.AcceptInvitation(ctx, h3, "carol", "takeover", time.Now()); !errors.Is(err, domain.ErrInvitationInvalid) {
		t.Fatalf("a refused reissue left a usable token: %v", err)
	}
	if stored, _ := f.stores.Users.GetUserByUsername(ctx, "carol"); stored.PasswordHash != "hash" {
		t.Fatalf("a working account's password changed: %q", stored.PasswordHash)
	}

	// An expired activation token is refused and keeps the account locked.
	late, err := inv.AddMember(ctx, newMember(ws, "dave", "", owner.ID, strings.Repeat("4", 64), time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inv.AcceptInvitation(ctx, strings.Repeat("4", 64), "dave", "h", time.Now().Add(2*time.Hour)); !errors.Is(err, domain.ErrInvitationInvalid) {
		t.Errorf("expired token error = %v", err)
	}
	if stored, _ := f.stores.Users.GetUserByUsername(ctx, "dave"); stored.PasswordHash != domain.LockedPasswordHash || stored.ID != late.ID {
		t.Errorf("an expired token changed the account: %+v", stored)
	}

	// Removing a locked member removes its membership; its token can then do nothing.
	if err := f.stores.Users.RemoveWorkspaceMember(ctx, ws, late.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := inv.AcceptInvitation(ctx, strings.Repeat("4", 64), "dave", "h", time.Now()); !errors.Is(err, domain.ErrInvitationInvalid) {
		t.Errorf("a removed member's token still works: %v", err)
	}

	var actions []string
	for _, e := range auditEvents(t, f, ws) {
		switch e.Action {
		case domain.AuditMemberAdded, domain.AuditMemberLinkReissued, domain.AuditMemberJoined:
			actions = append(actions, e.Action)
			for _, v := range e.Details {
				if strings.Contains(v, h1) || strings.Contains(v, h2) || strings.Contains(v, "newhash") {
					t.Errorf("%s detail leaks a secret: %v", e.Action, e.Details)
				}
			}
			if e.Action == domain.AuditMemberJoined && (e.ActorUsername != "bob" || e.Source != "onboarding") {
				t.Errorf("joined actor = %q source %q", e.ActorUsername, e.Source)
			}
		}
	}
	want := []string{domain.AuditMemberAdded, domain.AuditMemberLinkReissued, domain.AuditMemberJoined, domain.AuditMemberAdded}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Errorf("audit actions = %v, want %v", actions, want)
	}
	if v, err := f.stores.Audit.VerifyAuditChain(ctx, ws); err != nil || !v.Valid {
		t.Errorf("audit chain = %+v, %v", v, err)
	}
}
