package commands

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
	"github.com/machinarchy/machinarchy/internal/cli/target"
)

// knownCapabilities are the capabilities agents implement today. The server
// decides what it accepts; there is no catalogue endpoint (C-08), so any name
// is passed through.
var knownCapabilities = []string{"runtime.info", "system.facts", "service.status", "service.control"}

// runOptions are the flags of `machinarchy run`.
type runOptions struct {
	on, except, do, inputJSON        string
	timeout, concurrency             string
	waitTimeoutText, groups          string
	host, stream, state              string
	limit                            int
	detach, noWait, noGroups         bool
	failOnDrift, follow, retryFailed bool
	cancel, raw                      bool
	includeUnknown, includeCancelled bool

	// Read from the flags by parse.
	timeoutSet       bool
	timeoutSeconds   int
	concurrencyValue int
	waitFor          time.Duration
}

// NewRunCommand is the one command for running, listing, following, retrying
// and cancelling runs (docs/cli/spec/02-commands.md section 3.10).
func NewRunCommand(f *cli.Factory) *cobra.Command {
	var o runOptions
	command := &cobra.Command{
		Use:   "run [ID] [--on TARGET [--except TARGET]] [-- PROGRAM [ARG...]]",
		Short: "Run a program or capability on hosts, and list, follow, retry or cancel runs",
		Long: "Run a program on hosts, then wait and show what happened, grouped: hosts that printed the same are one line.\n" +
			"The program is an argument list, never a shell string. You see the hosts first and confirm; the run is bound to\n" +
			"the hosts you saw, so if the set changed meanwhile, nothing runs.\n\n" +
			"  run --on TARGET [--except TARGET] -- PROGRAM [ARG...]    run it, wait, show the grouped result\n" +
			"  run --on TARGET --do CAPABILITY [KEY=VALUE...]           run an agent capability\n" +
			"  run                                                      the recent runs\n" +
			"  run ID                                                   the result of one run\n" +
			"  run ID --host NAME [--stream stdout|stderr] [--follow]   the full output of one host\n" +
			"  run ID --follow                                          watch a run that is going\n" +
			"  run ID --retry-failed                                    run it again on the hosts that failed\n" +
			"  run ID --cancel                                          ask for the run to be cancelled\n\n" +
			"TARGET is host names (web-01,web-02), a name pattern (web-*), tags (tier=web or tier=web,region in (eu,us)),\n" +
			"or all. Names and tags are not mixed. The active env limits the hosts; --env any lifts it. A tag called env is\n" +
			"just a tag: use --env to pick an env. A run targets at most 200 hosts.\n\n" +
			"A run is detached from your terminal with --detach, or when --timeout is above 15 minutes. Ctrl+C while\n" +
			"watching only stops the watching; the run keeps going. IDs are r- and the first characters, shown as r-xxxxxxxx.\n\n" +
			"Capabilities known today: " + strings.Join(knownCapabilities, ", ") + ". The server decides what it accepts; the\n" +
			"names and values are sent as you type them.\n\n" +
			"Exit codes: 0 every host succeeded, 8 some failed, 9 all failed, 5 --wait-timeout elapsed, 130 interrupted.",
		Example: "  machinarchy run --on tier=web -- systemctl is-active nginx\n" +
			"  machinarchy run --on 'web-*' --except web-02 -- uptime\n" +
			"  machinarchy run --on db-01 --do service.status service=postgresql.service\n" +
			"  machinarchy run --on all --detach -- /opt/backup/run.sh\n" +
			"  machinarchy run r-4k2p9x1a --host web-01\n" +
			"  machinarchy run r-4k2p9x1a --retry-failed",
		Args:        cobra.ArbitraryArgs,
		Annotations: map[string]string{cli.AnnotationDryRun: "true"},
		RunE: func(command *cobra.Command, args []string) error {
			return dispatchRun(command, f, &o, args)
		},
	}

	flags := command.Flags()
	flags.StringVar(&o.on, "on", "", "Where to run: host names, a pattern like web-*, tags like tier=web, or all")
	flags.StringVar(&o.except, "except", "", "Leave these hosts out (names or patterns)")
	flags.StringVar(&o.do, "do", "", "Run this agent capability instead of a program; inputs follow as KEY=VALUE")
	flags.StringVar(&o.inputJSON, "input-json", "", "With --do: the input as a JSON object, for values that are not text")
	flags.StringVar(&o.timeout, "timeout", "", "Time limit per host, like 90s, 15m or 2h (default 60s, at most 24h)")
	flags.StringVar(&o.concurrency, "concurrency", "", fmt.Sprintf("Hosts to work on at once, 1 to %d (default %d, or %d when detached)", maxConcurrency, defaultAttachedConcurrency, maxConcurrency))
	flags.BoolVar(&o.detach, "detach", false, "Return as soon as the run is accepted; it keeps going on the hosts")
	flags.StringVar(&o.waitTimeoutText, "wait-timeout", "", "Stop watching after this long and exit 5; the run keeps going")
	flags.BoolVar(&o.noGroups, "no-groups", false, "Show one block per host instead of grouping hosts that printed the same")
	flags.StringVar(&o.groups, "groups", "", "Show up to N groups, or all (default 10)")
	flags.BoolVar(&o.failOnDrift, "fail-on-drift", false, "Exit 8 when hosts that succeeded printed different output")
	flags.BoolVarP(&o.follow, "follow", "f", false, "Watch a run that is going (with --host: stream that host's output)")
	flags.BoolVar(&o.retryFailed, "retry-failed", false, "Start a new run of the same command on the hosts that failed")
	flags.BoolVar(&o.includeUnknown, "include-unknown", false, "With --retry-failed: also hosts whose outcome is unknown (the command may have run)")
	flags.BoolVar(&o.includeCancelled, "include-cancelled", false, "With --retry-failed: also hosts that were cancelled")
	flags.BoolVar(&o.cancel, "cancel", false, "Ask for the run to be cancelled, then watch it end")
	flags.BoolVar(&o.noWait, "no-wait", false, "With --cancel: return as soon as the request is sent")
	flags.StringVar(&o.host, "host", "", "With a run ID: show the full output of this host. Without: list runs that include it")
	flags.StringVar(&o.stream, "stream", "", "With --host: only stdout or only stderr (default both)")
	flags.BoolVar(&o.raw, "raw", false, "With --host: write the output as it is, unfiltered (not to a terminal)")
	flags.StringVar(&o.state, "state", "", "When listing: only runs in this state, for example Running")
	flags.IntVar(&o.limit, "limit", 20, "When listing: how many runs, newest first")

	registerRunCompletions(command, f)

	return command
}

