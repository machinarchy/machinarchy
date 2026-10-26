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
			},
		},
	}
}
