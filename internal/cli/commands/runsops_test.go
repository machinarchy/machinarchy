package commands_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

// retryFixture is run r-4c2d9e10 of spec 9: 14 hosts, web-[01-08] succeeded,
// web-[09-11] failed, web-[12,13] lost, web-14 cancelled.
func retryFixture(env int) (*runFake, string) {
	fake := newRunFake(fleet("web", 14, env)...)
	id := runID("4c2d9e10", 1)
	var targets []api.OperationTarget
	for i := 1; i <= 14; i++ {
		name := fmt.Sprintf("web-%02d", i)
		switch {
		case i <= 8:
			targets = append(targets, target(name, "Succeeded", exit(0), "active\n", ""))
		case i <= 11:
			targets = append(targets, target(name, "Failed", exit(3), "inactive\n", ""))
		case i <= 13:
			targets = append(targets, target(name, "Lost", nil, "", "outcome_unknown"))
		default:
			targets = append(targets, target(name, "Cancelled", nil, "", ""))
		}
	}
	fake.put(operation(id, "PartiallySucceeded", targets...))

	return fake, id
}

func includeNames(rule api.TargetRule) string {
	names := make([]string, len(rule.Include))
	for i, id := range rule.Include {
		names[i] = strings.TrimPrefix(id, "h-")
	}

	return strings.Join(names, ",")
}

// O-09, O-10: retry-failed runs the failed hosts again as a new run, lists the
// unknown and cancelled hosts as skipped, and leaves the old run alone.
func TestRunRetryFailed(t *testing.T) {
	fake, id := retryFixture(1)
	before, _ := fake.GetOperation(context.Background(), id)

	got := runCLI(t, fake, runCfg{}, "run", "r-4c2d9e10", "--retry-failed", "--yes")
	if got.code != 0 || fake.submitCount() != 1 {
		t.Fatalf("retry = %+v", got)
	}
	sent := fake.submits[0]
	if includeNames(sent.Targets) != "web-09,web-10,web-11" || strings.Join(sent.Run.Argv, " ") != "systemctl is-active nginx" || sent.ExpectTargetsHash == "" {
		t.Fatalf("retry submitted %+v", sent)
	}
	for _, want := range []string{
		"Run again on 3 hosts: web-[09-11]", "(same as r-4c2d9e10)",
		"Skipped   2 hosts with unknown outcome: web-[12-13]", "the command may already have run there", "add --include-unknown",
		"1 host cancelled: web-14", "add --include-cancelled",
		"Run r-9a8b7c6d", // a new ID
	} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, got.stderr)
		}
	}
	if strings.Contains(got.stdout+got.stderr, "fixed") {
		t.Errorf("a retry never says the old run is fixed:\n%s%s", got.stdout, got.stderr)
	}
	if after, _ := fake.GetOperation(context.Background(), id); after.Status.State != before.Status.State || len(after.Status.Targets) != 14 {
		t.Errorf("the old run changed: %+v", after.Status)
	}

	// O-10: --include-unknown adds the lost hosts and warns.
	fake, _ = retryFixture(1)
	got = runCLI(t, fake, runCfg{}, "run", "r-4c2d9e10", "--retry-failed", "--include-unknown", "--yes")
	if got.code != 0 || includeNames(fake.submits[0].Targets) != "web-09,web-10,web-11,web-12,web-13" ||
		!strings.Contains(got.stderr, "may already have run the command; it will run there again") || strings.Contains(got.stderr, "add --include-unknown") {
		t.Fatalf("include-unknown = %+v, %+v", got, fake.submits)
	}
	fake, _ = retryFixture(1)
	got = runCLI(t, fake, runCfg{}, "run", "r-4c2d9e10", "--retry-failed", "--include-unknown", "--include-cancelled", "--yes")
	if includeNames(fake.submits[0].Targets) != "web-09,web-10,web-11,web-12,web-13,web-14" || !strings.Contains(got.stderr, "before the cancel; it will run there again") {
		t.Fatalf("include-cancelled = %+v, %+v", got, fake.submits)
	}

	// Without --yes it asks, like any run.
	fake, _ = retryFixture(1)
	if got := runCLI(t, fake, runCfg{terminal: true, stdin: "n\n"}, "run", "r-4c2d9e10", "--retry-failed"); got.code != clierr.ExitFailure || fake.submitCount() != 0 ||
		!strings.Contains(got.stderr, "Run it? [y/N]") {
		t.Fatalf("declined retry = %+v", got)
	}
}

