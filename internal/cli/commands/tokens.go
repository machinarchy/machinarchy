package commands

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/msg"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
	"github.com/machinarchy/machinarchy/internal/cli/ui"
)

const (
	// defaultTokenDays and maxTokenDays are the lifetimes of an API token: the
	// CLI asks for 90 days unless told otherwise; the server allows 365 at most.
	defaultTokenDays = 90
	maxTokenDays     = 365
)

// NewTokensCommand lists, creates and revokes API tokens. They are for scripts
// and other computers; creating and revoking them needs an owner signed in
// with a password.
func NewTokensCommand(f *cli.Factory) *cobra.Command {
	command := &cobra.Command{
		Use:   "tokens",
		Short: "List, create and revoke API tokens",
		Long: "API tokens let scripts and other computers use Machinarchy without your password. A token is shown once, when it is\n" +
			"created. It has a role (reader or operator, never owner) and an expiry. Managing tokens needs an owner signed in\n" +
			"with a password.",
		Example: "  machinarchy tokens\n  machinarchy tokens create ci-deploy --role operator --expires 90d\n  machinarchy tokens revoke ci-deploy",
		Args:    cobra.NoArgs,
		RunE:    listTokens(f),
	}
	command.AddCommand(newTokensCreateCommand(f), newTokensRevokeCommand(f))
	markOwner(command)

	return command
}

func listTokens(f *cli.Factory) func(*cobra.Command, []string) error {
	return func(command *cobra.Command, _ []string) error {
		_, access, err := accessClient(command.Context(), f)
		if err != nil {
			return err
		}
		tokens, err := access.ListAPITokens(command.Context())
		if err != nil {
			return fmt.Errorf("list tokens: %w", err)
		}

		now := f.Now()
		rows := make([][]string, 0, len(tokens))
		ids := make([]string, 0, len(tokens))
		for _, t := range tokens {
			ids = append(ids, strconv.Itoa(t.ID))
			rows = append(rows, []string{strconv.Itoa(t.ID), t.Name, t.Role, t.CreatedAt.Local().Format("2006-01-02"),
				expiryText(t.ExpiresAt), tokenState(t, now)})
		}

		return printList(command, f, tokens, ids, []string{"ID", "NAME", "ROLE", "CREATED", "EXPIRES", "STATE"}, rows)
	}
}

func tokenState(t api.Token, now time.Time) string {
	switch {
	case t.RevokedAt != nil:
		return "revoked"
	case t.ExpiresAt != nil && !t.ExpiresAt.After(now):
		return "expired"
	default:
		return "active"
	}
}

func expiryText(t *time.Time) string {
	if t == nil {
		return "never"
	}

	return t.Local().Format("2006-01-02")
}

func newTokensCreateCommand(f *cli.Factory) *cobra.Command {
	var role, expires, file string
	command := &cobra.Command{
		Use:   "create NAME --role reader|operator [--expires 90d] [--out FILE]",
		Short: "Create an API token (shown once)",
		Long: "Creates an API token. The token is printed once and cannot be shown again. With -q it is printed alone, to pipe\n" +
			"into another command; with --out it is written to a private file instead of the terminal.",
		Example: "  machinarchy tokens create ci-deploy --role operator --expires 90d\n" +
			"  machinarchy tokens create ci-read --role reader --out ci-read.token",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if role != "reader" && role != "operator" {
				return clierr.Detailed(clierr.ExitUsage, errors.New("--role must be reader or operator"),
					"a token can never be an owner", "machinarchy tokens create "+args[0]+" --role operator")
			}
			hours, err := parseLifetime(expires)
			if err != nil {
				return err
			}
			name := strings.TrimSpace(args[0])
			if name == "" {
				return clierr.Usagef("the token needs a name")
			}

			printer, err := f.Printer(command.OutOrStdout(), output.FormatTable)
			if err != nil {
				return err
			}
			// A token is shown once, so a combination that could not show it
			// must fail before the token exists.
			if file != "" && printer.Format() != output.FormatTable {
				return clierr.Usagef("--out cannot be combined with -o json or -o yaml, which print the token")
			}
			_, access, err := accessClient(command.Context(), f)
			if err != nil {
				return err
			}
			issued, err := access.CreateAPIToken(command.Context(), name, role, hours)
			if err != nil {
				return fmt.Errorf("create token: %w", err)
			}

			return showIssuedToken(command, f, printer, issued, name, file)
		},
	}
	command.Flags().StringVar(&role, "role", "", "reader or operator")
	command.Flags().StringVar(&expires, "expires", fmt.Sprintf("%dd", defaultTokenDays), "How long the token lasts, for example 90d or 720h (at most 365d)")
	command.Flags().StringVar(&file, "out", "", "Write the token to this file (mode 0600; an existing file is never overwritten)")

	return command
}

