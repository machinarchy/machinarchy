package operationworker_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/hostaccess"
)

// timedConnector runs every command for a fixed time, or until it is
// cancelled, and records how many ran at once.
type timedConnector struct {
	each    time.Duration
	running atomic.Int32
	peak    atomic.Int32
	started atomic.Int32
}

func (c *timedConnector) Verify(context.Context, *resource.Host, *resource.AccessProfileRevision) error {
	return nil
}

func (c *timedConnector) Execute(ctx context.Context, _ *resource.Host, _ *resource.AccessProfileRevision, _ string, _ []string) (hostaccess.Result, error) {
	c.started.Add(1)
	now := c.running.Add(1)
	defer c.running.Add(-1)
	for {
		peak := c.peak.Load()
		if now <= peak || c.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	select {
	case <-time.After(c.each):
		return hostaccess.Result{Stdout: "ok"}, nil
	case <-ctx.Done():
		return hostaccess.Result{}, ctx.Err()
	}
}

// sshHosts creates n hosts on an SSH profile; the fake connector never dials.
func (e *capabilityEnv) sshHosts(n int) (resource.ID, uint64, []*resource.Host) {
	e.t.Helper()
	profile, revision, err := resource.NewAccessProfile(1, "ssh", "", resource.AccessProfileSpec{Connector: resource.ConnectorSSH,
		Username: "deploy", Authentication: resource.AuthenticationPassword, Enabled: true,
		Credential: resource.SecretReference{ID: "secret-1", Version: 1}}, 1, time.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.stores.ResourceProfiles.CreateAccessProfile(e.ctx, profile, revision); err != nil {
		e.t.Fatal(err)
	}
	hosts := make([]*resource.Host, 0, n)
	for i := range n {
		host, err := resource.NewHost(1, 0, fmt.Sprintf("web-%02d", i), nil, resource.HostSpec{AccessProfileID: profile.ID,
			AccessTarget: resource.HostAccessTarget{Endpoint: fmt.Sprintf("10.0.0.%d:22", i+1)}}, time.Now())
		if err != nil {
			e.t.Fatal(err)
		}
		if err := e.stores.ResourceHosts.CreateHost(e.ctx, host); err != nil {
			e.t.Fatal(err)
		}
		hosts = append(hosts, host)
	}
	return profile.ID, revision.Revision, hosts
}

func (e *capabilityEnv) submitAttached(profile resource.ID, revision uint64, concurrency int, hosts []*resource.Host) resource.ID {
	e.t.Helper()
	var targets []resource.OperationTargetSpec
	for _, host := range hosts {
		targets = append(targets, resource.OperationTargetSpec{HostID: host.Metadata.ID, HostGeneration: host.Metadata.Generation,
			DisplayName: host.Metadata.Name, AccessProfileID: profile, AccessProfileRevision: revision})
	}
	op, err := resource.NewRunOperation(1, resource.ActorRef{UserID: 1}, resource.OperationSpec{Targets: targets,
		Run: &resource.RunSpec{Argv: []string{"uptime"}, TimeoutSeconds: 30, Concurrency: concurrency}}, time.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.stores.Operations.CreateOperation(e.ctx, op); err != nil {
		e.t.Fatal(err)
	}
	return op.ID
}

// TestAttachedRunFansOutToConcurrency: N targets at concurrency C take about
// N/C times one target, never with more than C at once (D-10).
func TestAttachedRunFansOutToConcurrency(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ targets, concurrency int }{{12, 4}, {5, 1}, {3, 20}} {
		t.Run(fmt.Sprintf("%d targets at %d", c.targets, c.concurrency), func(t *testing.T) {
			t.Parallel()
			e := newCapabilityEnv(t)
			connector := &timedConnector{each: 300 * time.Millisecond}
			e.connector = connector
			profile, revision, hosts := e.sshHosts(c.targets)
			id := e.submitAttached(profile, revision, c.concurrency, hosts)
			began := time.Now()
			e.start()
			op := e.waitTerminal(id)
			elapsed := time.Since(began)

			waves := (c.targets + c.concurrency - 1) / c.concurrency
			ideal := time.Duration(waves) * connector.each
			if op.Status.State != resource.OperationSucceeded || targetStates(op)[resource.TargetSucceeded] != c.targets {
				t.Fatalf("Operation %s, targets %v", op.Status.State, targetStates(op))
			}
			if peak, want := int(connector.peak.Load()), min(c.concurrency, c.targets); peak != want {
				t.Errorf("peak concurrency %d, want %d", peak, want)
			}
			// The worker polls the queue every 500 ms; allow that and scheduling.
			if elapsed < ideal || elapsed > ideal+1500*time.Millisecond {
				t.Errorf("took %s, want about %s (%d waves of %s)", elapsed, ideal, waves, connector.each)
			}
			t.Logf("%d targets at concurrency %d: %s (ideal %s)", c.targets, c.concurrency, elapsed.Round(10*time.Millisecond), ideal)
		})
	}
}

// TestAttachedRunCancelStopsLaunchingTargets: after a cancel, no target
// starts; running ones are cancelled and the Operation ends Cancelled.
func TestAttachedRunCancelStopsLaunchingTargets(t *testing.T) {
	t.Parallel()
	e := newCapabilityEnv(t)
	connector := &timedConnector{each: 30 * time.Second}
	e.connector = connector
	profile, revision, hosts := e.sshHosts(8)
	id := e.submitAttached(profile, revision, 2, hosts)
	e.start()
	e.waitFor("two running targets", func() bool { return connector.running.Load() == 2 })
	if _, err := e.worker.Cancel(e.ctx, 1, id); err != nil {
		t.Fatal(err)
	}
	op := e.waitTerminal(id)
	if op.Status.State != resource.OperationCancelled {
		t.Fatalf("Operation %s, targets %v", op.Status.State, targetStates(op))
	}
	if started := connector.started.Load(); started != 2 {
		t.Errorf("%d targets started, want only the 2 running at cancel", started)
	}
	if states := targetStates(op); states[resource.TargetRunning] != 0 || states[resource.TargetSucceeded] != 0 {
		t.Errorf("targets after cancel: %v", states)
	}
}

// TestAttachedRunWorkerStopInterrupts: stopping the worker mid-run leaves no
// target Running and finishes the Operation as Interrupted.
func TestAttachedRunWorkerStopInterrupts(t *testing.T) {
	t.Parallel()
	e := newCapabilityEnv(t)
	connector := &timedConnector{each: 30 * time.Second}
	e.connector = connector
	profile, revision, hosts := e.sshHosts(6)
	id := e.submitAttached(profile, revision, 3, hosts)
	e.start()
	e.waitFor("three running targets", func() bool { return connector.running.Load() == 3 })
	var stopped sync.WaitGroup
	stopped.Add(1)
	go func() { defer stopped.Done(); e.stop() }()
	stopped.Wait()
	op := e.op(id)
	if op.Status.State != resource.OperationInterrupted || targetStates(op)[resource.TargetRunning] != 0 {
		t.Fatalf("after the worker stopped: %s, targets %v", op.Status.State, targetStates(op))
	}
	if started := connector.started.Load(); started != 3 {
		t.Errorf("%d targets started, want 3", started)
	}
}