// Nothing failed with a known outcome: it says so and what would add hosts.
func TestRunRetryFailedHasNothingToRetry(t *testing.T) {
	fake := newRunFake(fleet("web", 3, 1)...)
	id := runID("5d3e0f21", 1)
	fake.put(operation(id, "PartiallySucceeded",
		target("web-01", "Succeeded", exit(0), "ok\n", ""), target("web-02", "Lost", nil, "", "outcome_unknown"), target("web-03", "Succeeded", exit(0), "ok\n", "")))
	got := runCLI(t, fake, runCfg{}, "run", "r-5d3e0f21", "--retry-failed", "--yes")
	if got.code != 0 || fake.submitCount() != 0 || !strings.Contains(got.stdout, "Nothing to run again") || !strings.Contains(got.stdout, "--include-unknown") {
		t.Fatalf("nothing to retry = %+v", got)
	}

	// A run that has not finished cannot be retried; a reader cannot see the command.
	live := newRunFake(fleet("web", 3, 1)...)
	live.put(operation(runID("6e4f1a32", 1), "Running", running(live.hosts)[0].Status.Targets...))
	if got := runCLI(t, live, runCfg{}, "run", "r-6e4f1a32", "--retry-failed", "--yes"); got.code != clierr.ExitConflict || live.submitCount() != 0 {
		t.Fatalf("retry of a running run = %+v", got)
	}
	hidden := newRunFake(fleet("web", 3, 1)...)
	op := operation(runID("7f5a2b43", 1), "Failed", target("web-01", "Failed", exit(1), "", ""))
	op.Spec = api.OperationSpec{}
	hidden.put(op)
	if got := runCLI(t, hidden, runCfg{}, "run", "r-7f5a2b43", "--retry-failed", "--yes"); got.code != clierr.ExitAuth || hidden.submitCount() != 0 {
		t.Fatalf("retry as a reader = %+v", got)
	}
}

// O-11: retrying in a protected env needs --confirm-env without a terminal.
func TestRunRetryInAProtectedEnv(t *testing.T) {
	cfg := runCfg{protected: []string{"production"}}
	fake, _ := retryFixture(1)
	got := runCLI(t, fake, cfg, "run", "r-4c2d9e10", "--retry-failed", "--yes")
	if got.code != clierr.ExitUsage || fake.submitCount() != 0 || !strings.Contains(got.stderr, "--confirm-env production") {
		t.Fatalf("protected retry = %+v", got)
	}
	if got := runCLI(t, fake, cfg, "run", "r-4c2d9e10", "--retry-failed", "--yes", "--confirm-env", "production"); got.code != 0 || fake.submitCount() != 1 {
		t.Fatalf("confirmed retry = %+v", got)
	}
}

// A host removed since the run is left out and said so.
func TestRunRetrySkipsRemovedHosts(t *testing.T) {
	fake, _ := retryFixture(1)
	fake.hosts = append(fake.hosts[:9:9], fake.hosts[10:]...) // web-10 is gone
	got := runCLI(t, fake, runCfg{}, "run", "r-4c2d9e10", "--retry-failed", "--yes")
	if got.code != 0 || includeNames(fake.submits[len(fake.submits)-1].Targets) != "web-09,web-11" || !strings.Contains(got.stderr, "no longer managed") {
		t.Fatalf("removed host = %+v, %+v", got, fake.submits)
	}
}

// cancelling is a run that goes Running -> CancelRequested -> Cancelled.
func cancelling(current api.Operation) []api.Operation {
	var requested, final []api.OperationTarget
	for i, t := range current.Status.Targets {
		switch i {
		case 0:
			requested = append(requested, target(t.Spec.DisplayName, "Succeeded", exit(0), "done\n", ""))
			final = append(final, target(t.Spec.DisplayName, "Succeeded", exit(0), "done\n", ""))
		case 1:
			requested = append(requested, api.OperationTarget{Spec: t.Spec, State: "Running"})
			final = append(final, target(t.Spec.DisplayName, "Lost", nil, "", "unreachable_during_cancel"))
		default:
			requested = append(requested, api.OperationTarget{Spec: t.Spec, State: "Running"})
			final = append(final, target(t.Spec.DisplayName, "Cancelled", nil, "", ""))
		}
	}

	return []api.Operation{operation(current.ID, "CancelRequested", requested...), operation(current.ID, "CancelRequested", requested...), operation(current.ID, "Cancelled", final...)}
}

