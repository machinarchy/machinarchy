package commands_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

// mixedScript is the run of spec 7.5: 14 web hosts, 9 succeed, web-09 exits 3,
// web-13 cannot connect, web-14 times out, web-05 and web-11 are lost.
func mixedScript(hosts []api.Host) []api.Operation {
	var done []api.OperationTarget
	for _, host := range hosts {
		name := host.Metadata.Name
		switch name {
		case "web-09":
			done = append(done, target(name, "Failed", exit(3), "inactive\n", ""))
		case "web-13":
			done = append(done, target(name, "Failed", nil, "", "unreachable"))
		case "web-14":
			done = append(done, target(name, "TimedOut", nil, "", ""))
		case "web-05", "web-11":
			done = append(done, target(name, "Lost", nil, "", "outcome_unknown"))
		default:
			done = append(done, target(name, "Succeeded", exit(0), "active\n", ""))
		}
	}

	return []api.Operation{operation("", "PartiallySucceeded", done...)}
}

// O-06, O-08, O-27: a run where some hosts fail is grouped, ends with exit 8,
// and says that a timed out or unknown host may still have run the command.
func TestRunPartialFailure(t *testing.T) {
	fake := newRunFake(fleet("web", 14, 1)...)
	fake.script = mixedScript

	got := runCLI(t, fake, runCfg{}, "run", "--on", "tier=web", "--yes", "--", "systemctl", "is-active", "nginx")
	if got.code != clierr.ExitPartial {
		t.Fatalf("exit = %d, want 8:\n%s\n%s", got.code, got.stdout, got.stderr)
	}

	for _, want := range []string{
		"9 of 14 hosts succeeded, 5 did not",
		"web-09", "exit 3", "inactive",
		"could not connect (unreachable)",
		"timed out after 60 s",
		"web-[05,11]", "outcome unknown",
		"may still have run",
		"machinarchy run r-9a8b7c6d --retry-failed",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, got.stdout)
		}
	}
	// The preview and the progress go to stderr, the result to stdout.
	for _, want := range []string{"[test · none]", "Run on 14 hosts", "Command   systemctl is-active nginx", "60 s per host, 5 at a time", "Run r-9a8b7c6d"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, got.stderr)
		}
	}
	if strings.Contains(got.stdout, "Run on 14 hosts") || strings.Contains(got.stdout, "progress ") {
		t.Errorf("preview or progress on stdout:\n%s", got.stdout)
	}

	// The run is bound to the previewed hosts.
	sent := fake.submits[0]
	if sent.ExpectTargetsHash == "" || sent.Targets.Selector != "tier=web" || strings.Join(sent.Run.Argv, " ") != "systemctl is-active nginx" ||
		sent.Run.Detach || sent.Run.Concurrency != 5 || sent.Run.TimeoutSeconds != 60 {
		t.Fatalf("submitted %+v run %+v", sent, sent.Run)
	}
}

// O-07, O-06: every host succeeds (0) or fails (9); a clean run is one line per
// group, and -o json is the aggregator's stable document.
func TestRunAllSucceedAndAllFail(t *testing.T) {
	fake := newRunFake(fleet("web", 14, 1)...)
	got := runCLI(t, fake, runCfg{}, "run", "--on", "tier=web", "--yes", "--", "uptime")
	if got.code != 0 || !strings.Contains(got.stdout, "14 of 14 hosts succeeded") || !strings.Contains(got.stdout, "web-[01-14]") ||
		!strings.Contains(got.stdout, "machinarchy run r-9a8b7c6d --host web-01") {
		t.Fatalf("all ok = %+v", got)
	}

	failing := newRunFake(fleet("web", 4, 1)...)
	failing.script = func(hosts []api.Host) []api.Operation {
		var done []api.OperationTarget
		for _, host := range hosts {
			done = append(done, target(host.Metadata.Name, "Failed", exit(3), "inactive\n", ""))
		}

		return []api.Operation{operation("", "Failed", done...)}
	}
	got = runCLI(t, failing, runCfg{}, "run", "--on", "tier=web", "--yes", "--", "uptime")
	if got.code != clierr.ExitAllFailed || !strings.Contains(got.stdout, "4 of 4 hosts failed") {
		t.Fatalf("all failed = %+v", got)
	}

	got = runCLI(t, fake, runCfg{}, "run", "--on", "tier=web", "--yes", "-o", "json", "--", "uptime")
	if got.code != 0 || !strings.Contains(got.stdout, `"succeeded": 14`) || !strings.Contains(got.stdout, `"web-14"`) {
		t.Fatalf("json = %+v", got)
	}
}

