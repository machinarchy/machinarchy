package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/msg"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
	"github.com/machinarchy/machinarchy/internal/cli/ui"
)

// anyEnv is the name `envs use` accepts to clear the active env.
const anyEnv = "any"

// NewEnvsCommand lists, creates, selects and removes environments.
func NewEnvsCommand(f *cli.Factory) *cobra.Command {
	command := &cobra.Command{
		Use:   "envs",
		Short: "List, create, choose and remove environments",
		Long: "An environment (env) groups hosts, such as production or staging. The active env is the one commands work on\n" +
			"unless --env says otherwise. A protected env asks you to type its name before it is changed.",
		Example: "  machinarchy envs\n  machinarchy envs add production --protected\n  machinarchy envs use staging\n  machinarchy envs remove old-lab",
		Args:    cobra.NoArgs,
		RunE:    listEnvs(f),
	}
	command.AddCommand(newEnvsAddCommand(f), newEnvsUseCommand(f), newEnvsRemoveCommand(f))
	completeChildren(command, cli.CompleteEnvNames(f, true), "remove")
	completeChildren(command, cli.CompleteEnvNames(f, true, anyEnv), "use")

	return command
}

// envClients returns the environment and inventory halves of the API client.
func envClients(ctx context.Context, f *cli.Factory) (api.Client, api.InventoryClient, error) {
	client, err := f.NewAPIClient(ctx)
	if err != nil {
		return nil, nil, err
	}
	inventory, ok := client.(api.InventoryClient)
	if !ok {
		return nil, nil, errors.New("API client does not support environments")
	}

	return client, inventory, nil
}

// envRow is one environment as `envs -o json` prints it.
type envRow struct {
	api.Environment `yaml:",inline"`
	Hosts           int  `json:"hosts" yaml:"hosts"`
	Protected       bool `json:"protectedLocal" yaml:"protectedLocal"`
	Active          bool `json:"active" yaml:"active"`
}

func listEnvs(f *cli.Factory) func(*cobra.Command, []string) error {
	return func(command *cobra.Command, _ []string) error {
		client, err := f.NewAPIClient(command.Context())
		if err != nil {
			return err
		}
		environments, err := client.ListEnvironments(command.Context())
		if err != nil {
			return fmt.Errorf("list environments: %w", err)
		}
		hosts, err := listAllHosts(command.Context(), client, api.HostQuery{})
		if err != nil {
			return fmt.Errorf("list hosts: %w", err)
		}
		counts := make(map[int]int)
		for _, host := range hosts {
			counts[host.Metadata.EnvironmentID]++
		}

		store := f.Config()
		active := store.Environment()
		documents := make([]envRow, 0, len(environments))
		rows := make([][]string, 0, len(environments))
		names := make([]string, 0, len(environments))
		for _, environment := range environments {
			row := envRow{Environment: environment, Hosts: counts[environment.ID], Protected: store.LocalProtected(environment.Name),
				Active: isActiveEnv(active, environment)}
			serverHeld := environment.ServerProtected()
			documents = append(documents, row)
			names = append(names, environment.Name)
			rows = append(rows, []string{sanitize.Line(environment.Name), strconv.Itoa(row.Hosts), protectedText(serverHeld, row.Protected), activeText(row.Active)})
		}

		return printList(command, f, documents, names, []string{"ENV", "HOSTS", "PROTECTED", "ACTIVE"}, rows)
	}
}

// protectedText says who holds the guard: the server (enforced for every
// client) or only this computer's local guard.
func protectedText(server, local bool) string {
	switch {
	case server:
		return "yes"
	case local:
		return "yes (local)"
	}

	return "no"
}

func activeText(active bool) string {
	if active {
		return "yes"
	}

	return ""
}

// isActiveEnv reports whether the saved or flagged env value names environment.
func isActiveEnv(active string, environment api.Environment) bool {
	active = strings.TrimSpace(active)
	if active == "" {
		return false
	}
	if id, err := strconv.Atoi(active); err == nil {
		return id == environment.ID
	}

	return strings.EqualFold(active, environment.Name)
}

// findEnv returns the one environment called name (any letter case).
func findEnv(environments []api.Environment, name string) (api.Environment, error) {
	for _, environment := range environments {
		if strings.EqualFold(environment.Name, name) {
			return environment, nil
		}
	}

	return api.Environment{}, clierr.Detailed(clierr.ExitNotFound, fmt.Errorf("there is no environment called %q", sanitize.Line(name)), "", "machinarchy envs")
}

