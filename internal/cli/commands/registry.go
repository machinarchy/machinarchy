// Package commands implements the Machinarchy CLI commands and registers them
// with the root command. Commands reach the API only through cli.Factory.
package commands

import (
	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
)

// DefaultGroups registers the built-in command packages. Applications may
// append further groups without changing the root command implementation.
func DefaultGroups() []cli.CommandGroup {
	return []cli.CommandGroup{
		{
			ID:    "start",
			Title: "Get started",
			Commands: []cli.CommandBuilder{
				NewLoginCommand,
				owner(NewHostsCommand, "add", "move", "trust", "enroll", "revoke-agent"),
				NewRunCommand,
			},
		},
		{
			ID:    "everyday",
			Title: "Everyday",
			Commands: []cli.CommandBuilder{
				owner(NewAccessCommand, "add", "remove"),
				NewEnvsCommand,
				NewTokensCommand,
			},
		},
		{
			ID:    "account",
			Title: "Account",
			Commands: []cli.CommandBuilder{
				NewWhoamiCommand,
				NewSwitchCommand,
				NewLogoutCommand,
			},
		},
		{
			ID:    "advanced",
			Title: "Advanced",
			Commands: []cli.CommandBuilder{
				owner(NewGetCommand, "agent-tokens"),
				NewConfigCommand,
				owner(NewRuntimeConfigCommand),
				owner(NewSecretCommand),
				NewVersionCommand,
			},
		},
	}
}

// owner marks a command, or only the named subcommands of a group, as acting
// as the workspace owner. Such a command runs with the saved login session
// and never with an API token (cli.AnnotationOwner).
func owner(build cli.CommandBuilder, subcommands ...string) cli.CommandBuilder {
	return func(factory *cli.Factory) *cobra.Command {
		command := build(factory)
		if command == nil {
			return nil
		}
		if len(subcommands) == 0 {
			markOwner(command)

			return command
		}

		for _, name := range subcommands {
			for _, child := range command.Commands() {
				if child.Name() == name {
					markOwner(child)
				}
			}
		}

		return command
	}
}

// markOwner marks a command and everything below it as acting as the owner.
func markOwner(command *cobra.Command) {
	if command.Annotations == nil {
		command.Annotations = make(map[string]string)
	}
	command.Annotations[cli.AnnotationOwner] = "true"
	for _, child := range command.Commands() {
		markOwner(child)
	}
}