// O-12: cancel asks, follows to the final state, never says "stopped" before
// the controller does, and flags hosts that were unreachable while cancelling.
func TestRunCancel(t *testing.T) {
	fake := newRunFake(fleet("web", 4, 1)...)
	id := runID("8a6b3c54", 1)
	live := running(fake.hosts)
	live[0].ID = id
	fake.scripts[id] = live
	fake.put(live[0])
	fake.cancelScript = cancelling

	got := runCLI(t, fake, runCfg{}, "run", "r-8a6b3c54", "--cancel", "--yes")
	if got.code != clierr.ExitPartial || len(fake.cancels) != 1 {
		t.Fatalf("cancel = %+v, cancels %v", got, fake.cancels)
	}
	for _, want := range []string{
		"Cancel run r-8a6b3c54 (4 hosts still running)",
		"Cancellation requested. The hosts have not confirmed yet.",
		"Run cancelled.",
		"1 host could not be reached while cancelling; the command may still be running there.",
	} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, got.stderr)
		}
	}
	if strings.Contains(strings.ToLower(got.stderr+got.stdout), "stopped") {
		t.Errorf("a cancel must not claim anything stopped:\n%s%s", got.stderr, got.stdout)
	}
	if !strings.Contains(got.stdout, "cancelled") || !strings.Contains(got.stdout, "web-[03-04]") {
		t.Errorf("the final result lacks the hosts:\n%s", got.stdout)
	}
	if strings.Index(got.stderr, "Cancellation requested") > strings.Index(got.stderr, "Run cancelled.") {
		t.Errorf("the request must come before the end:\n%s", got.stderr)
	}

	// Nobody succeeded: exit 9.
	none := newRunFake(fleet("web", 2, 1)...)
	none.scripts[id] = running(none.hosts)
	none.scripts[id][0].ID = id
	none.put(none.scripts[id][0])
	none.cancelScript = func(current api.Operation) []api.Operation {
		var final []api.OperationTarget
		for _, t := range current.Status.Targets {
			final = append(final, target(t.Spec.DisplayName, "Cancelled", nil, "", ""))
		}

		return []api.Operation{operation(current.ID, "Cancelled", final...)}
	}
	if got := runCLI(t, none, runCfg{}, "run", "r-8a6b3c54", "--cancel", "--yes"); got.code != clierr.ExitAllFailed {
		t.Fatalf("cancel with no success = %+v", got)
	}
}

// O-13: --no-wait returns right after the request.
func TestRunCancelNoWait(t *testing.T) {
	fake := newRunFake(fleet("web", 4, 1)...)
	id := runID("8a6b3c54", 1)
	live := running(fake.hosts)
	live[0].ID = id
	fake.scripts[id] = live
	fake.put(live[0])
	fake.cancelScript = cancelling

	got := runCLI(t, fake, runCfg{}, "run", "r-8a6b3c54", "--cancel", "--no-wait", "--yes")
	if got.code != 0 || len(fake.cancels) != 1 || !strings.Contains(got.stdout, "CancelRequested") || fake.pollCount(id) != 0 { // the fake resets the count when the cancel lands: no poll followed
		t.Fatalf("--no-wait = %+v (polls %d)", got, fake.pollCount(id))
	}

	// Declined: nothing is sent. Finished runs cannot be cancelled.
	fake2 := newRunFake(fleet("web", 4, 1)...)
	fake2.scripts[id] = live
	fake2.put(live[0])
	if got := runCLI(t, fake2, runCfg{terminal: true, stdin: "n\n"}, "run", "r-8a6b3c54", "--cancel"); got.code != clierr.ExitFailure || len(fake2.cancels) != 0 {
		t.Fatalf("declined cancel = %+v", got)
	}
	done, doneID := retryFixture(1)
	if got := runCLI(t, done, runCfg{}, "run", "r-4c2d9e10", "--cancel", "--yes"); got.code != clierr.ExitConflict || len(done.cancels) != 0 {
		t.Fatalf("cancel of a finished run (%s) = %+v", doneID, got)
	}
}

// Cancelling a run in a protected env asks for the env's name.
func TestRunCancelInAProtectedEnv(t *testing.T) {
	cfg := runCfg{protected: []string{"production"}}
	fake := newRunFake(fleet("web", 4, 1)...)
	id := runID("8a6b3c54", 1)
	live := running(fake.hosts)
	live[0].ID = id
	fake.scripts[id] = live
	fake.put(live[0])
	fake.cancelScript = cancelling

	if got := runCLI(t, fake, cfg, "run", "r-8a6b3c54", "--cancel", "--yes"); got.code != clierr.ExitUsage || len(fake.cancels) != 0 {
		t.Fatalf("protected cancel without the env = %+v", got)
	}
	cfg.terminal, cfg.stdin = true, "production\n"
	if got := runCLI(t, fake, cfg, "run", "r-8a6b3c54", "--cancel"); got.code != clierr.ExitPartial || len(fake.cancels) != 1 {
		t.Fatalf("protected cancel with the env typed = %+v", got)
	}
}

