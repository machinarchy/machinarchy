package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/config"
	"github.com/machinarchy/machinarchy/internal/cli/cred"
	"github.com/machinarchy/machinarchy/internal/cli/fsutil"
	"github.com/machinarchy/machinarchy/internal/cli/msg"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
	"github.com/machinarchy/machinarchy/internal/cli/ui"
)

const (
	// maxPasswordInput bounds a password read from stdin (bcrypt uses 72 bytes).
	maxPasswordInput = 1024
	// deviceTokenDays is how long a device token lasts.
	deviceTokenDays = 90
	// deviceTokenRole is the role of a device token: never more than operator,
	// so a stolen laptop cannot become an owner.
	deviceTokenRole = "operator"
)

type loginOptions struct {
	withToken      bool
	name           string
	email          string
	passwordStdin  bool
	stay, noStay   bool
	insecureHTTP   bool
	caFile         string
	serverArgument string
}

// NewLoginCommand signs in to a server: with an email address and password
// (a login session), or with an API token. It asks what it needs on a
// terminal and takes everything from flags and stdin when it cannot ask
// (docs/cli/spec/02-commands.md 3.2).
func NewLoginCommand(factory *cli.Factory) *cobra.Command {
	var o loginOptions
	command := &cobra.Command{
		Use:   "login [SERVER]",
		Short: "Sign in to a server",
		Long: "Signs in to a Machinarchy server with an email address and password, or with an API token (--with-token).\n" +
			"With no SERVER it signs in to the active server again. The password and the token are never taken from\n" +
			"a flag: they are typed without echo, or read from stdin.",
		Example: "  machinarchy login https://machinarchy.example.com\n" +
			"  machinarchy login                      # sign in to the active server again\n" +
			"  printf '%s' \"$PASSWORD\" | machinarchy login https://machinarchy.example.com --email admin@example.com --password-stdin\n" +
			"  printf '%s' \"$TOKEN\" | machinarchy login https://machinarchy.example.com --with-token",
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if len(args) == 1 {
				o.serverArgument = args[0]
			}

			err := runLogin(command, factory, o)
			if errors.Is(err, ui.ErrBack) {
				// "back" has nothing to go back to in the middle of a sign-in
				// that has not saved anything: it ends it, and nothing changed.
				return clierr.Newf(clierr.ExitFailure, "", "aborted; nothing was changed")
			}

			return err
		},
	}

	flags := command.Flags()
	flags.BoolVar(&o.withToken, "with-token", false, "Sign in with an API token read from a hidden prompt or from stdin")
	flags.StringVar(&o.name, "name", "", "Name to save the server under (default: derived from its address)")
	flags.StringVar(&o.email, "email", "", "Email address or username to sign in with")
	flags.BoolVar(&o.passwordStdin, "password-stdin", false, "Read the password from stdin")
	flags.BoolVar(&o.stay, "stay-signed-in", false, "Save a 90-day device token so you need not sign in with the password every day")
	flags.BoolVar(&o.noStay, "no-stay-signed-in", false, "Do not offer to save a device token")
	flags.BoolVar(&o.insecureHTTP, "insecure-http", false, "Allow plain http:// to a host that is not on this computer (a network you trust)")
	flags.StringVar(&o.caFile, "ca-file", "", "PEM file with the certificate authority of this server (a private CA or self-signed certificate)")
	command.MarkFlagsMutuallyExclusive("stay-signed-in", "no-stay-signed-in")

	return command
}

// target is the server a login is about, with the choices saved for it.
type loginTarget struct {
	name     string // empty until decided
	url      string
	server   config.Server
	existing bool // already saved
}

