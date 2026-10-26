package commands

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/spf13/cobra"
)

const maxCredentialInput = 64 << 10

// NewConfigCommand registers configuration and context management commands.
func NewConfigCommand(factory *cli.Factory) *cobra.Command {
	command := &cobra.Command{
		Use:   "config",
		Short: "Inspect and update CLI contexts and credentials",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(
		newCurrentContextCommand(factory),
		newGetContextsCommand(factory),
		newUseContextCommand(factory),
		newSetClusterCommand(factory),
		newSetCredentialsCommand(factory),
		newSetContextCommand(factory),
	)
	return command
}

func newCurrentContextCommand(factory *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "current-context",
		Short: "Print the selected context",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			name := factory.Config().CurrentContext()
			if name == "" {
				return fmt.Errorf("no current context is selected")
			}
			_, err := fmt.Fprintln(command.OutOrStdout(), name)
			return err
		},
	}
}

func newGetContextsCommand(factory *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "get-contexts",
		Short: "List configured contexts",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			writer := tabwriter.NewWriter(command.OutOrStdout(), 0, 4, 2, ' ', 0)
			if _, err := fmt.Fprintln(writer, "CURRENT\tNAME\tCLUSTER\tUSER\tENVIRONMENT"); err != nil {
				return err
			}
			for _, name := range factory.Config().ContextNames() {
				context, _ := factory.Config().GetContext(name)
				current := ""
				if name == factory.Config().CurrentContext() {
					current = "*"
				}
				if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
					current, name, context.Cluster, context.User, context.Environment); err != nil {
					return err
				}
			}
			return writer.Flush()
		},
	}
}

func newUseContextCommand(factory *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "use-context NAME",
		Short: "Set the current context",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := factory.Config().UseContext(args[0]); err != nil {
				return err
			}
			_, err := fmt.Fprintf(command.OutOrStdout(), "Switched to context %q.\n", args[0])
			return err
		},
	}
}

func newSetClusterCommand(factory *cli.Factory) *cobra.Command {
	var server string
	command := &cobra.Command{
		Use:   "set-cluster NAME --server URL",
		Short: "Add or update an API server",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if strings.TrimSpace(server) == "" {
				return fmt.Errorf("--server is required")
			}
			if err := factory.Config().SetCluster(args[0], server); err != nil {
				return err
			}
			_, err := fmt.Fprintf(command.OutOrStdout(), "Cluster %q configured.\n", args[0])
			return err
		},
	}
	command.Flags().StringVar(&server, "server", "", "Machinarchy API base URL")
	return command
}

func newSetCredentialsCommand(factory *cli.Factory) *cobra.Command {
	var token string
	var tokenStdin bool
	command := &cobra.Command{
		Use:   "set-credentials NAME [--token TOKEN | --token-stdin]",
		Short: "Store an API bearer token in the CLI config",
		Long:  "Stores credentials in the config file with owner-only permissions. Prefer --token-stdin to avoid placing a token in shell history or process arguments.",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if tokenStdin == (strings.TrimSpace(token) != "") {
				return fmt.Errorf("provide exactly one of --token or --token-stdin")
			}
			if tokenStdin {
				data, err := io.ReadAll(io.LimitReader(command.InOrStdin(), maxCredentialInput+1))
				if err != nil {
					return fmt.Errorf("read credential from stdin: %w", err)
				}
				if len(data) > maxCredentialInput {
					return fmt.Errorf("credential input exceeds %d bytes", maxCredentialInput)
				}
				token = string(data)
			}
			if err := factory.Config().SetUser(args[0], token); err != nil {
				return err
			}
			_, err := fmt.Fprintf(command.OutOrStdout(), "Credentials %q configured.\n", args[0])
			return err
		},
	}
	command.Flags().StringVar(&token, "token", "", "API bearer token (prefer --token-stdin)")
	command.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the API bearer token from stdin")
	return command
}

func newSetContextCommand(factory *cli.Factory) *cobra.Command {
	var cluster string
	var user string
	var environment string
	command := &cobra.Command{
		Use:   "set-context NAME --cluster CLUSTER --user USER [--env ENVIRONMENT]",
		Short: "Create or update a named context",
		Args: func(command *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("missing context NAME\n\nUsage: %s\n\nTo configure a local context, run these commands in order (replace the API URL and provide a JWT):\n  machinarchy config set-cluster local --server http://localhost:8080\n  printf '%%s' \"$MACHINARCHY_USER_TOKEN\" | machinarchy config set-credentials local --token-stdin\n  machinarchy config set-context local --cluster local --user local\n  machinarchy config use-context local\n\nRun '%s --help' for details", command.UseLine(), command.CommandPath())
			}
			if len(args) != 1 {
				return fmt.Errorf("expected one context NAME, received %d; usage: %s", len(args), command.UseLine())
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			if strings.TrimSpace(cluster) == "" || strings.TrimSpace(user) == "" {
				return fmt.Errorf("--cluster and --user are required; example: machinarchy config set-context %s --cluster local --user local", args[0])
			}
			if err := factory.Config().SetContext(args[0], cluster, user, environment); err != nil {
				return err
			}
			_, err := fmt.Fprintf(command.OutOrStdout(), "Context %q configured.\n", args[0])
			return err
		},
	}
	command.Flags().StringVar(&cluster, "cluster", "", "Cluster name from config set-cluster")
	command.Flags().StringVar(&user, "user", "", "Credential name from config set-credentials")
	command.Flags().StringVar(&environment, "env", "", "Default Machinarchy environment name or ID")
	return command
}
