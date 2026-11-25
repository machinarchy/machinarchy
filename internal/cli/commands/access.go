package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/output"
)

// Commands for what grants access: Access Profiles, API tokens and agent
// enrolment tokens (AUDIT G-13C-03). Before them the guide used curl.

func accessClient(ctx context.Context, f *cli.Factory) (api.Client, api.AccessClient, error) {
	client, err := f.NewAPIClient(ctx)
	if err != nil {
		return nil, nil, err
	}
	access, ok := client.(api.AccessClient)
	if !ok {
		return nil, nil, errors.New("API client does not support Access Profiles and tokens")
	}
	return client, access, nil
}

func newCreateProfileCommand(f *cli.Factory) *cobra.Command {
	var connector, username, secret, auth, hostKeyPolicy, supervisor, description string
	command := &cobra.Command{
		Use:   "profile NAME --connector ssh|agent [...]",
		Short: "Create an Access Profile (owner)",
		Long: "Creates an Access Profile. An ssh profile names the login user and the credential secret;\n" +
			"an agent profile takes nothing else: its hosts are reached through their enrolled agent.",
		Example: "  machinarchy create profile lab-key --connector ssh --username testuser --secret lab-key --auth private-key\n" +
			"  machinarchy create profile agents --connector agent",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			spec := map[string]any{"connectorKind": connector, "enabled": true}
			switch connector {
			case "agent":
				if username != "" || secret != "" || auth != "" || hostKeyPolicy != "" || supervisor != "" {
					return clierr.Usagef("an agent profile takes no --username, --secret, --auth, --host-key-policy or --supervisor")
				}
			case "ssh":
				if username == "" || secret == "" {
					return clierr.Usagef("an ssh profile needs --username and --secret")
				}
				if auth == "" {
					auth = "password"
				}
				ref, err := resolveSecret(command, f, secret)
				if err != nil {
					return err
				}
				spec["username"], spec["authenticationMethod"], spec["credential"] = username, auth, ref
				if hostKeyPolicy != "" {
					spec["hostKeyPolicy"] = hostKeyPolicy
				}
				if supervisor != "" {
					spec["execution"] = map[string]any{"supervisor": supervisor}
				}
			default:
				return clierr.Usagef("--connector must be ssh or agent")
			}
			_, access, err := accessClient(command.Context(), f)
			if err != nil {
				return err
			}
			profile, err := access.CreateAccessProfile(command.Context(), api.AccessProfileInput{Name: args[0], Description: description, Spec: spec})
			if err != nil {
				return fmt.Errorf("create profile: %w", err)
			}
			if f.Config().Quiet() {
				_, err = fmt.Fprintln(command.OutOrStdout(), profile.ID)
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "Access Profile %q created: %s.\n", profile.Name, profile.ID)
			return err
		},
	}
	flags := command.Flags()
	flags.StringVar(&connector, "connector", "", "ssh or agent")
	flags.StringVar(&username, "username", "", "SSH login user (ssh)")
	flags.StringVar(&secret, "secret", "", "Credential secret name or ID, current version (ssh)")
	flags.StringVar(&auth, "auth", "", "password (default) or private-key (ssh)")
	flags.StringVar(&hostKeyPolicy, "host-key-policy", "", "tofu, approve, pinned or ca (ssh; default: the server's)")
	flags.StringVar(&supervisor, "supervisor", "", "Detached runs: auto, systemd, setsid or attached (ssh)")
	flags.StringVar(&description, "description", "", "Description")
	return command
}

// resolveSecret finds a secret by name or ID and returns a reference to its
// current version. Listing secrets needs an owner session.
func resolveSecret(command *cobra.Command, f *cli.Factory, nameOrID string) (map[string]any, error) {
	client, err := f.NewManagementClient(command.Context())
	if err != nil {
		return nil, err
	}
	raw, err := client.ListSecrets(command.Context())
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	var secrets []secretMetadata
	if err := json.Unmarshal(raw, &secrets); err != nil {
		return nil, fmt.Errorf("decode secrets: %w", err)
	}
	for _, s := range secrets {
		if s.ID == nameOrID || s.Name == nameOrID {
			return map[string]any{"id": s.ID, "version": s.CurrentVersion}, nil
		}
	}
	return nil, clierr.Newf(clierr.ExitNotFound, "List secrets with 'machinarchy secret list'.", "no secret named %q", nameOrID)
}

func newDeleteProfileCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "profile NAME-OR-ID",
		Short: "Delete an Access Profile (owner; refused while hosts use it)",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			client, access, err := accessClient(command.Context(), f)
			if err != nil {
				return err
			}
			id, err := resolveProfile(command, client, args[0])
			if err != nil {
				return err
			}
			if err := f.RequireConfirmation(fmt.Sprintf("Delete Access Profile %q (%s)?", args[0], id)); err != nil {
				return err
			}
			if err := access.DeleteAccessProfile(command.Context(), id); err != nil {
				return fmt.Errorf("delete profile: %w", err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "Access Profile %q deleted.\n", args[0])
			return err
		},
	}
}