func runLogin(command *cobra.Command, f *cli.Factory, o loginOptions) error {
	ctx := command.Context()
	prompter := f.Prompter()
	store := f.Config()
	errOut := command.ErrOrStderr()

	if len(store.ServerNames()) == 0 {
		// The first screen of the first sign-in (07-brand.md section 4).
		if _, err := io.WriteString(errOut, f.Logotype(errOut)); err != nil {
			return err
		}
	}

	target, err := chooseServer(prompter, store, o)
	if err != nil {
		return err
	}
	if err := config.CheckTransport(target.url, target.server.InsecureHTTP); err != nil {
		return err
	}

	endpoint := api.Endpoint{URL: target.url, Timeout: requestTimeout(store), CAFile: store.CAPath(target.server.CAFile)}
	if o.caFile != "" {
		endpoint.CAFile = o.caFile
	}
	if _, err := fmt.Fprintf(errOut, "Checking %s ... ", target.url); err != nil {
		return err
	}
	if err := api.Ping(ctx, endpoint); err != nil {
		_, _ = fmt.Fprintln(errOut)

		return err
	}
	if _, err := fmt.Fprintln(errOut, f.Style().Marker(ui.SymOK)+" reached."); err != nil {
		return err
	}

	if err := chooseName(prompter, store, &target, o); err != nil {
		return err
	}

	method, err := chooseMethod(prompter, o)
	if err != nil {
		return err
	}

	var saved []cred.Credential
	var detail string
	switch method {
	case methodToken:
		credential, text, err := signInWithToken(ctx, command, f, prompter, endpoint)
		if err != nil {
			return err
		}
		saved, detail = []cred.Credential{credential}, text
	default:
		credentials, text, err := signInWithPassword(ctx, command, f, prompter, endpoint, o)
		if err != nil {
			return err
		}
		saved, detail = credentials, text
	}

	if err := saveLogin(f, target, o, saved); err != nil {
		return err
	}

	user := saved[0].User
	next := "machinarchy whoami"
	message := msg.Get("auth.signed_in").With(msg.Vars{"server": target.name, "user": user, "detail": detail, "next": next})
	if _, err := fmt.Fprint(command.OutOrStdout(), ui.Render(message, f.StyleFor(command.OutOrStdout()))); err != nil {
		return err
	}

	if len(saved) == 2 {
		device := saved[1]
		message := msg.Get("auth.device_token_saved").With(msg.Vars{"role": device.Role, "days": strconv.Itoa(deviceTokenDays)})
		if _, err := fmt.Fprint(command.OutOrStdout(), ui.Render(message, f.StyleFor(command.OutOrStdout()))); err != nil {
			return err
		}
	}

	showCompletionHint(command, f, prompter.Interactive)

	return nil
}

// showCompletionHint prints the one-time completion line after the first
// sign-in on a terminal, on stderr (docs/cli/spec/03-ux.md section 11). It is remembered
// in the config, so it appears once per computer, and never blocks a login.
func showCompletionHint(command *cobra.Command, f *cli.Factory, interactive bool) {
	store := f.Config()
	if !interactive || f.Plain() || store.Quiet() || store.HintSeen("completion") {
		return
	}
	out := command.ErrOrStderr() // guidance, not data: stdout stays clean for pipes
	if _, err := fmt.Fprint(out, ui.Render(msg.Get("hint.completion"), f.StyleFor(out))); err != nil {
		return
	}
	if err := store.MarkHintSeen("completion"); err != nil {
		return // a failed save only repeats the hint
	}
}

// chooseServer works out which server to sign in to: the argument (a saved name
// or an address), the active server when there is no argument, or the answer
// to a question.
func chooseServer(p *ui.Prompter, store *config.Store, o loginOptions) (loginTarget, error) {
	argument := strings.TrimSpace(o.serverArgument)

	if argument == "" && store.ActiveName() != "" {
		if saved, ok := store.Server(store.ActiveName()); ok {
			return loginTarget{name: store.ActiveName(), url: saved.URL, server: applyFlags(saved, o), existing: true}, nil
		}
	}

	if argument == "" {
		answer, err := p.Line("Server address (for example https://machinarchy.example.com)", "")
		if errors.Is(err, ui.ErrNoInput) {
			return loginTarget{}, clierr.Detailed(clierr.ExitUsage, errors.New("the server address is required when nothing can be asked"), "",
				"machinarchy login https://your-server")
		}
		if err != nil {
			return loginTarget{}, err
		}
		argument = answer
	}

	if saved, ok := store.Server(argument); ok {
		return loginTarget{name: argument, url: saved.URL, server: applyFlags(saved, o), existing: true}, nil
	}

	address, err := config.NormalizeURL(argument)
	if err != nil {
		return loginTarget{}, err
	}
	for _, name := range store.ServerNames() {
		if saved, _ := store.Server(name); strings.EqualFold(saved.URL, address) {
			return loginTarget{name: name, url: saved.URL, server: applyFlags(saved, o), existing: true}, nil
		}
	}

	return loginTarget{url: address, server: applyFlags(config.Server{URL: address}, o)}, nil
}

func applyFlags(server config.Server, o loginOptions) config.Server {
	if o.insecureHTTP {
		server.InsecureHTTP = true
	}

	return server
}

