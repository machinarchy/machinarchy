package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/config"
	"github.com/machinarchy/machinarchy/internal/cli/cred"
	"github.com/machinarchy/machinarchy/internal/cli/msg"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
	"github.com/machinarchy/machinarchy/internal/cli/ui"
)

// notReported is shown where the server does not tell the CLI: /api/v1/me does
// not return a role or a token's expiry, and the CLI never guesses them.
const notReported = "not reported by this server"

// whoamiDocument is the stable machine-readable form of `whoami`.
type whoamiDocument struct {
	Server      string     `json:"server" yaml:"server"`
	URL         string     `json:"url" yaml:"url"`
	User        string     `json:"user" yaml:"user"`
	Email       string     `json:"email,omitempty" yaml:"email,omitempty"`
	Method      string     `json:"method" yaml:"method"`
	TokenName   string     `json:"tokenName,omitempty" yaml:"tokenName,omitempty"`
	Role        string     `json:"role,omitempty" yaml:"role,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty" yaml:"expiresAt,omitempty"`
	Environment string     `json:"environment,omitempty" yaml:"environment,omitempty"`
}

// NewWhoamiCommand shows who the CLI is signed in as, and how.
func NewWhoamiCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "whoami",
		Short:   "Show the server, the user and how you are signed in",
		Args:    cobra.NoArgs,
		Example: "  machinarchy whoami\n  machinarchy whoami -o json",
		RunE: func(command *cobra.Command, _ []string) error {
			printer, err := f.Printer(command.OutOrStdout(), output.FormatTable)
			if err != nil {
				return err
			}
			target, err := f.ResolveTarget()
			if err != nil {
				return err
			}
			client, err := f.ClientFor(target)
			if err != nil {
				return err
			}
			auth, ok := client.(api.AuthClient)
			if !ok {
				return errors.New("this client cannot identify the caller")
			}

			user, err := auth.Me(command.Context())
			if err != nil {
				return err
			}

			doc := whoamiDocument{
				Server: target.ServerName, URL: target.Server, User: user.Label(), Email: user.Email,
				Method: methodName(target), TokenName: target.CredentialName, Role: target.Role, Environment: target.Environment,
			}
			doc.ExpiresAt = expiryOf(command.Context(), f, target, auth)

			if printer.Format() != output.FormatTable {
				return printer.Document(doc)
			}

			return printWhoami(command, f, doc)
		},
	}
}

func methodName(target config.Target) string {
	switch {
	case target.FromEnv:
		return "API token from MACHINARCHY_TOKEN"
	case target.Kind == cred.KindSession:
		return "password session"
	case target.Kind == cred.KindDeviceToken:
		return "device token"
	default:
		return "API token"
	}
}

// expiryOf returns when the credential stops working, as far as the server or
// the saved credential says. A session's expiry is read from the server; a
// token's is only known when the CLI created the token.
func expiryOf(ctx context.Context, f *cli.Factory, target config.Target, auth api.AuthClient) *time.Time {
	if target.Kind == cred.KindSession && !target.FromEnv {
		if sessions, err := auth.Sessions(ctx); err == nil {
			for _, session := range sessions {
				if session.Current && !session.ExpiresAt.IsZero() {
					expires := session.ExpiresAt

					return &expires
				}
			}
		}
	}
	if target.FromEnv || target.ServerName == "" || f.Credentials() == nil {
		return nil
	}
	entry, err := f.Credentials().Get(target.ServerName)
	if err != nil {
		return nil
	}
	saved := entry.Token
	if target.Kind == cred.KindSession {
		saved = entry.Session
	}
	if saved != nil {
		return saved.ExpiresAt
	}

	return nil
}