// Modes of `run`, decided from the arguments and flags.
const (
	modeExecute = "execute"
	modeList    = "list"
	modeShow    = "show"
	modeHost    = "host"
	modeFollow  = "follow"
	modeRetry   = "retry"
	modeCancel  = "cancel"
)

// allowedFlags are the flags each mode accepts; any other flag the person set
// is refused rather than ignored.
var allowedFlags = map[string][]string{
	modeExecute: {"on", "except", "do", "input-json", "timeout", "concurrency", "detach", "wait-timeout", "no-groups", "groups", "fail-on-drift"},
	modeList:    {"state", "host", "limit"},
	modeShow:    {"no-groups", "groups", "fail-on-drift"},
	modeHost:    {"host", "stream", "raw", "follow"},
	modeFollow:  {"follow", "wait-timeout", "no-groups", "groups", "fail-on-drift"},
	modeRetry: {"retry-failed", "include-unknown", "include-cancelled", "timeout", "concurrency", "detach", "wait-timeout",
		"no-groups", "groups", "fail-on-drift"},
	modeCancel: {"cancel", "no-wait", "wait-timeout", "no-groups", "groups", "fail-on-drift"},
}

var modeNames = map[string]string{
	modeExecute: "a run", modeList: "listing runs", modeShow: "showing a run", modeHost: "showing a host's output",
	modeFollow: "following a run", modeRetry: "--retry-failed", modeCancel: "--cancel",
}

// dispatchRun decides what the person asked for and does it.
func dispatchRun(command *cobra.Command, f *cli.Factory, o *runOptions, args []string) error {
	dash := command.ArgsLenAtDash()
	pos, program := args, []string(nil)
	if dash >= 0 {
		pos, program = args[:dash], args[dash:]
	}

	mode, err := o.mode(command, pos, program, dash)
	if err != nil {
		return err
	}
	if err := o.checkFlags(command, mode); err != nil {
		return err
	}
	if err := o.parse(command, mode); err != nil {
		return err
	}

	if mode == modeExecute {
		return executeRun(command, f, o, pos, program)
	}
	if mode == modeList {
		return listRuns(command, f, *o)
	}

	client, err := f.NewAPIClient(command.Context())
	if err != nil {
		return err
	}
	id, err := resolveRunID(command.Context(), client, pos[0])
	if err != nil {
		return err
	}

	switch mode {
	case modeRetry:
		return retryFailed(command, f, client, id, *o)
	case modeCancel:
		return cancelRun(command, f, client, id, *o)
	case modeHost:
		return showHostOutput(command, f, client, id, *o)
	case modeFollow:
		return finish(command, f, client, id, o.followUp(f, nil))
	default:
		return showRun(command, f, client, id, *o)
	}
}