// Ctrl+C while a cancel is being followed only stops watching.
func TestRunCancelCtrlC(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := newRunFake(fleet("web", 4, 1)...)
	id := runID("8a6b3c54", 1)
	live := running(fake.hosts)
	live[0].ID = id
	fake.put(live[0])
	fake.scripts[id] = live
	fake.cancelScript = func(current api.Operation) []api.Operation {
		return []api.Operation{operation(current.ID, "CancelRequested", current.Status.Targets...)}
	}
	fake.onPoll = func(n int) {
		if n == 3 {
			cancel()
		}
	}
	got := runCLI(t, fake, runCfg{ctx: ctx}, "run", "r-8a6b3c54", "--cancel", "--yes")
	if got.code != clierr.ExitInterrupted || len(fake.cancels) != 1 || !strings.Contains(got.stderr, "Stopped watching") {
		t.Fatalf("ctrl-c during cancel = %+v", got)
	}
}

// O-31, N-08: listing runs; a reader sees the result but not the command.
func TestRunList(t *testing.T) {
	fake := newRunFake(fleet("web", 3, 1)...)
	fake.put(operation(runID("4b2d9e10", 1), "Succeeded", target("web-01", "Succeeded", exit(0), "", ""), target("web-02", "Succeeded", exit(0), "", "")))
	fake.put(operation(runID("8d1a0b7c", 2), "PartiallySucceeded", target("web-01", "Succeeded", exit(0), "", ""), target("web-02", "Failed", exit(1), "", "")))

	got := runCLI(t, fake, runCfg{}, "run")
	if got.code != 0 {
		t.Fatalf("list = %+v", got)
	}
	lines := dataLines(got.stdout)
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "RUN") || !strings.Contains(lines[0], "WHAT") || !strings.Contains(lines[0], "RESULT") {
		t.Fatalf("table = %q", got.stdout)
	}
	if !strings.HasPrefix(lines[1], "r-8d1a0b7c") || !strings.Contains(lines[1], "systemctl is-active nginx") || !strings.Contains(lines[1], "1 ok, 1 failed") ||
		!strings.Contains(lines[1], "2 min ago") {
		t.Errorf("row = %q", lines[1])
	}
	if !strings.Contains(lines[2], "2 ok") {
		t.Errorf("row = %q", lines[2])
	}

	if got := runCLI(t, fake, runCfg{}, "run", "-q"); strings.TrimSpace(got.stdout) != runID("8d1a0b7c", 2)+"\n"+runID("4b2d9e10", 1) {
		t.Errorf("-q = %q", got.stdout)
	}
	var items []map[string]any
	if got := runCLI(t, fake, runCfg{}, "run", "-o", "json"); json.Unmarshal([]byte(got.stdout), &items) != nil || len(items) != 2 || items[0]["id"] != runID("8d1a0b7c", 2) {
		t.Errorf("json = %q", got.stdout)
	}
	if got := runCLI(t, fake, runCfg{}, "run", "--limit", "1"); len(dataLines(got.stdout)) != 2 {
		t.Errorf("--limit 1 = %q", got.stdout)
	}
	if got := runCLI(t, fake, runCfg{}, "run", "--limit", "0"); got.code != clierr.ExitUsage {
		t.Errorf("--limit 0 = %+v", got)
	}
	if got := runCLI(t, fake, runCfg{}, "run", "--host", "nobody"); got.code != clierr.ExitNotFound {
		t.Errorf("unknown --host = %+v", got)
	}

	// The reader cannot see the command.
	hidden := newRunFake(fleet("web", 1, 1)...)
	op := operation(runID("4b2d9e10", 1), "Succeeded", target("web-01", "Succeeded", exit(0), "", ""))
	op.Spec = api.OperationSpec{}
	hidden.put(op)
	if got := runCLI(t, hidden, runCfg{}, "run"); got.code != 0 || !strings.Contains(got.stdout, "(hidden)") || !strings.Contains(got.stdout, "1 ok") {
		t.Fatalf("reader list = %+v", got)
	}

	if got := runCLI(t, newRunFake(), runCfg{}, "run"); got.code != 0 || !strings.Contains(got.stdout, "No runs yet") {
		t.Fatalf("empty list = %+v", got)
	}
}