// O-23..O-26: the active env joins the selector, a conflicting env is refused,
// and env= is a tag.
func TestRunTargetsAndEnv(t *testing.T) {
	hosts := append(fleet("web", 3, 1), fleet("stg", 2, 2)...)

	fake := newRunFake(hosts...)
	got := runCLI(t, fake, runCfg{}, "run", "--env", "production", "--on", "tier=web", "--yes", "--", "uptime")
	if got.code != 0 || fake.submits[0].Targets.Selector != "environment=production,tier=web" {
		t.Fatalf("env selector = %+v, submitted %+v", got, fake.submits)
	}
	if !strings.Contains(got.stderr, "[test · production]") || !strings.Contains(got.stderr, `3 of 3 in "production"`) {
		t.Errorf("preview lacks the env:\n%s", got.stderr)
	}

	fake = newRunFake(hosts...)
	got = runCLI(t, fake, runCfg{}, "run", "--env", "staging", "--on", "environment=production", "--yes", "--", "uptime")
	if got.code != clierr.ExitUsage || fake.submitCount() != 0 || !strings.Contains(got.stderr, "active env") {
		t.Fatalf("conflicting env = %+v", got)
	}

	tagged := fleet("web", 2, 1)
	tagged[0].Metadata.Labels["env"] = "production"
	fake = newRunFake(tagged...)
	if got := runCLI(t, fake, runCfg{}, "run", "--on", "env=production", "--yes", "--", "uptime"); fake.submitCount() != 1 ||
		fake.submits[0].Targets.Selector != "env=production" {
		t.Fatalf("env= must stay a tag: %+v %+v", got, fake.submits)
	}

	// Names, a pattern, --except, all; and a name outside the env is refused.
	for _, test := range []struct {
		args []string
		want api.TargetRule
	}{
		{[]string{"--on", "web-01,web-02"}, api.TargetRule{Include: []string{"web-01", "web-02"}}},
		{[]string{"--on", "web-*", "--except", "web-02"}, api.TargetRule{Include: []string{"web-01", "web-02", "web-03"}, Exclude: []string{"web-02"}}},
		{[]string{"--on", "all"}, api.TargetRule{All: true}},
	} {
		fake = newRunFake(hosts...)
		got := runCLI(t, fake, runCfg{}, append(append([]string{"run"}, test.args...), "--yes", "--env", "any", "--", "uptime")...)
		if got.code != 0 || fmt.Sprint(fake.submits[0].Targets) != fmt.Sprint(test.want) {
			t.Errorf("%v: rule %+v, want %+v (%+v)", test.args, fake.submits, test.want, got)
		}
	}
	fake = newRunFake(hosts...)
	if got := runCLI(t, fake, runCfg{}, "run", "--env", "production", "--on", "stg-01", "--yes", "--", "uptime"); got.code != clierr.ExitNotFound ||
		fake.submitCount() != 0 || !strings.Contains(got.stderr, `"stg-01" is not in env "production"`) {
		t.Fatalf("name outside the env = %+v", got)
	}
	if got := runCLI(t, fake, runCfg{}, "run", "--on", "web-01,tier=web", "--yes", "--", "uptime"); got.code != clierr.ExitUsage {
		t.Fatalf("mixed targets = %+v", got)
	}
}

