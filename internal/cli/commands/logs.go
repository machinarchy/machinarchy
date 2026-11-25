package commands

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/spf13/cobra"
)

// NewLogsCommand prints, and optionally follows, a target's output.
func NewLogsCommand(factory *cli.Factory) *cobra.Command {
	command := &cobra.Command{Use: "logs", Short: "Print a resource's output", Args: cobra.NoArgs}
	var host, stream string
	var follow bool
	operation := &cobra.Command{
		Use:     "operation ID [--host NAME-OR-ID] [--stream stdout|stderr] [--follow]",
		Aliases: []string{"op", "run"},
		Short:   "Print an operation's output per host; --follow streams a running detached command",
		Long: "Print one stream of a target's output. A detached command keeps its first and last 15 KiB per stream;\n" +
			"a skipped middle part is shown as one \"[… N bytes omitted …]\" line. Without --host every target is\n" +
			"printed in turn; --follow needs --host when the operation has several targets.",
		Example: "  machinarchy logs operation 0b7c… --follow\n" +
			"  machinarchy logs operation 0b7c… --host web-01 --stream stderr",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if stream != "stdout" && stream != "stderr" {
				return clierr.Usagef("--stream must be stdout or stderr")
			}
			client, err := factory.NewAPIClient(command.Context())
			if err != nil {
				return err
			}
			op, err := client.GetOperation(command.Context(), args[0])
			if err != nil {
				return fmt.Errorf("get operation: %w", err)
			}
			targets, err := selectTargets(op, host)
			if err != nil {
				return err
			}
			if follow && len(targets) > 1 {
				names := make([]string, len(targets))
				for i, target := range targets {
					names[i] = target.Spec.DisplayName
				}
				return clierr.Usagef("this operation has %d targets; pick one with --host (%s)", len(targets), summarize(names, 10))
			}
			out := command.OutOrStdout()
			for i, target := range targets {
				if len(targets) > 1 {
					if i > 0 {
						fmt.Fprintln(out)
					}
					fmt.Fprintf(out, "==> %s (%s) <==\n", target.Spec.DisplayName, target.State)
				}
				if err := printOutput(command.Context(), client, out, op.ID, target.Spec.HostID, stream, follow); err != nil {
					return err
				}
			}
			return nil
		},
	}
	flags := operation.Flags()
	flags.StringVar(&host, "host", "", "The target host, by name or ID")
	flags.StringVar(&stream, "stream", "stdout", "stdout or stderr")
	flags.BoolVarP(&follow, "follow", "f", false, "Keep printing new output until the command ends")
	command.AddCommand(operation)
	return command
}

func selectTargets(op *api.Operation, host string) ([]api.OperationTarget, error) {
	if host == "" {
		if len(op.Status.Targets) == 0 {
			return nil, clierr.Newf(clierr.ExitNotFound, "", "operation %s has no targets", op.ID)
		}
		return op.Status.Targets, nil
	}
	for _, target := range op.Status.Targets {
		if target.Spec.HostID == host || target.Spec.DisplayName == host {
			return []api.OperationTarget{target}, nil
		}
	}
	return nil, clierr.Newf(clierr.ExitNotFound, "", "host %q is not a target of operation %s", host, op.ID)
}

// printOutput prints a target's stream from the start; with follow it keeps
// asking from the last offset until the command has ended.
func printOutput(ctx context.Context, client api.Client, out io.Writer, operationID, hostID, stream string, follow bool) error {
	var from int64
	for {
		chunk, err := client.TargetOutput(ctx, operationID, hostID, stream, from)
		if err != nil {
			return fmt.Errorf("read output: %w", err)
		}
		if _, err := io.WriteString(out, chunk.Text); err != nil {
			return err
		}
		from = chunk.Next
		if !follow || !chunk.Live {
			if chunk.Text != "" && !strings.HasSuffix(chunk.Text, "\n") {
				fmt.Fprintln(out)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}
