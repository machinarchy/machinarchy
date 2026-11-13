package secrets

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

type bindingFixture struct {
	profile   *resource.AccessProfile
	revisions map[uint64]*resource.AccessProfileRevision
	host      *resource.Host
	operation *resource.Operation
}

func (f *bindingFixture) GetAccessProfile(_ context.Context, workspace int, id resource.ID) (*resource.AccessProfile, error) {
	if workspace != f.profile.WorkspaceID || id != f.profile.ID {
		return nil, resource.ErrAccessProfileNotFound
	}
	return f.profile, nil
}
func (f *bindingFixture) GetAccessProfileRevision(_ context.Context, workspace int, id resource.ID, revision uint64) (*resource.AccessProfileRevision, error) {
	if workspace != f.profile.WorkspaceID || id != f.profile.ID || f.revisions[revision] == nil {
		return nil, resource.ErrAccessProfileNotFound
	}
	return f.revisions[revision], nil
}
func (f *bindingFixture) GetHost(_ context.Context, workspace int, id resource.ID) (*resource.Host, error) {
	if workspace != f.host.Metadata.WorkspaceID || id != f.host.Metadata.ID {
		return nil, resource.ErrHostNotFound
	}
	return f.host, nil
}
func (f *bindingFixture) GetOperation(_ context.Context, workspace int, id resource.ID) (*resource.Operation, error) {
	if f.operation == nil || workspace != f.operation.WorkspaceID || id != f.operation.ID {
		return nil, resource.ErrOperationNotFound
	}
	return f.operation, nil
}

func TestProfileAuthorizerBindsSecretToTrustedWorkloadAndResources(t *testing.T) {
	fixture := newBindingFixture(t)
	cm, err := NewProfileAuthorizer(fixture, WorkloadControlManager)
	if err != nil {
		t.Fatal(err)
	}
	request := ResolutionRequest{WorkloadID: WorkloadControlManager, Ref: Ref{ID: "ssh-secret", Version: 3}, Purpose: PurposeSSHCredential,
		WorkspaceID: fixture.profile.WorkspaceID, AccessProfileID: string(fixture.profile.ID), AccessProfileRevision: 1, HostID: string(fixture.host.Metadata.ID)}
	if err := cm.AuthorizeSecretResolution(context.Background(), request); err != nil {
		t.Fatalf("valid Host binding denied: %v", err)
	}
	request.Ref.Version++
	if err := cm.AuthorizeSecretResolution(context.Background(), request); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong secret version was not denied: %v", err)
	}
	request.Ref.Version--
	request.WorkspaceID++
	if err := cm.AuthorizeSecretResolution(context.Background(), request); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("foreign workspace was not denied: %v", err)
	}
}

func TestProfileAuthorizerBindsOperationsToCapturedRevision(t *testing.T) {
	fixture := newBindingFixture(t)
	operation, err := resource.NewRunOperation(fixture.profile.WorkspaceID, resource.ActorRef{UserID: 1}, resource.OperationSpec{
		Targets: []resource.OperationTargetSpec{{HostID: fixture.host.Metadata.ID, HostGeneration: fixture.host.Metadata.Generation,
			DisplayName: fixture.host.Metadata.Name, AccessProfileID: fixture.profile.ID, AccessProfileRevision: 1}},
		Run: &resource.RunSpec{Argv: []string{"true"}, TimeoutSeconds: 30, Concurrency: 1},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fixture.operation = operation
	worker, err := NewProfileAuthorizer(fixture, WorkloadOperationWorker)
	if err != nil {
		t.Fatal(err)
	}
	request := ResolutionRequest{WorkloadID: WorkloadOperationWorker, Ref: Ref{ID: "ssh-secret", Version: 3}, Purpose: PurposeSSHCredential,
		WorkspaceID: fixture.profile.WorkspaceID, AccessProfileID: string(fixture.profile.ID), AccessProfileRevision: 1,
		HostID: string(fixture.host.Metadata.ID), OperationID: string(operation.ID)}
	if err := worker.AuthorizeSecretResolution(context.Background(), request); err != nil {
		t.Fatalf("valid Operation snapshot denied: %v", err)
	}
	request.OperationID = string(fixture.host.Metadata.ID)
	if err := worker.AuthorizeSecretResolution(context.Background(), request); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong Operation binding was not denied: %v", err)
	}
	fixture.profile.CurrentRevision = 2
	fixture.revisions[2] = &resource.AccessProfileRevision{ProfileID: fixture.profile.ID, WorkspaceID: fixture.profile.WorkspaceID, Revision: 2,
		Spec: resource.AccessProfileSpec{Enabled: false}, CreatedAt: time.Now(), CreatedBy: 1}
	request.OperationID = string(operation.ID)
	if err := worker.AuthorizeSecretResolution(context.Background(), request); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("disabled profile did not revoke Operation credential access: %v", err)
	}
}

func newBindingFixture(t *testing.T) *bindingFixture {
	t.Helper()
	profile, revision, err := resource.NewAccessProfile(7, "profile", "", resource.AccessProfileSpec{Connector: resource.ConnectorSSH, Username: "deploy",
		Authentication: resource.AuthenticationPassword, Credential: resource.SecretReference{ID: "ssh-secret", Version: 3}, Enabled: true}, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	host, err := resource.NewHost(7, 0, "host", nil, resource.HostSpec{AccessProfileID: profile.ID}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return &bindingFixture{profile: profile, revisions: map[uint64]*resource.AccessProfileRevision{1: &revision}, host: host}
}