// Run IDs: any unique prefix of at least 6 characters, written r-PREFIX or not.
func TestRunIDResolution(t *testing.T) {
	fake := newRunFake(fleet("web", 1, 1)...)
	fake.put(operation(runID("4b2d9e10", 1), "Succeeded", target("web-01", "Succeeded", exit(0), "ok\n", "")))
	fake.put(operation(runID("4b2dffff", 2), "Succeeded", target("web-01", "Succeeded", exit(0), "ok\n", "")))
	fake.put(operation(runID("9c0a1b2d", 3), "Succeeded", target("web-01", "Succeeded", exit(0), "ok\n", "")))

	for _, arg := range []string{"r-9c0a1b", "9c0a1b2d", "R-9C0A1B2D", runID("9c0a1b2d", 3)} {
		if got := runCLI(t, fake, runCfg{}, "run", arg); got.code != 0 || !strings.Contains(got.stdout, "1 of 1 hosts succeeded") {
			t.Errorf("run %s = %+v", arg, got)
		}
	}
	for arg, want := range map[string]int{
		"r-4b2d": clierr.ExitUsage, "r-4b2d9": clierr.ExitUsage, // too short; "r-4b2d9e" below is unique
		"r-4b2d0000": clierr.ExitNotFound, "r-zzzzzz": clierr.ExitNotFound, "r-bad id": clierr.ExitUsage,
	} {
		if got := runCLI(t, fake, runCfg{}, "run", arg); got.code != want {
			t.Errorf("run %q exit = %d, want %d: %s", arg, got.code, want, got.stderr)
		}
	}
	if got := runCLI(t, fake, runCfg{}, "run", "r-4b2d9e"); got.code != 0 {
		t.Errorf("unique prefix = %+v", got)
	}
	if got := runCLI(t, fake, runCfg{}, "run", "r-4b2d"); got.code != clierr.ExitUsage {
		t.Errorf("short prefix = %+v", got)
	}
	// Two runs share 6 characters.
	fake.put(operation(runID("4b2d9e11", 4), "Succeeded", target("web-01", "Succeeded", exit(0), "ok\n", "")))
	if got := runCLI(t, fake, runCfg{}, "run", "r-4b2d9e"); got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "matches 2 runs") {
		t.Errorf("ambiguous prefix = %+v", got)
	}
}

// run ID: the grouped result of one run, with --groups, --no-groups and drift.
func TestRunShowResult(t *testing.T) {
	fake := newRunFake(fleet("web", 14, 1)...)
	id := runID("4b2d9e10", 1)
	var targets []api.OperationTarget
	for i := 1; i <= 14; i++ {
		out := "nginx version: nginx/1.24.0\n"
		switch i {
		case 10, 13:
			out = "nginx version: nginx/1.22.1\n"
		case 3:
			out = "nginx version: nginx/1.24.0 (Debian)\n"
		}
		targets = append(targets, target(fmt.Sprintf("web-%02d", i), "Succeeded", exit(0), out, ""))
	}
	fake.put(operation(id, "Succeeded", targets...))

	got := runCLI(t, fake, runCfg{}, "run", "r-4b2d9e10")
	if got.code != 0 || !strings.Contains(got.stdout, "14 of 14 hosts succeeded") || !strings.Contains(got.stdout, "different output") ||
		!strings.Contains(got.stdout, "3 hosts printed something different from the other 11") {
		t.Fatalf("drift = %+v", got)
	}
	// --fail-on-drift turns it into 8.
	if got := runCLI(t, fake, runCfg{}, "run", "r-4b2d9e10", "--fail-on-drift"); got.code != clierr.ExitPartial {
		t.Fatalf("--fail-on-drift = %+v", got)
	}
	// --no-groups: one block per host.
	if got := runCLI(t, fake, runCfg{}, "run", "r-4b2d9e10", "--no-groups"); got.code != 0 || strings.Count(got.stdout, "nginx version") != 14 ||
		!strings.Contains(got.stdout, "web-14") {
		t.Fatalf("--no-groups = %+v", got)
	}
	// --groups 1 hides the others and says where to see them.
	if got := runCLI(t, fake, runCfg{}, "run", "r-4b2d9e10", "--groups", "1"); !strings.Contains(got.stdout, "and 2 more groups, 3 hosts") ||
		!strings.Contains(got.stdout, "--groups all") {
		t.Fatalf("--groups 1 = %q", got.stdout)
	}
	// -o json is the aggregator's document, with every host.
	got = runCLI(t, fake, runCfg{}, "run", "r-4b2d9e10", "-o", "json")
	var doc struct {
		Total  int
		Exit   int
		Counts map[string]int
	}
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil || doc.Total != 14 || doc.Counts["succeeded"] != 14 || doc.Exit != 0 {
		t.Fatalf("json = %q (%v)", got.stdout, err)
	}
	// A run that is still going has no result code yet.
	live := newRunFake(fleet("web", 2, 1)...)
	op := running(live.hosts)[0]
	op.ID = runID("5c3e0f21", 1)
	live.put(op)
	if got := runCLI(t, live, runCfg{}, "run", "r-5c3e0f"); got.code != 0 || !strings.Contains(got.stderr, "--follow") {
		t.Fatalf("running run = %+v", got)
	}
}

