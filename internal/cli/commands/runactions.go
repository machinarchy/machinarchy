package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/group"
	"github.com/machinarchy/machinarchy/internal/cli/msg"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
	"github.com/machinarchy/machinarchy/internal/cli/ui"
)

// retryPlan sorts the hosts of a finished run into the ones to run again and
// the ones that are left out, and why (spec 3.10.3): a host whose command
// failed with a known outcome is retried; a host whose outcome is unknown
// (lost contact, interrupted) or that was cancelled may already have run the
// command, so it is only included when asked.
type retryPlan struct {
	again, unknown, cancelled []api.OperationTarget
	succeeded                 int
}

func planRetry(op *api.Operation, includeUnknown, includeCancelled bool) retryPlan {
	var plan retryPlan
	for _, target := range op.Status.Targets {
		switch group.ClassOf(target.State) {
		case group.ClassFailed, group.ClassTimedOut:
			plan.again = append(plan.again, target)
		case group.ClassUnknown:
			plan.unknown = append(plan.unknown, target)
		case group.ClassCancelled:
			plan.cancelled = append(plan.cancelled, target)
		case group.ClassSucceeded:
			plan.succeeded++
		case group.ClassRunning, group.ClassPending:
		}
	}
	if includeUnknown {
		plan.again = append(plan.again, plan.unknown...)
	}
	if includeCancelled {
		plan.again = append(plan.again, plan.cancelled...)
	}

	return plan
}

func targetNames(targets []api.OperationTarget) []string {
	names := make([]string, len(targets))
	for i, target := range targets {
		names[i] = sanitize.Line(target.Spec.DisplayName)
	}

	return group.SortNatural(names)
}

// compressNames lists hosts for a preview line.
func compressNames(names []string) string { return group.CompressHosts(names, 60) }

// retryFailed starts a new run of the same command on the hosts of run id that
// failed with a known outcome. It never changes the old run and never says the
// old run is fixed.
func retryFailed(command *cobra.Command, f *cli.Factory, client api.Client, id string, o runOptions) error {
	ctx := command.Context()
	op, err := client.GetOperation(ctx, id)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}
	if err := checkOperation(op, "GET /api/v1/managed/operations/"+id); err != nil {
		return err
	}
	short := shortRunID(op.ID)
	if !terminalOperationStates[op.Status.State] {
		return clierr.Newf(clierr.ExitConflict, "machinarchy run "+short+" --follow", "run %s has not finished, so there is nothing to retry yet", short)
	}

	// The command is read from the old run; the server shows it to operators
	// and owners only.
	original := op.Spec
	if (original.Run == nil || len(original.Run.Argv) == 0) && (original.Capability == nil || original.Capability.Name == "") {
		return clierr.Detailed(clierr.ExitAuth, fmt.Errorf("the command of run %s is not shown to your role, so it cannot be run again", short),
			"readers can see states and exit codes, not commands", "ask an operator or an owner to run it")
	}

	plan := planRetry(op, o.includeUnknown, o.includeCancelled)
	if len(plan.again) == 0 {
		return nothingToRetry(command, op, plan)
	}

	renv, err := newRunEnv(ctx, f, client)
	if err != nil {
		return err
	}
	request := &runRequest{title: "Run again on", note: "(same as " + short + ")", envName: renv.scope.env}
	ids := make([]string, len(plan.again))
	for i, target := range plan.again {
		ids[i] = target.Spec.HostID
	}
	request.rule = api.TargetRule{Include: ids}
	if err := o.applyTo(request, original, len(ids)); err != nil {
		return err
	}
	if !o.includeUnknown && len(plan.unknown) > 0 {
		request.skipped = append(request.skipped,
			fmt.Sprintf("%s with unknown outcome: %s   (the command may already have run there)", plural(len(plan.unknown), "host"), compressNames(targetNames(plan.unknown))),
			"add --include-unknown to include them")
	}
	if !o.includeCancelled && len(plan.cancelled) > 0 {
		request.skipped = append(request.skipped,
			fmt.Sprintf("%s cancelled: %s   (the command may have run there before the cancel)", plural(len(plan.cancelled), "host"), compressNames(targetNames(plan.cancelled))),
			"add --include-cancelled to include them")
	}
	if o.includeUnknown && len(plan.unknown) > 0 {
		request.skipped = append(request.skipped, fmt.Sprintf("Warning: %s may already have run the command; it will run there again", compressNames(targetNames(plan.unknown))))
	}
	if o.includeCancelled && len(plan.cancelled) > 0 {
		request.skipped = append(request.skipped, fmt.Sprintf("Warning: %s may have run the command before the cancel; it will run there again", compressNames(targetNames(plan.cancelled))))
	}

	set, err := client.PreviewTargets(ctx, request.rule)
	if err != nil {
		return fmt.Errorf("preview the hosts: %w", err)
	}
	if len(set.UnknownIncludes) > 0 { // removed since the run: left out, said so
		gone := map[string]bool{}
		for _, hostID := range set.UnknownIncludes {
			gone[hostID] = true
		}
		var left []string
		var removed []api.OperationTarget
		for _, target := range plan.again {
			if gone[target.Spec.HostID] {
				removed = append(removed, target)
			} else {
				left = append(left, target.Spec.HostID)
			}
		}
		request.skipped = append(request.skipped, fmt.Sprintf("%s no longer managed: %s", plural(len(removed), "host"), compressNames(targetNames(removed))))
		if len(left) == 0 {
			return clierr.Newf(clierr.ExitNotFound, "machinarchy hosts", "none of the hosts of run %s is still managed", short)
		}
		request.rule = api.TargetRule{Include: left}
		if set, err = client.PreviewTargets(ctx, request.rule); err != nil {
			return fmt.Errorf("preview the hosts: %w", err)
		}
	}
	if err := checkTargetCount(len(set.Hosts)); err != nil {
		return err
	}

	return runPrepared(command, f, client, renv, request, set, o)
}

