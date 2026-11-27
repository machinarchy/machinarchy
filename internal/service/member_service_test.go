package service

import (
	"context"
	"errors"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
)

type memberFakeUsers struct {
	domain.UserStore
	members     []domain.WorkspaceMember
	updateErr   error
	removeErr   error
	removeCalls int
	lastRemove  struct{ workspaceID, userID int }
	updateCalls int
	lastUpdate  struct {
		workspaceID, userID int
		role                domain.Role
	}
}

func (f *memberFakeUsers) ListWorkspaceMembers(_ context.Context, _ int) ([]domain.WorkspaceMember, error) {
	out := make([]domain.WorkspaceMember, len(f.members))
	copy(out, f.members)
	return out, nil
}

func (f *memberFakeUsers) UpdateWorkspaceMemberRole(_ context.Context, workspaceID, userID int, role domain.Role) error {
	f.updateCalls++
	f.lastUpdate.workspaceID, f.lastUpdate.userID, f.lastUpdate.role = workspaceID, userID, role
	return f.updateErr
}

func (f *memberFakeUsers) RemoveWorkspaceMember(_ context.Context, workspaceID, userID int) error {
	f.removeCalls++
	f.lastRemove.workspaceID, f.lastRemove.userID = workspaceID, userID
	return f.removeErr
}

func newMemberFake() *memberFakeUsers {
	return &memberFakeUsers{members: []domain.WorkspaceMember{
		{UserID: 1, Username: "owner", Role: domain.RoleOwner},
		{UserID: 2, Username: "reader", Role: domain.RoleReader},
	}}
}

func ownerPrincipal() domain.Principal {
	return domain.Principal{UserID: 1, WorkspaceID: 7, Role: domain.RoleOwner, Username: "owner"}
}

func TestMemberServiceRequiresAHumanOwner(t *testing.T) {
	denied := map[string]domain.Principal{
		"operator":     {UserID: 3, WorkspaceID: 7, Role: domain.RoleOperator},
		"reader":       {UserID: 2, WorkspaceID: 7, Role: domain.RoleReader},
		"api token":    {UserID: 1, WorkspaceID: 7, Role: domain.RoleOwner, APITokenID: 9},
		"no user":      {WorkspaceID: 7, Role: domain.RoleOwner},
		"no workspace": {UserID: 1, Role: domain.RoleOwner},
		"no role":      {UserID: 1, WorkspaceID: 7},
	}
	for name, principal := range denied {
		t.Run(name, func(t *testing.T) {
			users := newMemberFake()
			svc, err := NewMemberService(users)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.List(context.Background(), principal); !errors.Is(err, domain.ErrPermissionDenied) {
				t.Errorf("List() error = %v, want ErrPermissionDenied", err)
			}
			if _, err := svc.SetRole(context.Background(), principal, 2, domain.RoleOperator); !errors.Is(err, domain.ErrPermissionDenied) {
				t.Errorf("SetRole() error = %v, want ErrPermissionDenied", err)
			}
			if users.updateCalls != 0 {
				t.Error("a denied caller must never reach the store")
			}
		})
	}
}