// chooseName decides what a new server is saved as.
func chooseName(p *ui.Prompter, store *config.Store, target *loginTarget, o loginOptions) error {
	if target.existing {
		if o.name != "" && o.name != target.name {
			return clierr.Detailed(clierr.ExitUsage, fmt.Errorf("%s is already saved as %q", target.url, target.name), "",
				"machinarchy login "+target.name)
		}

		return nil
	}

	name := o.name
	if name == "" {
		name = config.DefaultServerName(target.url)
		if answer, err := p.Line("Name for this server", name); err == nil {
			name = answer
		} else if !errors.Is(err, ui.ErrNoInput) {
			return err
		}
	}
	if _, taken := store.Server(name); taken {
		return clierr.Detailed(clierr.ExitUsage, fmt.Errorf("a server called %q is already saved for a different address", name), "",
			"machinarchy login "+target.url+" --name OTHER-NAME")
	}
	target.name = name

	return nil
}

type signInMethod int

const (
	methodPassword signInMethod = iota
	methodToken
)

func chooseMethod(p *ui.Prompter, o loginOptions) (signInMethod, error) {
	switch {
	case o.withToken:
		return methodToken, nil
	case o.email != "" || o.passwordStdin:
		return methodPassword, nil
	}

	choice, err := p.Choice("How do you want to sign in?", []string{
		"Email and password    best on your own computer",
		"API token             for scripts and shared machines",
	}, 0)
	if errors.Is(err, ui.ErrNoInput) {
		return methodPassword, nil // the missing pieces are named below
	}
	if err != nil {
		return methodPassword, err
	}
	if choice == 1 {
		return methodToken, nil
	}

	return methodPassword, nil
}

// signInWithPassword asks for an email address and a password, signs in, and
// optionally creates a device token. It returns the credentials to save: the
// session first, then the device token when there is one.
func signInWithPassword(ctx context.Context, command *cobra.Command, f *cli.Factory, p *ui.Prompter, endpoint api.Endpoint, o loginOptions) ([]cred.Credential, string, error) {
	identifier := strings.TrimSpace(o.email)
	if identifier == "" {
		answer, err := p.Line("Email or username", "")
		if errors.Is(err, ui.ErrNoInput) {
			return nil, "", inputRequired("--email", "machinarchy login SERVER --email you@example.com --password-stdin")
		}
		if err != nil {
			return nil, "", err
		}
		identifier = answer
	}

	password, err := readPassword(command, p, o)
	if err != nil {
		return nil, "", err
	}
	if password == "" {
		return nil, "", clierr.Usagef("the password is empty")
	}

	result, err := api.Login(ctx, endpoint, identifier, password)
	if err != nil {
		return nil, "", loginFailed(err)
	}
	user := result.User.Label()
	if user == "" {
		user = identifier
	}

	now := f.Now()
	session := cred.Credential{Kind: cred.KindSession, Secret: result.Token, User: user}
	detail := "Role: not reported by this server."

	// The session's expiry comes from the server, not from a guess.
	authed, err := f.ClientFor(config.Target{Server: endpoint.URL, Token: result.Token, RequestTimeout: endpoint.Timeout, CAFile: endpoint.CAFile})
	if err != nil {
		return nil, "", err
	}
	if auth, ok := authed.(api.AuthClient); ok {
		if sessions, err := auth.Sessions(ctx); err == nil {
			for _, s := range sessions {
				if s.Current && !s.ExpiresAt.IsZero() {
					expires := s.ExpiresAt
					session.ExpiresAt = &expires
					detail = fmt.Sprintf("The session lasts %s (until %s). %s", humanDuration(expires.Sub(now)),
						expires.Local().Format("2006-01-02 15:04"), detail)
				}
			}
		}
	}

	credentials := []cred.Credential{session}

	stay, err := wantsToStay(p, o)
	if err != nil {
		return nil, "", err
	}
	if stay {
		if device, err := createDeviceToken(ctx, f, authed, user, now); err != nil {
			warning := msg.Get("auth.device_token_failed").With(msg.Vars{"why": sanitize.Line(err.Error())})
			_, _ = fmt.Fprint(command.ErrOrStderr(), ui.Render(warning, f.Style()))
		} else {
			credentials = append(credentials, device)
			// whoami reports the device token's role, so do not claim here that
			// the role is unknown.
			detail = strings.TrimSpace(strings.ReplaceAll(detail, "Role: not reported by this server.", ""))
		}
	}

	return credentials, detail, nil
}