func newEnvsAddCommand(f *cli.Factory) *cobra.Command {
	var description string
	var protected bool
	command := &cobra.Command{
		Use:   "add NAME [--description TEXT] [--protected]",
		Short: "Create an environment",
		Long: "Creates an environment. --protected makes the server refuse host changes and runs in it unless the caller confirms\n" +
			"its name, for every client. A server that predates this keeps a guard on this computer only, which protects you\n" +
			"from your own typing mistakes, not from other people, computers or tools.",
		Example: "  machinarchy envs add staging\n  machinarchy envs add production --protected --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" || strings.EqualFold(name, anyEnv) {
				return clierr.Detailed(clierr.ExitUsage, fmt.Errorf("%q cannot be the name of an environment", name),
					`"any" means every env`, "machinarchy envs add NAME")
			}
			client, inventory, err := envClients(command.Context(), f)
			if err != nil {
				return err
			}
			existing, err := client.ListEnvironments(command.Context())
			if err != nil {
				return fmt.Errorf("list environments: %w", err)
			}
			serverHolds := api.ServerEnforcesProtection(existing)

			// Only a local guard needs an answer first: ask before anything is
			// created, so a refusal leaves nothing behind.
			if protected && !serverHolds {
				if err := f.RequireConfirmation("This server cannot enforce protected environments. Add a local guard on this computer only?\n" +
					"It protects you from your own typing mistakes, not from other people or other computers."); err != nil {
					return err
				}
			}
			created, err := inventory.CreateEnvironment(command.Context(), name, description, protected && serverHolds)
			if err != nil {
				return fmt.Errorf("create environment: %w", err)
			}
			id := "env.created"
			if protected && created.ServerProtected() {
				id = "env.created_protected_server"
			} else if protected {
				if err := f.Config().SetLocalProtected(name, true); err != nil {
					return err
				}
				id = "env.created_protected"
			}

			return printMessage(command, f, msg.Get(id).With(msg.Vars{"env": sanitize.Line(name)}))
		},
	}
	command.Flags().StringVar(&description, "description", "", "What the environment is for")
	command.Flags().BoolVar(&protected, "protected", false, "Make host changes and runs in the env need its name (held by the server; local guard on an older server)")

	return command
}

func newEnvsUseCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "use NAME",
		Short:   "Choose the active environment (any clears it)",
		Long:    "Makes an environment the active one for this server. Nothing is changed on the server. `envs use any` clears it.",
		Example: "  machinarchy envs use staging\n  machinarchy envs use any",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if strings.EqualFold(name, anyEnv) {
				if err := f.Config().SetEnv(""); err != nil {
					return err
				}

				return printMessage(command, f, msg.Get("env.cleared"))
			}

			client, err := f.NewAPIClient(command.Context())
			if err != nil {
				return err
			}
			environments, err := client.ListEnvironments(command.Context())
			if err != nil {
				return fmt.Errorf("list environments: %w", err)
			}
			environment, err := findEnv(environments, name)
			if err != nil {
				return err
			}
			if err := f.Config().SetEnv(environment.Name); err != nil {
				return err
			}
			server := f.Config().ResolveServerName()

			return printMessage(command, f, msg.Get("env.used").With(msg.Vars{"env": sanitize.Line(environment.Name), "server": server}))
		},
	}
}

func newEnvsRemoveCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove an environment",
		Long: "Removes an environment from the server. You confirm by typing its name (in a script: --confirm NAME); --yes is not\n" +
			"enough. A protected env also needs --confirm-env NAME in a script. No host is deleted by this command.",
		Example: "  machinarchy envs remove old-lab\n  machinarchy envs remove old-lab --confirm old-lab",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			client, inventory, err := envClients(command.Context(), f)
			if err != nil {
				return err
			}
			environments, err := client.ListEnvironments(command.Context())
			if err != nil {
				return fmt.Errorf("list environments: %w", err)
			}
			environment, err := findEnv(environments, strings.TrimSpace(args[0]))
			if err != nil {
				return err
			}
			hosts, err := listAllHosts(command.Context(), client, api.HostQuery{EnvironmentID: environment.ID})
			if err != nil {
				return fmt.Errorf("list hosts: %w", err)
			}

			store := f.Config()
			protected := environment.ServerProtected() || store.LocalProtected(environment.Name)
			if err := checkProtectedConfirmation(f, environment.Name, protected); err != nil {
				return err
			}
			what := fmt.Sprintf("Remove environment %q on server %s? It holds %d host(s); this command deletes none of them.",
				sanitize.Line(environment.Name), store.ResolveServerName(), len(hosts))
			if err := f.ConfirmTyped(what, environment.Name); err != nil {
				return err
			}
			if err := inventory.DeleteEnvironment(command.Context(), environment.ID); err != nil {
				return fmt.Errorf("remove environment: %w", err)
			}

			if store.LocalProtected(environment.Name) {
				if err := store.SetLocalProtected(environment.Name, false); err != nil {
					return err
				}
			}
			if isActiveEnv(store.Environment(), environment) {
				if err := store.SetEnv(""); err != nil {
					return err
				}
			}

			return printMessage(command, f, msg.Get("env.removed").With(msg.Vars{"env": sanitize.Line(environment.Name)}))
		},
	}
}

// checkProtectedConfirmation applies the protected-env rule: --confirm-env,
// when given, must name this env; without a terminal a protected env needs it.
// On a terminal, typing the name (the typed confirmation that follows) answers
// it.
func checkProtectedConfirmation(f *cli.Factory, env string, protected bool) error {
	given := f.ConfirmEnvs()
	named := slices.Contains(given, env)
	if len(given) > 0 && !named {
		return clierr.Detailed(clierr.ExitUsage, fmt.Errorf("--confirm-env is %q, but this is about %q", strings.Join(given, ","), env), "", "--confirm-env "+env)
	}
	if !protected || named {
		return nil
	}
	if !f.Prompter().Interactive {
		return clierr.Detailed(clierr.ExitUsage, fmt.Errorf("%q is a protected env, and nothing can be asked here", env),
			"changes in a protected env are confirmed with its name; --yes is not enough", "--confirm-env "+env)
	}

	return nil
}

// printMessage writes a catalogue message to stdout in the current style.
func printMessage(command *cobra.Command, f *cli.Factory, message msg.Message) error {
	return printMessageTo(command.OutOrStdout(), f, message)
}

// printMessageTo renders a catalogue message on w, styled for what w is.
func printMessageTo(w io.Writer, f *cli.Factory, message msg.Message) error {
	_, err := fmt.Fprint(w, ui.Render(message, f.StyleFor(w)))

	return err
}
