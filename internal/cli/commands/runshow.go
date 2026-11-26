package commands

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/group"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
	"github.com/machinarchy/machinarchy/internal/cli/ui"
)

// listDetailWorkers is how many runs' commands are looked up at once.
const listDetailWorkers = 8

// listRuns prints `machinarchy run`: the recent runs, newest first.
func listRuns(command *cobra.Command, f *cli.Factory, o runOptions) error {
	if o.limit < 1 || o.limit > 500 {
		return clierr.Usagef("--limit must be 1 to 500")
	}
	printer, err := f.Printer(command.OutOrStdout(), output.FormatTable)
	if err != nil {
		return err
	}
	client, err := f.NewAPIClient(command.Context())
	if err != nil {
		return err
	}

	query := api.OperationQuery{State: o.state, Limit: o.limit}
	if o.host != "" {
		if query.HostID, err = hostIDByName(command.Context(), client, o.host); err != nil {
			return err
		}
	}
	page, err := client.ListOperations(command.Context(), query)
	if err != nil {
		return fmt.Errorf("list runs: %w", err)
	}

	switch {
	case printer.Format() != output.FormatTable:
		return printer.Document(page.Items)
	case f.Config().Quiet():
		ids := make([]string, len(page.Items))
		for i, item := range page.Items {
			ids[i] = item.ID
		}

		return printer.IDs(ids)
	case len(page.Items) == 0:
		writeLine(command.OutOrStdout(), "No runs yet.")
		writeLine(f.Streams.ErrOut, "Next:  machinarchy run --on tier=web -- uptime")

		return nil
	}

	what := runCommands(command.Context(), client, page.Items)
	now, style := f.Now(), f.StyleFor(command.OutOrStdout())
	rows := make([][]string, len(page.Items))
	for i, item := range page.Items {
		rows[i] = []string{shortRunID(item.ID), what[i], ago(now, item.CreatedAt), runResultText(item, style)}
	}

	return printer.Table([]string{"RUN", "WHAT", "WHEN", "RESULT"}, rows)
}

// hostIDByName finds a host's ID by its name (case-insensitive) or ID.
func hostIDByName(ctx context.Context, client api.Client, name string) (string, error) {
	hosts, err := listAllHosts(ctx, client, api.HostQuery{})
	if err != nil {
		return "", fmt.Errorf("list hosts: %w", err)
	}
	for _, host := range hosts {
		if host.Metadata.ID == name || strings.EqualFold(host.Metadata.Name, name) {
			return host.Metadata.ID, nil
		}
	}

	return "", clierr.Newf(clierr.ExitNotFound, "machinarchy hosts", "no host called %q", sanitize.Line(name))
}

// runCommands says what each listed run ran. The list carries no arguments,
// so each run is read, a few at a time; a reader (who may not see arguments)
// or a run that cannot be read shows "(hidden)".
func runCommands(ctx context.Context, client api.Client, items []api.OperationSummary) []string {
	what := make([]string, len(items))
	var wg sync.WaitGroup
	slots := make(chan struct{}, listDetailWorkers)
	for i, item := range items {
		slots <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			what[i] = "(hidden)"
			if op, err := client.GetOperation(ctx, item.ID); err == nil && op != nil {
				if text := specText(op.Spec); text != "" {
					what[i] = text
				}
			}
		}()
	}
	wg.Wait()

	return what
}

// ago says how long ago t was.
func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case t.IsZero():
		return "-"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d/time.Hour))
	}

	return fmt.Sprintf("%d days ago", int(d/(24*time.Hour)))
}

// runResultText is the RESULT column: how many hosts did what.
func runResultText(item api.OperationSummary, style ui.Style) string {
	count := func(states ...string) int {
		n := 0
		for _, state := range states {
			n += item.Targets[state]
		}

		return n
	}
	total := 0
	for _, n := range item.Targets {
		total += n
	}
	ok, failed := count(group.StateSucceeded), count(group.StateFailed, group.StateTimedOut)
	unknown, cancelled := count(group.StateLost, group.StateInterrupted), count(group.StateCancelled)
	running := count(group.StateRunning, group.StatePending)

	var parts []string
	add := func(n int, label string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, label))
		}
	}
	add(ok, "ok")
	add(failed, "failed")
	add(unknown, "unknown")
	add(cancelled, "cancelled")
	add(running, "running")
	if len(parts) == 0 {
		return item.State
	}

	symbol := ui.SymWarn
	switch {
	case running > 0 || !terminalOperationStates[item.State]:
		symbol = ui.SymRunning
	case ok == total:
		symbol = ui.SymOK
	case ok == 0:
		symbol = ui.SymFail
	}

	return style.Marker(symbol) + " " + strings.Join(parts, ", ")
}

// showRun prints one run: the grouped result of its hosts.
func showRun(command *cobra.Command, f *cli.Factory, client api.Client, id string, o runOptions) error {
	op, err := client.GetOperation(command.Context(), id)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}
	if err := checkOperation(op, "GET /api/v1/managed/operations/"+id); err != nil {
		return err
	}
	if err := printAndExit(command, f, client, op, o.result()); err != nil {
		return err
	}
	if !terminalOperationStates[op.Status.State] && !f.Config().Quiet() {
		writeLine(f.Streams.ErrOut, "\nThis run is still going. Watch it:  machinarchy run "+shortRunID(op.ID)+" --follow")
	}

	return nil
}