// Two hosts with the same display name are two hosts, not one.
func TestRunShowDuplicateNames(t *testing.T) {
	fake := newRunFake()
	id := runID("4b2d9e10", 1)
	a, b := target("dup", "Succeeded", exit(0), "a\n", ""), target("dup", "Succeeded", exit(0), "a\n", "")
	b.Spec.HostID = "h-other"
	fake.put(operation(id, "Succeeded", a, b))
	if got := runCLI(t, fake, runCfg{}, "run", "r-4b2d9e", "-o", "json"); !strings.Contains(got.stdout, `"total": 2`) {
		t.Fatalf("duplicate names collapsed: %s", got.stdout)
	}
}

// Output of a finished detached host that the run does not carry is read from
// the output endpoint, so grouping still sees it.
func TestRunShowFetchesDetachedOutput(t *testing.T) {
	fake := newRunFake()
	id := runID("4b2d9e10", 1)
	one, two := target("web-01", "Succeeded", exit(0), "", ""), target("web-02", "Succeeded", exit(0), "", "")
	one.Progress, two.Progress = &api.TargetProgress{StdoutBytes: 12}, &api.TargetProgress{StdoutBytes: 12}
	fake.put(operation(id, "Succeeded", one, two))
	fake.outputByHost[id+"/h-web-01/stdout"] = "hello world\n"
	fake.outputByHost[id+"/h-web-02/stdout"] = "hello there\n"

	got := runCLI(t, fake, runCfg{}, "run", "r-4b2d9e", "--no-groups")
	if !strings.Contains(got.stdout, "hello world") || !strings.Contains(got.stdout, "hello there") {
		t.Fatalf("fetched output missing:\n%s", got.stdout)
	}
}

// S-13, S-14: nothing a host or the server sends reaches the terminal as a
// control sequence, in a grouped result, a per-host dump and a follow.
func TestRunOutputIsSanitised(t *testing.T) {
	fake := newRunFake()
	id := runID("4b2d9e10", 1)
	fake.put(operation(id, "Failed",
		target(hostileName, "Failed", exit(1), hostileOutput, ""), target("web-02", "Failed", exit(1), "plain\n", "bad\x1b[2Jclass")))

	got := runCLI(t, fake, runCfg{}, "run", "r-4b2d9e")
	noControlBytes(t, "grouped result", got.stdout+got.stderr)
	if got.code != clierr.ExitAllFailed || !strings.Contains(got.stdout, "web-01") || !strings.Contains(got.stdout, "start red") {
		t.Fatalf("grouped = %+v", got)
	}
	got = runCLI(t, fake, runCfg{}, "run", "r-4b2d9e", "--no-groups")
	noControlBytes(t, "per-host result", got.stdout+got.stderr)

	// The full output of a host, split across polls (six bytes each), with and without --follow.
	hostID := "h-" + hostileName
	fake.outputByHost[id+"/"+hostID+"/stdout"] = hostileOutput
	for _, args := range [][]string{{"run", "r-4b2d9e", "--host", "web-01", "--stream", "stdout"}, {"run", "r-4b2d9e", "--host", "web-01", "--follow", "--stream", "stdout"}} {
		got := runCLI(t, fake, runCfg{}, args...)
		noControlBytes(t, strings.Join(args, " "), got.stdout+got.stderr)
		if got.code != 0 || got.stdout != "start red  end\n" {
			t.Fatalf("%v = %+v", args, got)
		}
	}
	got = runCLI(t, fake, runCfg{}, "run", "r-4b2d9e", "--host", "web-01", "--raw", "--stream", "stdout")
	if got.code != 0 || got.stdout != hostileOutput {
		t.Fatalf("--raw to a pipe = %q", got.stdout)
	}
}

