package resource

import (
	"errors"
	"testing"
	"time"
)

func TestHostGenerationAndRevision(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	host, err := NewHost(1, 0, "edge-1", nil, HostSpec{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if host.Metadata.Generation != 1 || host.Metadata.Revision != 1 || host.Spec.Presence != PresenceManaged {
		t.Fatalf("initial Host = generation %d revision %d presence %q", host.Metadata.Generation, host.Metadata.Revision, host.Spec.Presence)
	}
	if err := host.UpdateMetadata(1, "edge-primary", 0, map[string]string{"role": "edge"}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if host.Metadata.Generation != 1 || host.Metadata.Revision != 2 {
		t.Fatalf("metadata update should only increment revision: generation=%d revision=%d", host.Metadata.Generation, host.Metadata.Revision)
	}
	if err := host.UpdateSpec(1, HostSpec{Presence: PresenceAbsent}, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if host.Metadata.Generation != 2 || host.Metadata.Revision != 3 {
		t.Fatalf("spec update should increment generation and revision: generation=%d revision=%d", host.Metadata.Generation, host.Metadata.Revision)
	}
	if err := host.Observe(1, HostActive, nil, now.Add(3*time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale observation error = %v, want ErrConflict", err)
	}
}

func TestOperationLifecycle(t *testing.T) {
	hostID, err := NewResourceID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	op, err := NewRunOperation(1, ActorRef{UserID: 5}, OperationSpec{
		Targets: []OperationTargetSpec{{HostID: hostID, HostGeneration: 1, DisplayName: "edge-1"}},
		Run:     &RunSpec{Argv: []string{"/usr/bin/true"}, TimeoutSeconds: 30, Concurrency: 1},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Start(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := op.SetTargetState(hostID, TargetRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := op.SetTargetState(hostID, TargetSucceeded, &TargetResult{ExitCode: intPointer(0)}); err != nil {
		t.Fatal(err)
	}
	if err := op.Finish(OperationSucceeded, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if !op.IsTerminal() || op.Revision != 5 {
		t.Fatalf("final Operation state=%s revision=%d", op.Status.State, op.Revision)
	}
}

func TestQueuedOperationCancellation(t *testing.T) {
	hostID, err := NewResourceID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	op, err := NewRunOperation(1, ActorRef{}, OperationSpec{
		Targets: []OperationTargetSpec{{HostID: hostID, HostGeneration: 1, DisplayName: "edge"}},
		Run:     &RunSpec{Argv: []string{"/usr/bin/true"}, TimeoutSeconds: 10, Concurrency: 1},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.RequestCancel(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if op.Status.State != OperationCancelled || op.Status.Targets[0].State != TargetCancelled || !op.IsTerminal() {
		t.Fatalf("queued cancellation state = %s, target = %s", op.Status.State, op.Status.Targets[0].State)
	}
}

func intPointer(value int) *int { return &value }