// O-22: more than 200 hosts is refused before anything is asked or sent.
func TestRunRefusesMoreThan200Hosts(t *testing.T) {
	fake := newRunFake(fleet("app", 201, 1)...)
	got := runCLI(t, fake, runCfg{terminal: true, stdin: "y\n"}, "run", "--on", "all", "--", "uptime")
	if got.code != clierr.ExitUsage || fake.submitCount() != 0 || !strings.Contains(got.stderr, "201 hosts") || !strings.Contains(got.stderr, "at most 200") ||
		strings.Contains(got.stderr, "[y/N]") {
		t.Fatalf("201 hosts = %+v", got)
	}
}

// O-21: the hosts changed between preview and submit: nothing ran.
func TestRunTargetsChanged(t *testing.T) {
	fake := newRunFake(fleet("web", 3, 1)...)
	fake.changed = true
	got := runCLI(t, fake, runCfg{}, "run", "--on", "tier=web", "--yes", "--", "uptime")
	if got.code != clierr.ExitConflict || !strings.Contains(got.stderr, "set of hosts changed while you were deciding. Nothing ran") {
		t.Fatalf("targets_changed = %+v", got)
	}
}

// O-16: Ctrl+C while the run is being sent: it may or may not exist, the CLI
// says so, points at `machinarchy run` and never sends it again.
func TestRunInterruptedDuringSubmit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := newRunFake(fleet("web", 3, 1)...)
	fake.onSubmit = cancel

	got := runCLI(t, fake, runCfg{ctx: ctx}, "run", "--on", "tier=web", "--yes", "--", "uptime")
	if got.code != clierr.ExitInterrupted || fake.submitCount() != 1 ||
		!strings.Contains(got.stderr, "Interrupted while sending the run. It may or may not have been created.") ||
		!strings.Contains(got.stderr, "Next:  machinarchy run") {
		t.Fatalf("interrupt during submit = %+v (submits %d)", got, fake.submitCount())
	}
	if strings.Contains(got.stderr, "Error:") || strings.Contains(got.stderr, "Interrupted.\n") {
		t.Errorf("the notice must stand alone:\n%s", got.stderr)
	}
}

// O-17: the connection drops while the run is being sent. Exactly one POST.
func TestRunConnectionLostDuringSubmit(t *testing.T) {
	fake := newRunFake(fleet("web", 3, 1)...)
	fake.submitErr = errors.New("unexpected EOF")

	got := runCLI(t, fake, runCfg{}, "run", "--on", "tier=web", "--yes", "--", "uptime")
	if got.code != clierr.ExitUnavailable || fake.submitCount() != 1 ||
		!strings.Contains(got.stderr, "It may or may not have been created") || !strings.Contains(got.stderr, "Next:  machinarchy run") {
		t.Fatalf("lost submit = %+v (submits %d)", got, fake.submitCount())
	}
	if strings.Contains(got.stderr, "EOF") {
		t.Errorf("the raw transport error leaked:\n%s", got.stderr)
	}
}

// running is a run that never finishes.
func running(hosts []api.Host) []api.Operation {
	var targets []api.OperationTarget
	for _, host := range hosts {
		targets = append(targets, api.OperationTarget{Spec: api.OperationTargetSpec{HostID: host.Metadata.ID, DisplayName: host.Metadata.Name}, State: "Running"})
	}

	return []api.Operation{operation("", "Running", targets...)}
}