// run ID --host: both streams, one stream, an unknown host, and the flag rules.
func TestRunHostOutput(t *testing.T) {
	fake := newRunFake()
	id := runID("4b2d9e10", 1)
	fake.put(operation(id, "Failed", target("web-01", "Failed", exit(3), "", ""), target("web-02", "Succeeded", exit(0), "", "")))
	fake.outputByHost[id+"/h-web-01/stdout"] = "line one\nline two"
	fake.outputByHost[id+"/h-web-01/stderr"] = "boom\n"

	got := runCLI(t, fake, runCfg{}, "run", "r-4b2d9e", "--host", "web-01")
	if got.code != 0 || got.stdout != "line one\nline two\n" || !strings.Contains(got.stderr, "boom") || !strings.Contains(got.stderr, "web-01: Failed, exit 3") {
		t.Fatalf("both streams = %+v", got)
	}
	if got := runCLI(t, fake, runCfg{}, "run", "r-4b2d9e", "--host", "h-web-01", "--stream", "stderr"); got.stdout != "boom\n" {
		t.Fatalf("stderr only = %+v", got)
	}
	if got := runCLI(t, fake, runCfg{}, "run", "r-4b2d9e", "--host", "web-09"); got.code != clierr.ExitNotFound {
		t.Fatalf("unknown host = %+v", got)
	}
	for _, args := range [][]string{
		{"run", "r-4b2d9e", "--host", "web-01", "--stream", "stdin"},
		{"run", "r-4b2d9e", "--stream", "stdout"},               // --stream needs --host
		{"run", "r-4b2d9e", "--raw"},                            // --raw needs --host
		{"run", "r-4b2d9e", "--host", "web-01", "--cancel"},     // one thing at a time
		{"run", "r-4b2d9e", "--retry-failed", "--cancel"},       // ditto
		{"run", "--follow"},                                     // which run?
		{"run", "r-4b2d9e", "--include-unknown"},                // needs --retry-failed
		{"run", "r-4b2d9e", "--no-wait"},                        // needs --cancel
		{"run", "r-4b2d9e", "--on", "web-01"},                   // a run ID and a new run
		{"run", "r-4b2d9e", "extra"},                            // two IDs
		{"run", "--yes", "--", "uptime"},                        // a program without --on
		{"run", "--on", "web-01"},                               // no program
		{"run", "--except", "web-01", "--", "uptime"},           // --except without --on
		{"run", "--on", "web-01", "--follow", "--", "uptime"},   // --follow does not start a run
		{"run", "--on", "web-01", "--limit", "3", "--", "true"}, // --limit is for the list
	} {
		if got := runCLI(t, fake, runCfg{}, args...); got.code != clierr.ExitUsage {
			t.Errorf("%v = %+v, want a usage error", args, got)
		}
	}
}

// O-29, O-30: the capability form sends the name and the values as typed.
func TestRunCapability(t *testing.T) {
	fake := newRunFake(fleet("db", 2, 1)...)
	got := runCLI(t, fake, runCfg{}, "run", "--on", "db-01", "--yes", "--do", "service.status", "service=nginx")
	if got.code != 0 || len(fake.submits) != 1 {
		t.Fatalf("capability = %+v", got)
	}
	sent := fake.submits[0]
	if sent.Run != nil || sent.Capability == nil || sent.Capability.Name != "service.status" || string(sent.Capability.Input) != `{"service":"nginx"}` ||
		sent.Capability.TimeoutSeconds != 60 || sent.ExpectTargetsHash == "" {
		t.Fatalf("submitted %+v capability %+v", sent, sent.Capability)
	}
	if !strings.Contains(got.stderr, "Command   --do service.status") {
		t.Errorf("preview:\n%s", got.stderr)
	}

	fake = newRunFake(fleet("db", 2, 1)...)
	runCLI(t, fake, runCfg{}, "run", "--on", "db-01", "--yes", "--do", "service.control", "--input-json", `{"service":"nginx.service","action":"restart","force":true}`, "--timeout", "30s")
	if c := fake.submits[0].Capability; string(c.Input) != `{"service":"nginx.service","action":"restart","force":true}` || c.TimeoutSeconds != 30 {
		t.Fatalf("input-json = %+v", c)
	}
	fake = newRunFake(fleet("db", 2, 1)...)
	runCLI(t, fake, runCfg{}, "run", "--on", "db-01", "--yes", "--do", "runtime.info")
	if c := fake.submits[0].Capability; c.Input != nil {
		t.Fatalf("no inputs must send none: %s", c.Input)
	}

	for _, args := range [][]string{
		{"--do", "service.status", "--", "uptime"},                   // O-30
		{"--do", "service.status", "service"},                        // not KEY=VALUE
		{"--do", "service.status", "a=1", "a=2"},                     // twice
		{"--do", "service.status", "a=1", "--input-json", `{"b":1}`}, // both forms
		{"--do", "service.status", "--input-json", `[1]`},            // not an object
		{"--do", "service.status", "--input-json", `{bad`},           // not JSON
		{"--do", "bad name"},                                         // a space
		{"--do", "service.status", "--concurrency", "3"},             // programs only
		{"--input-json", `{"a":1}`},                                  // needs --do
		{"service=nginx"},                                            // inputs without --do
	} {
		fake := newRunFake(fleet("db", 2, 1)...)
		if got := runCLI(t, fake, runCfg{}, append([]string{"run", "--on", "db-01", "--yes"}, args...)...); got.code != clierr.ExitUsage || fake.submitCount() != 0 {
			t.Errorf("%v = %+v, want a usage error and no submit", args, got)
		}
	}
}

