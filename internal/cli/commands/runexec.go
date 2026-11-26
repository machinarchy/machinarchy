package commands

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/config"
	"github.com/machinarchy/machinarchy/internal/cli/group"
	"github.com/machinarchy/machinarchy/internal/cli/msg"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
	"github.com/machinarchy/machinarchy/internal/cli/target"
	"github.com/machinarchy/machinarchy/internal/cli/ui"
)

// runRequest is one run about to be sent: what to run, where, and how the
// preview describes it. Retrying a run builds the same thing from the old run.
type runRequest struct {
	rule api.TargetRule
	run  *api.RunSpec
	do   *api.CapabilitySpec

	// title starts the preview line: "Run on" or "Run again on".
	title string
	// note follows the command in the preview, for example "(same as r-xxxx)".
	note string
	// skipped are lines of "Skipped" notes of a retry.
	skipped []string

	// envName is the env the run is scoped to, "" for none; envTotal is how
	// many hosts it has, 0 when not known.
	envName  string
	envTotal int
}

// scope is the server and env of a command, as the context line says them.
type scope struct {
	target config.Target
	env    string
}

func (s scope) line() string {
	server := s.target.ServerName
	if server == "" {
		server = s.target.Server
	}
	env := s.env
	if env == "" {
		env = "none"
	}

	return "[" + sanitize.Line(server) + " · " + sanitize.Line(env) + "]"
}

// envIndex is what the CLI knows about the server's envs.
type envIndex struct {
	byID map[int]string
	// serverProtected holds the names the server reports as protected.
	serverProtected map[string]bool
}

func loadEnvIndex(ctx context.Context, client api.Client) (envIndex, error) {
	environments, err := client.ListEnvironments(ctx)
	if err != nil {
		return envIndex{}, fmt.Errorf("list envs: %w", err)
	}
	index := envIndex{byID: make(map[int]string, len(environments)), serverProtected: map[string]bool{}}
	for _, environment := range environments {
		index.byID[environment.ID] = environment.Name
		if environment.ServerProtected() {
			index.serverProtected[environment.Name] = true
		}
	}

	return index, nil
}

// guarded reports whether the env needs its name confirmed: the server holds
// it as protected or this computer guards it.
func (e envIndex) guarded(f *cli.Factory, name string) bool {
	return e.serverProtected[name] || f.Config().LocalProtected(name)
}

// nameOf is the env name of an env ID, "" for none.
func (e envIndex) nameOf(id int) string { return e.byID[id] }

// activeEnv resolves --env (or the saved default) to the env's name; "" for
// no scope.
func activeEnv(ctx context.Context, f *cli.Factory, client api.Client, index envIndex) (name string, id int, err error) {
	text := strings.TrimSpace(f.Config().Environment())
	if text == "" || strings.EqualFold(text, target.AnyEnv) {
		return "", 0, nil
	}
	id, err = resolveEnvironment(ctx, client, text)
	if err != nil {
		return "", 0, err
	}

	return index.nameOf(id), id, nil
}

// protectedEnvsOf lists, in order, the envs of the hosts that this computer
// guards (held by the server, or locally on an older server).
func protectedEnvsOf(f *cli.Factory, index envIndex, hosts []api.Host) []string {
	var protected []string
	for _, host := range hosts {
		name := index.nameOf(host.Metadata.EnvironmentID)
		if name != "" && index.guarded(f, name) && !slices.Contains(protected, name) {
			protected = append(protected, name)
		}
	}
	slices.Sort(protected)

	return protected
}

// previewRule asks the controller which hosts a rule selects and refuses what
// cannot run: unknown names, no hosts, and more than the controller allows.
func previewRule(ctx context.Context, client api.Client, rule api.TargetRule) (*api.TargetSet, error) {
	set, err := client.PreviewTargets(ctx, rule)
	if err != nil {
		return nil, fmt.Errorf("preview the hosts: %w", err)
	}
	if unknown := append(append([]string(nil), set.UnknownIncludes...), set.UnknownExcludes...); len(unknown) > 0 {
		for i, name := range unknown {
			unknown[i] = sanitize.Line(name)
		}

		return nil, clierr.Newf(clierr.ExitNotFound, "machinarchy hosts", "no such host: %s", summarize(unknown, 10))
	}

	return set, checkTargetCount(len(set.Hosts))
}

