// Package cli builds the Machinarchy command tree and wires shared dependencies.
package cli

import (
	"os"

	"github.com/spf13/cobra"
)

// CommandBuilder creates a command with the shared CLI dependencies.
type CommandBuilder func(*Factory) *cobra.Command

// CommandGroup lets feature packages register related top-level commands
// without editing the root command's implementation.
type CommandGroup struct {
	ID       string
	Title    string
	Commands []CommandBuilder
}

// Options injects the command streams, API client builder, and command groups.
type Options struct {
	Streams       IOStreams
	ClientBuilder APIClientBuilder
	Groups        []CommandGroup
}

// NewRootCommand creates a Cobra root and binds shared configuration flags.
func NewRootCommand(options Options) *cobra.Command {
	streams := normalizedStreams(options.Streams)
	factory := &Factory{
		Streams:       streams,
		ClientBuilder: options.ClientBuilder,
	}
	var (
		configPath  string
		context     string
		environment string
		output      string
		verbose     bool
	)

	root := &cobra.Command{
		Use:           "machinarchy",
		Short:         "Manage Machinarchy environments and hosts",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetIn(streams.In)
	root.SetOut(streams.Out)
	root.SetErr(streams.ErrOut)

	flags := root.PersistentFlags()
	flags.StringVar(&configPath, "config", "", "CLI config file (default: user config directory)")
	flags.StringVar(&context, "context", "", "Context to use for this command")
	flags.StringVarP(&environment, "env", "n", "", "Machinarchy environment name or ID")
	flags.StringVarP(&output, "output", "o", "table", "Output format: table, json, or yaml")
	flags.BoolVarP(&verbose, "verbose", "v", false, "Enable debug logging")

	root.PersistentPreRunE = func(command *cobra.Command, _ []string) error {
		if err := factory.LoadConfig(configPath, command.Root().PersistentFlags()); err != nil {
			return err
		}
		return nil
	}

	groups := options.Groups
	for _, group := range groups {
		if group.ID != "" && group.Title != "" {
			root.AddGroup(&cobra.Group{ID: group.ID, Title: group.Title})
		}
		for _, build := range group.Commands {
			if build == nil {
				continue
			}
			child := build(factory)
			if child == nil {
				continue
			}
			child.GroupID = group.ID
			root.AddCommand(child)
		}
	}
	return root
}

func normalizedStreams(streams IOStreams) IOStreams {
	if streams.In == nil {
		streams.In = os.Stdin
	}
	if streams.Out == nil {
		streams.Out = os.Stdout
	}
	if streams.ErrOut == nil {
		streams.ErrOut = os.Stderr
	}
	return streams
}
