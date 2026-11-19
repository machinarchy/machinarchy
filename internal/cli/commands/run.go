package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/spf13/cobra"
)

// pollInterval is how often --wait checks an operation.
var pollInterval = time.Second

var terminalOperationStates = map[string]bool{"Succeeded": true, "PartiallySucceeded": true, "Failed": true,
	"Cancelled": true, "TimedOut": true, "Interrupted": true}

// maxAttachedTimeout is the longest run that keeps its SSH session open.
const maxAttachedTimeout = 900

// NewRunCommand runs a program on managed hosts. It previews the targets,
// asks for confirmation, and submits bound to the previewed set, so the
// operation never runs on hosts the user did not see.
func NewRunCommand(factory *cli.Factory) *cobra.Command {
	var rule api.TargetRule
	var timeout int
	var wait, detach bool
	command := &cobra.Command{
		Use:   "run (-l SELECTOR | --host NAME-OR-ID... | --all) [--exclude NAME-OR-ID...] -- PROGRAM [ARG...]",
		Short: "Run a program (no shell) on selected managed hosts",
		Long: "Run a program on managed hosts. Without --wait the run is detached: each host's command runs under a\n" +
			"supervisor (systemd-run or setsid) and the SSH session ends at once, so it may run up to 24 hours;\n" +
			"follow it with `machinarchy logs operation ID --follow`. With --wait the session stays open (at most\n" +
			"15 minutes) unless --detach is given or the timeout is longer.",
		Example: "  machinarchy run -l tier=web -- uptime\n" +
			"  machinarchy run --host web-01 --host web-02 --wait -- systemctl is-active nginx\n" +
			"  machinarchy run -l tier=db --timeout 7200 -- /opt/backup/run.sh",
		Args: cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, argv []string) error {
			if command.ArgsLenAtDash() != 0 {
				return clierr.Usagef("put the program after --, for example: machinarchy run -l tier=web -- uptime")
			}
			if rule.Selector == "" && len(rule.Include) == 0 && !rule.All {
				return clierr.Usagef("select hosts with -l SELECTOR, --host or --all")
			}
			printer, err := factory.Printer(command.OutOrStdout(), output.FormatTable)
			if err != nil {
				return err
			}
			client, err := factory.NewAPIClient(command.Context())
			if err != nil {
				return err
			}
			preview, err := client.PreviewTargets(command.Context(), rule)
			if err != nil {
				return fmt.Errorf("preview targets: %w", err)
			}
			if len(preview.UnknownIncludes)+len(preview.UnknownExcludes) > 0 {
				return clierr.Newf(clierr.ExitNotFound, "", "unknown hosts: %s",
					strings.Join(append(preview.UnknownIncludes, preview.UnknownExcludes...), ", "))
			}
			if len(preview.Hosts) == 0 {
				return clierr.Newf(clierr.ExitNotFound, "", "no hosts match the selection")
			}
			names := make([]string, len(preview.Hosts))
			for i, host := range preview.Hosts {
				names[i] = host.Metadata.Name
			}
			prompt := fmt.Sprintf("Run %q on %d host(s): %s?", strings.Join(argv, " "), len(names), summarize(names, 10))
			if err := factory.RequireConfirmation(prompt); err != nil {
				return err
			}
			detached := detach || !wait || timeout > maxAttachedTimeout
			concurrency := 1
			if detached {
				concurrency = min(len(preview.Hosts), 20) // a launch takes about a second
			}
			op, err := client.SubmitOperation(command.Context(), api.OperationInput{Targets: rule, ExpectTargetsHash: preview.TargetsHash,
				Run: api.RunSpec{Argv: argv, TimeoutSeconds: timeout, Concurrency: concurrency, Detach: detached}})
			if err != nil {
				return fmt.Errorf("submit operation: %w", err)
			}
			if !wait {
				_, err = fmt.Fprintf(command.OutOrStdout(), "Operation %s submitted (detached).\nFollow it with:  machinarchy logs operation %s --follow\nStatus:          machinarchy get operation %s\n", op.ID, op.ID, op.ID)
				return err
			}
			if op, err = waitForOperation(command.Context(), client, op.ID); err != nil {
				return err
			}
			if err := printOperation(printer, op); err != nil {
				return err
			}
			if op.Status.State != "Succeeded" {
				return clierr.Newf(clierr.ExitFailure, "", "operation %s finished %s", op.ID, op.Status.State)
			}
			return nil
		},
	}
	flags := command.Flags()
	flags.StringVarP(&rule.Selector, "selector", "l", "", "Select hosts, for example tier=web,environment=prod")
	flags.StringArrayVar(&rule.Include, "host", nil, "Add a host by name or ID (repeatable)")
	flags.StringArrayVar(&rule.Exclude, "exclude", nil, "Remove a host by name or ID (repeatable)")
	flags.BoolVar(&rule.All, "all", false, "Target every managed host")
	flags.IntVar(&timeout, "timeout", 60, "Per-host timeout in seconds (up to 86400 when detached, 900 otherwise)")
	flags.BoolVar(&wait, "wait", false, "Wait for the result and print it; exit non-zero unless every host succeeded")
	flags.BoolVar(&detach, "detach", false, "Run detached even with --wait (the default without --wait)")
	return command
}

func waitForOperation(ctx context.Context, client api.Client, id string) (*api.Operation, error) {
	for {
		op, err := client.GetOperation(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("get operation: %w", err)
		}
		if terminalOperationStates[op.Status.State] {
			return op, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func summarize(names []string, limit int) string {
	if len(names) <= limit {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:limit], ", ") + fmt.Sprintf(" and %d more", len(names)-limit)
}

// NewCancelCommand cancels a running operation.
func NewCancelCommand(factory *cli.Factory) *cobra.Command {
	command := &cobra.Command{Use: "cancel", Short: "Cancel a running resource", Args: cobra.NoArgs}
	command.AddCommand(&cobra.Command{
		Use:     "operation ID",
		Aliases: []string{"op", "run"},
		Short:   "Ask a running operation to stop",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			client, err := factory.NewAPIClient(command.Context())
			if err != nil {
				return err
			}
			op, err := client.CancelOperation(command.Context(), args[0])
			if err != nil {
				return fmt.Errorf("cancel operation: %w", err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "Cancellation requested for operation %s (%s).\n", op.ID, op.Status.State)
			return err
		},
	})
	return command
}