// readPassword reads the password from stdin (--password-stdin) or from a
// hidden prompt. It is never read from a flag or the environment.
func readPassword(command *cobra.Command, p *ui.Prompter, o loginOptions) (string, error) {
	if o.passwordStdin {
		data, err := io.ReadAll(io.LimitReader(command.InOrStdin(), maxPasswordInput+1))
		if err != nil {
			return "", fmt.Errorf("read the password from stdin: %w", err)
		}
		if len(data) > maxPasswordInput {
			return "", clierr.Usagef("the password on stdin is longer than %d bytes", maxPasswordInput)
		}

		return strings.TrimRight(string(data), "\r\n"), nil
	}

	password, err := p.Secret("Password")
	if errors.Is(err, ui.ErrNoInput) {
		return "", inputRequired("--password-stdin", "printf '%s' \"$PASSWORD\" | machinarchy login SERVER --email you@example.com --password-stdin")
	}

	return password, err
}

// wantsToStay decides whether to create a device token: the flags win; on a
// terminal the person is asked once, with yes as the default; in a script it
// is off unless asked for.
func wantsToStay(p *ui.Prompter, o loginOptions) (bool, error) {
	switch {
	case o.stay:
		return true, nil
	case o.noStay:
		return false, nil
	}

	answer, err := p.YesNo(fmt.Sprintf("Stay signed in on this computer? A %d-day device token (%s) will be saved, so you need not type your password every day", deviceTokenDays, deviceTokenRole), true)
	if errors.Is(err, ui.ErrNoInput) {
		return false, nil
	}

	return answer, err
}

// createDeviceToken creates the operator token that "stay signed in" saves,
// named after this computer and the day.
func createDeviceToken(ctx context.Context, f *cli.Factory, client api.Client, user string, now time.Time) (cred.Credential, error) {
	access, ok := client.(api.AccessClient)
	if !ok {
		return cred.Credential{}, errors.New("this client cannot create tokens")
	}

	host, err := f.Hostname()
	if err != nil || host == "" {
		host = "computer"
	}
	name := deviceTokenName(host, now)

	issued, err := access.CreateAPIToken(ctx, name, deviceTokenRole, deviceTokenDays*24)
	if err != nil {
		return cred.Credential{}, err
	}

	credential := cred.Credential{Kind: cred.KindDeviceToken, Secret: issued.Secret, User: user, Name: name, Role: deviceTokenRole, TokenID: issued.ID}
	if issued.ExpiresAt != nil {
		credential.ExpiresAt = issued.ExpiresAt
	} else {
		expires := now.Add(deviceTokenDays * 24 * time.Hour)
		credential.ExpiresAt = &expires
	}

	return credential, nil
}

var nameUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// deviceTokenName builds "device-<computer>-<yyyymmdd>".
func deviceTokenName(host string, now time.Time) string {
	short, _, _ := strings.Cut(strings.ToLower(host), ".")
	short = strings.Trim(nameUnsafe.ReplaceAllString(short, "-"), "-")
	if short == "" {
		short = "computer"
	}
	if len(short) > 30 {
		short = short[:30]
	}

	return "device-" + short + "-" + now.Format("20060102")
}

// signInWithToken reads an API token, checks it with the server before saving
// it, and returns the credential.
func signInWithToken(ctx context.Context, command *cobra.Command, f *cli.Factory, p *ui.Prompter, endpoint api.Endpoint) (cred.Credential, string, error) {
	token, err := readToken(command, p)
	if err != nil {
		return cred.Credential{}, "", err
	}
	if err := config.ValidateToken(token); err != nil {
		return cred.Credential{}, "", err
	}

	client, err := f.ClientFor(config.Target{Server: endpoint.URL, Token: token, Kind: cred.KindToken, RequestTimeout: endpoint.Timeout, CAFile: endpoint.CAFile})
	if err != nil {
		return cred.Credential{}, "", err
	}
	auth, ok := client.(api.AuthClient)
	if !ok {
		return cred.Credential{}, "", errors.New("this client cannot check a token")
	}

	user, err := auth.Me(ctx)
	if err != nil {
		var apiErr *api.Error
		if errors.As(err, &apiErr) && apiErr.Status == 401 {
			return cred.Credential{}, "", clierr.Detailed(clierr.ExitAuth,
				errors.New("the server did not accept this token"), "it may be wrong, expired or revoked", "machinarchy login --with-token")
		}

		return cred.Credential{}, "", err
	}

	// /api/v1/me does not report the token's role or expiry, so they are shown
	// as unknown instead of being guessed.
	detail := "Role: not reported by this server. Expires: unknown."

	return cred.Credential{Kind: cred.KindToken, Secret: token, User: user.Label()}, detail, nil
}

