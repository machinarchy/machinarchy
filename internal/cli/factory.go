package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/config"
	"github.com/machinarchy/machinarchy/internal/cli/deprecate"
	"github.com/machinarchy/machinarchy/internal/cli/msg"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/machinarchy/machinarchy/internal/cli/ui"
)

// IOStreams keeps command input, output, and diagnostics independently injectable.
type IOStreams struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer
}

// APIClientBuilder creates the narrow API client used by a command.
type APIClientBuilder func(config.Target) (api.Client, error)

// Factory is the command dependency boundary. Commands receive it explicitly
// instead of reaching into package globals or constructing clients themselves.
type Factory struct {
	Streams       IOStreams
	Logger        *slog.Logger
	ClientBuilder APIClientBuilder

	// Getenv reads the environment for terminal detection (NO_COLOR, TERM,
	// locale, COLUMNS). Nil selects os.Getenv; tests replace it.
	Getenv func(string) string

	config *config.Store

	// style is how output to the diagnostics stream looks; set by install.
	style ui.Style

	// yes is read from the --yes flag only, never from the environment or the
	// config file, so a confirmation bypass is always an explicit choice.
	yes bool

	// started records that command-line parsing succeeded and the selected
	// command began to run. Errors before that point are usage errors.
	started bool

	// isTerminal reports whether a reader is an interactive terminal. Tests
	// replace it; nil selects the real check.
	isTerminal func(io.Reader) bool
}

// LoadConfig reads the selected configuration file and binds the root flags
// and environment overrides.
func (f *Factory) LoadConfig(path string, flags *pflag.FlagSet) error {
	store, err := config.Load(path, flags)
	if err != nil {
		return err
	}

	return f.install(store, flags)
}

// LoadSettings binds only the root flags and environment overrides and reads
// no config file. Commands that must work with a missing or damaged config
// file (help, version, completion) use it so global flags still apply.
func (f *Factory) LoadSettings(flags *pflag.FlagSet) error {
	store, err := config.LoadSettings(flags)
	if err != nil {
		return err
	}

	return f.install(store, flags)
}

func (f *Factory) install(store *config.Store, flags *pflag.FlagSet) error {
	// Validate global settings up front so a bad value fails the same way
	// whichever command was typed.
	if _, err := output.ParseFormat(store.OutputExplicit()); err != nil {
		return err
	}
	if _, err := store.RequestTimeout(); err != nil {
		return err
	}

	f.config = store

	f.yes = false
	if flags != nil {
		if value, err := flags.GetBool("yes"); err == nil {
			f.yes = value
		}
	}

	level := slog.LevelInfo
	if store.Verbose() {
		level = slog.LevelDebug
	}
	f.Logger = slog.New(slog.NewJSONHandler(f.Streams.ErrOut,
		&slog.HandlerOptions{Level: level}))

	f.style = ui.DetectStyle(f.getenv, f.Streams.ErrOut, store.Plain())
	if !deprecationWarningsOff(f.getenv) {
		for _, legacy := range store.LegacyEnv() {
			text := ui.Render(msg.Get("deprecate.env_var").With(msg.Vars{
				"old": legacy.Old, "new": legacy.New, "version": "the next release"}), f.style)
			if _, err := fmt.Fprint(f.Streams.ErrOut, text); err != nil {
				return err
			}
		}
	}

	for _, warning := range store.Warnings() {
		if _, err := fmt.Fprintf(f.Streams.ErrOut, "Warning: %s\n", warning); err != nil {
			return err
		}
	}

	return nil
}

func (f *Factory) getenv(key string) string {
	if f.Getenv != nil {
		return f.Getenv(key)
	}

	return os.Getenv(key)
}

// deprecationWarningsOff reports MACHINARCHY_NO_DEPRECATION_WARNINGS=1, the
// stop-gap for scripts that cannot change yet.
func deprecationWarningsOff(getenv func(string) string) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(getenv("MACHINARCHY_NO_DEPRECATION_WARNINGS")))

	return err == nil && value
}

// Style returns how output to the diagnostics stream (stderr) looks: symbols,
// colour and plain mode. Before the configuration is loaded it is the style
// the environment alone gives.
func (f *Factory) Style() ui.Style {
	if f == nil {
		return ui.Style{}
	}

	return f.style
}

// StyleFor returns the style for output written to w, which may be stdout.
func (f *Factory) StyleFor(w io.Writer) ui.Style {
	return ui.DetectStyle(f.getenv, w, f.Plain())
}

// Width returns the line width available on w.
func (f *Factory) Width(w io.Writer) int {
	return ui.Width(f.getenv, w)
}

// Plain reports whether plain output was asked for (--plain,
// MACHINARCHY_PLAIN, or a saved default).
func (f *Factory) Plain() bool {
	return f != nil && f.config.Plain()
}

// DryRun reports whether --dry-run was given.
func (f *Factory) DryRun() bool {
	return f != nil && f.config.DryRun()
}

// ConfirmName returns the value of --confirm.
func (f *Factory) ConfirmName() string {
	if f == nil {
		return ""
	}

	return f.config.ConfirmName()
}

// ConfirmEnv returns the value of --confirm-env.
func (f *Factory) ConfirmEnv() string {
	if f == nil {
		return ""
	}

	return f.config.ConfirmEnv()
}

