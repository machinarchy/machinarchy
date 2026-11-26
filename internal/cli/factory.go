package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/config"
	"github.com/machinarchy/machinarchy/internal/cli/cred"
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

	// readSecret reads a line without echo. Tests replace it; nil reads from
	// the terminal.
	readSecret func(prompt string) (string, error)

	now      func() time.Time
	hostname func() (string, error)

	// creds keeps the saved credentials; nil when no config file was selected.
	creds cred.Store

	// needsOwner is set when the running command acts as the owner.
	needsOwner bool

	// input is the one buffered reader over Streams.In: a reader per question
	// would lose what an earlier one had buffered.
	input *bufio.Reader
}

// LoadConfig reads the selected configuration file and binds the root flags
// and environment overrides.
func (f *Factory) LoadConfig(path string, flags *pflag.FlagSet) error {
	store, err := config.Load(path, flags)
	if err != nil {
		return err
	}
	f.creds = cred.NewFile(config.CredentialsPath(store.Path()))

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

	return nil
}

func (f *Factory) getenv(key string) string {
	if f.Getenv != nil {
		return f.Getenv(key)
	}

	return os.Getenv(key)
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

// Logotype returns the logotype for output written to w, followed by a blank
// line, or "" when w is not a terminal or plain output was asked for
// (07-brand.md section 4).
func (f *Factory) Logotype(w io.Writer) string {
	if f == nil || f.Plain() || !ui.IsTerminal(w) {
		return ""
	}

	logo := f.StyleFor(w).Logotype()
	if logo == "" {
		return ""
	}

	return logo + "\n"
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

	line, err := f.reader().ReadString('\n')
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

// ConfirmTyped asks the person to type name before something is removed or
// revoked. --yes does not answer it: removing a thing is confirmed by naming
// it, on a terminal by typing it and in a script with --confirm NAME.
func (f *Factory) ConfirmTyped(what, name string) error {
	if given := f.ConfirmName(); given != "" {
		if given == name {
			return nil
		}

		return clierr.Detailed(clierr.ExitUsage, fmt.Errorf("--confirm is %q, but this is about %q", given, name), "", "--confirm "+name)
	}

	if f.config.NoInput() || !f.interactive() {
		return clierr.Detailed(clierr.ExitUsage,
			fmt.Errorf("this needs confirmation, and --yes is not enough here (%s)", what),
			"removing something is confirmed by naming it", "--confirm "+name)
	}

	if _, err := fmt.Fprintf(f.Streams.ErrOut, "%s\nType %q to confirm: ", what, name); err != nil {
		return err
	}
	line, err := f.reader().ReadString('\n')
	if err != nil && line == "" {
		return clierr.Newf(clierr.ExitFailure, "", "aborted; nothing was changed")
	}
	if strings.TrimSpace(line) != name {
		return clierr.Newf(clierr.ExitFailure, "", "aborted; nothing was changed")
	}

	return nil
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

func isTerminal(reader io.Reader) bool { return ui.IsTerminalReader(reader) }

// NewAPIClient resolves the server and the credential this command runs with
// and constructs its API client. A command marked AnnotationOwner gets the
// saved login session or an explanation, never an API token.
func (f *Factory) NewAPIClient(ctx context.Context) (api.Client, error) {
	if ctx == nil {
		return nil, errors.New("command context is required")
	}
	target, err := f.ResolveTarget()
	if err != nil {
		return nil, err
	}

	return f.ClientFor(target)
}

// ResolveTarget decides the server and the credential the running command
// uses: the server --server or MACHINARCHY_SERVER names, else the active one;
// the credential MACHINARCHY_TOKEN, else the saved API token, else the saved
// login session. A command marked AnnotationOwner takes the saved login
// session or stops with an explanation.
func (f *Factory) ResolveTarget() (config.Target, error) {
	if f == nil || f.config == nil {
		return config.Target{}, errors.New("CLI configuration is not initialized")
	}

	need := config.NeedAny
	if f.needsOwner {
		need = config.NeedOwner
	}

	return f.config.Resolve(f.creds, need, f.Now())
}

// ClientFor constructs the API client for an explicit target, such as the
// credential a login has just received and not yet saved.
func (f *Factory) ClientFor(target config.Target) (api.Client, error) {
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
	if !f.interactive() || f.config.NoInput() {
		return "", ErrNoTerminal
	}
	if f.readSecret != nil {
		return f.readSecret(prompt)
	}
	file, ok := f.Streams.In.(*os.File)
	if !ok {
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

// reader returns the one buffered reader over standard input.
func (f *Factory) reader() *bufio.Reader {
	if f.input == nil {
		f.input = bufio.NewReader(f.Streams.In)
	}

	return f.input
}

// Prompter returns a prompter for the conversation of one command. It asks
// nothing, and returns ui.ErrNoInput, when input is not a terminal or
// --no-input is set.
func (f *Factory) Prompter() *ui.Prompter {
	interactive := f.interactive() && !f.config.NoInput()
	prompter := &ui.Prompter{In: f.reader(), Out: f.Streams.ErrOut, Interactive: interactive}
	if interactive {
		prompter.ReadSecret = f.ReadSecret
	}

	return prompter
}

// Now returns the current time.
func (f *Factory) Now() time.Time {
	if f.now != nil {
		return f.now()
	}

	return time.Now()
}

// Hostname returns the computer's name.
func (f *Factory) Hostname() (string, error) {
	if f.hostname != nil {
		return f.hostname()
	}

	return os.Hostname()
}

// Credentials returns the saved-credential store; nil when no config file was
// selected (commands that skip the config).
func (f *Factory) Credentials() cred.Store { return f.creds }
