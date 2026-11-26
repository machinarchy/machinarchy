package service

import (
	"context"
	"errors"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

// Hosts join the default environment unless told otherwise, can be moved with
// inventory write, and never reach an environment that does not exist.
func TestHostEnvironments(t *testing.T) {
	ctx := context.Background()
	f := newManagedFixture(t)
	s := f.service
	spec := resource.HostSpec{Presence: resource.PresenceManaged, AccessTarget: resource.HostAccessTarget{Endpoint: "192.0.2.10:22"}}

	envs, err := f.stores.Environments.ListEnvironments(ctx, f.owner.WorkspaceID)
	if err != nil || len(envs) != 1 || envs[0].Name != domain.DefaultEnvironmentName {
		t.Fatalf("fresh workspace environments = %+v, %v", envs, err)
	}
	defaultID := envs[0].ID
	prod := &domain.Environment{WorkspaceID: f.owner.WorkspaceID, Name: "production"}
	if err = f.stores.Environments.CreateEnvironment(ctx, prod); err != nil {
		t.Fatal(err)
	}

	host, err := s.CreateHost(ctx, f.owner, "web-1", nil, spec)
	if err != nil || host.Metadata.EnvironmentID != defaultID {
		t.Fatalf("host without environment = %+v, %v (want default %d)", host.Metadata, err, defaultID)
	}
	if h, err := s.CreateHostInEnvironment(ctx, f.operator, prod.ID, "web-2", nil, spec); err != nil || h.Metadata.EnvironmentID != prod.ID {
		t.Fatalf("host in production = %+v, %v", h, err)
	}
	for _, bad := range []int{9999, -1} {
		if _, err = s.CreateHostInEnvironment(ctx, f.owner, bad, "bad", nil, spec); !errors.Is(err, domain.ErrEnvironmentUnknown) {
			t.Fatalf("create in environment %d = %v", bad, err)
		}
	}

	// Move: absent keeps, an ID moves, a wrong ID and a stale revision are refused.
	if err = s.UpdateHostInEnvironment(ctx, f.owner, host.Metadata.ID, 1, 1, 9999, spec); !errors.Is(err, domain.ErrEnvironmentUnknown) {
		t.Fatalf("move to unknown = %v", err)
	}
	if err = s.UpdateHostInEnvironment(ctx, f.reader, host.Metadata.ID, 1, 1, prod.ID, spec); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("reader move = %v", err)
	}
	if err = s.UpdateHostInEnvironment(ctx, f.operator, host.Metadata.ID, 7, 1, prod.ID, spec); !errors.Is(err, resource.ErrConflict) {
		t.Fatalf("stale move = %v", err)
	}
	if err = s.UpdateHostInEnvironment(ctx, f.operator, host.Metadata.ID, 1, 1, prod.ID, spec); err != nil {
		t.Fatal(err)
	}
	moved, _ := s.GetHost(ctx, f.owner, host.Metadata.ID)
	if moved.Metadata.EnvironmentID != prod.ID || moved.Metadata.Revision != 2 {
		t.Fatalf("after move = %+v", moved.Metadata)
	}
	if err = s.UpdateHostInEnvironment(ctx, f.operator, host.Metadata.ID, 2, 1, 0, spec); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetHost(ctx, f.owner, host.Metadata.ID); again.Metadata.EnvironmentID != prod.ID {
		t.Fatalf("absent environment moved the host to %d", again.Metadata.EnvironmentID)
	}

	// The environment with hosts cannot go; the default never can.
	if err = f.stores.Environments.DeleteEnvironment(ctx, f.owner.WorkspaceID, prod.ID); !errors.Is(err, domain.ErrEnvironmentInUse) {
		t.Fatalf("delete environment with hosts = %v", err)
	}
	if err = f.stores.Environments.DeleteEnvironment(ctx, f.owner.WorkspaceID, defaultID); !errors.Is(err, domain.ErrDefaultEnvironment) {
		t.Fatalf("delete default = %v", err)
	}
	renamed := &domain.Environment{ID: defaultID, WorkspaceID: f.owner.WorkspaceID, Name: "dev"}
	if err = f.stores.Environments.UpdateEnvironment(ctx, renamed); !errors.Is(err, domain.ErrDefaultEnvironment) {
		t.Fatalf("rename default = %v", err)
	}
	renamed.Name = "Default"
	renamed.Description = "shared"
	if err = f.stores.Environments.UpdateEnvironment(ctx, renamed); err != nil {
		t.Fatalf("re-describe default = %v", err)
	}
}