// checkDryRun refuses --dry-run for a command that does not support it, so the
// flag can never be silently ignored while the command changes something.
func (f *Factory) checkDryRun(command *cobra.Command) error {
	if !f.DryRun() || command.Annotations[AnnotationDryRun] == "true" {
		return nil
	}

	return clierr.Newf(clierr.ExitUsage, "Run it without --dry-run, or read the command's help.",
		"%q does not support --dry-run yet, so it would make the change for real", command.CommandPath())
}

// printDeprecation tells the user, on stderr, that the selected command has a
// replacement. It does not change what the command does.
func (f *Factory) printDeprecation(command *cobra.Command) error {
	notice, deprecated := deprecate.For(command)
	if !deprecated || deprecationWarningsOff(f.getenv) {
		return nil
	}

	_, err := fmt.Fprint(f.Streams.ErrOut, ui.Render(notice.Message(), f.style))

	return err
}

// Config returns the loaded context configuration.
func (f *Factory) Config() *config.Store {
	if f == nil {
		return nil
	}
	return f.config
}

// Yes reports whether --yes was given.
func (f *Factory) Yes() bool {
	return f != nil && f.yes
}

// OutputFormat returns the format the user selected with --output or
// MACHINARCHY_OUTPUT, or defaultFormat when the user made no choice. A
// command picks its own default (a list is a table, a document is JSON).
func (f *Factory) OutputFormat(defaultFormat output.Format) (output.Format, error) {
	explicit := f.Config().OutputExplicit()
	if explicit == "" {
		return defaultFormat, nil
	}

	return output.ParseFormat(explicit)
}

// Printer returns a Printer for writer using the effective output format and
// the --quiet and --no-headers settings.
func (f *Factory) Printer(writer io.Writer, defaultFormat output.Format) (*output.Printer, error) {
	format, err := f.OutputFormat(defaultFormat)
	if err != nil {
		return nil, err
	}

	store := f.Config()

	return output.NewPrinter(writer, format, output.Options{
		NoHeaders: store.NoHeaders(),
		Quiet:     store.Quiet(),
	})
}

// Confirm asks the user to approve an action. It returns true when --yes was
// given or the user answers "y" or "yes". It never prompts without an
// interactive terminal (or with --no-input): then it fails with a usage error
// so an unattended run can never hang or proceed unconfirmed.
func (f *Factory) Confirm(prompt string) (bool, error) {
	if f.Yes() {
		return true, nil
	}

	required := clierr.Wrap(clierr.ExitUsage,
		errors.New("confirmation required"),
		"Re-run with --yes to proceed without a prompt.")

	if f.Config().NoInput() || !f.interactive() {
		return false, required
	}

	if _, err := fmt.Fprintf(f.Streams.ErrOut, "%s [y/N]: ", prompt); err != nil {
		return false, err
	}

	line, err := bufio.NewReader(f.Streams.In).ReadString('\n')
	if err != nil && line == "" {
		if errors.Is(err, io.EOF) {
			return false, required
		}

		return false, fmt.Errorf("read confirmation: %w", err)
	}

	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// RequireConfirmation is Confirm for callers that simply stop when the user
// declines.
func (f *Factory) RequireConfirmation(prompt string) error {
	confirmed, err := f.Confirm(prompt)
	if err != nil {
		return err
	}
	if !confirmed {
		return clierr.Newf(clierr.ExitFailure, "", "aborted; nothing was changed")
	}

	return nil
}

func (f *Factory) interactive() bool {
	check := f.isTerminal
	if check == nil {
		check = isTerminal
	}

	return check(f.Streams.In)
}

func isTerminal(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()

	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// NewAPIClient resolves the effective context and constructs its API client.
func (f *Factory) NewAPIClient(ctx context.Context) (api.Client, error) {
	if f == nil || f.config == nil {
		return nil, errors.New("CLI configuration is not initialized")
	}
	if ctx == nil {
		return nil, errors.New("command context is required")
	}
	target, err := f.config.Resolve()
	if err != nil {
		return nil, err
	}
	builder := f.ClientBuilder
	if builder == nil {
		builder = func(target config.Target) (api.Client, error) {
			return api.NewHTTPClient(target)
		}
	}
	client, err := builder(target)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("API client builder returned a nil client")
	}
	return client, nil
}

// NewManagementClient is NewAPIClient for runtime configuration and Secret
// Store administration.
func (f *Factory) NewManagementClient(ctx context.Context) (api.ManagementClient, error) {
	client, err := f.NewAPIClient(ctx)
	if err != nil {
		return nil, err
	}
	managementClient, ok := client.(api.ManagementClient)
	if !ok {
		return nil, errors.New("API client does not support configuration and Secret Store management")
	}
	return managementClient, nil
}

// ErrNoTerminal: ReadSecret has no interactive terminal to prompt on.
var ErrNoTerminal = errors.New("no terminal to prompt on")

// ReadSecret prompts on the terminal and reads a line without echo. It
// returns ErrNoTerminal when input is not an interactive terminal or
// --no-input is set; commands then offer a --*-stdin flag instead.
func (f *Factory) ReadSecret(prompt string) (string, error) {
	file, ok := f.Streams.In.(*os.File)
	if !ok || !f.interactive() || f.config.NoInput() {
		return "", ErrNoTerminal
	}
	if _, err := fmt.Fprint(f.Streams.ErrOut, prompt); err != nil {
		return "", err
	}
	value, err := term.ReadPassword(int(file.Fd())) // #nosec G115 -- a file descriptor fits in int
	_, _ = fmt.Fprintln(f.Streams.ErrOut)
	if err != nil {
		return "", fmt.Errorf("read from the terminal: %w", err)
	}
	return string(value), nil
}