// mode works out what is being asked for.
func (o *runOptions) mode(command *cobra.Command, pos, program []string, dash int) (string, error) {
	flags := command.Flags()
	switch {
	case flags.Changed("on") || flags.Changed("except") || flags.Changed("do"):
		if !flags.Changed("on") {
			return "", clierr.Newf(clierr.ExitUsage, "for example: machinarchy run --on tier=web -- uptime", "say where to run it with --on")
		}

		return modeExecute, nil
	case len(pos) == 0 && len(program) == 0:
		if o.retryFailed || o.cancel || o.follow {
			return "", clierr.Newf(clierr.ExitUsage, "machinarchy run   lists the recent runs and their IDs", "which run? give its ID, for example: machinarchy run r-4k2p9x1a --follow")
		}

		return modeList, nil
	case len(program) > 0 && len(pos) == 0:
		return "", clierr.Newf(clierr.ExitUsage, "for example: machinarchy run --on tier=web -- uptime", "say where to run it with --on")
	case len(pos) > 1 || dash >= 0:
		return "", clierr.Newf(clierr.ExitUsage, "give one run ID", "too many arguments; put a program after -- and its hosts after --on")
	}

	switch {
	case o.retryFailed && o.cancel:
		return "", clierr.Usagef("--retry-failed and --cancel cannot be used together")
	case o.retryFailed:
		return modeRetry, nil
	case o.cancel:
		return modeCancel, nil
	case o.host != "":
		return modeHost, nil
	case o.follow:
		return modeFollow, nil
	}

	return modeShow, nil
}

// checkFlags refuses a flag that does not apply to what is being done.
func (o *runOptions) checkFlags(command *cobra.Command, mode string) error {
	var refused error
	command.LocalNonPersistentFlags().VisitAll(func(flag *pflag.Flag) {
		if flag.Changed && refused == nil && flag.Name != "help" && !slices.Contains(allowedFlags[mode], flag.Name) {
			refused = clierr.Usagef("--%s does not apply to %s", flag.Name, modeNames[mode])
		}
	})

	return refused
}

// parse reads the values that need more than a type.
func (o *runOptions) parse(command *cobra.Command, mode string) error {
	flags := command.Flags()
	var err error
	if flags.Changed("timeout") {
		if o.timeoutSeconds, err = parseSeconds("--timeout", o.timeout, 1, maxRunTimeout); err != nil {
			return err
		}
		o.timeoutSet = true
	}
	if flags.Changed("concurrency") {
		n := 0
		if _, scanErr := fmt.Sscanf(o.concurrency, "%d", &n); scanErr != nil || n < 1 || n > maxConcurrency || fmt.Sprint(n) != strings.TrimSpace(o.concurrency) {
			return clierr.Usagef("--concurrency must be 1 to %d", maxConcurrency)
		}
		o.concurrencyValue = n
	}
	if flags.Changed("wait-timeout") {
		seconds, err := parseSeconds("--wait-timeout", o.waitTimeoutText, 1, 30*24*60*60)
		if err != nil {
			return err
		}
		o.waitFor = time.Duration(seconds) * time.Second
	}
	if _, err := o.result().maxGroups(); err != nil {
		return clierr.Usagef("%s", err.Error())
	}
	if flags.Changed("groups") && flags.Changed("no-groups") {
		return clierr.Usagef("--groups and --no-groups cannot be used together")
	}
	if mode == modeHost && o.stream != "" && o.stream != "stdout" && o.stream != "stderr" {
		return clierr.Usagef("--stream must be stdout or stderr")
	}

	return nil
}

func (o runOptions) waitTimeout() time.Duration { return o.waitFor }

func (o runOptions) result() resultOptions {
	return resultOptions{noGroups: o.noGroups, groups: o.groups, failOnDrift: o.failOnDrift}
}

func (o runOptions) watchOptions(f *cli.Factory) watchOptions {
	return watchOptions{waitTimeout: o.waitFor, quiet: f.Config().Quiet()}
}

// concurrencyFor is how many hosts the run works on at once: --concurrency, or
// what the old run used, or the default for how it is run; never more than
// there are hosts.
func (o runOptions) concurrencyFor(detached bool, hosts, previous int) int {
	n := o.concurrencyValue
	switch {
	case n > 0:
	case previous > 0:
		n = previous
	case detached:
		n = maxConcurrency
	default:
		n = defaultAttachedConcurrency
	}

	return max(1, min(n, hosts))
}

