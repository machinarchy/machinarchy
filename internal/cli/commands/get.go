package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/spf13/cobra"
)

// maxListPages bounds how many pages a command follows (500 items each).
const maxListPages = 200

// NewGetCommand registers the one remaining read command: agent-tokens (the source of the agent ID that
// `hosts revoke-agent` needs until the server reports it per host, C-09).
func NewGetCommand(factory *cli.Factory) *cobra.Command {
	command := &cobra.Command{
		Use:   "get",
		Short: "Display one or more resources",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(newGetAgentTokensCommand(factory))
	return command
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
