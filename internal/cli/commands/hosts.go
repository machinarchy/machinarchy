package commands

import (
	"fmt"
	"maps"
	"strings"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/spf13/cobra"
)

// NewCreateCommand registers resource creation commands.
func NewCreateCommand(factory *cli.Factory) *cobra.Command {
	command := &cobra.Command{Use: "create", Short: "Create a resource", Args: cobra.NoArgs}
	command.AddCommand(newCreateHostCommand(factory))
	return command
}

// NewDeleteCommand registers resource deletion commands.
func NewDeleteCommand(factory *cli.Factory) *cobra.Command {
	command := &cobra.Command{Use: "delete", Short: "Delete a resource", Args: cobra.NoArgs}
	command.AddCommand(newDeleteHostCommand(factory))
	return command
}

// NewLabelCommand registers label editing commands.
func NewLabelCommand(factory *cli.Factory) *cobra.Command {
	command := &cobra.Command{Use: "label", Short: "Add, change or remove resource labels", Args: cobra.NoArgs}
	command.AddCommand(newLabelHostCommand(factory))
	return command
}

func newCreateHostCommand(factory *cli.Factory) *cobra.Command {
	var endpoint, profile string
	var labels, trust []string
	command := &cobra.Command{
		Use:     "host NAME --endpoint HOST:PORT --profile NAME-OR-ID",
		Short:   "Register a managed host bound to an Access Profile",
		Args:    cobra.ExactArgs(1),
		Example: "  machinarchy create host web-01 --endpoint 10.0.0.5:22 --profile deploy --label tier=web",
		RunE: func(command *cobra.Command, args []string) error {
			labelMap, removals, err := parseLabels(labels)
			if err != nil {
				return err
			}
			if len(removals) > 0 {
				return clierr.Usagef("--label takes KEY=VALUE; removing labels is only possible with 'label host'")
			}
			if strings.TrimSpace(endpoint) == "" || strings.TrimSpace(profile) == "" {
				return clierr.Usagef("--endpoint and --profile are required")
			}
			printer, err := factory.Printer(command.OutOrStdout(), output.FormatTable)
			if err != nil {
				return err
			}
			client, err := factory.NewAPIClient(command.Context())
			if err != nil {
				return err
			}
			profileID, err := resolveProfile(command, client, profile)
			if err != nil {
				return err
			}
			host, err := client.CreateHost(command.Context(), api.HostInput{Name: args[0], Labels: labelMap, Spec: api.HostSpec{
				Presence: "Managed", AccessProfileID: profileID, AccessTarget: api.HostAccessTarget{Endpoint: endpoint, Trust: trust}}})
			if err != nil {
				return fmt.Errorf("create host: %w", err)
			}
			return printer.Host(*host)
		},
	}
	command.Flags().StringVar(&endpoint, "endpoint", "", "SSH endpoint as HOST:PORT")
	command.Flags().StringVar(&profile, "profile", "", "Access Profile name or ID")
	command.Flags().StringArrayVar(&labels, "label", nil, "Label as KEY=VALUE (repeatable)")
	command.Flags().StringArrayVar(&trust, "trust", nil, "Trusted host key fingerprint, for example SHA256:... (repeatable)")
	return command
}

func resolveProfile(command *cobra.Command, client api.Client, nameOrID string) (string, error) {
	profiles, err := collect(func(cursor string) (api.Page[api.AccessProfile], error) {
		return client.ListAccessProfiles(command.Context(), cursor)
	})
	if err != nil {
		return "", fmt.Errorf("list access profiles: %w", err)
	}
	for _, profile := range profiles {
		if profile.ID == nameOrID || strings.EqualFold(profile.Name, nameOrID) {
			return profile.ID, nil
		}
	}
	return "", clierr.Newf(clierr.ExitNotFound, "", "access profile %q was not found", nameOrID)
}

func newDeleteHostCommand(factory *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "host NAME-OR-ID",
		Short: "Delete a managed host (refused while an operation targets it)",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			client, err := factory.NewAPIClient(command.Context())
			if err != nil {
				return err
			}
			host, err := findHost(command.Context(), factory, client, args[0])
			if err != nil {
				return err
			}
			if err := factory.RequireConfirmation(fmt.Sprintf("Delete host %q (%s)?", host.Metadata.Name, host.Metadata.ID)); err != nil {
				return err
			}
			if err := client.DeleteHost(command.Context(), host.Metadata.ID, host.Metadata.Revision); err != nil {
				return fmt.Errorf("delete host: %w", err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "Host %q deleted.\n", host.Metadata.Name)
			return err
		},
	}
}

func newLabelHostCommand(factory *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "host NAME-OR-ID KEY=VALUE... KEY-...",
		Short:   "Set labels with KEY=VALUE and remove them with KEY-",
		Args:    cobra.MinimumNArgs(2),
		Example: "  machinarchy label host web-01 tier=web canary-",
		RunE: func(command *cobra.Command, args []string) error {
			set, remove, err := parseLabels(args[1:])
			if err != nil {
				return err
			}
			printer, err := factory.Printer(command.OutOrStdout(), output.FormatTable)
			if err != nil {
				return err
			}
			client, err := factory.NewAPIClient(command.Context())
			if err != nil {
				return err
			}
			host, err := findHost(command.Context(), factory, client, args[0])
			if err != nil {
				return err
			}
			labels := maps.Clone(host.Metadata.Labels)
			if labels == nil {
				labels = map[string]string{}
			}
			maps.Copy(labels, set)
			for _, key := range remove {
				delete(labels, key)
			}
			updated, err := client.SetHostLabels(command.Context(), host.Metadata.ID, host.Metadata.Revision, labels)
			if err != nil {
				return fmt.Errorf("label host: %w", err)
			}
			return printer.Host(*updated)
		},
	}
}

// parseLabels splits KEY=VALUE assignments from KEY- removals.
func parseLabels(args []string) (map[string]string, []string, error) {
	set := map[string]string{}
	var remove []string
	for _, arg := range args {
		if key, value, ok := strings.Cut(arg, "="); ok {
			if key = strings.TrimSpace(key); key == "" {
				return nil, nil, clierr.Usagef("label %q has an empty key", arg)
			}
			set[key] = value
			continue
		}
		if key, ok := strings.CutSuffix(arg, "-"); ok && key != "" {
			remove = append(remove, key)
			continue
		}
		return nil, nil, clierr.Usagef("label %q must be KEY=VALUE or KEY-", arg)
	}
	return set, remove, nil
}
