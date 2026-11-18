package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/store"
)

type denyResolution struct{}

func (denyResolution) AuthorizeSecretResolution(context.Context, secrets.ResolutionRequest) error {
	return secrets.ErrUnauthorized
}

// managedFixture is the managed-resource service on real SQLite stores and a
// real Secret Store, with an execution policy the test can switch.
type managedFixture struct {
	service                 *ManagedResourceService
	secrets                 *secrets.Service
	owner, operator, reader domain.Principal
	execution               atomic.Pointer[domain.ExecutionPolicy]
}

func newManagedFixture(t *testing.T) *managedFixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	stores, err := store.Open(ctx, store.Config{Engine: store.EngineSQLite, SQLitePath: filepath.Join(dir, "m.db"),
		EncryptionSecret: "managed-flow-test-key-material-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	f := &managedFixture{}
	member := func(name string, role domain.Role) domain.Principal {
		user, err := stores.Users.CreateUser(ctx, name, name+"@example.test", "hash")
		if err != nil {
			t.Fatal(err)
		}
		if role != domain.RoleReader {
			if err := stores.Users.UpdateWorkspaceMemberRole(ctx, user.WorkspaceID, user.ID, role); err != nil {
				t.Fatal(err)
			}
		}
		return domain.Principal{UserID: user.ID, WorkspaceID: user.WorkspaceID, Role: role}
	}
	f.owner, f.operator, f.reader = member("owner", domain.RoleOwner), member("operator", domain.RoleOperator), member("reader", domain.RoleReader)
	if f.secrets, err = secrets.New(stores.Management, &secrets.StaticKeyProvider{KeyID: "k1", Key: bytes.Repeat([]byte{3}, 32)}, denyResolution{}); err != nil {
		t.Fatal(err)
	}
	f.execution.Store(&domain.ExecutionPolicy{})
	if f.service, err = NewManagedResourceService(stores.ResourceHosts, stores.ResourceProfiles, stores.Operations,
		func() domain.ExecutionPolicy { return *f.execution.Load() }, f.secrets); err != nil {
		t.Fatal(err)
	}
	f.service.UseEnvironments(stores.Environments)
	return f
}

func TestManagedResourceFlow(t *testing.T) {
	ctx := context.Background()
	f := newManagedFixture(t)
	s := f.service

	secret, err := f.secrets.Create(ctx, f.owner, secrets.Input{Name: "deploy-key", Purpose: secrets.PurposeSSHCredential, Value: "key material"})
	if err != nil {
		t.Fatal(err)
	}
	spec := resource.AccessProfileSpec{Connector: resource.ConnectorSSH, Authentication: resource.AuthenticationPrivateKey,
		Username: "deploy", Enabled: true, Credential: resource.SecretReference{ID: secret.ID, Version: 1}}

	// Access Profiles: owner only, and the secret must exist.
	if _, err := s.CreateAccessProfile(ctx, f.operator, "web", "", spec); !errors.Is(err, ErrAccessProfileOwnerRequired) {
		t.Fatalf("operator creating a profile = %v", err)
	}
	missing := spec
	missing.Credential.ID = "no-such-secret"
	if _, err := s.CreateAccessProfile(ctx, f.owner, "web", "", missing); err == nil {
		t.Fatal("a profile pointing at a missing secret was created")
	}
	profile, err := s.CreateAccessProfile(ctx, f.owner, "web", "web servers", spec)
	if err != nil {
		t.Fatal(err)
	}
	disabled := spec
	disabled.Username = "ops"
	if _, err := s.UpdateAccessProfile(ctx, f.operator, profile.ID, 1, disabled); !errors.Is(err, ErrAccessProfileOwnerRequired) {
		t.Fatalf("operator updating a profile = %v", err)
	}
	if revision, err := s.UpdateAccessProfile(ctx, f.owner, profile.ID, 1, disabled); err != nil || revision.Revision != 2 {
		t.Fatalf("update profile = %+v, %v", revision, err)
	}
	view, err := s.GetAccessProfile(ctx, f.reader, profile.ID)
	if err != nil || view.Spec.Username != "ops" || view.Spec.Credential.ID != secret.ID {
		t.Fatalf("reader view = %+v, %v", view, err)
	}
	if page, err := s.ListAccessProfiles(ctx, f.reader, "", 10); err != nil || len(page.Items) != 1 {
		t.Fatalf("list profiles = %+v, %v", page, err)
	}

	// Hosts: inventory:write to create and label; readers list and filter.
	bound := resource.HostSpec{Presence: resource.PresenceManaged, AccessProfileID: profile.ID, ProfileRevision: 2,
		AccessTarget: resource.HostAccessTarget{Endpoint: "192.0.2.10:22", Trust: []string{"SHA256:test"}}}
	web1, err := s.CreateHost(ctx, f.owner, "web-1", map[string]string{"tier": "web"}, bound)
	if err != nil {
		t.Fatal(err)
	}
	web2, err := s.CreateHost(ctx, f.owner, "web-2", map[string]string{"tier": "web"}, bound)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateHost(ctx, f.operator, "db-1", map[string]string{"tier": "db"}, resource.HostSpec{Presence: resource.PresenceManaged}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateHost(ctx, f.reader, "nope", nil, resource.HostSpec{Presence: resource.PresenceManaged}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("reader creating a host = %v", err)
	}
	page, err := s.ListHosts(ctx, f.reader, HostQuery{Selector: "tier=web", Limit: 10})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("selector tier=web = %d hosts, %v", len(page.Items), err)
	}
	if _, err := s.ListHosts(ctx, f.reader, HostQuery{Selector: "tier in (", Limit: 10}); err == nil {
		t.Fatal("an invalid selector was accepted")
	}
	if host, err := s.GetHost(ctx, f.reader, web1.Metadata.ID); err != nil || host.Metadata.Name != "web-1" {
		t.Fatalf("get host = %+v, %v", host, err)
	}
	if _, err := s.SetHostLabels(ctx, f.reader, web2.Metadata.ID, web2.Metadata.Revision, nil); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("reader labelling = %v", err)
	}
	if _, err := s.SetHostLabels(ctx, f.operator, web2.Metadata.ID, web2.Metadata.Revision+5, nil); !errors.Is(err, resource.ErrConflict) {
		t.Fatalf("stale labels = %v", err)
	}
	relabelled, err := s.SetHostLabels(ctx, f.operator, web2.Metadata.ID, web2.Metadata.Revision, map[string]string{"tier": "web", "canary": "yes"})
	if err != nil || relabelled.Metadata.Labels["canary"] != "yes" {
		t.Fatalf("relabel = %+v, %v", relabelled, err)
	}

	// Operations: preview, policy, the reviewed target hash, cancel.
	rule := domain.TargetRule{Selector: "tier=web"}
	preview, err := s.PreviewTargets(ctx, f.reader, rule)
	if err != nil || len(preview.Matched) != 2 || preview.TargetsHash == "" {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	run := resource.RunSpec{Argv: []string{"uptime"}, TimeoutSeconds: 30, Concurrency: 1}
	if _, err := s.SubmitOperationForRule(ctx, f.operator, rule, preview.TargetsHash, run); !errors.Is(err, domain.ErrExecutionNotAllowed) {
		t.Fatalf("submit with execution disabled = %v", err)
	}
	policy, err := domain.NewExecutionPolicy(true, []string{"uptime"})
	if err != nil {
		t.Fatal(err)
	}
	f.execution.Store(&policy)
	if _, err := s.SubmitOperationForRule(ctx, f.reader, rule, "", run); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("reader submit = %v", err)
	}
	if _, err := s.SubmitOperationForRule(ctx, f.operator, rule, "stale-hash", run); !errors.Is(err, resource.ErrTargetsChanged) {
		t.Fatalf("submit with a stale target hash = %v", err)
	}
	if _, err := s.SubmitOperationForRule(ctx, f.operator, domain.TargetRule{Selector: "tier=none"}, "", run); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("submit matching no hosts = %v", err)
	}
	if _, err := s.SubmitOperationForRule(ctx, f.operator, domain.TargetRule{All: true}, "", run); !errors.Is(err, resource.ErrInvalidResource) {
		t.Fatalf("submit including an unbound host = %v", err)
	}
	if _, err := s.SubmitOperationForRule(ctx, f.operator, rule, "", resource.RunSpec{Argv: []string{"rm", "-rf", "/"}, TimeoutSeconds: 30, Concurrency: 1}); !errors.Is(err, domain.ErrExecutionNotAllowed) {
		t.Fatalf("submit of a program outside the allowlist = %v", err)
	}
	operation, err := s.SubmitOperationForRule(ctx, f.operator, rule, preview.TargetsHash, run)
	if err != nil || len(operation.Spec.Targets) != 2 {
		t.Fatalf("submit = %+v, %v", operation, err)
	}
	if list, err := s.ListOperations(ctx, f.reader, resource.OperationListFilter{WorkspaceID: f.reader.WorkspaceID, Limit: 10}); err != nil || len(list.Items) != 1 {
		t.Fatalf("list operations = %+v, %v", list, err)
	}
	if _, err := s.CancelOperation(ctx, f.reader, operation.ID); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("reader cancel = %v", err)
	}
	cancelled, err := s.CancelOperation(ctx, f.operator, operation.ID)
	if err != nil || !cancelled.Status.CancelAsked {
		t.Fatalf("cancel = %+v, %v", cancelled, err)
	}
	if again, err := s.CancelOperation(ctx, f.operator, operation.ID); err != nil || !again.Status.CancelAsked {
		t.Fatalf("second cancel = %+v, %v", again, err)
	}

	// Deletion: revisions are checked; profiles stay owner-only.
	if err := s.DeleteHost(ctx, f.operator, web1.Metadata.ID, web1.Metadata.Revision+9); !errors.Is(err, resource.ErrConflict) {
		t.Fatalf("stale host delete = %v", err)
	}
	if err := s.DeleteHost(ctx, f.reader, web1.Metadata.ID, web1.Metadata.Revision); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("reader host delete = %v", err)
	}
	if err := s.DeleteHost(ctx, f.operator, web1.Metadata.ID, web1.Metadata.Revision); err != nil {
		t.Fatalf("host delete = %v", err)
	}
	if err := s.DeleteAccessProfile(ctx, f.operator, profile.ID); !errors.Is(err, ErrAccessProfileOwnerRequired) {
		t.Fatalf("operator profile delete = %v", err)
	}
	// web-2 still uses the profile, so it cannot be deleted yet.
	if err := s.DeleteAccessProfile(ctx, f.owner, profile.ID); !errors.Is(err, resource.ErrConflict) {
		t.Fatalf("deleting a profile in use = %v, want ErrConflict", err)
	}
	current, err := s.GetHost(ctx, f.reader, web2.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteHost(ctx, f.operator, web2.Metadata.ID, current.Metadata.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccessProfile(ctx, f.owner, profile.ID); err != nil {
		t.Fatalf("deleting an unused profile = %v", err)
	}
	if _, err := s.GetAccessProfile(ctx, f.reader, profile.ID); err == nil {
		t.Fatal("a deleted profile is still readable")
	}
}
