package commands

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/spf13/cobra"
)

// maxListPages bounds how many pages a command follows (500 items each).
const maxListPages = 200

// NewGetCommand registers resource read commands.
func NewGetCommand(factory *cli.Factory) *cobra.Command {
	command := &cobra.Command{
		Use:   "get",
		Short: "Display one or more resources",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(newGetHostsCommand(factory), newGetProfilesCommand(factory), newGetOperationsCommand(factory))
	return command
}

func newGetHostsCommand(factory *cli.Factory) *cobra.Command {
	var selector string
	command := &cobra.Command{
		Use:     "hosts [name-or-id]",
		Aliases: []string{"host"},
		Short:   "List managed hosts or display one",
		Args:    cobra.MaximumNArgs(1),
		Example: "  machinarchy get hosts\n  machinarchy get hosts -l tier=web\n  machinarchy get hosts web-01 -o yaml\n  machinarchy get hosts --env production",
		RunE: func(command *cobra.Command, args []string) error {
			printer, err := factory.Printer(command.OutOrStdout(), output.FormatTable)
			if err != nil {
				return err
			}
			client, err := factory.NewAPIClient(command.Context())
			if err != nil {
				return err
			}
			query := api.HostQuery{Selector: selector}
			if query.EnvironmentID, err = resolveEnvironment(command.Context(), client, factory.Config().Environment()); err != nil {
				return err
			}
			hosts, err := listAllHosts(command.Context(), client, query)
			if err != nil {
				return fmt.Errorf("list hosts: %w", err)
			}
			if len(args) == 1 {
				host, err := pickHost(hosts, args[0])
				if err != nil {
					return err
				}
				return printer.Host(host)
			}
			return printer.Hosts(hosts)
		},
	}
	command.Flags().StringVarP(&selector, "selector", "l", "", "Filter by selector, for example tier=web,region in (eu,us)")
	return command
}

func newGetProfilesCommand(factory *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "profiles",
		Aliases: []string{"profile", "access-profiles"},
		Short:   "List Access Profiles (credential references, never secret values)",
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			printer, err := factory.Printer(command.OutOrStdout(), output.FormatTable)
			if err != nil {
				return err
			}
			client, err := factory.NewAPIClient(command.Context())
			if err != nil {
				return err
			}
			profiles, err := collect(func(cursor string) (api.Page[api.AccessProfile], error) {
				return client.ListAccessProfiles(command.Context(), cursor)
			})
			if err != nil {
				return fmt.Errorf("list access profiles: %w", err)
			}
			if printer.Format() != output.FormatTable {
				return printer.Document(profiles)
			}
			rows := make([][]string, 0, len(profiles))
			for _, profile := range profiles {
				rows = append(rows, []string{profile.Name, profile.ID, strconv.FormatUint(profile.CurrentRevision, 10), profile.Description})
			}
			return printer.Table([]string{"NAME", "ID", "REVISION", "DESCRIPTION"}, rows)
		},
	}
}

func newGetOperationsCommand(factory *cli.Factory) *cobra.Command {
	var state string
	var limit int
	command := &cobra.Command{
		Use:     "operations [id]",
		Aliases: []string{"operation", "ops", "runs"},
		Short:   "List recent operations or display one with per-host results",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			printer, err := factory.Printer(command.OutOrStdout(), output.FormatTable)
			if err != nil {
				return err
			}
			client, err := factory.NewAPIClient(command.Context())
			if err != nil {
				return err
			}
			if len(args) == 1 {
				op, err := client.GetOperation(command.Context(), args[0])
				if err != nil {
					return fmt.Errorf("get operation: %w", err)
				}
				return printOperation(printer, op)
			}
			page, err := client.ListOperations(command.Context(), api.OperationQuery{State: state, Limit: limit})
			if err != nil {
				return fmt.Errorf("list operations: %w", err)
			}
			if printer.Format() != output.FormatTable {
				return printer.Document(page.Items)
			}
			rows := make([][]string, 0, len(page.Items))
			for _, op := range page.Items {
				rows = append(rows, []string{op.ID, op.State, targetCounts(op.Targets), op.CreatedAt.Local().Format(time.DateTime)})
			}
			return printer.Table([]string{"ID", "STATE", "TARGETS", "CREATED"}, rows)
		},
	}
	command.Flags().StringVar(&state, "state", "", "Only operations in this state, for example Running")
	command.Flags().IntVar(&limit, "limit", 20, "Number of operations to show (newest first)")
	return command
}