// O-28: a program on an agent-only host is refused by the controller; the CLI
// shows the reason with the capability form as the way forward.
func TestRunProgramOnAnAgentHost(t *testing.T) {
	fake := newRunFake(fleet("db", 1, 1)...)
	fake.submitErr = &api.Error{Status: 400, Message: "host db-01 is reached through an agent; it runs capabilities, not programs"}
	got := runCLI(t, fake, runCfg{}, "run", "--on", "db-01", "--yes", "--", "uptime")
	if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "--do runtime.info") || strings.Contains(got.stderr, "{") {
		t.Fatalf("agent host = %+v", got)
	}
}

// O-19: an answer that is not a run is named, not half-shown.
func TestRunMalformedAnswer(t *testing.T) {
	fake := newRunFake(fleet("web", 1, 1)...)
	id := runID("4b2d9e10", 1)
	fake.scripts[id] = []api.Operation{{}} // no ID, no state
	fake.put(operation(id, "Succeeded"))
	got := runCLI(t, fake, runCfg{}, "run", "r-4b2d9e")
	if got.code != clierr.ExitUnavailable || !strings.Contains(got.stderr, "did not answer like a Machinarchy server") || !strings.Contains(got.stderr, "/api/v1/managed/operations/") {
		t.Fatalf("malformed = %+v", got)
	}
}

// The old forms are gone: they are unknown commands or flags, with no alias.
func TestRunOldFormsAreGone(t *testing.T) {
	fake := newRunFake(fleet("web", 1, 1)...)
	for _, args := range [][]string{
		{"run", "-l", "tier=web", "--", "uptime"}, {"run", "--host", "web-01", "--wait", "--", "uptime"}, {"run", "--all", "--", "uptime"},
		{"run", "--capability", "runtime.info"}, {"run", "--exclude", "web-01"},
		{"get", "operations"}, {"cancel", "operation", "x"}, {"logs", "operation", "x"},
	} {
		if got := runCLI(t, fake, runCfg{}, args...); got.code != clierr.ExitUsage || fake.submitCount() != 0 {
			t.Errorf("%v = %+v, want a usage error (unknown command or flag)", args, got)
		}
	}
}

// S-17: -o json of a run with hostile output is valid JSON with no raw
// control byte. (S-16, --raw refused on a terminal, needs a pty: not run.)
func TestRunJSONOutputIsEscaped(t *testing.T) {
	fake := newRunFake()
	id := runID("4b2d9e10", 1)
	fake.put(operation(id, "Failed",
		target(hostileName, "Failed", exit(1), hostileOutput, ""), target("web-02", "Failed", exit(1), "plain\n", "bad\x1b[2Jclass")))

	got := runCLI(t, fake, runCfg{}, "run", "r-4b2d9e", "-o", "json")
	if !json.Valid([]byte(got.stdout)) {
		t.Fatalf("not valid JSON:\n%q", got.stdout)
	}
	if strings.ContainsAny(got.stdout, "\x1b\x07\x7f") {
		t.Fatalf("raw control byte in JSON: %q", got.stdout)
	}
}
