package commands

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/group"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
)

const (
	// outputFetchers is how many hosts' output is read at once (spec 7.2 rule 10).
	outputFetchers = 8
	// outputFetchCap is the most of one stream that is read to group a host.
	outputFetchCap = 64 << 10
	// outputFetchSteps bounds the requests for one stream.
	outputFetchSteps = 64
)

// hostLabels names every target of a run. Two hosts can share a display name
// (different envs); the aggregator keys on the name, so the second one gets
// its host ID appended rather than being merged into the first.
func hostLabels(targets []api.OperationTarget) []string {
	seen := make(map[string]int, len(targets))
	for _, target := range targets {
		seen[target.Spec.DisplayName]++
	}
	labels := make([]string, len(targets))
	for i, target := range targets {
		name := target.Spec.DisplayName
		if name == "" {
			name = target.Spec.HostID
		}
		if seen[target.Spec.DisplayName] > 1 {
			name += " [" + target.Spec.HostID + "]"
		}
		labels[i] = name
	}

	return labels
}

// runResults turns the targets of a run into the aggregator's input. Output
// comes from the run itself; a finished detached target that has output but
// whose text the run did not carry is read from the output endpoint, eight at
// a time. A failed read leaves that host without output.
func runResults(ctx context.Context, client api.Client, op *api.Operation) []group.Result {
	labels := hostLabels(op.Status.Targets)
	results := make([]group.Result, len(op.Status.Targets))
	var fetch []int

	for i, target := range op.Status.Targets {
		result := group.Result{Host: labels[i], State: target.State}
		if target.Result != nil {
			result.ExitCode = target.Result.ExitCode
			result.ErrorClass = target.Result.ErrorClass
			result.Stdout, result.Stderr = target.Result.Stdout, target.Result.Stderr
			if result.Stdout == "" && result.Stderr == "" {
				result.Stdout = target.Result.Output
			}
		}
		if result.Stdout == "" && result.Stderr == "" && terminalTargetStates[target.State] && target.Progress != nil &&
			(target.Progress.StdoutBytes > 0 || target.Progress.StderrBytes > 0) {
			fetch = append(fetch, i)
		}
		results[i] = result
	}

	if len(fetch) > 0 {
		var wg sync.WaitGroup
		slots := make(chan struct{}, outputFetchers)
		for _, i := range fetch {
			slots <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				hostID := op.Status.Targets[i].Spec.HostID
				results[i].Stdout = readStream(ctx, client, op.ID, hostID, "stdout")
				results[i].Stderr = readStream(ctx, client, op.ID, hostID, "stderr")
			}()
		}
		wg.Wait()
	}

	return results
}

// readStream reads one stream of one host up to outputFetchCap bytes. It
// returns what it got; a failed read ends it.
func readStream(ctx context.Context, client api.Client, runID, hostID, stream string) string {
	var text strings.Builder
	var from int64
	for range outputFetchSteps {
		chunk, err := client.TargetOutput(ctx, runID, hostID, stream, from)
		if err != nil || chunk == nil {
			break
		}
		text.WriteString(chunk.Text)
		if chunk.Next <= from || chunk.Next >= chunk.Bytes || text.Len() >= outputFetchCap {
			break
		}
		from = chunk.Next
	}

	return text.String()
}

// summarise groups the results of a run.
func summarise(results []group.Result) group.Summary {
	aggregator := group.New(group.Options{})
	for _, result := range results {
		aggregator.Add(result)
	}

	return aggregator.Summary()
}

// resultOptions are the flags that shape a printed result.
type resultOptions struct {
	noGroups    bool
	groups      string
	failOnDrift bool
	quiet       bool
}

// maxGroups reads --groups: a number, or "all".
func (o resultOptions) maxGroups() (int, error) {
	text := strings.TrimSpace(o.groups)
	if text == "" {
		return 0, nil
	}
	if strings.EqualFold(text, "all") {
		return -1, nil
	}
	var n int
	if _, err := fmt.Sscanf(text, "%d", &n); err != nil || n < 1 || fmt.Sprint(n) != text {
		return 0, fmt.Errorf("--groups must be a number from 1 or all, not %q", sanitize.Line(text))
	}

	return n, nil
}

// printResult prints the grouped result of a run on stdout (or as -o json/yaml)
// and returns the exit code it earns: 0 when every host succeeded, 8 when some
// did not, 9 when none did. --fail-on-drift turns an otherwise clean run that
// printed different output into 8. A run that is still going exits 0: it has
// no result yet.
func printResult(command *cobra.Command, f *cli.Factory, client api.Client, op *api.Operation, o resultOptions) (int, error) {
	out := command.OutOrStdout()
	maxGroups, err := o.maxGroups()
	if err != nil {
		return 0, err
	}
	results := runResults(command.Context(), client, op)
	summary := summarise(results)

	code := group.ExitFor(summary)
	if o.failOnDrift && code == 0 && (summary.Drift.Kind == group.DriftDominant || summary.Drift.Kind == group.DriftMixed) {
		code = 8
	}
	if !terminalOperationStates[op.Status.State] {
		code = 0
	}

	printer, err := f.Printer(out, output.FormatTable)
	if err != nil {
		return 0, err
	}
	switch {
	case printer.Format() != output.FormatTable:
		doc := summary.Document()
		doc.Exit = code

		return code, printer.Document(doc)
	case o.quiet:
		return code, nil
	}

	render := group.RenderOptions{
		Style: f.StyleFor(out), Width: f.Width(out), MaxGroups: maxGroups,
		RunID: shortRunID(op.ID), TimeoutLabel: timeoutLabel(op.Spec),
	}
	if o.noGroups {
		return code, group.RenderPerHost(out, results, render)
	}

	return code, group.Render(out, summary, render)
}

// writeLine writes text and a newline. Status text ignores a closed pipe.
func writeLine(w io.Writer, text string) {
	_, _ = fmt.Fprintln(w, text)
}