func printWhoami(command *cobra.Command, f *cli.Factory, doc whoamiDocument) error {
	server := doc.URL
	if doc.Server != "" {
		server = doc.Server + " (" + doc.URL + ")"
	}
	role := doc.Role
	if role == "" {
		role = notReported
	}
	expires := "unknown"
	if doc.ExpiresAt != nil {
		expires = doc.ExpiresAt.Local().Format("2006-01-02 15:04") + " (in " + humanDuration(doc.ExpiresAt.Sub(f.Now())) + ")"
		if !doc.ExpiresAt.After(f.Now()) {
			expires = doc.ExpiresAt.Local().Format("2006-01-02 15:04") + " (expired)"
		}
	}
	environment := doc.Environment
	if environment == "" {
		environment = "none"
	}

	method := doc.Method
	if doc.TokenName != "" {
		method += " " + doc.TokenName
	}

	rows := [][2]string{{"Server", server}, {"User", doc.User}, {"Signed in with", method}, {"Role", role}, {"Expires", expires}, {"Env", environment}}
	for _, row := range rows {
		if _, err := fmt.Fprintf(command.OutOrStdout(), "%-15s %s\n", row[0]+":", sanitize.Line(row[1])); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(command.OutOrStdout(), "\nNext:  machinarchy")

	return err
}

// NewLogoutCommand ends the CLI's access to a server: it ends the login session
// on the server, revokes the device token this CLI created, and removes what is
// saved on this computer.
func NewLogoutCommand(f *cli.Factory) *cobra.Command {
	var everywhere bool
	command := &cobra.Command{
		Use:   "logout [--everywhere]",
		Short: "Sign out of the active server",
		Long: "Ends your login session on the server, revokes the device token this CLI created for you, and removes the saved credentials from this computer.\n" +
			"An API token you supplied yourself is only removed from this computer: it keeps working until it expires or is revoked.\n" +
			"With --everywhere every login session of your account ends, on every computer. API tokens are not affected.",
		Example: "  machinarchy logout\n  machinarchy logout --everywhere",
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runLogout(command, f, everywhere)
		},
	}
	command.Flags().BoolVar(&everywhere, "everywhere", false, "End every login session of your account, on every computer")

	return command
}

func runLogout(command *cobra.Command, f *cli.Factory, everywhere bool) error {
	ctx := command.Context()
	store := f.Config()
	name := store.ResolveServerName()
	if name == "" {
		return clierr.Detailed(clierr.ExitUsage, errors.New("logout needs a saved server"), "no server is active, or --server is an address that was never saved", "machinarchy switch")
	}

	server, _ := store.Server(name)
	entry, err := f.Credentials().Get(name)
	if err != nil {
		return err
	}
	if entry.Empty() {
		return printMessage(command, f, msg.Get("auth.not_signed_in_server").With(msg.Vars{"server": name}))
	}

	var session api.Client
	if entry.Session != nil {
		session, err = f.ClientFor(config.Target{Server: server.URL, Token: entry.Session.Secret, Kind: cred.KindSession,
			RequestTimeout: requestTimeout(store), CAFile: store.CAPath(server.CAFile)})
		if err != nil {
			return err
		}
	}

	if everywhere {
		if session == nil {
			return clierr.Detailed(clierr.ExitAuth, errors.New("--everywhere needs a login session, and you are signed in with an API token"),
				"only a password session can end the sessions of your account", "machinarchy login")
		}
		if err := f.RequireConfirmation("End every login session of your account, on every computer?"); err != nil {
			return err
		}
	}

	var problems []string
	warn := func(what string, err error) { problems = append(problems, what+": "+sanitize.Line(err.Error())) }

	// The device token is revoked first, while the session that may do so still
	// exists.
	if entry.Token != nil && entry.Token.Kind == cred.KindDeviceToken && entry.Token.TokenID > 0 && session != nil {
		if access, ok := session.(api.AccessClient); ok {
			if err := access.RevokeAPIToken(ctx, fmt.Sprint(entry.Token.TokenID)); err != nil && !isStatus(err, 401, 404) {
				warn("could not revoke the device token", err)
			}
		}
	}

	if session != nil {
		if auth, ok := session.(api.AuthClient); ok {
			var err error
			if everywhere {
				err = auth.RevokeSessions(ctx)
			} else {
				err = auth.Logout(ctx)
			}
			if err != nil && !isStatus(err, 401) { // 401: it had already ended
				warn("could not end the session", err)
			}
		}
	}

	// Whatever the server said, the credentials leave this computer.
	if err := f.Credentials().Delete(name); err != nil {
		return err
	}

	if len(problems) > 0 {
		m := msg.Get("auth.logout_unconfirmed").With(msg.Vars{"why": strings.Join(problems, "; ")})

		return clierr.Detailed(clierr.ExitUnavailable, errors.New(m.Primary), m.Why, m.Next)
	}

	out := command.OutOrStdout()
	detail := ""
	if everywhere {
		detail = "Every login session of your account has ended."
	}
	if _, err := fmt.Fprint(out, ui.Render(msg.Get("auth.logged_out").With(msg.Vars{"server": name, "detail": detail}), f.StyleFor(out))); err != nil {
		return err
	}

	if entry.Session == nil && entry.Token != nil {
		next := "Revoke it from a password session on another computer: machinarchy tokens revoke NAME"
		if entry.Token.Name != "" {
			next = "machinarchy tokens revoke " + entry.Token.Name
		}
		_, err := fmt.Fprint(out, ui.Render(msg.Get("auth.logged_out_token").With(msg.Vars{"next": next}), f.StyleFor(out)))

		return err
	}

	return nil
}

