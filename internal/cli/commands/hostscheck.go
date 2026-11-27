package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
)

func newHostsCheckCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "check NAME",
		Short: "Test the SSH connection to a host now",
		Long: "Asks the server to connect to the host now and verify the address policy, the trusted SSH host key and the sign-in.\n" +
			"It runs no command, trusts no key and changes nothing; a changed or unknown key fails closed.\n" +
			"Exit code 0 when the host is ready, 1 with the reason when it is not.",
		Example: "  machinarchy hosts check web-01\n  machinarchy hosts check web-01 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			printer, err := f.Printer(command.OutOrStdout(), output.FormatTable)
			if err != nil {
				return err
			}
			client, err := f.NewAPIClient(command.Context())
			if err != nil {
				return err
			}
			host, err := findHost(command.Context(), f, client, args[0])
			if err != nil {
				return err
			}
			check, err := client.CheckHost(command.Context(), host.Metadata.ID)
			if err != nil {
				return fmt.Errorf("check host: %w", err)
			}
			if printer.Format() != output.FormatTable {
				if err := printer.Document(check); err != nil {
					return err
				}
			} else if err := printCheck(command, host.Metadata.Name, check); err != nil {
				return err
			}
			if check.Ready {
				return nil
			}

			return clierr.Detailed(clierr.ExitFailure,
				fmt.Errorf("%s is not ready: %s", sanitize.Line(host.Metadata.Name), sanitize.Line(check.Reason)),
				"", checkNextStep(host.Metadata.Name, check.Reason))
		},
	}
}

func printCheck(command *cobra.Command, name string, check *api.HostCheck) error {
	out := command.OutOrStdout()
	state := "ready"
	if !check.Ready {
		state = "not ready"
	}
	if _, err := fmt.Fprintf(out, "%s: %s (%s)\n", sanitize.Line(name), state, sanitize.Line(check.Reason)); err != nil {
		return err
	}
	if check.Message != "" {
		if _, err := fmt.Fprintf(out, "  %s\n", sanitize.Line(check.Message)); err != nil {
			return err
		}
	}

	return nil
}

// checkNextStep names the command that deals with the reason, never one that
// trusts a key for the person.
func checkNextStep(name, reason string) string {
	switch reason {
	case "HostKeyChanged":
		return "check the new key out of band, then: machinarchy hosts trust " + name + " --replace"
	case "HostKeyPendingApproval", "HostKeyNotTrusted", "HostKeyRejected":
		return "machinarchy hosts trust " + name
	default:
		return "machinarchy hosts show " + name
	}
}
