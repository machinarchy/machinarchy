package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

var (
	opsPrincipal     = domain.Principal{UserID: 1, Username: "ops", WorkspaceID: 1, Role: domain.RoleOperator}
	readerRunUser    = domain.Principal{UserID: 2, Username: "reader", WorkspaceID: 1, Role: domain.RoleReader}
	foreignWorkspace = domain.Principal{UserID: 3, Username: "other", WorkspaceID: 2, Role: domain.RoleOperator}
)

func allowAllPrograms(t *testing.T) domain.ExecutionPolicy {
	t.Helper()
	policy, err := domain.NewExecutionPolicy(true, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func standardHosts() *runHosts {
	return newRunHosts(
		runHost(1, "web-01", "10.0.0.1", 22, map[string]string{"role": "web"}),
		runHost(2, "web-02", "10.0.0.2", 22, map[string]string{"role": "web"}),
		runHost(3, "db-01", "10.0.0.3", 22, map[string]string{"role": "db"}),
	)
}

func newRunSvc(t *testing.T, store *memRunStore, hosts *runHosts, runner *fakeRunner, policy domain.ExecutionPolicy, opts ...Option) RunManager {
	t.Helper()
	capacity, err := NewSSHCapacity(32)
	if err != nil {
		t.Fatal(err)
	}
	all := append([]Option{WithCommandRunner(runner), WithRunTiming(20*time.Millisecond, 400*time.Millisecond)}, opts...)
	svc, err := NewRunService(store, hosts, noEnvironments{}, capacity, policy, all...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Close(ctx)
	})
	return svc
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func blockUntilStopped(ctx context.Context, _ runnerCall) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestSubmitChecksPermissionAndPolicyBeforeTouchingTheInventory(t *testing.T) {
	disabled, _ := domain.NewExecutionPolicy(false, nil)
	listed, _ := domain.NewExecutionPolicy(true, []string{"uptime"})
	tests := []struct {
		name      string
		principal domain.Principal
		policy    domain.ExecutionPolicy
		request   domain.RunRequest
		want      error
	}{
		{"execution disabled", opsPrincipal, disabled, domain.RunRequest{Argv: []string{"uptime"}, Rule: domain.TargetRule{All: true}}, domain.ErrExecutionNotAllowed},
		{"program not allowed", opsPrincipal, listed, domain.RunRequest{Argv: []string{"rm", "-rf", "/"}, Rule: domain.TargetRule{All: true}}, domain.ErrExecutionNotAllowed},
		{"reader cannot execute", readerRunUser, listed, domain.RunRequest{Argv: []string{"uptime"}, Rule: domain.TargetRule{All: true}}, domain.ErrPermissionDenied},
		{"no workspace", domain.Principal{UserID: 1, Role: domain.RoleOwner}, listed, domain.RunRequest{Argv: []string{"uptime"}, Rule: domain.TargetRule{All: true}}, domain.ErrPermissionDenied},
		{"shell syntax in program", opsPrincipal, listed, domain.RunRequest{Argv: []string{"uptime; id"}, Rule: domain.TargetRule{All: true}}, domain.ErrInvalidInput},
		{"empty command", opsPrincipal, listed, domain.RunRequest{Rule: domain.TargetRule{All: true}}, domain.ErrInvalidInput},
		{"timeout too large", opsPrincipal, listed, domain.RunRequest{Argv: []string{"uptime"}, Rule: domain.TargetRule{All: true}, TimeoutSeconds: domain.MaxRunTimeout + 1}, domain.ErrInvalidInput},
		{"negative timeout", opsPrincipal, listed, domain.RunRequest{Argv: []string{"uptime"}, Rule: domain.TargetRule{All: true}, TimeoutSeconds: -1}, domain.ErrInvalidInput},
		{"concurrency too large", opsPrincipal, listed, domain.RunRequest{Argv: []string{"uptime"}, Rule: domain.TargetRule{All: true}, Concurrency: domain.MaxRunConcurrency + 1}, domain.ErrInvalidInput},
		{"empty rule never means all hosts", opsPrincipal, listed, domain.RunRequest{Argv: []string{"uptime"}}, domain.ErrInvalidInput},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, hosts, runner := newMemRunStore(), standardHosts(), &fakeRunner{}
			svc := newRunSvc(t, store, hosts, runner, tc.policy)
			if _, err := svc.Submit(context.Background(), tc.principal, tc.request); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if len(store.runs) != 0 || runner.callCount() != 0 {
				t.Error("a rejected request must not store a run or contact a host")
			}
			if !errors.Is(tc.want, domain.ErrInvalidInput) && hosts.listCalls != 0 {
				t.Error("permission and policy failures must be decided before the inventory is read")
			}
		})
	}
}

