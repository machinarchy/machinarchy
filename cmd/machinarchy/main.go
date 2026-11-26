// Package main is the entry point for the machinarchy application.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/commands"
)

func main() {
	os.Exit(run())
}

// run executes the CLI and returns its exit code. Deferred cleanup runs
// before main calls os.Exit.
func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// After the first interrupt, restore default signal handling so a second
	// Ctrl-C terminates a command that is slow to unwind.
	go func() {
		<-ctx.Done()
		stop()
	}()

	return cli.Run(ctx, cli.Options{
		Streams: cli.IOStreams{
			In:     os.Stdin,
			Out:    os.Stdout,
			ErrOut: os.Stderr,
		},
		Groups: commands.DefaultGroups(),
		Home:   commands.Home,
	}, nil)
}