// nothingToRetry says why a retry has no hosts, and what would add some.
func nothingToRetry(command *cobra.Command, op *api.Operation, plan retryPlan) error {
	short := shortRunID(op.ID)
	out := command.OutOrStdout()
	writeLine(out, fmt.Sprintf("Nothing to run again: no host of run %s failed with a known outcome.", short))
	if len(plan.unknown) > 0 {
		writeLine(out, fmt.Sprintf("  %s with unknown outcome: %s. The command may already have run there.", plural(len(plan.unknown), "host"), compressNames(targetNames(plan.unknown))))
		writeLine(out, "  Next:  machinarchy run "+short+" --retry-failed --include-unknown")
	}
	if len(plan.cancelled) > 0 {
		writeLine(out, fmt.Sprintf("  %s cancelled: %s.", plural(len(plan.cancelled), "host"), compressNames(targetNames(plan.cancelled))))
		writeLine(out, "  Next:  machinarchy run "+short+" --retry-failed --include-cancelled")
	}
	return nil
}

// applyTo fills a request's program or capability from an old run, with the
// flags the person gave this time on top.
func (o runOptions) applyTo(request *runRequest, original api.OperationSpec, hosts int) error {
	switch {
	case original.Run != nil && len(original.Run.Argv) > 0:
		spec := *original.Run
		spec.Argv = slices.Clone(original.Run.Argv)
		if o.timeoutSet {
			spec.TimeoutSeconds = o.timeoutSeconds
		}
		if spec.TimeoutSeconds == 0 {
			spec.TimeoutSeconds = defaultRunTimeout
		}
		if o.detach || spec.TimeoutSeconds > maxAttachedTimeout {
			spec.Detach = true
		}
		spec.Concurrency = o.concurrencyFor(spec.Detach, hosts, spec.Concurrency)
		request.run = &spec
	default:
		spec := *original.Capability
		spec.Input = slices.Clone(original.Capability.Input)
		if o.timeoutSet {
			spec.TimeoutSeconds = o.timeoutSeconds
		}
		if spec.TimeoutSeconds == 0 {
			spec.TimeoutSeconds = defaultRunTimeout
		}
		request.do = &spec
	}

	return nil
}