// readToken reads the token from a hidden prompt on a terminal, or from stdin.
func readToken(command *cobra.Command, p *ui.Prompter) (string, error) {
	if p.Interactive {
		token, err := p.Secret("API token")
		if err != nil {
			return "", err
		}

		return strings.TrimSpace(token), nil
	}

	data, err := io.ReadAll(io.LimitReader(command.InOrStdin(), config.MaxTokenBytes+1))
	if err != nil {
		return "", fmt.Errorf("read the token from stdin: %w", err)
	}
	if len(data) > config.MaxTokenBytes {
		return "", clierr.Usagef("the token on stdin exceeds %d bytes", config.MaxTokenBytes)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", inputRequired("a token on stdin", "printf '%s' \"$TOKEN\" | machinarchy login SERVER --with-token")
	}

	return token, nil
}

// saveLogin saves the server and its credentials. The CA file is copied next
// to the config so the server keeps working when the original is moved.
func saveLogin(f *cli.Factory, target loginTarget, o loginOptions, credentials []cred.Credential) error {
	store := f.Config()
	server := target.server

	if o.caFile != "" {
		saved, err := copyCA(store, target.name, o.caFile)
		if err != nil {
			return err
		}
		server.CAFile = saved
	}
	server.URL = target.url

	if err := store.SaveServer(target.name, server); err != nil {
		return err
	}
	if f.Credentials() == nil {
		return errors.New("no credential store is available")
	}
	for _, credential := range credentials {
		if err := f.Credentials().Put(target.name, credential); err != nil {
			return err
		}
	}

	return nil
}

// copyCA copies a CA file to ca/NAME.pem next to the config file, 0600, and
// returns the path relative to the config directory.
func copyCA(store *config.Store, name, source string) (string, error) {
	data, err := os.ReadFile(source) // #nosec G304 -- the CA file the person chose with --ca-file
	if err != nil {
		return "", clierr.Detailed(clierr.ExitUsage, fmt.Errorf("read the CA file %q: %w", source, err), "", "Check the path.")
	}
	if !bytes.Contains(data, []byte("BEGIN CERTIFICATE")) {
		return "", clierr.Detailed(clierr.ExitUsage, fmt.Errorf("the CA file %q holds no PEM certificate", source), "",
			"Give a PEM file with the certificate of your server's CA.")
	}

	relative := filepath.Join("ca", name+".pem")
	if err := fsutil.WriteAtomic(store.CAPath(relative), data, 0o700, 0o600); err != nil {
		return "", fmt.Errorf("save the CA file: %w", err)
	}

	return relative, nil
}

func requestTimeout(store *config.Store) time.Duration {
	timeout, err := store.RequestTimeout()
	if err != nil {
		return config.DefaultRequestTimeout
	}

	return timeout
}

func inputRequired(flag, next string) error {
	m := msg.Get("input.required").With(msg.Vars{"flag": flag, "why": "input is not a terminal or --no-input is set", "next": next})

	return clierr.Detailed(clierr.ExitUsage, errors.New(m.Primary), m.Why, m.Next)
}

// loginFailed turns the server's answer to a sign-in into a message that does
// not say which part was wrong.
func loginFailed(err error) error {
	var apiErr *api.Error
	if !errors.As(err, &apiErr) {
		return err
	}

	switch apiErr.Status {
	case 401:
		return clierr.Detailed(clierr.ExitAuth, errors.New("sign-in failed: the email or password is not right"), "", "machinarchy login")
	case 429:
		wait := "a moment"
		if apiErr.RetryAfter > 0 {
			wait = humanDuration(apiErr.RetryAfter)
		}

		return clierr.Detailed(clierr.ExitUnavailable, errors.New("too many sign-in attempts"), "try again in "+wait, "machinarchy login")
	}

	return err
}

// humanDuration writes a duration the way a person would say it.
func humanDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Round(24*time.Hour)/(24*time.Hour)))
	case d >= time.Hour:
		return plural(int(d.Round(time.Hour)/time.Hour), "hour")
	case d >= time.Minute:
		return plural(int(d.Round(time.Minute)/time.Minute), "minute")
	default:
		return plural(max(int(d.Round(time.Second)/time.Second), 0), "second")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}

	return fmt.Sprintf("%d %ss", n, unit)
}
