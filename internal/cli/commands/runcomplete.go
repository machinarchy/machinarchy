package commands

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
)

// completionRuns is how many recent runs are offered as IDs.
const completionRuns = 20

// registerRunCompletions wires tab completion for `run`: where to run, run
// IDs, the hosts of a run and the known capabilities. Like the other
// completions it asks the server with the saved credential, never prompts and
// offers nothing when the server cannot be asked.
func registerRunCompletions(command *cobra.Command, f *cli.Factory) {
	command.ValidArgsFunction = completeRunArgs(f)
	for name, complete := range map[string]cobra.CompletionFunc{
		"on":     completeTargets(f, true),
		"except": completeTargets(f, false),
		"host":   completeRunHost(f),
		"do":     completeCapabilities,
	} {
		if err := command.RegisterFlagCompletionFunc(name, complete); err != nil {
			panic(err.Error()) // the flag is defined by the caller
		}
	}
	if err := command.RegisterFlagCompletionFunc("stream", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"stdout", "stderr"}, cobra.ShellCompDirectiveNoFileComp
	}); err != nil {
		panic(err.Error())
	}
}

// completeCapabilities offers the capabilities known today, marked as such.
func completeCapabilities(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	var out []string
	for _, name := range knownCapabilities {
		if strings.HasPrefix(name, toComplete) {
			out = append(out, name+"\tknown; the server decides")
		}
	}

	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeTargets completes the last entry of a comma-separated --on or
// --except: host names, and for --on also all and key=value tags.
func completeTargets(f *cli.Factory, tags bool) cobra.CompletionFunc {
	return func(command *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		prefix, part := "", toComplete
		if i := strings.LastIndex(toComplete, ","); i >= 0 {
			prefix, part = toComplete[:i+1], toComplete[i+1:]
		}
		ctx, cancel, client := f.CompletionClient(command)
		defer cancel()
		if client == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		hosts, err := listAllHosts(ctx, client, api.HostQuery{})
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		var candidates []string
		selectorSoFar := strings.Contains(prefix, "=")
		if !selectorSoFar {
			for _, host := range hosts {
				candidates = append(candidates, host.Metadata.Name)
			}
			if tags && prefix == "" {
				candidates = append(candidates, target0All)
			}
		}
		if tags && (prefix == "" || selectorSoFar) {
			seen := map[string]bool{}
			for _, host := range hosts {
				for key, value := range host.Metadata.Labels {
					if pair := key + "=" + value; !seen[pair] {
						seen[pair] = true
						candidates = append(candidates, pair)
					}
				}
			}
		}
		sort.Strings(candidates)

		var out []string
		for _, name := range cli.CompletionNames(candidates, part) {
			out = append(out, prefix+name)
		}

		return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	}
}

// target0All is the --on word for every host.
const target0All = "all"

// completeRunArgs offers the 20 most recent run IDs, with the result as the
// description.
func completeRunArgs(f *cli.Factory) cobra.CompletionFunc {
	return func(command *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 || command.Flags().Changed("on") {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		ctx, cancel, client := f.CompletionClient(command)
		defer cancel()
		if client == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		page, err := client.ListOperations(ctx, api.OperationQuery{Limit: completionRuns})
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		var out []string
		for _, item := range page.Items {
			id := shortRunID(item.ID)
			if strings.HasPrefix(id, strings.ToLower(toComplete)) {
				out = append(out, fmt.Sprintf("%s\t%s", id, item.State))
			}
		}

		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeRunHost completes --host: the hosts of the run named first, or any
// host when the command lists runs.
func completeRunHost(f *cli.Factory) cobra.CompletionFunc {
	return func(command *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		ctx, cancel, client := f.CompletionClient(command)
		defer cancel()
		if client == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		var names []string
		if len(args) > 0 {
			id, err := resolveRunID(ctx, client, args[0])
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			op, err := client.GetOperation(ctx, id)
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			for _, target := range op.Status.Targets {
				names = append(names, target.Spec.DisplayName)
			}
		} else if hosts, err := listAllHosts(ctx, client, api.HostQuery{}); err == nil {
			for _, host := range hosts {
				names = append(names, host.Metadata.Name)
			}
		}
		slices.Sort(names)

		return cli.CompletionNames(names, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}