// followUp says what to do after a run is accepted: whether to watch it, for
// how long, and how to print the result. r is nil when an existing run is
// followed.
func (o runOptions) followUp(f *cli.Factory, r *runRequest) followUp {
	fu := followUp{watch: o.watchOptions(f), waitText: o.waitFor, result: o.result()}
	if r == nil {
		return fu
	}
	seconds := 0
	switch {
	case r.run != nil:
		seconds = r.run.TimeoutSeconds
	case r.do != nil:
		seconds = r.do.TimeoutSeconds
	}
	long := seconds > maxAttachedTimeout
	fu.returnAfter = o.detach || long
	fu.long = long && !o.detach

	return fu
}

// executeRun is `run --on TARGET -- PROGRAM`: resolve the hosts, show them,
// confirm, submit, then wait.
func executeRun(command *cobra.Command, f *cli.Factory, o *runOptions, pos, program []string) error {
	ctx := command.Context()
	flags := command.Flags()

	var capability *api.CapabilitySpec
	if o.do != "" || flags.Changed("do") {
		if len(program) > 0 {
			return clierr.Newf(clierr.ExitUsage, "give inputs as KEY=VALUE after the capability name, for example: --do service.status service=nginx.service",
				"--do takes no program after --")
		}
		name := strings.TrimSpace(o.do)
		if name == "" || strings.ContainsAny(name, " \t") || sanitize.Line(name) != name {
			return clierr.Newf(clierr.ExitUsage, "known capabilities: "+strings.Join(knownCapabilities, ", "), "--do needs a capability name")
		}
		if flags.Changed("concurrency") {
			return clierr.Usagef("--concurrency applies to programs, not capabilities")
		}
		input, err := o.capabilityInput(pos)
		if err != nil {
			return err
		}
		capability = &api.CapabilitySpec{Name: name, Input: input}
	} else {
		if flags.Changed("input-json") {
			return clierr.Usagef("--input-json needs --do")
		}
		if len(pos) > 0 || len(program) == 0 {
			return clierr.Newf(clierr.ExitUsage, "for example: machinarchy run --on tier=web -- uptime", "put the program after --")
		}
	}
	if flags.Changed("wait-timeout") && flags.Changed("detach") {
		return clierr.Usagef("--wait-timeout needs a run that is watched; --detach returns at once")
	}

	seconds := defaultRunTimeout
	if o.timeoutSet {
		seconds = o.timeoutSeconds
	}
	if seconds > maxAttachedTimeout && flags.Changed("wait-timeout") {
		return clierr.Usagef("a run over %s runs detached and is not watched, so --wait-timeout does not apply", humanSeconds(maxAttachedTimeout))
	}

	client, err := f.NewAPIClient(ctx)
	if err != nil {
		return err
	}
	renv, err := newRunEnv(ctx, f, client)
	if err != nil {
		return err
	}
	plan, err := target.Parse(o.on, o.except, renv.scope.env)
	if err != nil {
		return err
	}

	var names []string
	envTotal := 0
	if plan.NeedsHosts() || renv.scope.env != "" {
		hosts, err := listAllHosts(ctx, client, api.HostQuery{EnvironmentID: renv.envID})
		if err != nil {
			return fmt.Errorf("list hosts: %w", err)
		}
		envTotal = len(hosts)
		for _, host := range hosts {
			names = append(names, host.Metadata.Name)
		}
	}
	rule, err := plan.Rule(names)
	if err != nil {
		return err
	}
	set, err := previewRule(ctx, client, rule)
	if err != nil {
		return err
	}

	request := &runRequest{rule: rule, title: "Run on", envName: renv.scope.env, envTotal: envTotal}
	if capability != nil {
		capability.TimeoutSeconds = seconds
		request.do = capability
	} else {
		detached := o.detach || seconds > maxAttachedTimeout
		request.run = &api.RunSpec{Argv: program, TimeoutSeconds: seconds, Detach: detached,
			Concurrency: o.concurrencyFor(detached, len(set.Hosts), 0)}
	}

	return runPrepared(command, f, client, renv, request, set, *o)
}

// capabilityInput is the JSON object a capability gets: KEY=VALUE words (all
// text, sent unchanged) or --input-json.
func (o *runOptions) capabilityInput(pairs []string) ([]byte, error) {
	if o.inputJSON != "" {
		if len(pairs) > 0 {
			return nil, clierr.Usagef("give the input as KEY=VALUE or with --input-json, not both")
		}

		return validObject(o.inputJSON)
	}
	if len(pairs) == 0 {
		return nil, nil
	}

	return inputObject(pairs)
}
