package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/msg"
	"github.com/machinarchy/machinarchy/internal/cli/ui"
)

// maxPollFailures is how many polls in a row may fail before the CLI stops
// watching. The run itself is not affected: it lives in the controller.
const maxPollFailures = 6

// How watching ends when the run has not finished. They carry the exit code
// and what to say; reportWatchEnd prints it.
var (
	errStoppedWatching = errors.New("stopped watching")
	errWaitTimeout     = errors.New("wait timeout elapsed")
	errLostContact     = errors.New("lost contact")
)

type watchEnd struct {
	kind  error
	cause error // the last poll error, for errLostContact
}

func (w *watchEnd) Error() string { return w.kind.Error() }
func (w *watchEnd) Unwrap() error { return w.kind }

// watchOptions say how a run is watched.
type watchOptions struct {
	// waitTimeout stops watching after this long; 0 means no limit.
	waitTimeout time.Duration
	// quiet shows no progress.
	quiet bool
}

// progressCounts reads the number of hosts in each state off a run.
func progressCounts(op *api.Operation) ui.Counts {
	counts := ui.Counts{Total: len(op.Status.Targets)}
	for _, target := range op.Status.Targets {
		switch {
		case !terminalTargetStates[target.State]:
			counts.Running++
		case target.State == "Succeeded":
			counts.Done++
			counts.OK++
		default:
			counts.Done++
			counts.Failed++
		}
	}

	return counts
}

// watchRun polls a run until it reaches a final state and draws its progress
// on stderr. It stops early, with a *watchEnd, when the person interrupts it
// (the run keeps going), when waitTimeout elapses, or when the server cannot
// be reached several times in a row. A poll that fails once or twice is
// retried with a growing pause and nothing is said about it.
func watchRun(command *cobra.Command, f *cli.Factory, client api.Client, id string, o watchOptions) (*api.Operation, error) {
	ctx := command.Context()
	sink := f.Streams.ErrOut
	if o.quiet {
		sink = io.Discard
	}
	progress := ui.NewProgress(sink, f.Style(), ui.IsTerminal(f.Streams.ErrOut), f.Now)

	var timeout <-chan time.Time
	if o.waitTimeout > 0 {
		timer := time.NewTimer(o.waitTimeout)
		defer timer.Stop()
		timeout = timer.C
	}

	var last ui.Counts
	failures := 0
	for {
		op, err := client.GetOperation(ctx, id)
		switch {
		case err == nil:
			if err = checkOperation(op, "GET /api/v1/managed/operations/"+id); err != nil {
				return nil, err
			}
			failures = 0
			last = progressCounts(op)
			if terminalOperationStates[op.Status.State] {
				progress.Finish(last)

				return op, nil
			}
			progress.Update(last)
		case ctx.Err() != nil:
			progress.Finish(last)

			return nil, &watchEnd{kind: errStoppedWatching}
		case !transientPollError(err):
			progress.Finish(last)

			return nil, fmt.Errorf("get run: %w", err)
		default:
			failures++
			if failures >= maxPollFailures {
				progress.Finish(last)

				return nil, &watchEnd{kind: errLostContact, cause: err}
			}
		}

		select {
		case <-ctx.Done():
			progress.Finish(last)

			return nil, &watchEnd{kind: errStoppedWatching}
		case <-timeout:
			progress.Finish(last)

			return nil, &watchEnd{kind: errWaitTimeout}
		case <-time.After(pollInterval << min(failures, 3)):
		}
	}
}

// reportWatchEnd prints what the person needs when watching ended early and
// returns the error that carries the exit code. Errors of any other kind are
// returned as they are.
func reportWatchEnd(f *cli.Factory, id string, waitTimeout time.Duration, err error) error {
	var end *watchEnd
	if !errors.As(err, &end) {
		return err
	}
	errOut := f.Streams.ErrOut
	vars := msg.Vars{"id": shortRunID(id), "wait": waitTimeout.String()}

	var (
		message msg.Message
		code    int
	)
	switch {
	case errors.Is(end.kind, errStoppedWatching):
		message, code = msg.Get("run.stopped"), clierr.ExitInterrupted
	case errors.Is(end.kind, errWaitTimeout):
		message, code = msg.Get("run.wait_timeout"), clierr.ExitWaitTimeout
	default:
		message, code = msg.Get("run.lost_contact"), clierr.ExitUnavailable
		if end.cause != nil {
			message = message.WithWhy(describeFailure(end.cause))
		}
	}
	_, _ = fmt.Fprint(errOut, "\n"+ui.Render(message.With(vars), f.Style()))

	return clierr.Reported(code)
}

// describeFailure is one line about why a request failed, with nothing in it a
// terminal could act on.
func describeFailure(err error) string {
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("the server answered HTTP %d", apiErr.Status)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "the server did not answer in time"
	}

	return "the server could not be reached"
}
