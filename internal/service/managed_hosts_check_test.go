package service

import (
	"context"
	"errors"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

type fakeChecker struct {
	observation resource.HostObservation
	started     chan struct{}
	release     chan struct{}
	calls       int
}

func (f *fakeChecker) ReconcileHost(context.Context, *resource.Host) (resource.HostObservation, error) {
	f.calls++
	if f.started != nil {
		f.started <- struct{}{}
		<-f.release
	}
	return f.observation, nil
}

func checkHostFixture(t *testing.T) (*managedFixture, resource.ID) {
	t.Helper()
	f := newManagedFixture(t)
	host, err := f.service.CreateHost(context.Background(), f.owner, "web-1", nil, resource.HostSpec{
		AccessTarget: resource.HostAccessTarget{Endpoint: "192.0.2.10:22", Trust: []string{"SHA256:test"}}})
	if err != nil {
		t.Fatal(err)
	}
	return f, host.Metadata.ID
}

func TestCheckHostAuthorizationAndAvailability(t *testing.T) {
	ctx := context.Background()
	f, id := checkHostFixture(t)
	if _, err := f.service.CheckHost(ctx, f.owner, id); !errors.Is(err, ErrHostCheckUnavailable) {
		t.Fatalf("no checker: %v", err)
	}
	f.service.UseHostChecker(&fakeChecker{})
	for name, principal := range map[string]domain.Principal{"reader": f.reader, "anonymous": {}} {
		if _, err := f.service.CheckHost(ctx, principal, id); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Errorf("%s: %v, want permission denied", name, err)
		}
	}
	if _, err := f.service.CheckHost(ctx, f.operator, id); err != nil {
		t.Errorf("operator: %v", err)
	}
	other := domain.Principal{UserID: f.owner.UserID, WorkspaceID: f.owner.WorkspaceID + 99, Role: domain.RoleOwner}
	if _, err := f.service.CheckHost(ctx, other, id); !errors.Is(err, resource.ErrHostNotFound) {
		t.Errorf("another workspace: %v, want not found", err)
	}
}

// A check reports what the verifier saw and never writes: a verifier that
// asks to pin keys (tofu) is ignored.
func TestCheckHostReportsAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	f, id := checkHostFixture(t)
	f.service.UseHostChecker(&fakeChecker{observation: resource.HostObservation{
		Conditions: []resource.Condition{
			{Type: "Ready", Status: resource.ConditionFalse, Reason: "HostKeyChanged", Message: "the host presents SHA256:new"},
			{Type: "Reachable", Status: resource.ConditionTrue, Reason: "Reachable"},
		},
		TrustHostKeys: []string{"SHA256:new"},
	}})
	before, err := f.service.GetHost(ctx, f.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.service.CheckHost(ctx, f.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ready || !got.Reachable || got.Reason != "HostKeyChanged" || got.Message != "the host presents SHA256:new" {
		t.Fatalf("check = %+v", got)
	}
	after, err := f.service.GetHost(ctx, f.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Metadata.Revision != before.Metadata.Revision || len(after.Spec.AccessTarget.Trust) != 1 || len(after.Status.ObservedHostKeys) != 0 {
		t.Fatalf("a check wrote to the host: before %+v after %+v", before.Metadata, after.Metadata)
	}
}

// A second check of the same Host while one runs is refused, so repeated
// requests cannot pile up connections; after it ends, checks work again.
func TestCheckHostRefusesAConcurrentCheckOfTheSameHost(t *testing.T) {
	ctx := context.Background()
	f, id := checkHostFixture(t)
	checker := &fakeChecker{started: make(chan struct{}), release: make(chan struct{})}
	f.service.UseHostChecker(checker)
	done := make(chan error, 1)
	go func() {
		_, err := f.service.CheckHost(ctx, f.owner, id)
		done <- err
	}()
	<-checker.started
	if _, err := f.service.CheckHost(ctx, f.owner, id); !errors.Is(err, ErrHostCheckInProgress) {
		t.Fatalf("second check: %v, want ErrHostCheckInProgress", err)
	}
	checker.release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	go func() { checker.release <- struct{}{} }()
	go func() { <-checker.started }()
	if _, err := f.service.CheckHost(ctx, f.owner, id); err != nil {
		t.Fatalf("after the first ended: %v", err)
	}
}