// O-14: Ctrl+C while following: stop watching, say how to resume, exit 130,
// and never send a cancel.
func TestRunCtrlCWhileFollowing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := newRunFake(fleet("web", 4, 1)...)
	fake.script = running
	fake.onPoll = func(n int) {
		if n == 3 {
			cancel()
		}
	}

	got := runCLI(t, fake, runCfg{ctx: ctx}, "run", "--on", "tier=web", "--yes", "--", "sleep", "60")
	if got.code != clierr.ExitInterrupted || len(fake.cancels) != 0 {
		t.Fatalf("ctrl-c = %+v, cancels %v", got, fake.cancels)
	}
	for _, want := range []string{
		"Stopped watching. The run keeps going on the hosts.",
		"machinarchy run r-9a8b7c6d --follow", "machinarchy run r-9a8b7c6d --cancel",
	} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, got.stderr)
		}
	}
	if strings.Contains(got.stderr, "Error:") || strings.Contains(got.stdout, "hosts succeeded") {
		t.Errorf("an interrupt is not an error and has no result:\n%+v", got)
	}

	// The same for `run ID --follow` on an existing run.
	ctx, cancel = context.WithCancel(context.Background())
	again := newRunFake(fleet("web", 4, 1)...)
	id := runID("11112222", 1)
	op := operation(id, "Running", running(again.hosts)[0].Status.Targets...)
	again.scripts[id] = []api.Operation{op}
	again.put(op)
	again.onPoll = func(n int) {
		if n == 2 {
			cancel()
		}
	}
	got = runCLI(t, again, runCfg{ctx: ctx}, "run", "r-11112222", "--follow")
	if got.code != clierr.ExitInterrupted || !strings.Contains(got.stderr, "machinarchy run r-11112222 --follow") || len(again.cancels) != 0 {
		t.Fatalf("follow ctrl-c = %+v", got)
	}
}

// O-18: the connection drops while following. A poll that fails twice is
// retried silently; one that keeps failing ends with the resume line, exit 4.
func TestRunLostStreamWhileFollowing(t *testing.T) {
	fake := newRunFake(fleet("web", 3, 1)...)
	fake.pollErr[1] = errors.New("connection reset by peer")
	fake.pollErr[2] = errors.New("connection reset by peer")
	got := runCLI(t, fake, runCfg{}, "run", "--on", "tier=web", "--yes", "--", "uptime")
	if got.code != 0 || !strings.Contains(got.stdout, "3 of 3 hosts succeeded") || strings.Contains(got.stderr, "Lost contact") {
		t.Fatalf("a brief outage must be survived: %+v", got)
	}

	fake = newRunFake(fleet("web", 3, 1)...)
	fake.script = running
	for n := 1; n <= 20; n++ {
		fake.pollErr[n] = errors.New("connection reset by peer")
	}
	got = runCLI(t, fake, runCfg{}, "run", "--on", "tier=web", "--yes", "--", "uptime")
	if got.code != clierr.ExitUnavailable || fake.submitCount() != 1 ||
		!strings.Contains(got.stderr, "Lost contact with the server; the run keeps going.") ||
		!strings.Contains(got.stderr, "machinarchy run r-9a8b7c6d --follow") {
		t.Fatalf("lost stream = %+v", got)
	}
	if strings.Contains(got.stderr, "connection reset") || len(fake.cancels) != 0 {
		t.Errorf("raw error leaked or a cancel was sent:\n%s", got.stderr)
	}
}

// --wait-timeout stops watching, exits 5 and says the run keeps going.
func TestRunWaitTimeout(t *testing.T) {
	fake := newRunFake(fleet("web", 3, 1)...)
	fake.script = running
	got := runCLI(t, fake, runCfg{}, "run", "--on", "tier=web", "--yes", "--wait-timeout", "30ms", "--", "sleep", "60")
	if got.code != clierr.ExitWaitTimeout || !strings.Contains(got.stderr, "Still running") || !strings.Contains(got.stderr, "machinarchy run r-9a8b7c6d --follow") ||
		len(fake.cancels) != 0 {
		t.Fatalf("wait timeout = %+v", got)
	}
}