// checkTargetCount refuses a run on no hosts or on more than the controller
// accepts, before anything is shown or asked.
func checkTargetCount(count int) error {
	switch {
	case count == 0:
		return clierr.Newf(clierr.ExitNotFound, "check the tags and names with: machinarchy hosts", "no hosts match")
	case count > maxRunTargets:
		return clierr.Newf(clierr.ExitUsage, "narrow it, for example: machinarchy run --on tier=web -- uptime",
			"this would run on %d hosts; one run can target at most %d", count, maxRunTargets)
	}

	return nil
}

// naturalNames lists host names sanitised, in natural order.
func naturalNames(hosts []api.Host) []string {
	names := make([]string, len(hosts))
	for i, host := range hosts {
		names[i] = sanitize.Line(host.Metadata.Name)
	}

	return group.SortNatural(names)
}

// showPreview prints the blast-radius preview on stderr (spec section 5).
func showPreview(f *cli.Factory, s scope, r *runRequest, set *api.TargetSet, index envIndex, protected []string) {
	out := f.Streams.ErrOut
	names := naturalNames(set.Hosts)
	width := f.Width(out)

	head := fmt.Sprintf("%s  %s %s: ", s.line(), r.title, plural(len(names), "host"))
	_, _ = fmt.Fprintf(out, "%s%s\n", head, group.CompressHosts(names, max(width-len([]rune(head)), 24)))

	switch {
	case r.run != nil:
		line := commandText(r.run.Argv)
		if r.note != "" {
			line += "    " + r.note
		}
		_, _ = fmt.Fprintf(out, "  Command   %s\n", line)
	case r.do != nil:
		line := "--do " + sanitize.Line(r.do.Name)
		if len(r.do.Input) > 0 {
			line += " " + sanitize.Line(string(r.do.Input))
		}
		if r.note != "" {
			line += "    " + r.note
		}
		_, _ = fmt.Fprintf(out, "  Command   %s\n", line)
	}
	for i, line := range r.skipped {
		label := "          "
		if i == 0 {
			label = "  Skipped   "
		}
		_, _ = fmt.Fprintf(out, "%s%s\n", label, line)
	}
	hosts := hostsLine(r, set, index, protected)
	_, _ = fmt.Fprintf(out, "  Hosts     %s\n", hosts)
	if strings.Contains(hosts, "no env ") {
		_, _ = fmt.Fprintln(out, "            no env: hosts that are not in any environment")
	}
	_, _ = fmt.Fprintf(out, "  Limits    %s\n", limitsLine(r))
}

// hostsLine says how many hosts of what the run reaches.
func hostsLine(r *runRequest, set *api.TargetSet, index envIndex, protected []string) string {
	if r.rule.All && r.envName == "" {
		return fmt.Sprintf("All %d hosts on this server", len(set.Hosts))
	}

	counts := map[string]int{}
	for _, host := range set.Hosts {
		counts[index.nameOf(host.Metadata.EnvironmentID)]++
	}
	envs := make([]string, 0, len(counts))
	for name := range counts {
		envs = append(envs, name)
	}
	slices.Sort(envs)

	tag := func(name string) string {
		if slices.Contains(protected, name) {
			return " (protected)"
		}

		return ""
	}
	if len(envs) == 1 && envs[0] != "" {
		name := sanitize.Line(envs[0])
		if r.envName != "" && r.envTotal >= counts[envs[0]] {
			return fmt.Sprintf("%d of %d in %q%s", counts[envs[0]], r.envTotal, name, tag(envs[0]))
		}

		return fmt.Sprintf("%d in %q%s", counts[envs[0]], name, tag(envs[0]))
	}

	parts := make([]string, 0, len(envs))
	for _, name := range envs {
		label := sanitize.Line(name)
		if name == "" {
			label = "no env"
		}
		parts = append(parts, fmt.Sprintf("%s %d%s", label, counts[name], tag(name)))
	}

	return strings.Join(parts, ", ")
}

