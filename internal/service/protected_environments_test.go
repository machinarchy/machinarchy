package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/secrets"
)

func requireProtected(t *testing.T, err error, names ...string) {
	t.Helper()
	var protected *domain.ProtectedEnvironmentError
	if !errors.Is(err, domain.ErrProtectedEnvironmentConfirmation) || !errors.As(err, &protected) {
		t.Fatalf("err = %v, want protected environment confirmation", err)
	}
	if len(protected.Names) != len(names) {
		t.Fatalf("names = %v, want %v", protected.Names, names)
	}
	for i, name := range names {
		if protected.Names[i] != name {
			t.Fatalf("names = %v, want %v", protected.Names, names)
		}
	}
}

// Guarded calls on hosts in a protected environment need the exact name, for
// every principal, however the targets are chosen.
func TestProtectedEnvironmentsAreEnforcedByTheServer(t *testing.T) {
	ctx := context.Background()
	f := newManagedFixture(t)
	s := f.service
	policy, err := domain.NewExecutionPolicy(true, []string{"uptime"})
	if err != nil {
		t.Fatal(err)
	}
	f.execution.Store(&policy)
	secret, err := f.secrets.Create(ctx, f.owner, secrets.Input{Name: "k", Purpose: secrets.PurposeSSHCredential, Value: "key"})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := s.CreateAccessProfile(ctx, f.owner, "web", "", resource.AccessProfileSpec{Connector: resource.ConnectorSSH,
		Authentication: resource.AuthenticationPrivateKey, Username: "deploy", Enabled: true, Credential: resource.SecretReference{ID: secret.ID, Version: 1}})
	if err != nil {
		t.Fatal(err)
	}
	spec := resource.HostSpec{Presence: resource.PresenceManaged, AccessProfileID: profile.ID, ProfileRevision: 1,
		AccessTarget: resource.HostAccessTarget{Endpoint: "192.0.2.10:22", Trust: []string{"SHA256:test"}}}
	prod := &domain.Environment{WorkspaceID: f.owner.WorkspaceID, Name: "prod", Protected: true}
	stage := &domain.Environment{WorkspaceID: f.owner.WorkspaceID, Name: "stage", Protected: true}
	open := &domain.Environment{WorkspaceID: f.owner.WorkspaceID, Name: "dev"}
	for _, environment := range []*domain.Environment{prod, stage, open} {
		if err := f.stores.Environments.CreateEnvironment(ctx, environment); err != nil {
			t.Fatal(err)
		}
	}
	inProd, err := s.CreateHostInEnvironment(ctx, f.owner, prod.ID, "p1", nil, spec)
	if err != nil {
		t.Fatal(err)
	}
	inStage, err := s.CreateHostInEnvironment(ctx, f.owner, stage.ID, "s1", nil, spec)
	if err != nil {
		t.Fatal(err)
	}
	inDev, err := s.CreateHostInEnvironment(ctx, f.owner, open.ID, "d1", nil, spec)
	if err != nil {
		t.Fatal(err)
	}
	confirm := func(names ...string) context.Context { return domain.WithConfirmedEnvironments(ctx, names) }
	run := resource.RunSpec{Argv: []string{"uptime"}, TimeoutSeconds: 30, Concurrency: 1}

	// Runs: by id, by rule, and the bulk rule that includes a protected host.
	for _, principal := range []domain.Principal{f.operator, f.owner} {
		_, err = s.SubmitOperation(ctx, principal, []resource.ID{inProd.Metadata.ID}, run)
		requireProtected(t, err, "prod")
		_, err = s.SubmitOperation(confirm("stage"), principal, []resource.ID{inProd.Metadata.ID}, run)
		requireProtected(t, err, "prod") // wrong name
		_, err = s.SubmitOperation(confirm("PROD"), principal, []resource.ID{inProd.Metadata.ID}, run)
		requireProtected(t, err, "prod") // names are confirmed exactly
		_, err = s.SubmitOperationForRule(ctx, principal, domain.TargetRule{All: true}, "", run)
		requireProtected(t, err, "prod", "stage")
		_, err = s.SubmitOperationForRule(confirm("prod"), principal, domain.TargetRule{All: true}, "", run)
		requireProtected(t, err, "stage") // one confirmation per protected environment
	}
	if _, err := s.SubmitOperation(ctx, f.operator, []resource.ID{inDev.Metadata.ID}, run); err != nil {
		t.Fatalf("run on an unprotected environment = %v", err)
	}
	if _, err := s.SubmitOperationForRule(confirm("prod", "stage"), f.operator, domain.TargetRule{All: true}, "", run); err != nil {
		t.Fatalf("confirmed bulk run = %v", err)
	}
	if _, err := s.SubmitOperation(confirm("prod"), f.operator, []resource.ID{inProd.Metadata.ID}, run); err != nil {
		t.Fatalf("confirmed run = %v", err)
	}

	// A refused call does not reveal more than the names, and permission is checked first.
	if _, err := s.SubmitOperation(ctx, f.reader, []resource.ID{inProd.Metadata.ID}, run); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("reader run = %v", err)
	}

	// Host update, move, trust and delete.
	current, err := s.GetHost(ctx, f.owner, inProd.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = s.UpdateHostInEnvironment(ctx, f.operator, current.Metadata.ID, current.Metadata.Revision, current.Metadata.Generation, 0, current.Spec)
	requireProtected(t, err, "prod")
	err = s.UpdateHostInEnvironment(ctx, f.operator, current.Metadata.ID, current.Metadata.Revision, current.Metadata.Generation, open.ID, current.Spec)
	requireProtected(t, err, "prod") // moving out of prod
	dev, _ := s.GetHost(ctx, f.owner, inDev.Metadata.ID)
	err = s.UpdateHostInEnvironment(ctx, f.operator, dev.Metadata.ID, dev.Metadata.Revision, dev.Metadata.Generation, stage.ID, dev.Spec)
	requireProtected(t, err, "stage") // moving into stage
	_, err = s.TrustHostKeys(ctx, f.owner, current.Metadata.ID, current.Metadata.Generation, []string{"SHA256:" + strings.Repeat("A", 43)}, true)
	requireProtected(t, err, "prod")
	// A host with no queued runs, so a confirmed delete is not refused for another reason.
	spare, err := s.CreateHostInEnvironment(ctx, f.owner, prod.ID, "p2", nil, spec)
	if err != nil {
		t.Fatal(err)
	}
	err = s.DeleteHost(ctx, f.operator, spare.Metadata.ID, spare.Metadata.Revision)
	requireProtected(t, err, "prod")
	requireProtected(t, s.DeleteHost(confirm("stage"), f.operator, spare.Metadata.ID, spare.Metadata.Revision), "prod")
	if err := s.UpdateHostInEnvironment(confirm("stage"), f.operator, dev.Metadata.ID, dev.Metadata.Revision, dev.Metadata.Generation, stage.ID, dev.Spec); err != nil {
		t.Fatalf("confirmed move = %v", err)
	}
	if _, err := s.TrustHostKeys(confirm("prod"), f.owner, current.Metadata.ID, current.Metadata.Generation, []string{"SHA256:" + strings.Repeat("A", 43)}, true); err != nil {
		t.Fatalf("confirmed trust = %v", err)
	}
	if err := s.DeleteHost(confirm("prod"), f.operator, spare.Metadata.ID, spare.Metadata.Revision); err != nil {
		t.Fatalf("confirmed delete = %v", err)
	}
	_ = inStage
}
