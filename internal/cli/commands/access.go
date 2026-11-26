package commands

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/msg"
	"github.com/machinarchy/machinarchy/internal/cli/output"
)

// Shared helpers for the commands that act on access and tokens.

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
		return printMessage(command, f, msg.Get("list.empty"))
	}
	return printer.Table(headers, rows)
}

func newGetAgentTokensCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "agent-tokens",
		Example: "  machinarchy get agent-tokens",
		Short:   "List agent enrolment tokens (never their secrets)",
		Args:    cobra.NoArgs,
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

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