// limitsLine says how long and how wide the run goes.
func limitsLine(r *runRequest) string {
	switch {
	case r.run != nil && r.run.Detach:
		return fmt.Sprintf("%s per host, %d at a time, runs on the hosts without this terminal", humanSeconds(r.run.TimeoutSeconds), r.run.Concurrency)
	case r.run != nil:
		return fmt.Sprintf("%s per host, %d at a time", humanSeconds(r.run.TimeoutSeconds), r.run.Concurrency)
	case r.do != nil:
		return humanSeconds(r.do.TimeoutSeconds) + " per host"
	}

	return ""
}

// confirmRun asks for the go-ahead: T1 (y/N, --yes) normally, the env's name
// typed (or --confirm-env) in a protected env, and the number of hosts typed
// for --on all outside any env. --yes alone never answers a typed question on
// a terminal and never stands in for --confirm-env without one.
func confirmRun(f *cli.Factory, r *runRequest, set *api.TargetSet, protected []string, prompt string) error {
	typed := false
	for _, env := range protected {
		byFlag := slices.Contains(f.ConfirmEnvs(), env)
		if err := confirmProtectedEnv(f, env, true); err != nil {
			return err
		}
		typed = typed || !byFlag
	}
	if len(protected) > 0 && typed && f.Prompter().Interactive {
		return nil
	}

	if r.rule.All && r.envName == "" && !f.Yes() && f.Prompter().Interactive {
		count := strconv.Itoa(len(set.Hosts))
		answer, err := f.Prompter().Line(fmt.Sprintf("This runs on every host on this server. Type %q to run it", count), "")
		if err != nil || strings.TrimSpace(answer) != count {
			return clierr.Newf(clierr.ExitFailure, "", "aborted; nothing was changed")
		}

		return nil
	}

	return f.RequireConfirmation(prompt)
}

// submitRun sends the run. A reply that says no is an error with a next step;
// a failure with no reply at all, or an interrupt while sending, is the one
// case where it is not known whether the run exists: it says so, never
// resubmits and shows how to check.
func submitRun(command *cobra.Command, f *cli.Factory, client api.Client, r *runRequest, set *api.TargetSet) (*api.Operation, error) {
	input := api.OperationInput{Targets: r.rule, ExpectTargetsHash: set.TargetsHash, Run: r.run, Capability: r.do}
	op, err := client.SubmitOperation(command.Context(), input)
	if err == nil {
		return op, checkOperation(op, "POST /api/v1/managed/operations")
	}

	errOut := f.Streams.ErrOut
	switch {
	case errors.Is(err, context.Canceled):
		_, _ = fmt.Fprint(errOut, "\n"+ui.Render(msg.Get("run.submit_interrupted"), f.Style()))

		return nil, clierr.Reported(clierr.ExitInterrupted)
	case isTransportFailure(err) || serverFailedMidway(err):
		_, _ = fmt.Fprint(errOut, "\n"+ui.Render(msg.Get("run.submit_lost").With(msg.Vars{"why": describeFailure(err)}), f.Style()))

		return nil, clierr.Reported(clierr.ExitUnavailable)
	}

	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Status == 409 && apiErr.Field("code") == "targets_changed":
			return nil, clierr.Newf(clierr.ExitConflict, "run the command again to see the new set of hosts",
				"the set of hosts changed while you were deciding. Nothing ran")
		case r.run != nil && apiErr.Status >= 400 && apiErr.Status < 500 && mentionsAgent(apiErr.Message):
			return nil, &clierr.Error{Code: apiErr.ExitCode(), Err: fmt.Errorf("the server refused the program: %s", apiErr.Message),
				Why:  "these hosts are reached through an agent, which runs capabilities, not programs",
				Hint: "machinarchy run --on ... --do runtime.info"}
		}
	}

	return nil, fmt.Errorf("send the run: %w", err)
}

// serverFailedMidway reports a 5xx that may have come after the run was
// stored: the server broke after it began, so the run might exist.
func serverFailedMidway(err error) bool {
	var apiErr *api.Error

	return errors.As(err, &apiErr) && (apiErr.Status == 500 || apiErr.Status == 502 || apiErr.Status == 504)
}

