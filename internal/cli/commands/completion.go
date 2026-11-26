package commands

import (
	"slices"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
)

// Dynamic completion of names the server knows. Each function completes only
// the first argument (or flag value), asks the server with the saved
// credential, and offers nothing when that fails.

// completeChildren sets one completion function on the named subcommands.
func completeChildren(parent *cobra.Command, complete cobra.CompletionFunc, names ...string) {
	for _, child := range parent.Commands() {
		if slices.Contains(names, child.Name()) {
			child.ValidArgsFunction = complete
		}
	}
}

// completeHostNames completes the NAME argument of the hosts commands.
func completeHostNames(f *cli.Factory) cobra.CompletionFunc {
	return func(command *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		ctx, cancel, client := f.CompletionClient(command)
		defer cancel()
		var names []string
		if client != nil {
			if hosts, err := listAllHosts(ctx, client, api.HostQuery{}); err == nil {
				for _, host := range hosts {
					names = append(names, host.Metadata.Name)
				}
			}
		}

		return cli.CompletionNames(names, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

// completeAccessNames completes an access entry name (argument or --access).
func completeAccessNames(f *cli.Factory, firstArgOnly bool) cobra.CompletionFunc {
	return func(command *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if firstArgOnly && len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		ctx, cancel, client := f.CompletionClient(command)
		defer cancel()
		var names []string
		if client != nil {
			profiles, err := collect(func(cursor string) (api.Page[api.AccessProfile], error) {
				return client.ListAccessProfiles(ctx, cursor)
			})
			if err == nil {
				for _, profile := range profiles {
					names = append(names, profile.Name)
				}
			}
		}

		return cli.CompletionNames(names, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}