// A run too long to watch, or --detach, returns once it is accepted.
func TestRunDetachedAndLong(t *testing.T) {
	for name, args := range map[string][]string{
		"detach":       {"--detach"},
		"over 15 min":  {"--timeout", "20m"},
		"24 hours":     {"--timeout", "24h"},
		"in seconds":   {"--timeout", "7200"},
		"detach again": {"--detach", "--concurrency", "3"},
	} {
		fake := newRunFake(fleet("web", 3, 1)...)
		got := runCLI(t, fake, runCfg{}, append(append([]string{"run", "--on", "tier=web", "--yes"}, args...), "--", "backup.sh")...)
		if got.code != 0 || !strings.Contains(got.stdout, "machinarchy run r-9a8b7c6d --follow") || fake.pollCount(runID("9a8b7c6d", 1)) != 0 {
			t.Errorf("%s: %+v (polls %d)", name, got, fake.pollCount(runID("9a8b7c6d", 1)))

			continue
		}
		if run := fake.submits[0].Run; !run.Detach {
			t.Errorf("%s: submitted attached: %+v", name, run)
		}
	}

	fake := newRunFake(fleet("web", 3, 1)...)
	got := runCLI(t, fake, runCfg{}, "run", "--on", "tier=web", "--yes", "--timeout", "20m", "--", "backup.sh")
	if !strings.Contains(got.stdout, "This run is long, so it runs detached") || fake.submits[0].Run.Concurrency != 3 || fake.submits[0].Run.TimeoutSeconds != 1200 {
		t.Fatalf("long run = %+v %+v", got, fake.submits[0].Run)
	}

	for _, args := range [][]string{
		{"--timeout", "0s"}, {"--timeout", "25h"}, {"--timeout", "soon"}, {"--concurrency", "0"}, {"--concurrency", "21"},
		{"--detach", "--wait-timeout", "1m"}, {"--timeout", "20m", "--wait-timeout", "1m"}, {"--groups", "0"}, {"--groups", "2", "--no-groups"},
	} {
		fake := newRunFake(fleet("web", 3, 1)...)
		if got := runCLI(t, fake, runCfg{}, append(append([]string{"run", "--on", "tier=web", "--yes"}, args...), "--", "uptime")...); got.code != clierr.ExitUsage || fake.submitCount() != 0 {
			t.Errorf("%v = %+v, want a usage error and no submit", args, got)
		}
	}
}

// Quiet prints only the run ID; --dry-run sends nothing.
func TestRunQuietAndDryRun(t *testing.T) {
	fake := newRunFake(fleet("web", 3, 1)...)
	got := runCLI(t, fake, runCfg{}, "run", "--on", "tier=web", "--yes", "-q", "--", "uptime")
	if got.code != 0 || strings.TrimSpace(got.stdout) != runID("9a8b7c6d", 1) {
		t.Fatalf("quiet = %+v", got)
	}

	fake = newRunFake(fleet("web", 3, 1)...)
	got = runCLI(t, fake, runCfg{}, "run", "--on", "tier=web", "--dry-run", "--", "uptime")
	if got.code != 0 || fake.submitCount() != 0 || !strings.Contains(got.stderr, "Run on 3 hosts") || !strings.Contains(got.stderr, "Dry run") {
		t.Fatalf("dry run = %+v", got)
	}
}

// Without --yes and without a terminal nothing runs; with --no-input it says so.
func TestRunNeedsAnAnswer(t *testing.T) {
	fake := newRunFake(fleet("web", 3, 1)...)
	if got := runCLI(t, fake, runCfg{}, "run", "--on", "tier=web", "--", "uptime"); got.code != clierr.ExitUsage || fake.submitCount() != 0 ||
		!strings.Contains(got.stderr, "--yes") || !strings.Contains(got.stderr, "Run on 3 hosts") {
		t.Fatalf("no answer = %+v", got)
	}
	if got := runCLI(t, fake, runCfg{terminal: true, stdin: "n\n"}, "run", "--on", "tier=web", "--", "uptime"); got.code != clierr.ExitFailure || fake.submitCount() != 0 {
		t.Fatalf("declined = %+v", got)
	}
	if got := runCLI(t, fake, runCfg{terminal: true, stdin: "y\n"}, "run", "--on", "tier=web", "--", "uptime"); got.code != 0 || fake.submitCount() != 1 ||
		!strings.Contains(got.stderr, "Run it? [y/N]") {
		t.Fatalf("accepted = %+v", got)
	}
}