// isStatus reports whether err is an API answer with one of the statuses.
func isStatus(err error, statuses ...int) bool {
	var apiErr *api.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, status := range statuses {
		if apiErr.Status == status {
			return true
		}
	}

	return false
}

// NewSwitchCommand lists the saved servers and makes one the active server.
func NewSwitchCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "switch [SERVER]",
		Short:   "List your servers and choose the active one",
		Args:    cobra.MaximumNArgs(1),
		Example: "  machinarchy switch\n  machinarchy switch lab",
		RunE: func(command *cobra.Command, args []string) error {
			store := f.Config()
			names := store.ServerNames()
			if len(names) == 0 {
				return clierr.Detailed(clierr.ExitUsage, errors.New("no server has been saved yet"), "", "machinarchy login YOUR-SERVER-URL")
			}

			chosen := ""
			if len(args) == 1 {
				chosen = args[0]
			} else {
				if err := printServers(command, f, names); err != nil {
					return err
				}
				prompter := f.Prompter()
				if !prompter.Interactive {
					return nil
				}
				active := 0
				for i, name := range names {
					if name == store.ActiveName() {
						active = i
					}
				}
				index, err := prompter.Choice("Make which server active?", names, active)
				if err != nil {
					return err
				}
				chosen = names[index]
			}

			if err := store.Activate(chosen); err != nil {
				return err
			}
			out := command.OutOrStdout()
			_, err := fmt.Fprint(out, ui.Render(msg.Get("server.switched").With(msg.Vars{"server": chosen}), f.StyleFor(out)))

			return err
		},
	}
}

// printServers lists the saved servers, marks the active one and says whether
// a credential is saved.
func printServers(command *cobra.Command, f *cli.Factory, names []string) error {
	printer, err := f.Printer(command.OutOrStdout(), output.FormatTable)
	if err != nil {
		return err
	}
	store := f.Config()

	rows := make([][]string, 0, len(names))
	for _, name := range names {
		server, _ := store.Server(name)
		active := ""
		if name == store.ActiveName() {
			active = "*"
		}
		rows = append(rows, []string{active, name, server.URL, signedInState(f, name)})
	}
	if printer.Format() != output.FormatTable {
		docs := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			docs = append(docs, map[string]any{"name": row[1], "url": row[2], "active": row[0] == "*", "signedIn": row[3]})
		}

		return printer.Document(docs)
	}

	return printer.Table([]string{"", "NAME", "ADDRESS", "SIGNED IN"}, rows)
}

func signedInState(f *cli.Factory, name string) string {
	entry, err := f.Credentials().Get(name)
	if err != nil {
		return "unknown"
	}
	now := f.Now()
	switch {
	case entry.Session != nil && !entry.Session.Expired(now):
		return "yes"
	case entry.Token != nil && !entry.Token.Expired(now):
		return "yes (token)"
	case !entry.Empty():
		return "expired"
	default:
		return "no"
	}
}
