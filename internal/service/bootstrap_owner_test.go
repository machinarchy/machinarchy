package service

import (
	"context"
	"errors"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

type bootstrapFakeUsers struct {
	domain.UserStore
	owners     int
	createErr  error
	created    []string
	hashes     []string
	promotedID int
	promoted   domain.Role
}

func (f *bootstrapFakeUsers) CountWorkspaceOwners(context.Context, int) (int, error) {
	return f.owners, nil
}

func (f *bootstrapFakeUsers) CreateUser(_ context.Context, username, _ string, hash string) (*domain.User, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = append(f.created, username)
	f.hashes = append(f.hashes, hash)
	return &domain.User{ID: 42, Username: username, WorkspaceID: domain.DefaultWorkspaceID, Role: domain.RoleReader}, nil
}

func (f *bootstrapFakeUsers) UpdateWorkspaceMemberRole(_ context.Context, workspaceID, userID int, role domain.Role) error {
	if workspaceID != domain.DefaultWorkspaceID {
		return errors.New("unexpected workspace")
	}
	f.promotedID, f.promoted = userID, role
	return nil
}

const bootstrapTestPassword = "correct horse battery"

func TestBootstrapOwnerCreatesAndPromotesWhenNoOwnerExists(t *testing.T) {
	users := &bootstrapFakeUsers{}
	created, err := BootstrapOwner(context.Background(), users, " Admin ", "", bootstrapTestPassword)
	if err != nil || !created {
		t.Fatalf("BootstrapOwner() = %v, %v; want created", created, err)
	}
	if len(users.created) != 1 || users.created[0] != "admin" {
		t.Errorf("created users = %v, want normalized [admin]", users.created)
	}
	if users.promotedID != 42 || users.promoted != domain.RoleOwner {
		t.Errorf("promotion = user %d role %q, want user 42 owner", users.promotedID, users.promoted)
	}
	if users.hashes[0] == bootstrapTestPassword || bcrypt.CompareHashAndPassword([]byte(users.hashes[0]), []byte(bootstrapTestPassword)) != nil {
		t.Error("the password must be stored only as a valid bcrypt hash")
	}
}

func TestBootstrapOwnerIsNoOpWhenOwnerExists(t *testing.T) {
	users := &bootstrapFakeUsers{owners: 1}
	created, err := BootstrapOwner(context.Background(), users, "admin", "", bootstrapTestPassword)
	if err != nil || created {
		t.Fatalf("BootstrapOwner() = %v, %v; want no-op", created, err)
	}
	if len(users.created) != 0 {
		t.Fatal("an existing owner must prevent creating another account")
	}
}

func TestBootstrapOwnerRejectsWeakInput(t *testing.T) {
	for name, tc := range map[string]struct{ user, pass string }{
		"short password": {"admin", "short"},
		"long password":  {"admin", string(make([]byte, 73))},
		"empty username": {"  ", bootstrapTestPassword},
	} {
		t.Run(name, func(t *testing.T) {
			users := &bootstrapFakeUsers{}
			if _, err := BootstrapOwner(context.Background(), users, tc.user, "", tc.pass); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
			if len(users.created) != 0 {
				t.Fatal("no account may be created from invalid input")
			}
		})
	}
}

func TestBootstrapOwnerExistingNonOwnerUsernameIsAnError(t *testing.T) {
	users := &bootstrapFakeUsers{createErr: domain.ErrUserAlreadyExists}
	created, err := BootstrapOwner(context.Background(), users, "admin", "", bootstrapTestPassword)
	if err == nil || created {
		t.Fatalf("BootstrapOwner() = %v, %v; want an explicit error, not silent success", created, err)
	}
}