func mentionsAgent(message string) bool {
	message = strings.ToLower(message)

	return strings.Contains(message, "agent") || strings.Contains(message, "capabilit")
}

// followUp is what happens after a run is accepted.
type followUp struct {
	watch    watchOptions
	waitText time.Duration
	result   resultOptions
	// returnAfter ends the command once the run is accepted (--detach, or a
	// run too long to watch); long says it was the latter.
	returnAfter, long bool
}

// launch submits a run that was previewed and confirmed, then waits for it
// (the default) and prints the grouped result, or returns after the submit
// when the run is detached.
func launch(command *cobra.Command, f *cli.Factory, client api.Client, r *runRequest, set *api.TargetSet, fu followUp) error {
	op, err := submitRun(command, f, client, r, set)
	if err != nil {
		return err
	}
	id := op.ID
	quiet := f.Config().Quiet()

	if quiet {
		// Only the ID goes to stdout, so a script can keep it.
		writeLine(command.OutOrStdout(), id)
	} else {
		_, _ = fmt.Fprint(f.Streams.ErrOut, ui.Render(msg.Get("run.started").With(msg.Vars{"id": shortRunID(id)}), f.Style()))
	}

	if fu.returnAfter {
		if quiet {
			return nil
		}
		hosts := plural(len(op.Status.Targets), "host")
		if len(op.Status.Targets) == 0 {
			hosts = plural(len(set.Hosts), "host")
		}
		message := "run.detached"
		if fu.long {
			message = "run.detached_long"
		}
		if printer, perr := f.Printer(command.OutOrStdout(), output.FormatTable); perr == nil && printer.Format() != output.FormatTable {
			return printer.Document(op)
		}

		return printMessage(command, f, msg.Get(message).With(msg.Vars{"id": shortRunID(id), "hosts": hosts}))
	}

	return finish(command, f, client, id, fu)
}

// finish watches a run to its end and prints its result.
func finish(command *cobra.Command, f *cli.Factory, client api.Client, id string, fu followUp) error {
	final, err := watchRun(command, f, client, id, fu.watch)
	if err != nil {
		return reportWatchEnd(f, id, fu.waitText, err)
	}

	return printAndExit(command, f, client, final, fu.result)
}

// printAndExit prints the result of a finished run and turns its code into
// the command's exit.
func printAndExit(command *cobra.Command, f *cli.Factory, client api.Client, op *api.Operation, o resultOptions) error {
	o.quiet = o.quiet || f.Config().Quiet()
	code, err := printResult(command, f, client, op, o)
	if err != nil {
		return err
	}
	if code != 0 {
		return clierr.Reported(code)
	}

	return nil
}

// runEnv is the server, the env and the envs' names a command works in.
type runEnv struct {
	scope scope
	index envIndex
	envID int
}

// newRunEnv resolves the server and the active env of a run command.
func newRunEnv(ctx context.Context, f *cli.Factory, client api.Client) (runEnv, error) {
	server, err := f.ResolveTarget()
	if err != nil {
		return runEnv{}, err
	}
	index, err := loadEnvIndex(ctx, client)
	if err != nil {
		return runEnv{}, err
	}
	name, id, err := activeEnv(ctx, f, client, index)
	if err != nil {
		return runEnv{}, err
	}

	return runEnv{scope: scope{target: server, env: name}, index: index, envID: id}, nil
}

// runPrepared shows the preview of a run, asks for the go-ahead and launches
// it. With --dry-run it stops after the preview.
func runPrepared(command *cobra.Command, f *cli.Factory, client api.Client, renv runEnv, r *runRequest, set *api.TargetSet, o runOptions) error {
	protected := protectedEnvsOf(f, renv.index, set.Hosts)
	showPreview(f, renv.scope, r, set, renv.index, protected)
	if f.DryRun() {
		_, _ = fmt.Fprint(f.Streams.ErrOut, ui.Render(msg.Get("run.dry_run"), f.Style()))

		return nil
	}
	if err := confirmRun(f, r, set, protected, "Run it?"); err != nil {
		return err
	}

	return launch(command, f, client, r, set, o.followUp(f, r))
}