func printOperation(printer *output.Printer, op *api.Operation) error {
	if printer.Format() != output.FormatTable {
		return printer.Document(op)
	}
	rows := make([][]string, 0, len(op.Status.Targets))
	for _, target := range op.Status.Targets {
		exit, detail := "-", ""
		if target.Result != nil {
			if target.Result.ExitCode != nil {
				exit = strconv.Itoa(*target.Result.ExitCode)
			}
			detail = target.Result.ErrorClass
			if detail == "" {
				detail = firstLine(target.Result.Output)
			}
		}
		rows = append(rows, []string{target.Spec.DisplayName, target.State, exit, detail})
	}
	if err := printer.Table([]string{"HOST", "STATE", "EXIT", "OUTPUT"}, rows); err != nil {
		return err
	}
	return printer.Table([]string{"OPERATION", "STATE"}, [][]string{{op.ID, op.Status.State}})
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if len(line) > 80 {
		line = line[:77] + "..."
	}
	return line
}

func targetCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, ",")
}

// collect follows nextCursor until the last page.
func collect[T any](fetch func(cursor string) (api.Page[T], error)) ([]T, error) {
	items := make([]T, 0)
	cursor := ""
	for range maxListPages {
		page, err := fetch(cursor)
		if err != nil {
			return nil, err
		}
		items = append(items, page.Items...)
		if page.NextCursor == "" || page.NextCursor == cursor {
			return items, nil
		}
		cursor = page.NextCursor
	}
	return nil, clierr.Usagef("the result has more than %d pages; narrow it with a selector", maxListPages)
}

func listAllHosts(ctx context.Context, client api.Client, query api.HostQuery) ([]api.Host, error) {
	return collect(func(cursor string) (api.Page[api.Host], error) {
		query.Cursor = cursor
		return client.ListHosts(ctx, query)
	})
}

// resolveEnvironment maps the --env name or numeric ID to an environment ID;
// an empty value means no filter.
func resolveEnvironment(ctx context.Context, client api.Client, environmentName string) (int, error) {
	environmentName = strings.TrimSpace(environmentName)
	if environmentName == "" {
		return 0, nil
	}
	environments, err := client.ListEnvironments(ctx)
	if err != nil {
		return 0, fmt.Errorf("resolve environment %q: %w", environmentName, err)
	}
	var environmentID, matches int
	id, parseErr := strconv.Atoi(environmentName)
	for _, environment := range environments {
		if (parseErr == nil && environment.ID == id) || (parseErr != nil && strings.EqualFold(environment.Name, environmentName)) {
			environmentID = environment.ID
			matches++
		}
	}
	if matches == 0 {
		return 0, clierr.Newf(clierr.ExitNotFound, "", "environment %q was not found", environmentName)
	}
	if matches > 1 {
		return 0, clierr.Usagef("environment %q is ambiguous; use its numeric ID with --env", environmentName)
	}
	return environmentID, nil
}

// pickHost finds one host by ID or exact name.
func pickHost(hosts []api.Host, nameOrID string) (api.Host, error) {
	var match api.Host
	count := 0
	for _, host := range hosts {
		if host.Metadata.ID == nameOrID || host.Metadata.Name == nameOrID {
			match = host
			count++
		}
	}
	switch count {
	case 0:
		return api.Host{}, clierr.Newf(clierr.ExitNotFound, "", "host %q was not found", nameOrID)
	case 1:
		return match, nil
	default:
		return api.Host{}, clierr.Usagef("host name %q is ambiguous; use its ID or select an environment with --env", nameOrID)
	}
}

// findHost resolves a name or ID to one host, honouring --env.
func findHost(ctx context.Context, factory *cli.Factory, client api.Client, nameOrID string) (api.Host, error) {
	environmentID, err := resolveEnvironment(ctx, client, factory.Config().Environment())
	if err != nil {
		return api.Host{}, err
	}
	hosts, err := listAllHosts(ctx, client, api.HostQuery{EnvironmentID: environmentID})
	if err != nil {
		return api.Host{}, fmt.Errorf("list hosts: %w", err)
	}
	return pickHost(hosts, nameOrID)
}
