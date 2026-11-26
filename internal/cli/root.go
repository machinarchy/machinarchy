// Package cli builds the Machinarchy command tree and wires shared dependencies.
package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// AnnotationSkipConfig marks a command that must run without reading the CLI
// config file (version, for example). Global flags and environment overrides
// still apply. Cobra's built-in help and completion commands are exempt too.
const AnnotationSkipConfig = "machinarchy.io/skip-config"

// AnnotationOwner marks a command that acts as the workspace owner. Such a
// command runs with the saved login session and with nothing else: an API
// token can never be an owner, so with only a token the command stops before
// it sends anything and says how to sign in with a password.
const AnnotationOwner = "machinarchy.io/owner"

// AnnotationDryRun marks a command that honours --dry-run. A command without
// it refuses the flag instead of ignoring it, so "--dry-run" can never run
// something for real.
const AnnotationDryRun = "machinarchy.io/dry-run"

// CommandBuilder creates a command with the shared CLI dependencies.
type CommandBuilder func(*Factory) *cobra.Command

// CommandGroup lets feature packages register related top-level commands
// without editing the root command's implementation.
type CommandGroup struct {
	ID       string
	Title    string
	Commands []CommandBuilder
}

// Options injects the command streams, API client builder, and command groups.
type Options struct {
	Streams       IOStreams
	ClientBuilder APIClientBuilder
	Groups        []CommandGroup
	// Getenv reads the environment for terminal detection (NO_COLOR, TERM,
	// locale, COLUMNS). Nil selects os.Getenv; tests inject one.
	Getenv func(string) string
	// IsTerminal reports whether a reader is an interactive terminal. Nil
	// selects the real check; tests inject one.
	IsTerminal func(io.Reader) bool
	// ReadSecret reads a line without echo. Nil reads from the terminal;
	// tests inject one.
	ReadSecret func(prompt string) (string, error)
	// Now returns the current time. Nil selects time.Now.
	Now func() time.Time
	// Hostname returns the computer's name, used to name a device token. Nil
	// selects os.Hostname.
	Hostname func() (string, error)
}

// NewRootCommand creates a Cobra root and binds shared configuration flags.
//
// It panics if two registered commands (or aliases) share a name: command
// registration is static, so a duplicate is a programming error that must not
// ship silently.
func NewRootCommand(options Options) *cobra.Command {
	root, _ := newRoot(options)

	return root
}

