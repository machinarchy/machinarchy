// Package commands implements the Machinarchy CLI commands and registers them
// with the root command. Commands reach the API only through cli.Factory.
package commands

import "github.com/machinarchy/machinarchy/internal/cli"

// DefaultGroups registers the built-in command packages. Applications may
// append further groups without changing the root command implementation.
func DefaultGroups() []cli.CommandGroup {
	return []cli.CommandGroup{
		{
			ID:    "core",
			Title: "Core Commands",
			Commands: []cli.CommandBuilder{
				NewGetCommand,
			},
		},
		{
			ID:    "configuration",
			Title: "Configuration Commands",
			Commands: []cli.CommandBuilder{
				NewConfigCommand,
				NewRuntimeConfigCommand,
				NewSecretCommand,
			},
		},
		{
			ID:    "other",
			Title: "Other Commands",
			Commands: []cli.CommandBuilder{
				NewVersionCommand,
			},
		},
	}
}
