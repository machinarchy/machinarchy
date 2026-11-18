package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

// NewTrustCommand registers host-key trust commands.
func NewTrustCommand(factory *cli.Factory) *cobra.Command {
	command := &cobra.Command{Use: "trust", Short: "Approve SSH host keys", Args: cobra.NoArgs}
	command.AddCommand(newTrustHostCommand(factory))
	return command
}

func newTrustHostCommand(factory *cli.Factory) *cobra.Command {
	var replace, allPending bool
	var selector string
	var fingerprints []string
	command := &cobra.Command{
		Use:   "host [NAME-OR-ID] [--replace] [--fingerprint SHA256:...] | --all-pending [--selector SELECTOR]",
		Short: "Trust the SSH host keys a host presents (owner only)",
		Long: "Shows the host keys the controller observed and, after confirmation, trusts exactly those keys.\n" +
			"--all-pending approves every host waiting for approval (HostKeyPendingApproval), optionally narrowed by --selector.\n" +
			"A host that already trusts keys is only changed with --replace, after checking the change out of band.",
		Example: "  machinarchy trust host web-01\n  machinarchy trust host --all-pending --selector tier=web\n  machinarchy trust host web-01 --replace",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if allPending == (len(args) == 1) {
				return clierr.Usagef("give a host NAME-OR-ID, or --all-pending")
			}
			if allPending && (replace || len(fingerprints) > 0) {
				return clierr.Usagef("--all-pending approves observed keys only; --replace and --fingerprint need a single host")
			}
			client, err := factory.NewAPIClient(command.Context())
			if err != nil {
				return err
			}
			var hosts []api.Host
			if allPending {
				all, err := listAllHosts(command.Context(), client, api.HostQuery{Selector: selector})
				if err != nil {
					return fmt.Errorf("list hosts: %w", err)
				}
				for _, host := range all {
					if readyReason(host) == "HostKeyPendingApproval" && len(host.Status.ObservedHostKeys) > 0 {
						hosts = append(hosts, host)
					}
				}
				if len(hosts) == 0 {
					_, err := fmt.Fprintln(command.OutOrStdout(), "No hosts are waiting for host key approval.")
					return err
				}
			} else {
				host, err := findHost(command.Context(), factory, client, args[0])
				if err != nil {
					return err
				}
				hosts = []api.Host{host}
			}

			plan := make([][]string, len(hosts))
			var prompt strings.Builder
			for i, host := range hosts {
				keys := fingerprints
				if len(keys) == 0 {
					for _, key := range host.Status.ObservedHostKeys {
						keys = append(keys, key.Fingerprint)
					}
				}
				if len(keys) == 0 {
					return clierr.Newf(clierr.ExitUsage, "", "no host keys have been observed for %q yet; wait for the controller or pass --fingerprint", host.Metadata.Name)
				}
				if len(host.Spec.AccessTarget.Trust) > 0 && !replace {
					return clierr.Newf(clierr.ExitUsage, "Check the new key out of band, then rerun with --replace.",
						"host %q already trusts %s", host.Metadata.Name, strings.Join(host.Spec.AccessTarget.Trust, ", "))
				}
				plan[i] = keys
				fmt.Fprintf(&prompt, "  %s (%s):\n", host.Metadata.Name, host.Spec.AccessTarget.Endpoint)
				for _, key := range keys {
					fmt.Fprintf(&prompt, "    %s\n", key)
				}
				if len(host.Spec.AccessTarget.Trust) > 0 {
					fmt.Fprintf(&prompt, "    replacing %s\n", strings.Join(host.Spec.AccessTarget.Trust, ", "))
				}
			}
			// Always show what will be trusted, also with --yes and when a
			// non-interactive run stops for confirmation.
			if _, err := fmt.Fprintf(command.OutOrStdout(), "SSH host keys to trust:\n%s", prompt.String()); err != nil {
				return err
			}
			if err := factory.RequireConfirmation("Trust these SSH host keys?"); err != nil {
				return err
			}
			for i, host := range hosts {
				if _, err := client.TrustHostKeys(command.Context(), host.Metadata.ID, host.Metadata.Generation, plan[i], replace); err != nil {
					return fmt.Errorf("trust host %q: %w", host.Metadata.Name, err)
				}
				if _, err := fmt.Fprintf(command.OutOrStdout(), "Host %q now trusts %d host key(s).\n", host.Metadata.Name, len(plan[i])); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&replace, "replace", false, "Replace keys the host already trusts")
	command.Flags().BoolVar(&allPending, "all-pending", false, "Approve every host waiting for host key approval")
	command.Flags().StringVar(&selector, "selector", "", "Narrow --all-pending with a label selector")
	command.Flags().StringArrayVar(&fingerprints, "fingerprint", nil, "Trust this fingerprint instead of the observed keys (repeatable)")
	return command
}

func readyReason(host api.Host) string {
	for _, condition := range host.Status.Conditions {
		if condition.Type == "Ready" {
			return condition.Reason
		}
	}
	return ""
}
