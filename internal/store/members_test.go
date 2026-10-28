package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/store"
	_ "modernc.org/sqlite"
)

func openMemberTestStore(t *testing.T) *store.Stores {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "machinarchy.db")
	if err := os.Chmod(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatalf("restrict test database directory: %v", err)
	}
	stores, err := store.Open(context.Background(), store.Config{
		Engine:           store.EngineSQLite,
		SQLitePath:       databasePath,
		EncryptionSecret: "integration-test-secret-with-more-than-thirty-two-bytes",
	})
	if err != nil {
		t.Fatalf("open migrated SQLite store: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	return stores
}

func createMember(t *testing.T, stores *store.Stores, name string, role domain.Role) *domain.User {
	t.Helper()
	ctx := context.Background()
	user, err := stores.Users.CreateUser(ctx, name, name+"@example.test", "hash")
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	if role != domain.RoleReader {
		if err := stores.Users.UpdateWorkspaceMemberRole(ctx, user.WorkspaceID, user.ID, role); err != nil {
			t.Fatalf("set %s to %s: %v", name, role, err)
		}
	}
	return user
}

func roleOf(t *testing.T, stores *store.Stores, username string) domain.Role {
	t.Helper()
	user, err := stores.Users.GetUserByUsername(context.Background(), username)
	if err != nil {
		t.Fatalf("load %s: %v", username, err)
	}
	return user.Role
}

func TestSQLiteListWorkspaceMembers(t *testing.T) {
	ctx := context.Background()
	stores := openMemberTestStore(t)
	createMember(t, stores, "alice", domain.RoleOwner)
	createMember(t, stores, "bob", domain.RoleOperator)
	createMember(t, stores, "carol", domain.RoleReader)

	members, err := stores.Users.ListWorkspaceMembers(ctx, domain.DefaultWorkspaceID)
	if err != nil {
		t.Fatalf("ListWorkspaceMembers() error = %v", err)
	}
	want := []struct {
		name string
		role domain.Role
	}{{"alice", domain.RoleOwner}, {"bob", domain.RoleOperator}, {"carol", domain.RoleReader}}
	if len(members) != len(want) {
		t.Fatalf("members = %+v, want %d", members, len(want))
	}
	for i, w := range want {
		if members[i].Username != w.name || members[i].Role != w.role || members[i].CreatedAt.IsZero() {
			t.Errorf("member[%d] = %+v, want %s/%s with a creation time", i, members[i], w.name, w.role)
		}
	}
	if other, err := stores.Users.ListWorkspaceMembers(ctx, 4242); err != nil || len(other) != 0 {
		t.Errorf("another workspace's members = %v, %v; want none", other, err)
	}
}

func TestSQLiteLastOwnerCannotBeDemoted(t *testing.T) {
	ctx := context.Background()
	stores := openMemberTestStore(t)
	alice := createMember(t, stores, "alice", domain.RoleOwner)
	bob := createMember(t, stores, "bob", domain.RoleOwner)

	// Two owners: demoting one is fine.
	if err := stores.Users.UpdateWorkspaceMemberRole(ctx, domain.DefaultWorkspaceID, bob.ID, domain.RoleReader); err != nil {
		t.Fatalf("demote with two owners: %v", err)
	}
	// Alice is now the only owner.
	if err := stores.Users.UpdateWorkspaceMemberRole(ctx, domain.DefaultWorkspaceID, alice.ID, domain.RoleOperator); !errors.Is(err, domain.ErrLastOwner) {
		t.Fatalf("demote last owner error = %v, want ErrLastOwner", err)
	}
	if got := roleOf(t, stores, "alice"); got != domain.RoleOwner {
		t.Fatalf("alice role = %s after a refused demotion, want owner", got)
	}
	// Keeping the owner role is allowed and is not a demotion.
	if err := stores.Users.UpdateWorkspaceMemberRole(ctx, domain.DefaultWorkspaceID, alice.ID, domain.RoleOwner); err != nil {
		t.Fatalf("owner -> owner on the last owner: %v", err)
	}
	// Promoting someone else makes the demotion legal again.
	if err := stores.Users.UpdateWorkspaceMemberRole(ctx, domain.DefaultWorkspaceID, bob.ID, domain.RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := stores.Users.UpdateWorkspaceMemberRole(ctx, domain.DefaultWorkspaceID, alice.ID, domain.RoleOperator); err != nil {
		t.Fatalf("demote after promoting another owner: %v", err)
	}
	owners, err := stores.Users.CountWorkspaceOwners(ctx, domain.DefaultWorkspaceID)
	if err != nil || owners != 1 {
		t.Fatalf("owners = %d, %v; want 1", owners, err)
	}
}

func TestSQLiteUpdateRoleUnknownMemberAndWorkspace(t *testing.T) {
	ctx := context.Background()
	stores := openMemberTestStore(t)
	user := createMember(t, stores, "alice", domain.RoleOwner)
	if err := stores.Users.UpdateWorkspaceMemberRole(ctx, domain.DefaultWorkspaceID, 9999, domain.RoleReader); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("unknown user error = %v, want ErrUserNotFound", err)
	}
	if err := stores.Users.UpdateWorkspaceMemberRole(ctx, 4242, user.ID, domain.RoleReader); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("wrong workspace error = %v, want ErrUserNotFound (no cross-workspace changes)", err)
	}
	if got := roleOf(t, stores, "alice"); got != domain.RoleOwner {
		t.Errorf("alice role = %s, want unchanged owner", got)
	}
}

func TestSQLiteConcurrentDemotionsLeaveOneOwner(t *testing.T) {
	ctx := context.Background()
	stores := openMemberTestStore(t)
	alice := createMember(t, stores, "alice", domain.RoleOwner)
	bob := createMember(t, stores, "bob", domain.RoleOwner)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []int{alice.ID, bob.ID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = stores.Users.UpdateWorkspaceMemberRole(ctx, domain.DefaultWorkspaceID, id, domain.RoleReader)
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
		t.Fatalf("succeeded=%d refused=%d; exactly one of two concurrent demotions may win", succeeded, refused)
	}
	if owners, err := stores.Users.CountWorkspaceOwners(ctx, domain.DefaultWorkspaceID); err != nil || owners != 1 {
		t.Fatalf("owners = %d, %v; want exactly 1", owners, err)
	}
}