func newRoot(options Options) (*cobra.Command, *Factory) {
	streams := normalizedStreams(options.Streams)
	factory := &Factory{
		Streams:       streams,
		ClientBuilder: options.ClientBuilder,
		Getenv:        options.Getenv,
		isTerminal:    options.IsTerminal,
		readSecret:    options.ReadSecret,
		now:           options.Now,
		hostname:      options.Hostname,
		Logger: slog.New(slog.NewJSONHandler(streams.ErrOut,
			&slog.HandlerOptions{Level: slog.LevelInfo})),
	}
	var configPath string

	root := &cobra.Command{
		Use:           "machinarchy",
		Short:         "Manage Machinarchy environments and hosts",
		SilenceErrors: true,
		SilenceUsage:  true,
		// Completion stays available but is not listed in help or menus:
		// people should not have to know about a script generator.
		CompletionOptions: cobra.CompletionOptions{HiddenDefaultCmd: true},
	}
	root.SetIn(streams.In)
	root.SetOut(streams.Out)
	root.SetErr(streams.ErrOut)

	// Global flags. Every flag except --config, --yes, --confirm, --confirm-env
	// and --dry-run resolves with the precedence flag > MACHINARCHY_<NAME> >
	// saved defaults > default
	// (config/settings.go). The four exceptions are flag-only on purpose.
	flags := root.PersistentFlags()
	flags.StringVar(&configPath, "config", "", "CLI config file (default: user config directory)")
	flags.StringP("env", "n", "", "Machinarchy environment name or ID")
	flags.StringP("output", "o", "", "Output format: table, json, or yaml (default depends on the command)")
	flags.BoolP("verbose", "v", false, "Enable debug logging")
	flags.String("server", "", "Saved server name, or an address for this command only (MACHINARCHY_SERVER)")
	flags.String("request-timeout", "", "Timeout for each API request, such as 10s or 2m (default 30s)")
	flags.BoolP("quiet", "q", false, "Suppress status output; print only identifiers where supported")
	flags.Bool("no-headers", false, "Omit table headers")
	flags.Bool("no-input", false, "Never prompt; fail if input or confirmation is required")
	flags.BoolP("yes", "y", false, "Confirm destructive or sensitive actions without prompting")
	flags.Bool("plain", false, "Plain output: no colour, symbols or flavour text (MACHINARCHY_PLAIN=1)")

	// Answers to confirmations and the dry run belong to commands that do not
	// exist yet; they are accepted and hidden so scripts can be written ahead.
	flags.Bool("dry-run", false, "Show what would happen and change nothing")
	flags.String("confirm", "", "Answer a type-the-name confirmation without a prompt")
	flags.String("confirm-env", "", "Answer a protected-environment confirmation without a prompt")
	for _, name := range []string{"dry-run", "confirm", "confirm-env"} {
		if err := flags.MarkHidden(name); err != nil {
			panic(err.Error()) // the flag was defined just above
		}
	}

	root.PersistentPreRunE = func(command *cobra.Command, _ []string) error {
		// Cobra validates the command line before this hook runs, so reaching
		// it means the invocation was well formed. Anything that fails later
		// is a runtime failure, not a usage error.
		factory.started = true
		factory.needsOwner = command.Annotations[AnnotationOwner] == "true"

		var err error
		if skipsConfig(command) {
			err = factory.LoadSettings(command.Root().PersistentFlags())
		} else {
			err = factory.LoadConfig(configPath, command.Root().PersistentFlags())
		}
		if err != nil {
			return err
		}

		return factory.checkDryRun(command)
	}

	registered := map[string]string{"help": "cobra", "completion": "cobra"}
	groupIDs := make(map[string]bool)
	for _, group := range options.Groups {
		if group.ID != "" && !groupIDs[group.ID] {
			title := group.Title
			if title == "" {
				title = group.ID
			}
			root.AddGroup(&cobra.Group{ID: group.ID, Title: title})
			groupIDs[group.ID] = true
		}
		for _, build := range group.Commands {
			if build == nil {
				continue
			}
			child := build(factory)
			if child == nil {
				continue
			}
			for _, name := range append([]string{child.Name()}, child.Aliases...) {
				if owner, taken := registered[name]; taken {
					panic(fmt.Sprintf("cli: command name %q is registered twice (%s and %s)", name, owner, child.Name()))
				}
				registered[name] = child.Name()
			}
			if group.ID != "" {
				child.GroupID = group.ID
			}
			root.AddCommand(child)
		}
	}

	normalizeGroupCommands(root)

	return root, factory
}

// normalizeGroupCommands makes every command group (a command that only has
// subcommands) behave the same way:
//
//   - With no arguments it prints its help and succeeds.
//   - With an unknown subcommand it fails with a usage error. Cobra checks
//     arguments only for runnable commands and otherwise falls back to help
//     with a zero exit status, which would make a typo such as
//     "machinarchy secret lst" look like success to a script.
//   - It never needs the config file, so help for a group works on a fresh or
//     damaged installation.
func normalizeGroupCommands(parent *cobra.Command) {
	for _, child := range parent.Commands() {
		if !child.HasSubCommands() {
			continue
		}

		if !child.Runnable() {
			child.RunE = showHelp
			if child.Args == nil {
				child.Args = cobra.NoArgs
			}
			if child.Annotations == nil {
				child.Annotations = make(map[string]string)
			}
			if _, set := child.Annotations[AnnotationSkipConfig]; !set {
				child.Annotations[AnnotationSkipConfig] = "true"
			}
		}

		normalizeGroupCommands(child)
	}
}

func showHelp(command *cobra.Command, _ []string) error {
	return command.Help()
}

// skipsConfig reports whether command (or an ancestor below the root) must run
// without reading the config file.
func skipsConfig(command *cobra.Command) bool {
	if command == nil {
		return false
	}

	// Explicit exemptions apply only to the selected command.
	if command.Annotations[AnnotationSkipConfig] == "true" {
		return true
	}

	// Detect Cobra's built-in help and completion commands.
	// These exemptions must also work when Cobra resolves a nested
	// command belonging to one of its built-in commands.
	for current := command; current != nil; current = current.Parent() {
		parent := current.Parent()

		if parent != nil && !parent.HasParent() {
			switch current.Name() {
			case "help",
				"completion",
				cobra.ShellCompRequestCmd,
				cobra.ShellCompNoDescRequestCmd:
				return true
			}
		}
	}

	return false
}

func normalizedStreams(streams IOStreams) IOStreams {
	if streams.In == nil {
		streams.In = os.Stdin
	}
	if streams.Out == nil {
		streams.Out = os.Stdout
	}
	if streams.ErrOut == nil {
		streams.ErrOut = os.Stderr
	}
	return streams
}