func TestSubmitRejectsBadTargetRules(t *testing.T) {
	big := make([]*domain.Host, domain.MaxRunHosts+1)
	for i := range big {
		big[i] = runHost(i+1, "host-"+strconv.Itoa(1000+i), "10.1.0."+strconv.Itoa(i%250+1), 1000+i, nil)
	}
	for name, tc := range map[string]struct {
		hosts *runHosts
		rule  domain.TargetRule
	}{
		"matches nothing":       {standardHosts(), domain.TargetRule{Selector: "role=none"}},
		"unknown include entry": {standardHosts(), domain.TargetRule{Selector: "role=web", Include: []string{"ghost"}}},
		"too many hosts":        {newRunHosts(big...), domain.TargetRule{All: true}},
		"all with a selector":   {standardHosts(), domain.TargetRule{All: true, Selector: "role=web"}},
		"everything excluded":   {standardHosts(), domain.TargetRule{All: true, Exclude: []string{"web-01", "web-02", "db-01"}}},
	} {
		t.Run(name, func(t *testing.T) {
			store, runner := newMemRunStore(), &fakeRunner{}
			svc := newRunSvc(t, store, tc.hosts, runner, allowAllPrograms(t))
			if _, err := svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{Argv: []string{"uptime"}, Rule: tc.rule}); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
			if len(store.runs) != 0 || runner.callCount() != 0 {
				t.Error("nothing may be stored or run")
			}
		})
	}
}

func TestDryRunPlansWithoutStoringOrContacting(t *testing.T) {
	store, runner := newMemRunStore(), &fakeRunner{}
	svc := newRunSvc(t, store, standardHosts(), runner, allowAllPrograms(t))
	run, err := svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{
		Argv: []string{"uptime"}, Rule: domain.TargetRule{Selector: "role=web"}, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunPlanned || run.ID != 0 || len(run.Hosts) != 2 || run.Counts.Pending != 2 || run.TargetsHash == "" {
		t.Fatalf("plan = %+v", run)
	}
	if run.TimeoutSeconds != domain.DefaultRunTimeout || run.Concurrency != domain.DefaultRunConcurrency {
		t.Errorf("a plan shows the defaults that would apply: %d/%d", run.TimeoutSeconds, run.Concurrency)
	}
	time.Sleep(50 * time.Millisecond)
	if len(store.runs) != 0 || runner.callCount() != 0 {
		t.Error("a dry run must not store a run or contact a host")
	}
}

func TestRunIsBoundToThePreviewedHosts(t *testing.T) {
	store, hosts, runner := newMemRunStore(), standardHosts(), &fakeRunner{}
	svc := newRunSvc(t, store, hosts, runner, allowAllPrograms(t))
	plan, err := svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{
		Argv: []string{"uptime"}, Rule: domain.TargetRule{Selector: "role=web"}, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Someone relabels another host so the same selector now matches more hosts.
	hosts.mutate(3, func(h *domain.Host) { h.Labels = map[string]string{"role": "web"} })
	_, err = svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{
		Argv: []string{"uptime"}, Rule: domain.TargetRule{Selector: "role=web"}, ExpectTargetsHash: plan.TargetsHash,
	})
	var changed *domain.TargetsChangedError
	if !errors.Is(err, domain.ErrTargetsChanged) || !errors.As(err, &changed) || changed.Total != 3 || changed.TargetsHash == plan.TargetsHash {
		t.Fatalf("error = %v, want ErrTargetsChanged describing the new 3-host set", err)
	}
	if len(store.runs) != 0 || runner.callCount() != 0 {
		t.Error("a run on unreviewed hosts must not start")
	}

	// Pinning the hosts explicitly still works.
	run, err := svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{
		Argv: []string{"uptime"}, Rule: domain.TargetRule{Include: []string{"web-01", "web-02"}}, ExpectTargetsHash: plan.TargetsHash,
	})
	if err != nil {
		t.Fatalf("a rule that yields the previewed hosts must be accepted: %v", err)
	}
	store.waitFinal(t, run.ID)
}

