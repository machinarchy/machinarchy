package commands

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

// maxPasswordInput bounds a password read from stdin (bcrypt uses 72 bytes).
const maxPasswordInput = 1024

// NewLoginCommand signs in with a username and password and stores the
// session token for the active context's user (AUDIT G-13C-03). A session
// is a human owner login, which secret and runtime-config commands need.
func NewLoginCommand(factory *cli.Factory) *cobra.Command {
	var username string
	var passwordStdin bool
	command := &cobra.Command{
		Use:   "login --username NAME [--password-stdin]",
		Short: "Sign in and store the session token for the current context",
		Long: "Signs in to the active context's server and stores the session token for the context's user.\n" +
			"The password is read from the terminal without echo, or from stdin with --password-stdin.",
		Example: "  machinarchy login --server https://machinarchy.example --context prod --username admin   # first login: creates the context\n" +
			"  machinarchy login --username admin\n" +
			"  printf '%s' \"$PASSWORD\" | machinarchy --context lab login --username admin --password-stdin",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if strings.TrimSpace(username) == "" {
				return clierr.Usagef("--username is required")
			}
			target, err := factory.Config().ResolveLogin()
			if err != nil {
				return err
			}
			var password string
			if passwordStdin {
				data, err := io.ReadAll(io.LimitReader(command.InOrStdin(), maxPasswordInput+1))
				if err != nil {
					return fmt.Errorf("read the password from stdin: %w", err)
				}
				if len(data) > maxPasswordInput {
					return clierr.Usagef("the password on stdin is longer than %d bytes", maxPasswordInput)
				}
				password = strings.TrimRight(string(data), "\r\n")
			} else if password, err = factory.ReadSecret("Password: "); errors.Is(err, cli.ErrNoTerminal) {
				return clierr.Newf(clierr.ExitUsage, "Pass the password on stdin with --password-stdin.", "no terminal to prompt for the password")
			} else if err != nil {
				return err
			}
			if password == "" {
				return clierr.Usagef("the password is empty")
			}
			token, err := api.Login(command.Context(), target.Server, target.RequestTimeout, username, password)
			if err != nil {
				return err
			}
			store := factory.Config()
			if target.Create {
				if err := store.SetCluster(target.ContextName, target.Server, false); err != nil {
					return err
				}
			}
			if err := store.SetUser(target.User, token); err != nil {
				return err
			}
			if target.Create {
				if err := store.SetContext(target.ContextName, target.ContextName, target.User, ""); err != nil {
					return err
				}
				if store.CurrentContext() == "" {
					if err := store.UseContext(target.ContextName); err != nil {
						return err
					}
				}
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "Logged in to %s as %s; the session is stored for user %q of context %q.\n",
				target.Server, username, target.User, target.ContextName)
			return err
		},
	}
	command.Flags().StringVar(&username, "username", "", "Username to sign in as")
	command.Flags().BoolVar(&passwordStdin, "password-stdin", false, "Read the password from stdin")
	return command
}