// selectTarget finds the host of a run by its name or ID.
func selectTarget(op *api.Operation, host string) (api.OperationTarget, error) {
	labels := hostLabels(op.Status.Targets)
	for i, target := range op.Status.Targets {
		clean := sanitize.Line(target.Spec.DisplayName) // a name is typed without its control bytes
		if target.Spec.HostID == host || strings.EqualFold(target.Spec.DisplayName, host) || strings.EqualFold(clean, host) || strings.EqualFold(labels[i], host) {
			return target, nil
		}
	}

	return api.OperationTarget{}, clierr.Newf(clierr.ExitNotFound, "machinarchy run "+shortRunID(op.ID),
		"%q is not a host of run %s", sanitize.Line(host), shortRunID(op.ID))
}

// hostStream is one stream of one host being printed.
type hostStream struct {
	name  string
	out   io.Writer
	from  int64
	clean sanitize.Stream
	done  bool
	wrote bool
	tail  byte
}

// showHostOutput prints the whole output of one host, ungrouped: stdout on
// stdout and stderr on stderr, or one stream with --stream. With follow it
// keeps asking for more while the command runs. Output is sanitised unless
// --raw is given and stdout is not a terminal.
func showHostOutput(command *cobra.Command, f *cli.Factory, client api.Client, id string, o runOptions) error {
	out := command.OutOrStdout()
	if o.stream != "" && o.stream != "stdout" && o.stream != "stderr" {
		return clierr.Usagef("--stream must be stdout or stderr")
	}
	if o.raw && ui.IsTerminal(out) {
		return clierr.Newf(clierr.ExitUsage, "pipe it to a file or a program, or leave --raw out",
			"--raw writes the host's bytes as they are, which a terminal would act on; stdout is a terminal")
	}

	op, err := client.GetOperation(command.Context(), id)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}
	if err := checkOperation(op, "GET /api/v1/managed/operations/"+id); err != nil {
		return err
	}
	target, err := selectTarget(op, o.host)
	if err != nil {
		return err
	}

	streams := []*hostStream{{name: "stdout", out: out}, {name: "stderr", out: command.ErrOrStderr()}}
	if o.stream != "" {
		streams = []*hostStream{{name: o.stream, out: out}}
	}

	failures := 0
	for {
		live := false
		for _, s := range streams {
			if s.done {
				continue
			}
			chunk, err := client.TargetOutput(command.Context(), op.ID, target.Spec.HostID, s.name, s.from)
			if err != nil {
				if command.Context().Err() != nil {
					return reportWatchEnd(f, op.ID, 0, &watchEnd{kind: errStoppedWatching})
				}
				if !transientPollError(err) || !o.follow || failures >= maxPollFailures {
					return fmt.Errorf("read output: %w", err)
				}
				failures++
				live = true

				continue
			}
			failures = 0
			if err := s.write(chunk.Text, o.raw); err != nil {
				return err
			}
			more := chunk.Next > s.from && chunk.Next < chunk.Bytes
			s.from = chunk.Next
			if !more && (!o.follow || !chunk.Live) {
				s.done = true
				if err := s.flush(o.raw); err != nil {
					return err
				}

				continue
			}
			live = live || more || chunk.Live
		}
		if !live {
			break
		}
		select {
		case <-command.Context().Done():
			return reportWatchEnd(f, op.ID, 0, &watchEnd{kind: errStoppedWatching})
		case <-time.After(pollInterval << min(failures, 3)):
		}
	}

	writeLine(f.Streams.ErrOut, hostStatusLine(target))

	return nil
}

func (s *hostStream) write(text string, raw bool) error {
	if text == "" {
		return nil
	}
	if !raw {
		text = s.clean.Clean(text)
	}
	if text == "" {
		return nil
	}
	s.wrote, s.tail = true, text[len(text)-1]
	_, err := io.WriteString(s.out, text)

	return err
}

// flush ends a stream: what the sanitiser held back, and a final newline.
func (s *hostStream) flush(raw bool) error {
	if !raw {
		if rest := s.clean.Flush(); rest != "" {
			s.wrote, s.tail = true, rest[len(rest)-1]
			if _, err := io.WriteString(s.out, rest); err != nil {
				return err
			}
		}
	}
	if s.wrote && s.tail != '\n' {
		_, err := io.WriteString(s.out, "\n")

		return err
	}

	return nil
}

// hostStatusLine is the last line of a host's output: how it ended.
func hostStatusLine(target api.OperationTarget) string {
	text := sanitize.Line(target.State)
	if target.Result != nil && target.Result.ExitCode != nil {
		text += fmt.Sprintf(", exit %d", *target.Result.ExitCode)
	}
	if target.Result != nil && target.Result.ErrorClass != "" {
		text += " (" + sanitize.Line(target.Result.ErrorClass) + ")"
	}

	return sanitize.Line(target.Spec.DisplayName) + ": " + text
}