func TestMemberServiceSetRole(t *testing.T) {
	ctx := context.Background()

	t.Run("promotes within the caller's workspace", func(t *testing.T) {
		users := newMemberFake()
		svc, _ := NewMemberService(users)
		member, err := svc.SetRole(ctx, ownerPrincipal(), 2, domain.RoleOperator)
		if err != nil {
			t.Fatalf("SetRole() error = %v", err)
		}
		if member.UserID != 2 || member.Role != domain.RoleOperator {
			t.Errorf("member = %+v", member)
		}
		if users.lastUpdate.workspaceID != 7 || users.lastUpdate.userID != 2 || users.lastUpdate.role != domain.RoleOperator {
			t.Errorf("store call = %+v; the workspace must come from the principal", users.lastUpdate)
		}
	})

	t.Run("unchanged role is a no-op", func(t *testing.T) {
		users := newMemberFake()
		svc, _ := NewMemberService(users)
		member, err := svc.SetRole(ctx, ownerPrincipal(), 2, domain.RoleReader)
		if err != nil || member.Role != domain.RoleReader {
			t.Fatalf("SetRole() = %+v, %v", member, err)
		}
		if users.updateCalls != 0 {
			t.Error("an unchanged role must not write")
		}
	})

	t.Run("unknown user", func(t *testing.T) {
		svc, _ := NewMemberService(newMemberFake())
		if _, err := svc.SetRole(ctx, ownerPrincipal(), 99, domain.RoleOperator); !errors.Is(err, domain.ErrUserNotFound) {
			t.Errorf("error = %v, want ErrUserNotFound", err)
		}
	})

	t.Run("invalid input", func(t *testing.T) {
		users := newMemberFake()
		svc, _ := NewMemberService(users)
		for _, role := range []domain.Role{"", "admin", "OWNER"} {
			if _, err := svc.SetRole(ctx, ownerPrincipal(), 2, role); !errors.Is(err, domain.ErrInvalidInput) {
				t.Errorf("role %q error = %v, want ErrInvalidInput", role, err)
			}
		}
		if _, err := svc.SetRole(ctx, ownerPrincipal(), 0, domain.RoleReader); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("user ID 0 error = %v, want ErrInvalidInput", err)
		}
		if users.updateCalls != 0 {
			t.Error("invalid input must not reach the store")
		}
	})

	t.Run("last owner protection comes from the store", func(t *testing.T) {
		users := newMemberFake()
		users.updateErr = domain.ErrLastOwner
		svc, _ := NewMemberService(users)
		if _, err := svc.SetRole(ctx, ownerPrincipal(), 1, domain.RoleReader); !errors.Is(err, domain.ErrLastOwner) {
			t.Errorf("error = %v, want ErrLastOwner", err)
		}
	})
}

func TestMemberServiceListUsesCallerWorkspace(t *testing.T) {
	svc, _ := NewMemberService(newMemberFake())
	members, err := svc.List(context.Background(), ownerPrincipal())
	if err != nil || len(members) != 2 {
		t.Fatalf("List() = %v, %v", members, err)
	}
}

func TestMemberServiceRemove(t *testing.T) {
	ctx := context.Background()

	for name, principal := range map[string]domain.Principal{
		"operator":  {UserID: 3, WorkspaceID: 7, Role: domain.RoleOperator},
		"api token": {UserID: 1, WorkspaceID: 7, Role: domain.RoleOwner, APITokenID: 9},
	} {
		users := newMemberFake()
		svc, _ := NewMemberService(users)
		if err := svc.Remove(ctx, principal, 2); !errors.Is(err, domain.ErrPermissionDenied) || users.removeCalls != 0 {
			t.Errorf("%s: err = %v, store calls = %d; want denied and no store call", name, err, users.removeCalls)
		}
	}

	t.Run("removes within the caller's workspace", func(t *testing.T) {
		users := newMemberFake()
		svc, _ := NewMemberService(users)
		if err := svc.Remove(ctx, ownerPrincipal(), 2); err != nil {
			t.Fatal(err)
		}
		if users.lastRemove.workspaceID != 7 || users.lastRemove.userID != 2 {
			t.Errorf("store got %+v, want workspace 7 user 2", users.lastRemove)
		}
	})
	t.Run("an owner may remove themselves; the store keeps the last owner", func(t *testing.T) {
		users := newMemberFake()
		svc, _ := NewMemberService(users)
		if err := svc.Remove(ctx, ownerPrincipal(), 1); err != nil || users.lastRemove.userID != 1 {
			t.Fatalf("self-removal err = %v, store user = %d; want it passed to the store", err, users.lastRemove.userID)
		}
		users.removeErr = domain.ErrLastOwner
		if err := svc.Remove(ctx, ownerPrincipal(), 1); !errors.Is(err, domain.ErrLastOwner) {
			t.Errorf("err = %v, want ErrLastOwner", err)
		}
	})
	t.Run("rejects a non-positive id before the store", func(t *testing.T) {
		users := newMemberFake()
		svc, _ := NewMemberService(users)
		if err := svc.Remove(ctx, ownerPrincipal(), 0); !errors.Is(err, domain.ErrInvalidInput) || users.removeCalls != 0 {
			t.Errorf("err = %v, calls = %d; want ErrInvalidInput and no store call", err, users.removeCalls)
		}
	})
	t.Run("passes not-found through", func(t *testing.T) {
		users := newMemberFake()
		users.removeErr = domain.ErrUserNotFound
		svc, _ := NewMemberService(users)
		if err := svc.Remove(ctx, ownerPrincipal(), 99); !errors.Is(err, domain.ErrUserNotFound) {
			t.Errorf("err = %v, want ErrUserNotFound", err)
		}
	})
}
