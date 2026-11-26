package cli

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
)

// completionTimeout bounds how long a tab press waits for the server.
const completionTimeout = 3 * time.Second

// CompletionClient prepares the API client a shell-completion request uses.
// Completion never prints errors and never prompts: when the configuration,
// the credential or the server is not usable it returns nil and the shell
// simply offers nothing. The returned context ends after completionTimeout.
func (f *Factory) CompletionClient(command *cobra.Command) (context.Context, context.CancelFunc, api.Client) {
	parent := command.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, completionTimeout)

	flags := command.Root().PersistentFlags()
	path, err := flags.GetString("config")
	if err == nil {
		err = f.LoadConfig(path, flags)
	}
	if err != nil {
		return ctx, cancel, nil
	}
	client, err := f.NewAPIClient(ctx)
	if err != nil {
		return ctx, cancel, nil
	}

	return ctx, cancel, client
}

// CompletionNames turns names the server sent into completion candidates for
// what the user has typed. A name with control characters or whitespace is
// dropped: it could not be typed back safely and must not reach the shell.
func CompletionNames(names []string, prefix string) []string {
	seen := make(map[string]bool, len(names))
	found := make([]string, 0, len(names))
	for _, name := range names {
		if name == "" || seen[name] || !strings.HasPrefix(name, prefix) || sanitize.Line(name) != name ||
			strings.IndexFunc(name, unicode.IsSpace) >= 0 {
			continue
		}
		seen[name] = true
		found = append(found, name)
	}

	return found
}

// CompleteEnvNames completes an env name; extra names (such as "any") are
// always offered. It serves --env (firstArgOnly false) and the env argument of
// the envs commands (firstArgOnly true).
func CompleteEnvNames(f *Factory, firstArgOnly bool, extra ...string) cobra.CompletionFunc {
	return func(command *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if firstArgOnly && len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		ctx, cancel, client := f.CompletionClient(command)
		defer cancel()
		names := append([]string(nil), extra...)
		if client != nil {
			if environments, err := client.ListEnvironments(ctx); err == nil {
				for _, environment := range environments {
					names = append(names, environment.Name)
				}
			}
		}

		return CompletionNames(names, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}
