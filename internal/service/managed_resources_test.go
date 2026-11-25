package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

// memHosts is an in-memory resource.HostRepository for service tests. Methods
// the service does not call are left to the embedded nil interface.
type memHosts struct {
	resource.HostRepository
	hosts map[resource.ID]*resource.Host
}

func (m *memHosts) CreateHost(_ context.Context, host *resource.Host) error {
	m.hosts[host.Metadata.ID] = host
	return nil
}

func (m *memHosts) GetHost(_ context.Context, workspace int, id resource.ID) (*resource.Host, error) {
	host, ok := m.hosts[id]
	if !ok || host.Metadata.WorkspaceID != workspace {
		return nil, resource.ErrHostNotFound
	}
	copied := *host
	return &copied, nil
}

func (m *memHosts) UpdateHostSpec(_ context.Context, workspace int, id resource.ID, revision, _ uint64, spec resource.HostSpec, _ time.Time) error {
	host, ok := m.hosts[id]
	if !ok || host.Metadata.WorkspaceID != workspace {
		return resource.ErrHostNotFound
	}
	if host.Metadata.Revision != revision {
		return resource.ErrConflict
	}
	host.Spec = spec
	host.Metadata.Revision++
	return nil
}

type memProfiles struct {
	resource.AccessProfileRepository
}
type memOperations struct {
	resource.OperationRepository
	operations map[resource.ID]*resource.Operation
}

func (m *memOperations) GetOperation(_ context.Context, workspace int, id resource.ID) (*resource.Operation, error) {
	op, ok := m.operations[id]
	if !ok || op.WorkspaceID != workspace {
		return nil, resource.ErrOperationNotFound
	}
	return op, nil
}

func newManagedService(t *testing.T) (*ManagedResourceService, *memHosts, *memOperations) {
	t.Helper()
	hosts := &memHosts{hosts: map[resource.ID]*resource.Host{}}
	operations := &memOperations{operations: map[resource.ID]*resource.Operation{}}
	svc, err := NewManagedResourceService(hosts, &memProfiles{}, operations, func() domain.ExecutionPolicy { return domain.ExecutionPolicy{} })
	if err != nil {
		t.Fatal(err)
	}
	return svc, hosts, operations
}

func principal(role domain.Role) domain.Principal {
	return domain.Principal{UserID: 1, WorkspaceID: 1, Role: role, Username: string(role)}
}