func TestRunExecutesOnEveryTargetAndRecordsResults(t *testing.T) {
	store, hosts := newMemRunStore(), standardHosts()
	runner := &fakeRunner{behave: func(_ context.Context, call runnerCall) (string, error) {
		return "\x1b[31mhello\x1b[0m from " + call.Address + "\r\n", nil
	}}
	svc := newRunSvc(t, store, hosts, runner, allowAllPrograms(t))

	argv := []string{"echo", "a b", "it's", "$(id)"}
	ctx := domain.WithAuditActor(context.Background(), domain.AuditActor{RequestID: "req-7", Source: "api"})
	run, err := svc.Submit(ctx, opsPrincipal, domain.RunRequest{Argv: argv, Rule: domain.TargetRule{Selector: "role=web"}})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if run.ID == 0 || run.Status != domain.RunQueued || run.Program != "echo" || run.ArgCount != 3 || len(run.Hosts) != 2 ||
		run.CreatedByUserID != 1 || run.CreatedByUsername != "ops" {
		t.Fatalf("accepted run = %+v", run)
	}

	final := store.waitFinal(t, run.ID)
	if final.Status != domain.RunSucceeded || final.Counts.Succeeded != 2 || final.StartedAt == nil || final.FinishedAt == nil {
		t.Fatalf("final run = %+v", final)
	}
	for i, address := range []string{"10.0.0.1", "10.0.0.2"} {
		h := final.Hosts[i]
		if h.Status != domain.RunHostSucceeded || h.ExitCode == nil || *h.ExitCode != 0 ||
			h.Output != "hello from "+address+"\n" || h.StartedAt == nil || h.FinishedAt == nil || h.Address != address {
			t.Errorf("host[%d] = %+v", i, h)
		}
	}
	// Every host received the same fully quoted command and its own credentials.
	wantCommand := domain.QuoteArgv(argv)
	if runner.callCount() != 2 {
		t.Fatalf("runner called %d times, want 2", runner.callCount())
	}
	for _, call := range runner.calls {
		if call.Command != wantCommand || call.Secret != "pw" {
			t.Errorf("call = %+v, want command %s", call, wantCommand)
		}
	}
	// Executor-side audit events are attributed to the person who submitted the run.
	store.mu.Lock()
	finishActors := store.actors["FinishRun"]
	store.mu.Unlock()
	if len(finishActors) != 1 || finishActors[0].UserID != 1 || finishActors[0].Username != "ops" || finishActors[0].Source != "executor" {
		t.Errorf("FinishRun actor = %+v", finishActors)
	}
}

