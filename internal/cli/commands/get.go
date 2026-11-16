package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/spf13/cobra"
)

// NewGetCommand registers resource read commands.
func NewGetCommand(factory *cli.Factory) *cobra.Command {
	command := &cobra.Command{
		Use:   "get",
		Short: "Display one or more resources",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(newGetHostsCommand(factory))
	return command
}

func newGetHostsCommand(factory *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "hosts [name-or-id]",
		Aliases: []string{"host"},
		Short:   "List hosts or display a named host",
		Args:    cobra.MaximumNArgs(1),
		Example: "  machinarchy get hosts\n  machinarchy get hosts web-01 -o yaml\n  machinarchy get hosts --env production",
		RunE: func(command *cobra.Command, args []string) error {
			printer, err := factory.Printer(command.OutOrStdout(), output.FormatTable)
			if err != nil {
				return err
			}

			client, err := factory.NewAPIClient(command.Context())
			if err != nil {
				return err
			}
			factory.Logger.Debug("listing hosts",
				"context", factory.Config().ContextName(),
				"environment", factory.Config().Environment())
			hosts, err := client.ListHosts(command.Context())
			if err != nil {
				return fmt.Errorf("list hosts: %w", err)
			}
			hosts, err = filterEnvironment(command, client, hosts, factory.Config().Environment())
			if err != nil {
				return err
			}

			if len(args) == 1 {
				host, err := findHost(hosts, args[0])
				if err != nil {
					return err
				}
				return printer.Host(host)
			}
			return printer.Hosts(hosts)
		},
	}
}

func filterEnvironment(
	command *cobra.Command,
	client api.Client,
	hosts []api.Host,
	environmentName string,
) ([]api.Host, error) {
	environmentName = strings.TrimSpace(environmentName)
	if environmentName == "" {
		return hosts, nil
	}
	environments, err := client.ListEnvironments(command.Context())
	if err != nil {
		return nil, fmt.Errorf("resolve environment %q: %w", environmentName, err)
	}

	var (
		environmentID int
		matches       int
	)
	if id, parseErr := strconv.Atoi(environmentName); parseErr == nil {
		for _, environment := range environments {
			if environment.ID == id {
				environmentID = environment.ID
				matches++
			}
		}
	} else {
		for _, environment := range environments {
			if strings.EqualFold(environment.Name, environmentName) {
				environmentID = environment.ID
				matches++
			}
		}
	}
	if matches == 0 {
		return nil, clierr.Newf(clierr.ExitNotFound, "", "environment %q was not found", environmentName)
	}
	if matches > 1 {
		return nil, clierr.Usagef("environment %q is ambiguous; use its numeric ID with --env", environmentName)
	}

	filtered := make([]api.Host, 0, len(hosts))
	for _, host := range hosts {
		if host.EnvironmentID == environmentID {
			filtered = append(filtered, host)
		}
	}
	return filtered, nil
}

func findHost(hosts []api.Host, nameOrID string) (api.Host, error) {
	var (
		match api.Host
		count int
	)
	for _, host := range hosts {
		if host.ID == nameOrID || host.Name == nameOrID {
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