func newCreateAgentTokenCommand(f *cli.Factory) *cobra.Command {
	var host, hostname string
	var ttl time.Duration
	command := &cobra.Command{
		Use:   "agent-token --host NAME-OR-ID [--hostname NAME] [--ttl 1h]",
		Short: "Issue a single-use enrolment token that binds an agent to a host (owner; shown once)",
		Example: "  machinarchy create agent-token --host web-01 --hostname web-01.example\n" +
			"  machinarchy create agent-token --host web-01 -q > agent.token",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if strings.TrimSpace(host) == "" {
				return clierr.Usagef("--host is required: the managed host the agent will serve")
			}
			if ttl < time.Minute || ttl > 24*time.Hour {
				return clierr.Usagef("--ttl must be between 1m and 24h")
			}
			client, access, err := accessClient(command.Context(), f)
			if err != nil {
				return err
			}
			target, err := findHost(command.Context(), f, client, host)
			if err != nil {
				return err
			}
			issued, err := access.CreateEnrollmentToken(command.Context(), api.EnrollmentTokenInput{
				Hostname: hostname, HostID: target.Metadata.ID, TTLSeconds: int(ttl.Seconds())})
			if err != nil {
				return fmt.Errorf("create agent token: %w", err)
			}
			return printIssued(command, f, issued, issued.Token,
				fmt.Sprintf("Enrolment token for host %q created (expires %s). It is shown only now; put it on the host as MACHINARCHY_AGENT_ENROLLMENT_TOKEN_FILE:",
					target.Metadata.Name, issued.ExpiresAt.Format(time.RFC3339)))
		},
	}
	command.Flags().StringVar(&host, "host", "", "Managed host the agent will serve (name or ID)")
	command.Flags().StringVar(&hostname, "hostname", "", "Only an agent reporting this hostname may use the token")
	command.Flags().DurationVar(&ttl, "ttl", time.Hour, "How long the token stays valid (1m to 24h)")
	return command
}

// printIssued prints a newly issued secret: -q prints the secret alone (for
// a pipe), -o json/yaml the whole document, the table mode a note and the
// secret on its own line.
func printIssued(command *cobra.Command, f *cli.Factory, document any, secret, note string) error {
	printer, err := f.Printer(command.OutOrStdout(), output.FormatTable)
	if err != nil {
		return err
	}
	if printer.Format() != output.FormatTable {
		return printer.Document(document)
	}
	if f.Config().Quiet() {
		_, err = fmt.Fprintln(command.OutOrStdout(), secret)
		return err
	}
	_, err = fmt.Fprintf(command.OutOrStdout(), "%s\n%s\n", note, secret)
	return err
}

func newGetAgentTokensCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "agent-tokens",
		Short: "List agent enrolment tokens (never their secrets)",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			_, access, err := accessClient(command.Context(), f)
			if err != nil {
				return err
			}
			tokens, err := access.ListEnrollmentTokens(command.Context())
			if err != nil {
				return fmt.Errorf("list agent tokens: %w", err)
			}
			rows := make([][]string, 0, len(tokens))
			ids := make([]string, 0, len(tokens))
			for _, t := range tokens {
				ids = append(ids, t.ID)
				state := "unused"
				switch {
				case t.RevokedAt != nil:
					state = "revoked"
				case t.UsedAt != nil:
					state = "used by " + t.UsedByAgent
				case time.Now().After(t.ExpiresAt):
					state = "expired"
				}
				rows = append(rows, []string{t.ID, orDash(t.HostID), orDash(t.Hostname), t.ExpiresAt.Format(time.RFC3339), state})
			}
			return printList(command, f, tokens, ids, []string{"ID", "HOST", "HOSTNAME", "EXPIRES", "STATE"}, rows)
		},
	}
}

// printList prints a collection in the chosen format.
func printList(command *cobra.Command, f *cli.Factory, document any, ids []string, headers []string, rows [][]string) error {
	printer, err := f.Printer(command.OutOrStdout(), output.FormatTable)
	if err != nil {
		return err
	}
	if printer.Format() != output.FormatTable {
		return printer.Document(document)
	}
	if f.Config().Quiet() {
		return printer.IDs(ids)
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(command.OutOrStdout(), "No resources found.")
		return err
	}
	return printer.Table(headers, rows)
}

func newDeleteAgentTokenCommand(f *cli.Factory) *cobra.Command {
	return newRevokeCommand(f, "agent-token ID", "Revoke an unused agent enrolment token (owner)", "Agent enrolment token",
		func(ctx context.Context, a api.AccessClient, id string) error {
			return a.RevokeEnrollmentToken(ctx, id)
		})
}

func newRevokeCommand(f *cli.Factory, use, short, what string, revoke func(context.Context, api.AccessClient, string) error) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			_, access, err := accessClient(command.Context(), f)
			if err != nil {
				return err
			}
			if err := f.RequireConfirmation(fmt.Sprintf("Revoke %s %s?", strings.ToLower(what), args[0])); err != nil {
				return err
			}
			if err := revoke(command.Context(), access, args[0]); err != nil {
				return fmt.Errorf("revoke: %w", err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "%s %s revoked.\n", what, args[0])
			return err
		},
	}
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