func TestRunReportsMixedOutcomesPerHost(t *testing.T) {
	store, hosts := newMemRunStore(), standardHosts()
	runner := &fakeRunner{behave: func(ctx context.Context, call runnerCall) (string, error) {
		switch call.Address {
		case "10.0.0.2":
			return "", domain.ErrAuthenticationFailed
		case "10.0.0.3":
			<-ctx.Done() // never finishes: hits the per-host timeout
			return "partial output", ctx.Err()
		}
		return "fine", nil
	}}
	svc := newRunSvc(t, store, hosts, runner, allowAllPrograms(t))
	run, err := svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{
		Argv: []string{"uptime"}, Rule: domain.TargetRule{All: true}, TimeoutSeconds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	final := store.waitFinal(t, run.ID)
	byName := map[string]domain.RunHost{}
	for _, h := range final.Hosts {
		byName[h.Name] = h
	}
	if final.Status != domain.RunPartiallySucceeded || final.Counts.Succeeded != 1 || final.Counts.Failed != 1 || final.Counts.TimedOut != 1 {
		t.Fatalf("final run = %s %+v", final.Status, final.Counts)
	}
	if h := byName["web-01"]; h.Status != domain.RunHostSucceeded || h.Output != "fine" {
		t.Errorf("web-01 = %+v", h)
	}
	if h := byName["web-02"]; h.Status != domain.RunHostFailed || h.Error != "SSH authentication failed" {
		t.Errorf("web-02 = %+v", h)
	}
	if h := byName["db-01"]; h.Status != domain.RunHostTimedOut || !strings.Contains(h.Error, "did not finish within 1 seconds") ||
		!strings.Contains(h.Error, "may still be running") || h.Output != "partial output" {
		t.Errorf("db-01 = %+v; a timeout must keep the partial output and say the command may still be running", h)
	}
}

func TestRunNeverExceedsItsConcurrencyLimit(t *testing.T) {
	hosts := newRunHosts()
	for i := 1; i <= 12; i++ {
		hosts.hosts[i] = runHost(i, "host-"+strconv.Itoa(100+i), "10.2.0."+strconv.Itoa(i), 22, nil)
	}
	store := newMemRunStore()
	runner := &fakeRunner{behave: func(ctx context.Context, _ runnerCall) (string, error) {
		select {
		case <-time.After(25 * time.Millisecond):
		case <-ctx.Done():
		}
		return "ok", nil
	}}
	svc := newRunSvc(t, store, hosts, runner, allowAllPrograms(t))
	run, err := svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{
		Argv: []string{"uptime"}, Rule: domain.TargetRule{All: true}, Concurrency: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	final := store.waitFinal(t, run.ID)
	if final.Status != domain.RunSucceeded || final.Counts.Succeeded != 12 {
		t.Fatalf("final run = %s %+v", final.Status, final.Counts)
	}
	if got := runner.max(); got > 3 || got < 2 {
		t.Fatalf("max concurrent commands = %d, want between 2 and the limit 3", got)
	}
}

func TestCancelStopsARunInFlight(t *testing.T) {
	store, hosts := newMemRunStore(), standardHosts()
	runner := &fakeRunner{behave: blockUntilStopped}
	svc := newRunSvc(t, store, hosts, runner, allowAllPrograms(t))
	run, err := svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{
		Argv: []string{"sleep", "600"}, Rule: domain.TargetRule{All: true}, Concurrency: 2, TimeoutSeconds: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "two commands to be running", func() bool { return runner.callCount() == 2 })

	cancelled, err := svc.Cancel(context.Background(), opsPrincipal, run.ID)
	if err != nil || !cancelled.CancelRequested {
		t.Fatalf("Cancel() = %+v, %v", cancelled, err)
	}
	final := store.waitFinal(t, run.ID)
	if final.Status != domain.RunCancelled || final.Counts.Cancelled != 3 || final.Counts.Succeeded != 0 {
		t.Fatalf("final run = %s %+v; running and unstarted hosts must all be cancelled", final.Status, final.Counts)
	}
	for _, h := range final.Hosts {
		if h.Status != domain.RunHostCancelled || h.Error == "" {
			t.Errorf("host %s = %+v; a cancelled host must say why", h.Name, h)
		}
	}
	if runner.callCount() != 2 {
		t.Errorf("runner called %d times; the host that never started must not be contacted after cancellation", runner.callCount())
	}
	if _, err := svc.Cancel(context.Background(), opsPrincipal, run.ID); !errors.Is(err, domain.ErrRunFinished) {
		t.Errorf("cancelling a finished run error = %v, want ErrRunFinished", err)
	}
}

func TestCloseInterruptsRunsInFlightAndRefusesNewOnes(t *testing.T) {
	store, hosts := newMemRunStore(), standardHosts()
	runner := &fakeRunner{behave: blockUntilStopped}
	svc := newRunSvc(t, store, hosts, runner, allowAllPrograms(t))
	run, err := svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{
		Argv: []string{"sleep", "600"}, Rule: domain.TargetRule{All: true}, Concurrency: 2, TimeoutSeconds: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "two commands to be running", func() bool { return runner.callCount() == 2 })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	final := store.snapshot(run.ID)
	if final.Status != domain.RunInterrupted || final.Counts.Interrupted != 3 {
		t.Fatalf("after shutdown the run is %s %+v, want interrupted with every host interrupted", final.Status, final.Counts)
	}
	if _, err := svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{Argv: []string{"uptime"}, Rule: domain.TargetRule{All: true}}); !errors.Is(err, domain.ErrServiceBusy) {
		t.Errorf("Submit after Close error = %v, want ErrServiceBusy", err)
	}
}

func TestRunDoesNotFollowAHostThatMovedOrWasRemoved(t *testing.T) {
	store, hosts := newMemRunStore(), standardHosts()
	gate := make(chan struct{})
	runner := &fakeRunner{behave: func(ctx context.Context, call runnerCall) (string, error) {
		if call.Address == "10.0.0.1" {
			select {
			case <-gate:
			case <-ctx.Done():
			}
		}
		return "ok", nil
	}}
	svc := newRunSvc(t, store, hosts, runner, allowAllPrograms(t))
	// One host at a time, in name order: db-01 first (held at the gate), then the others.
	run, err := svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{
		Argv: []string{"uptime"}, Rule: domain.TargetRule{Include: []string{"web-01", "web-02", "db-01"}}, Concurrency: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Order is db-01, web-01 (blocked), web-02. While web-01 is held, re-point
	// web-02 and delete db-01's record.
	waitFor(t, "web-01 to be running", func() bool {
		runner.mu.Lock()
		defer runner.mu.Unlock()
		for _, c := range runner.calls {
			if c.Address == "10.0.0.1" {
				return true
			}
		}
		return false
	})
	hosts.mutate(2, func(h *domain.Host) { h.IPAddress = "203.0.113.50" })
	close(gate)

	final := store.waitFinal(t, run.ID)
	byName := map[string]domain.RunHost{}
	for _, h := range final.Hosts {
		byName[h.Name] = h
	}
	if h := byName["web-02"]; h.Status != domain.RunHostFailed || !strings.Contains(h.Error, "connection details changed") {
		t.Errorf("web-02 = %+v; a host re-pointed after submission must not be contacted at its new address", h)
	}
	for _, c := range runner.calls {
		if c.Address == "203.0.113.50" {
			t.Error("the command was sent to the host's new address")
		}
	}
	if final.Status != domain.RunPartiallySucceeded {
		t.Errorf("status = %s, want partially_succeeded", final.Status)
	}

	// A host deleted before its turn fails with a clear message.
	store2, hosts2 := newMemRunStore(), standardHosts()
	gate2 := make(chan struct{})
	runner2 := &fakeRunner{behave: func(ctx context.Context, call runnerCall) (string, error) {
		if call.Address == "10.0.0.1" {
			select {
			case <-gate2:
			case <-ctx.Done():
			}
		}
		return "ok", nil
	}}
	svc2 := newRunSvc(t, store2, hosts2, runner2, allowAllPrograms(t))
	run2, err := svc2.Submit(context.Background(), opsPrincipal, domain.RunRequest{
		Argv: []string{"uptime"}, Rule: domain.TargetRule{Include: []string{"web-01", "web-02"}}, Concurrency: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "web-01 to be running", func() bool { return runner2.callCount() == 1 })
	hosts2.remove(2)
	close(gate2)
	final2 := store2.waitFinal(t, run2.ID)
	for _, h := range final2.Hosts {
		if h.Name == "web-02" && (h.Status != domain.RunHostFailed || !strings.Contains(h.Error, "no longer exists")) {
			t.Errorf("web-02 = %+v", h)
		}
	}
}

func TestSubmitRefusesWhenTooManyRunsAreExecuting(t *testing.T) {
	store, hosts := newMemRunStore(), standardHosts()
	runner := &fakeRunner{behave: blockUntilStopped}
	svc := newRunSvc(t, store, hosts, runner, allowAllPrograms(t), WithMaxActiveRuns(1))
	first, err := svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{Argv: []string{"sleep", "600"}, Rule: domain.TargetRule{All: true}, TimeoutSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{Argv: []string{"uptime"}, Rule: domain.TargetRule{All: true}})
	if !errors.Is(err, domain.ErrServiceBusy) {
		t.Fatalf("second Submit error = %v, want ErrServiceBusy", err)
	}
	if len(store.runs) != 1 {
		t.Errorf("%d runs stored; the refused request must not leave a run behind", len(store.runs))
	}
	if _, err := svc.Cancel(context.Background(), opsPrincipal, first.ID); err != nil {
		t.Fatal(err)
	}
	store.waitFinal(t, first.ID)
	waitFor(t, "the slot to be released", func() bool {
		_, err := svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{Argv: []string{"uptime"}, Rule: domain.TargetRule{Include: []string{"web-01"}}, DryRun: false})
		return err == nil
	})
}

func TestRunsQueueForSSHCapacityInsteadOfFailing(t *testing.T) {
	store, hosts := newMemRunStore(), standardHosts()
	runner := &fakeRunner{behave: func(ctx context.Context, _ runnerCall) (string, error) {
		select {
		case <-time.After(30 * time.Millisecond):
		case <-ctx.Done():
		}
		return "ok", nil
	}}
	capacity, err := NewSSHCapacity(1)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewRunService(store, hosts, noEnvironments{}, capacity, allowAllPrograms(t),
		WithCommandRunner(runner), WithRunTiming(20*time.Millisecond, 400*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	run, err := svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{Argv: []string{"uptime"}, Rule: domain.TargetRule{All: true}, Concurrency: 3})
	if err != nil {
		t.Fatal(err)
	}
	final := store.waitFinal(t, run.ID)
	if final.Status != domain.RunSucceeded || final.Counts.Succeeded != 3 {
		t.Fatalf("final run = %s %+v; waiting for a free SSH slot must not fail hosts", final.Status, final.Counts)
	}
	if runner.max() != 1 {
		t.Errorf("max concurrent commands = %d, want 1 (the SSH capacity)", runner.max())
	}
}

func TestGetRedactsArgumentsAndOutputForNonExecutors(t *testing.T) {
	store, hosts := newMemRunStore(), standardHosts()
	runner := &fakeRunner{behave: func(context.Context, runnerCall) (string, error) { return "secret output", nil }}
	svc := newRunSvc(t, store, hosts, runner, allowAllPrograms(t))
	run, err := svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{
		Argv: []string{"mysql", "-pTOP-SECRET"}, Rule: domain.TargetRule{Include: []string{"db-01"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store.waitFinal(t, run.ID)

	executor, err := svc.Get(context.Background(), opsPrincipal, run.ID)
	if err != nil || len(executor.Argv) != 2 || executor.Hosts[0].Output != "secret output" {
		t.Fatalf("an executor sees everything: %+v, %v", executor, err)
	}
	viewer, err := svc.Get(context.Background(), readerRunUser, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if viewer.Argv != nil || viewer.Hosts[0].Output != "" || viewer.Status != domain.RunSucceeded || viewer.Program != "mysql" || viewer.ArgCount != 1 {
		t.Errorf("a reader sees status and counts but never arguments or output: %+v", viewer)
	}
	// The stored run is unaffected by redacting a copy.
	if again, _ := svc.Get(context.Background(), opsPrincipal, run.ID); len(again.Argv) != 2 {
		t.Error("redaction must not modify the stored run")
	}

	runs, err := svc.List(context.Background(), readerRunUser, domain.RunFilter{})
	if err != nil || len(runs) != 1 || runs[0].Argv != nil || runs[0].Hosts != nil || runs[0].Counts.Succeeded != 1 {
		t.Errorf("listing = %+v, %v; it must carry counts but no arguments or host results", runs, err)
	}

	if _, err := svc.Get(context.Background(), foreignWorkspace, run.ID); !errors.Is(err, domain.ErrRunNotFound) {
		t.Errorf("another workspace's Get error = %v, want ErrRunNotFound", err)
	}
	if _, err := svc.Get(context.Background(), domain.Principal{UserID: 9, WorkspaceID: 1}, run.ID); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("a principal without a role error = %v, want ErrPermissionDenied", err)
	}
	if _, err := svc.List(context.Background(), readerRunUser, domain.RunFilter{Status: "exploded"}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("an unknown status filter error = %v, want ErrInvalidInput", err)
	}
}

func TestCancelAuthorizationAndErrors(t *testing.T) {
	store, hosts := newMemRunStore(), standardHosts()
	svc := newRunSvc(t, store, hosts, &fakeRunner{}, allowAllPrograms(t))
	ctx := context.Background()
	if _, err := svc.Cancel(ctx, readerRunUser, 1); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("reader cancel error = %v, want ErrPermissionDenied", err)
	}
	if _, err := svc.Cancel(ctx, opsPrincipal, 999); !errors.Is(err, domain.ErrRunNotFound) {
		t.Errorf("unknown run error = %v, want ErrRunNotFound", err)
	}
	run, err := svc.Submit(ctx, opsPrincipal, domain.RunRequest{Argv: []string{"uptime"}, Rule: domain.TargetRule{All: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cancel(ctx, foreignWorkspace, run.ID); !errors.Is(err, domain.ErrRunNotFound) {
		t.Errorf("another workspace's cancel error = %v, want ErrRunNotFound", err)
	}
	store.waitFinal(t, run.ID)
}

func TestRecoverInterruptsAbandonedRuns(t *testing.T) {
	store, hosts := newMemRunStore(), standardHosts()
	svc := newRunSvc(t, store, hosts, &fakeRunner{}, allowAllPrograms(t))

	// A run that no executor is working on, as after a crash.
	orphan := &domain.Run{
		WorkspaceID: 1, Program: "uptime", ArgCount: 0, Argv: []string{"uptime"},
		Hosts: []domain.RunHost{{HostID: "1", Name: "web-01", Address: "10.0.0.1", Port: 22, Status: domain.RunHostPending}},
	}
	if err := store.CreateRun(context.Background(), orphan); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond) // longer than the stale timeout
	if _, err := svc.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := store.snapshot(orphan.ID)
	if got.Status != domain.RunInterrupted || got.Counts.Interrupted != 1 {
		t.Fatalf("abandoned run = %s %+v, want interrupted", got.Status, got.Counts)
	}
}

func TestNewRunServiceValidatesItsInputs(t *testing.T) {
	capacity, _ := NewSSHCapacity(1)
	policy, _ := domain.NewExecutionPolicy(false, nil)
	if _, err := NewRunService(nil, standardHosts(), noEnvironments{}, capacity, policy); err == nil {
		t.Error("a nil run store must be rejected")
	}
	if _, err := NewRunService(newMemRunStore(), standardHosts(), noEnvironments{}, nil, policy); err == nil {
		t.Error("a nil SSH limiter must be rejected")
	}
	if _, err := NewRunService(newMemRunStore(), standardHosts(), noEnvironments{}, capacity, policy, WithRunTiming(time.Second, time.Second)); err == nil {
		t.Error("a stale timeout that is not longer than the heartbeat interval must be rejected")
	}
	if _, err := NewRunService(newMemRunStore(), standardHosts(), noEnvironments{}, capacity, policy, WithMaxActiveRuns(0)); err == nil {
		t.Error("zero active runs must be rejected")
	}
}
