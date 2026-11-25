package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

// Run executes the CLI and returns the process exit code. It owns error
// presentation so that every failure looks and exits the same way:
//
//	Error: <what happened>
//	Hint: <how to fix it>        (when there is advice)
//
// Passing nil args uses os.Args[1:]. The caller decides how to exit, which
// keeps the whole path testable.
func Run(ctx context.Context, options Options, args []string) int {
	if ctx == nil {
		ctx = context.Background()
	}
	options.Streams = normalizedStreams(options.Streams)

	root, factory := newRoot(options)
	if args != nil {
		root.SetArgs(args)
	}

	command, err := root.ExecuteContextC(ctx)
	path := ""
	if command != nil {
		path = command.CommandPath()
	}

	return reportError(options.Streams.ErrOut, err, factory.started, path)
}

// reportError prints err to w and returns the exit code it maps to.
// started reports whether the selected command began to run; an unclassified
// error before that point came from Cobra rejecting the command line. path is
// the command that failed ("machinarchy logs operation"), so a usage error
// points at that command's help rather than the list of all commands.
func reportError(w io.Writer, err error, started bool, path string) int {
	if err == nil {
		return clierr.ExitOK
	}

	code, classified := clierr.Classify(err)
	if !classified && !started {
		code = clierr.ExitUsage
	}

	if code == clierr.ExitInterrupted {
		_, _ = fmt.Fprintln(w, "Interrupted.")
		return code
	}

	_, _ = fmt.Fprintf(w, "Error: %s\n", err.Error())

	hint := clierr.Hint(err)
	if hint == "" && code == clierr.ExitUsage {
		hint = "Run 'machinarchy --help' to see available commands."
		if path != "" && path != "machinarchy" {
			hint = fmt.Sprintf("Run '%s --help' for its usage.", path)
		}
	}
	if hint != "" {
		_, _ = fmt.Fprintf(w, "Hint: %s\n", hint)
	}

	return code
}