// showIssuedToken prints a new token once, in the form the person asked for.
func showIssuedToken(command *cobra.Command, f *cli.Factory, printer *output.Printer, issued *api.IssuedToken, name, file string) error {
	out := command.OutOrStdout()

	if printer.Format() != output.FormatTable {
		return printer.Document(issued)
	}

	if file != "" {
		if err := writeSecretFile(file, issued.Secret); err != nil {
			return err
		}
		message := msg.Get("token.created_file").With(msg.Vars{"name": sanitize.Line(name), "role": issued.Role, "expires": expiryText(issued.ExpiresAt), "file": file})
		_, err := fmt.Fprint(out, ui.Render(message, f.StyleFor(out)))

		return err
	}

	if f.Config().Quiet() {
		_, err := fmt.Fprintln(out, issued.Secret)

		return err
	}

	message := msg.Get("token.created").With(msg.Vars{"name": sanitize.Line(name), "role": issued.Role, "expires": expiryText(issued.ExpiresAt)})
	if _, err := fmt.Fprint(out, ui.Render(message, f.StyleFor(out))); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "\n  %s\n\n  Use it in a script: MACHINARCHY_TOKEN=... MACHINARCHY_SERVER=...\n", issued.Secret)

	return err
}

// writeSecretFile writes a secret to a new private file. An existing file is
// never overwritten.
func writeSecretFile(path, secret string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- the path the person chose with --out
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return clierr.Detailed(clierr.ExitUsage, fmt.Errorf("%s already exists", path), "the CLI never overwrites a file with a token",
				"Choose another file, or remove this one first.")
		}

		return clierr.Detailed(clierr.ExitUsage, fmt.Errorf("write %s: %w", path, err), "", "Check the path and its permissions.")
	}
	if _, err := file.WriteString(secret + "\n"); err != nil {
		_ = file.Close()

		return fmt.Errorf("write %s: %w", path, err)
	}

	return file.Close()
}

// parseLifetime reads "90d" or "720h" and returns hours. The server allows up
// to 365 days.
func parseLifetime(text string) (int, error) {
	text = strings.TrimSpace(strings.ToLower(text))
	bad := clierr.Detailed(clierr.ExitUsage, fmt.Errorf("%q is not a lifetime", text),
		"use a number of days or hours, for example 90d or 720h", "machinarchy tokens create NAME --role operator --expires 90d")

	unit := time.Hour
	switch {
	case strings.HasSuffix(text, "d"):
		unit = 24 * time.Hour
		text = strings.TrimSuffix(text, "d")
	case strings.HasSuffix(text, "h"):
		text = strings.TrimSuffix(text, "h")
	default:
		return 0, bad
	}

	n, err := strconv.Atoi(text)
	if err != nil || n <= 0 {
		return 0, bad
	}
	hours := n * int(unit/time.Hour)
	if hours > maxTokenDays*24 {
		return 0, clierr.Detailed(clierr.ExitUsage, fmt.Errorf("a token can last at most %d days", maxTokenDays), "", "machinarchy tokens create NAME --role operator --expires 365d")
	}

	return hours, nil
}

func newTokensRevokeCommand(f *cli.Factory) *cobra.Command {
	var id int
	command := &cobra.Command{
		Use:   "revoke NAME [--id N]",
		Short: "Revoke an API token",
		Long: "Revokes an API token so it stops working at once. You confirm by typing its name (in a script: --confirm NAME);\n" +
			"--yes is not enough.",
		Example: "  machinarchy tokens revoke ci-deploy\n  machinarchy tokens revoke ci-deploy --confirm ci-deploy",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			name := args[0]
			_, access, err := accessClient(command.Context(), f)
			if err != nil {
				return err
			}
			tokens, err := access.ListAPITokens(command.Context())
			if err != nil {
				return fmt.Errorf("list tokens: %w", err)
			}

			target, err := pickToken(tokens, name, id, f.Now())
			if err != nil {
				return err
			}
			if err := f.ConfirmTyped(fmt.Sprintf("Revoke API token %q (%s, id %d)? It stops working at once.", sanitize.Line(name), target.Role, target.ID), name); err != nil {
				return err
			}
			if err := access.RevokeAPIToken(command.Context(), strconv.Itoa(target.ID)); err != nil {
				return fmt.Errorf("revoke token: %w", err)
			}

			out := command.OutOrStdout()
			_, err = fmt.Fprint(out, ui.Render(msg.Get("token.revoked").With(msg.Vars{"name": sanitize.Line(name)}), f.StyleFor(out)))

			return err
		},
	}
	command.Flags().IntVar(&id, "id", 0, "The token's ID, when several tokens have the same name")

	return command
}

// pickToken finds the one token to revoke: by ID, or by name among those not
// yet revoked or expired.
func pickToken(tokens []api.Token, name string, id int, now time.Time) (api.Token, error) {
	var matches []api.Token
	for _, t := range tokens {
		if id != 0 {
			if t.ID == id {
				return t, nil
			}

			continue
		}
		if t.Name == name && tokenState(t, now) == "active" {
			matches = append(matches, t)
		}
	}

	switch {
	case id != 0:
		return api.Token{}, clierr.Detailed(clierr.ExitNotFound, fmt.Errorf("there is no token with id %d", id), "", "machinarchy tokens")
	case len(matches) == 0:
		return api.Token{}, clierr.Detailed(clierr.ExitNotFound, fmt.Errorf("there is no active token called %q", sanitize.Line(name)), "", "machinarchy tokens")
	case len(matches) > 1:
		ids := make([]string, len(matches))
		for i, t := range matches {
			ids[i] = strconv.Itoa(t.ID)
		}

		return api.Token{}, clierr.Detailed(clierr.ExitUsage,
			fmt.Errorf("%d active tokens are called %q (ids %s)", len(matches), sanitize.Line(name), strings.Join(ids, ", ")), "",
			"machinarchy tokens revoke "+name+" --id "+ids[0])
	}

	return matches[0], nil
}