func mustID(t *testing.T) resource.ID {
	t.Helper()
	id, err := resource.NewResourceID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestOperatorCannotRedirectOwnerCredential covers AUDIT S-02: binding a host to
// an Access Profile, or changing the endpoint or trusted key of a bound host,
// decides where an owner's SSH credential is presented and needs the owner.
func TestOperatorCannotRedirectOwnerCredential(t *testing.T) {
	svc, hosts, _ := newManagedService(t)
	ctx := context.Background()
	operator, owner := principal(domain.RoleOperator), principal(domain.RoleOwner)
	profile := mustID(t)
	bound := resource.HostSpec{AccessProfileID: profile, AccessTarget: resource.HostAccessTarget{Endpoint: "10.0.0.5:22", Trust: []string{"SHA256:legit"}}}

	if _, err := svc.CreateHost(ctx, operator, "web-1", nil, bound); !errors.Is(err, ErrAccessProfileOwnerRequired) {
		t.Fatalf("operator binding a profile at create: err = %v, want ErrAccessProfileOwnerRequired", err)
	}
	unbound, err := svc.CreateHost(ctx, operator, "web-2", nil, resource.HostSpec{AccessTarget: resource.HostAccessTarget{Endpoint: "10.0.0.6:22"}})
	if err != nil {
		t.Fatalf("operator creating an unbound host: %v", err)
	}
	host, err := svc.CreateHost(ctx, owner, "web-1", nil, bound)
	if err != nil {
		t.Fatalf("owner binding a profile: %v", err)
	}

	attacks := map[string]resource.HostSpec{
		"redirect endpoint": {AccessProfileID: profile, AccessTarget: resource.HostAccessTarget{Endpoint: "attacker.example:22", Trust: []string{"SHA256:legit"}}},
		"replace trust":     {AccessProfileID: profile, AccessTarget: resource.HostAccessTarget{Endpoint: "10.0.0.5:22", Trust: []string{"SHA256:attacker"}}},
		"add trust":         {AccessProfileID: profile, AccessTarget: resource.HostAccessTarget{Endpoint: "10.0.0.5:22", Trust: []string{"SHA256:legit", "SHA256:attacker"}}},
		"pin old revision":  {AccessProfileID: profile, ProfileRevision: 7, AccessTarget: bound.AccessTarget},
	}
	for name, spec := range attacks {
		if err := svc.UpdateHost(ctx, operator, host.Metadata.ID, 1, 1, spec); !errors.Is(err, ErrAccessProfileOwnerRequired) {
			t.Errorf("%s: err = %v, want ErrAccessProfileOwnerRequired", name, err)
		}
	}
	if err := svc.UpdateHost(ctx, operator, unbound.Metadata.ID, 1, 1, resource.HostSpec{AccessProfileID: profile, AccessTarget: unbound.Spec.AccessTarget}); !errors.Is(err, ErrAccessProfileOwnerRequired) {
		t.Fatalf("operator binding an existing host: err = %v", err)
	}
	if got := hosts.hosts[host.Metadata.ID].Spec.AccessTarget.Endpoint; got != "10.0.0.5:22" {
		t.Fatalf("endpoint changed to %q by a rejected update", got)
	}

	// Changes that keep the binding intact remain available to operators.
	presenceOnly := bound
	presenceOnly.Presence = resource.PresenceAbsent
	if err := svc.UpdateHost(ctx, operator, host.Metadata.ID, 1, 1, presenceOnly); err != nil {
		t.Fatalf("operator changing presence only: %v", err)
	}
	// The owner may rebind.
	moved := bound
	moved.AccessTarget.Endpoint = "10.0.0.9:22"
	if err := svc.UpdateHost(ctx, owner, host.Metadata.ID, 2, 1, moved); err != nil {
		t.Fatalf("owner moving the endpoint: %v", err)
	}
	// A stale revision is rejected before authorization decisions are made.
	if err := svc.UpdateHost(ctx, owner, host.Metadata.ID, 1, 1, moved); !errors.Is(err, resource.ErrConflict) {
		t.Fatalf("stale revision: err = %v, want ErrConflict", err)
	}
}

// TestReadersCannotSeeOperationArgvOrOutput covers AUDIT S-04: readers (the
// role every self-signup receives) see operation state but not the command
// line or captured output, which may contain secrets.
func TestReadersCannotSeeOperationArgvOrOutput(t *testing.T) {
	svc, _, operations := newManagedService(t)
	id := mustID(t)
	exit := 0
	operations.operations[id] = &resource.Operation{
		ID: id, WorkspaceID: 1, Kind: resource.OperationKindRun, Revision: 3,
		Spec: resource.OperationSpec{Run: &resource.RunSpec{Argv: []string{"mysql", "-pS3cret"}, TimeoutSeconds: 30, Concurrency: 1}},
		Status: resource.OperationStatus{State: resource.OperationSucceeded, Targets: []resource.OperationTarget{{
			State:  resource.TargetSucceeded,
			Result: &resource.TargetResult{ExitCode: &exit, Output: "token=abc", Stdout: "token=abc", Stderr: "warn", ErrorClass: ""},
		}}},
	}
	ctx := context.Background()

	got, err := svc.GetOperation(ctx, principal(domain.RoleReader), id)
	if err != nil {
		t.Fatal(err)
	}
	result := got.Status.Targets[0].Result
	if len(got.Spec.Run.Argv) != 0 || result.Output != "" || result.Stdout != "" || result.Stderr != "" {
		t.Fatalf("reader saw sensitive fields: argv=%v result=%+v", got.Spec.Run.Argv, result)
	}
	if got.Status.State != resource.OperationSucceeded || result.ExitCode == nil || *result.ExitCode != 0 || got.Spec.Run.TimeoutSeconds != 30 {
		t.Fatalf("reader lost non-sensitive fields: %+v", got)
	}
	// The stored operation must not be modified by redaction.
	if stored := operations.operations[id]; len(stored.Spec.Run.Argv) != 2 || stored.Status.Targets[0].Result.Output != "token=abc" {
		t.Fatal("redaction mutated the stored operation")
	}

	full, err := svc.GetOperation(ctx, principal(domain.RoleOperator), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Spec.Run.Argv) != 2 || full.Status.Targets[0].Result.Stdout != "token=abc" {
		t.Fatalf("operator should see argv and output: %+v", full)
	}
}