// S-22..S-26: protected envs and --on all.
func TestRunBlastRadiusConfirmation(t *testing.T) {
	cfg := runCfg{protected: []string{"production"}}
	newFake := func() *runFake { return newRunFake(append(fleet("web", 3, 1), fleet("stg", 2, 2)...)...) }

	// S-22: no terminal, protected, no --confirm-env: refused, nothing sent.
	fake := newFake()
	got := runCLI(t, fake, cfg, "run", "--on", "tier=web", "--yes", "--", "systemctl", "restart", "nginx")
	if got.code != clierr.ExitUsage || fake.submitCount() != 0 || !strings.Contains(got.stderr, "--confirm-env production") ||
		!strings.Contains(got.stderr, "Hosts     3 in \"production\" (protected)") {
		t.Fatalf("S-22 = %+v", got)
	}
	// S-23.
	fake = newFake()
	if got := runCLI(t, fake, cfg, "run", "--on", "tier=web", "--yes", "--confirm-env", "production", "--", "systemctl", "restart", "nginx"); got.code != 0 || fake.submitCount() != 1 {
		t.Fatalf("S-23 = %+v", got)
	}
	// S-24.
	fake = newFake()
	if got := runCLI(t, fake, cfg, "run", "--on", "tier=web", "--yes", "--confirm-env", "staging", "--", "uptime"); got.code != clierr.ExitUsage || fake.submitCount() != 0 {
		t.Fatalf("S-24 = %+v", got)
	}
	// --confirm-env alone is not --yes.
	fake = newFake()
	if got := runCLI(t, fake, cfg, "run", "--on", "tier=web", "--confirm-env", "production", "--", "uptime"); got.code != clierr.ExitUsage || fake.submitCount() != 0 {
		t.Fatalf("--confirm-env without --yes = %+v", got)
	}
	// S-25: interactive, the wrong name is typed: not confirmed, nothing sent.
	terminal := runCfg{protected: cfg.protected, terminal: true, stdin: "prod\n"}
	fake = newFake()
	if got := runCLI(t, fake, terminal, "run", "--on", "tier=web", "--", "uptime"); got.code != clierr.ExitFailure || fake.submitCount() != 0 ||
		!strings.Contains(got.stderr, "Type its name") {
		t.Fatalf("S-25 = %+v", got)
	}
	// The right name is the confirmation: no extra y/N, --yes is not needed.
	terminal.stdin = "production\n"
	fake = newFake()
	if got := runCLI(t, fake, terminal, "run", "--on", "tier=web", "--", "uptime"); got.code != 0 || fake.submitCount() != 1 || strings.Contains(got.stderr, "[y/N]") {
		t.Fatalf("typed env name = %+v", got)
	}
	// --yes does not answer the typed question.
	terminal.stdin = "\n"
	fake = newFake()
	if got := runCLI(t, fake, terminal, "run", "--on", "tier=web", "--yes", "--", "uptime"); got.code == 0 || fake.submitCount() != 0 {
		t.Fatalf("--yes answered the typed question: %+v", got)
	}
	// An unprotected env is not asked for its name.
	fake = newFake()
	if got := runCLI(t, fake, cfg, "run", "--on", "tier=stg", "--yes", "--", "uptime"); got.code != 0 || fake.submitCount() != 1 {
		t.Fatalf("unprotected env = %+v", got)
	}

	// S-26: --on all outside any env asks for the number of hosts.
	everything := runCfg{terminal: true, stdin: "4\n"}
	fake = newFake()
	if got := runCLI(t, fake, everything, "run", "--on", "all", "--", "uptime"); got.code != clierr.ExitFailure || fake.submitCount() != 0 ||
		!strings.Contains(got.stderr, "All 5 hosts on this server") || !strings.Contains(got.stderr, `Type "5"`) {
		t.Fatalf("S-26 wrong count = %+v", got)
	}
	everything.stdin = "5\n"
	fake = newFake()
	if got := runCLI(t, fake, everything, "run", "--on", "all", "--", "uptime"); got.code != 0 || fake.submitCount() != 1 {
		t.Fatalf("S-26 right count = %+v", got)
	}
}