// cancelRun asks the controller to cancel a run and follows it to the end.
// It never says a run stopped before the controller does: a cancel is a
// request, and a host that finished first keeps its result.
func cancelRun(command *cobra.Command, f *cli.Factory, client api.Client, id string, o runOptions) error {
	ctx := command.Context()
	op, err := client.GetOperation(ctx, id)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}
	if err := checkOperation(op, "GET /api/v1/managed/operations/"+id); err != nil {
		return err
	}
	short := shortRunID(op.ID)
	if terminalOperationStates[op.Status.State] {
		return clierr.Newf(clierr.ExitConflict, "machinarchy run "+short, "run %s already finished (%s); there is nothing to cancel", short, sanitize.Line(op.Status.State))
	}

	renv, err := newRunEnv(ctx, f, client)
	if err != nil {
		return err
	}
	protected, err := protectedEnvsOfRun(ctx, f, client, renv.index, op)
	if err != nil {
		return err
	}

	running := 0
	for _, t := range op.Status.Targets {
		if !terminalTargetStates[t.State] {
			running++
		}
	}
	errOut := f.Streams.ErrOut
	_, _ = fmt.Fprintf(errOut, "%s  Cancel run %s (%s still running)\n", renv.scope.line(), short, plural(running, "host"))
	if len(protected) > 0 {
		_, _ = fmt.Fprintf(errOut, "  Hosts     in %s (protected)\n", strings.Join(protected, ", "))
	}
	if f.DryRun() {
		_, _ = fmt.Fprint(errOut, ui.Render(msg.Get("run.dry_run"), f.Style()))

		return nil
	}
	if err := confirmRun(f, &runRequest{}, &api.TargetSet{}, protected, "Cancel it?"); err != nil {
		return err
	}

	cancelled, err := client.CancelOperation(ctx, op.ID)
	if err != nil {
		return fmt.Errorf("cancel the run: %w", err)
	}
	state := sanitize.Line(cancelled.Status.State)
	if o.noWait {
		return printMessage(command, f, msg.Get("run.cancel_sent").With(msg.Vars{"id": short, "state": state}))
	}
	_, _ = fmt.Fprint(errOut, ui.Render(msg.Get("run.cancel_requested"), f.Style()))

	final, err := watchRun(command, f, client, op.ID, o.watchOptions(f))
	if err != nil {
		return reportWatchEnd(f, op.ID, o.waitTimeout(), err)
	}
	if final.Status.State == "Cancelled" {
		_, _ = fmt.Fprint(errOut, ui.Render(msg.Get("run.cancelled"), f.Style()))
	} else {
		_, _ = fmt.Fprint(errOut, ui.Render(msg.Get("run.cancel_late").With(msg.Vars{"state": sanitize.Line(final.Status.State)}), f.Style()))
	}
	unreached := 0
	for _, t := range final.Status.Targets {
		if t.Result != nil && t.Result.ErrorClass == "unreachable_during_cancel" {
			unreached++
		}
	}
	if unreached > 0 {
		_, _ = fmt.Fprint(errOut, ui.Render(msg.Get("run.cancel_unreached").With(msg.Vars{"hosts": plural(unreached, "host")}), f.Style()))
	}

	return printAndExit(command, f, client, final, o.result())
}

// protectedEnvsOfRun lists the guarded envs of the hosts of a run. It reads
// the host list only when this computer guards some env at all.
func protectedEnvsOfRun(ctx context.Context, f *cli.Factory, client api.Client, index envIndex, op *api.Operation) ([]string, error) {
	guarded := false
	for _, name := range index.byID {
		guarded = guarded || index.guarded(f, name)
	}
	if !guarded {
		return nil, nil
	}
	hosts, err := listAllHosts(ctx, client, api.HostQuery{})
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	inRun := map[string]bool{}
	for _, t := range op.Status.Targets {
		inRun[t.Spec.HostID] = true
	}
	var affected []api.Host
	for _, host := range hosts {
		if inRun[host.Metadata.ID] {
			affected = append(affected, host)
		}
	}

	return protectedEnvsOf(f, index, affected), nil
}

// inputObject builds the input of a capability from KEY=VALUE words, all
// strings, sent as they were typed; the CLI changes no value.
func inputObject(pairs []string) (json.RawMessage, error) {
	object := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			return nil, clierr.Newf(clierr.ExitUsage, "write inputs as KEY=VALUE, for example: --do service.status service=nginx.service",
				"%q is not KEY=VALUE", sanitize.Line(pair))
		}
		if _, dup := object[key]; dup {
			return nil, clierr.Usagef("the input %q is given twice", sanitize.Line(key))
		}
		object[key] = value
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("encode the capability input: %w", err)
	}

	return encoded, nil
}

// validObject checks that text is one JSON object and returns it unchanged.
func validObject(text string) ([]byte, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &object); err != nil || object == nil {
		return nil, clierr.Newf(clierr.ExitUsage, `for example: --input-json '{"service":"nginx.service"}'`, "--input-json must be one JSON object")
	}

	return []byte(strings.TrimSpace(text)), nil
}
