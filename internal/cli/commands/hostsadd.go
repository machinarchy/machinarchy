package commands

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/msg"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
	"github.com/machinarchy/machinarchy/internal/cli/ui"
)

// `hosts add` for machines reached over SSH (docs/cli/spec/02-commands.md
// section 3.8, 03-ux.md section 2.1). The agent path is in hostsagent.go.

// hostsAddPoll is how often the wait loop asks the server. Tests shorten it.
var hostsAddPoll = 2 * time.Second

// defaultAddWait is how long `hosts add` waits for the host, in total.
const defaultAddWait = 90 * time.Second

var dnsNamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`)

type hostsAddOptions struct {
	access, accessName, user, keyFile, hostKeyPolicy, trustFingerprint string
	tokenOut, natsURL, credsFile                                       string
	passwordStdin, agent, remember                                     bool
	tags                                                               []string
	wait, ttl                                                          time.Duration
}

func newHostsAddCommand(f *cli.Factory) *cobra.Command {
	var o hostsAddOptions
	command := &cobra.Command{
		Use:   "add [NAME [ADDRESS]]",
		Short: "Bring a machine reached over SSH under management (owner only)",
		Long: "Registers a machine, then waits for the server to see its SSH host key, asks you to check it and waits until the host\n" +
			"is ready. Without arguments it asks what it needs; type back to return to the previous question. Nothing is sent to the\n" +
			"server before every answer is in. At the end it prints the command that does the same without questions.\n" +
			"The key shown is the one the server saw, not one read from your computer. --yes does not approve a key: type yes\n" +
			"on a terminal, or pass --trust-fingerprint of a key you checked on the machine.\n" +
			"Without a terminal and without --trust-fingerprint the host is registered and left waiting for key approval.",
		Example: "  machinarchy hosts add\n" +
			"  machinarchy hosts add web-01 10.0.0.5 --access deploy --no-input\n" +
			"  machinarchy hosts add web-01 10.0.0.5 --user deploy --key-file ~/.ssh/id_ed25519 --trust-fingerprint SHA256:... --no-input",
		Args: cobra.MaximumNArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			return runHostsAdd(command, f, args, o)
		},
	}
	flags := command.Flags()
	flags.StringVar(&o.access, "access", "", "Use this existing access entry")
	if err := command.RegisterFlagCompletionFunc("access", completeAccessNames(f, false)); err != nil {
		panic(err.Error()) // the flag was defined just above
	}
	flags.StringVar(&o.accessName, "access-name", "", "Name of the access entry created on the way (default USER-key or USER-password)")
	flags.StringVar(&o.user, "user", "", "SSH login user (creates an access entry)")
	flags.StringVar(&o.keyFile, "key-file", "", "File with the SSH private key (creates an access entry)")
	flags.BoolVar(&o.passwordStdin, "password-stdin", false, "Read the SSH password from stdin (creates an access entry)")
	flags.StringVar(&o.hostKeyPolicy, "host-key-policy", "", "For a new access entry: approve (default), tofu (keys only) or pinned")
	flags.StringArrayVar(&o.tags, "tag", nil, "Tag as KEY=VALUE (repeatable)")
	flags.StringVar(&o.trustFingerprint, "trust-fingerprint", "", "Trust the host key with this fingerprint, if the server sees it (SHA256:...)")
	flags.DurationVar(&o.wait, "wait", defaultAddWait, "How long to wait for the host (0 registers it and returns)")
	flags.BoolVar(&o.agent, "agent", false, "Add a machine reached through an agent: registers it and prints the install blob (no address)")
	flags.DurationVar(&o.ttl, "ttl", time.Hour, "With --agent: how long the one-time token stays valid (1m to 24h)")
	flags.StringVar(&o.natsURL, "nats-url", "", "With --agent: broker address agents connect to (default: the saved one of this server)")
	flags.StringVar(&o.credsFile, "nats-creds-file", "", "With --agent: local bootstrap credential file to copy to the machine (default: the saved one)")
	flags.BoolVar(&o.remember, "remember", false, "With --agent: save --nats-url and --nats-creds-file as the defaults of this server")
	flags.StringVar(&o.tokenOut, "token-out", "", "With --agent: write the install blob to this new file (mode 0600)")

	return command
}

// hostsAddPlan is everything the questions collect. Nothing is sent before it is complete.
type hostsAddPlan struct {
	name, endpoint string
	// Either an existing entry or the data of a new one.
	existing              *api.AccessProfileDetail
	how, user, credential string
	policy                string
	prompted, asked       bool
}

// normaliseAddress turns HOST or HOST:PORT into HOST:PORT (port 22 by default).
func normaliseAddress(address string) (string, error) {
	bad := clierr.Usagef("the address must be HOST or HOST:PORT, for example 10.0.0.5 or web.example.com:2222 (got %q)", sanitize.Line(address))
	address = strings.TrimSpace(address)
	host, port := address, "22"
	switch {
	case strings.HasPrefix(address, "["):
		end := strings.Index(address, "]")
		if end < 0 {
			return "", bad
		}
		host = address[1:end]
		ip := net.ParseIP(host)
		if ip == nil || ip.To4() != nil {
			return "", bad
		}
		rest := address[end+1:]
		if rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return "", bad
			}
			port = rest[1:]
		}
		host = "[" + host + "]"
	case strings.Count(address, ":") == 1:
		host, port, _ = strings.Cut(address, ":")
		fallthrough
	default:
		if strings.Contains(host, ":") || !dnsNamePattern.MatchString(host) || len(host) > 253 {
			return "", bad
		}
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port {
		return "", bad
	}

	return net.JoinHostPort(strings.Trim(host, "[]"), port), nil
}

func validateHostName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 253 || strings.ContainsFunc(name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return clierr.Usagef("the host name must be 1-253 characters without spaces (got %q)", sanitize.Line(name))
	}

	return nil
}

func runHostsAdd(command *cobra.Command, f *cli.Factory, args []string, o hostsAddOptions) error {
	if flag := command.Flags().Lookup("env"); flag != nil && flag.Changed {
		return clierr.Detailed(clierr.ExitFailure, errors.New("this server cannot place a new host in an env yet"),
			"the server does not accept an env when a host is created (gap C-01); the host would land in the default env", "machinarchy hosts add NAME ADDRESS")
	}
	if o.agent {
		return runHostsAddAgent(command, f, args, o)
	}
	plan := &hostsAddPlan{}
	if len(args) > 0 {
		if err := validateHostName(args[0]); err != nil {
			return err
		}
		plan.name = strings.TrimSpace(args[0])
	}
	if len(args) > 1 {
		endpoint, err := normaliseAddress(args[1])
		if err != nil {
			return err
		}
		plan.endpoint = endpoint
	}
	labels, removals, err := parseLabels(o.tags)
	if err != nil {
		return err
	}
	if len(removals) > 0 {
		return clierr.Usagef("--tag takes KEY=VALUE; removing tags is done with 'hosts tag'")
	}
	if err := validateTags(labels, nil); err != nil {
		return err
	}
	if o.trustFingerprint != "" && !fingerprintShape.MatchString(o.trustFingerprint) {
		return clierr.Usagef("--trust-fingerprint %q is not a SHA256 fingerprint (SHA256:...)", sanitize.Line(o.trustFingerprint))
	}
	if o.wait < 0 {
		return clierr.Usagef("--wait cannot be negative")
	}
	creating := o.user != "" || o.keyFile != "" || o.passwordStdin || o.hostKeyPolicy != "" || o.accessName != ""
	switch {
	case o.access != "" && creating:
		return clierr.Usagef("--access names an existing entry; it cannot be combined with --user, --key-file, --password-stdin, --host-key-policy or --access-name")
	case o.keyFile != "" && o.passwordStdin:
		return clierr.Usagef("use one of --key-file and --password-stdin")
	}

	printer, err := f.Printer(command.OutOrStdout(), output.FormatTable)
	if err != nil {
		return err
	}
	client, _, inventory, err := accessClients(command.Context(), f)
	if err != nil {
		return err
	}
	prompter := f.Prompter()
	if o.access != "" {
		id, err := resolveProfile(command, client, o.access)
		if err != nil {
			return err
		}
		if plan.existing, err = sshEntry(command.Context(), inventory, id); err != nil {
			return err
		}
	}

	var entries []*api.AccessProfileDetail
	if prompter.Interactive && o.access == "" && o.keyFile == "" && !o.passwordStdin {
		if entries, err = sshEntries(command, client, inventory); err != nil {
			return err
		}
	}
	errOut := command.ErrOrStderr()
	if prompter.Interactive {
		if _, err := fmt.Fprintln(errOut, "Add a machine"); err != nil {
			return err
		}
	}
	if err := askHostsAdd(command, f, plan, o, entries); err != nil {
		return err
	}

	return executeHostsAdd(command, f, client, printer, plan, o, labels)
}

// sshEntry loads an access entry and refuses an agent entry.
func sshEntry(ctx context.Context, inventory api.InventoryClient, id string) (*api.AccessProfileDetail, error) {
	detail, err := inventory.GetAccessProfile(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("read access entry: %w", err)
	}
	if describeHow(detail.Spec) == howAgent {
		return nil, clierr.Detailed(clierr.ExitUsage, fmt.Errorf("access entry %q reaches machines through an agent", sanitize.Line(detail.Name)),
			"a host reached over SSH needs an SSH entry", "machinarchy access")
	}

	return detail, nil
}

// sshEntries lists the SSH access entries for the question "which access?".
func sshEntries(command *cobra.Command, client api.Client, inventory api.InventoryClient) ([]*api.AccessProfileDetail, error) {
	profiles, err := collect(func(cursor string) (api.Page[api.AccessProfile], error) {
		return client.ListAccessProfiles(command.Context(), cursor)
	})
	if err != nil {
		return nil, fmt.Errorf("list access entries: %w", err)
	}
	var entries []*api.AccessProfileDetail
	for _, profile := range profiles {
		detail, err := inventory.GetAccessProfile(command.Context(), profile.ID)
		if err != nil {
			return nil, fmt.Errorf("read access entry %q: %w", sanitize.Line(profile.Name), err)
		}
		if describeHow(detail.Spec) != howAgent {
			entries = append(entries, detail)
		}
	}

	return entries, nil
}

// wizardError keeps "back" as it is and turns everything else into a calm stop.
func wizardError(err error) error {
	if errors.Is(err, ui.ErrBack) {
		return err
	}

	return wizardStopped(err)
}

func askLine(p *ui.Prompter, question, def, flag, next string) (string, error) {
	answer, err := p.Line(question, def)
	if errors.Is(err, ui.ErrNoInput) {
		return "", inputRequired(flag, next)
	}
	if err != nil {
		return "", wizardError(err)
	}

	return strings.TrimSpace(answer), nil
}

// askHostsAdd fills the plan from flags and questions. A step that returns
// asked=false had its answer already; "back" returns to the last step that
// really asked, and before the first one it stops.
func askHostsAdd(command *cobra.Command, f *cli.Factory, plan *hostsAddPlan, o hostsAddOptions, entries []*api.AccessProfileDetail) error {
	p := f.Prompter()
	retry := "machinarchy hosts add NAME ADDRESS --access ENTRY --no-input"
	// What came with the command line is kept apart from what a question
	// answered, so going back and asking again starts clean.
	argName, argEndpoint := plan.name, plan.endpoint

	steps := []func() (bool, error){
		func() (bool, error) {
			if argName != "" {
				return false, nil
			}
			name, err := askLine(p, "Name for this machine", "", "NAME", retry)
			if err != nil {
				return true, err
			}
			if err := validateHostName(name); err != nil {
				return true, err
			}
			plan.name = name

			return true, nil
		},
		func() (bool, error) {
			if argEndpoint != "" {
				return false, nil
			}
			address, err := askLine(p, "Address (HOST or HOST:PORT)", "", "ADDRESS", retry)
			if err != nil {
				return true, err
			}
			if plan.endpoint, err = normaliseAddress(address); err != nil {
				return true, err
			}

			return true, nil
		},
		func() (bool, error) { // how to sign in
			switch {
			case plan.existing != nil && o.access != "":
				return false, nil
			case o.keyFile != "":
				plan.how = howSSHKey
				return false, nil
			case o.passwordStdin:
				plan.how = howSSHPassword
				return false, nil
			}
			options := []string{"With an SSH key from this computer", "With an SSH password"}
			for _, entry := range entries {
				options = append(options, "Use access I already created: "+sanitize.Line(entry.Name))
			}
			index, err := p.Choice(fmt.Sprintf("How should Machinarchy sign in to %s?", sanitize.Line(plan.name)), options, 0)
			if errors.Is(err, ui.ErrNoInput) {
				return false, inputRequired("--access", retry)
			}
			if err != nil {
				return true, wizardError(err)
			}
			plan.existing, plan.how = nil, ""
			switch index {
			case 0:
				plan.how = howSSHKey
			case 1:
				plan.how = howSSHPassword
			default:
				plan.existing = entries[index-2]
			}

			return true, nil
		},
		func() (bool, error) { // login user
			if plan.existing != nil {
				return false, nil
			}
			if o.user != "" {
				plan.user = strings.TrimSpace(o.user)
				return false, nil
			}
			user, err := askLine(p, fmt.Sprintf("Login user on %s", sanitize.Line(plan.name)), "", "--user", retry+" (or --user USER --key-file FILE)")
			if err != nil {
				return true, err
			}
			if user == "" {
				return true, clierr.Usagef("the SSH login user cannot be empty")
			}
			plan.user = user

			return true, nil
		},
		func() (bool, error) { // the key or password
			plan.credential, plan.prompted = "", false
			switch {
			case plan.existing != nil:
				return false, nil
			case plan.how == howSSHKey && o.keyFile != "":
				value, err := readKeyFile(o.keyFile)
				plan.credential = value

				return false, err
			case plan.how == howSSHPassword && o.passwordStdin:
				value, err := readLimitedStdin(command.InOrStdin(), "the password")
				plan.credential = value

				return false, err
			case plan.how == howSSHKey:
				path, err := askLine(p, "Private key file", defaultKeyFile(), "--key-file", retry+" (or --user USER --key-file FILE)")
				if err != nil {
					return true, err
				}
				if plan.credential, err = readKeyFile(expandHome(path)); err != nil {
					return true, err
				}
			default:
				password, err := p.Secret("SSH password (not shown)")
				if errors.Is(err, ui.ErrNoInput) {
					return false, inputRequired("--password-stdin", "printf '%s' \"$PASSWORD\" | machinarchy hosts add NAME ADDRESS --user USER --password-stdin")
				}
				if err != nil {
					return true, wizardError(err)
				}
				if password == "" {
					return true, clierr.Usagef("the password cannot be empty")
				}
				plan.credential = password
			}
			plan.prompted = true

			return true, nil
		},
		func() (bool, error) { // permission to send it
			if !plan.prompted {
				return false, nil
			}
			if _, err := fmt.Fprintln(command.ErrOrStderr(), "  It is sent to your server over HTTPS and stored encrypted there. It is never shown again."); err != nil {
				return true, err
			}
			send, err := p.YesNo("Send it?", true)
			if err != nil {
				return true, wizardError(err)
			}
			if !send {
				return true, clierr.Newf(clierr.ExitFailure, "", "stopped; nothing was changed")
			}

			return true, nil
		},
		func() (bool, error) { // host-key policy
			if plan.existing != nil {
				plan.policy = specString(plan.existing.Spec, "hostKeyPolicy")
				if plan.policy == "" {
					plan.policy = policyApprove
				}

				return false, nil
			}
			if o.hostKeyPolicy != "" || !p.Interactive {
				policy, err := chooseHostKeyPolicy(p, o.hostKeyPolicy, plan.how)
				plan.policy = policy

				return false, err
			}
			options := []string{"I approve them (recommended)", "Only keys I pin"}
			values := []string{policyApprove, policyPinned}
			if plan.how == howSSHKey {
				options = append(options, "Trust the first key seen (lab only)")
				values = append(values, policyTOFU)
			}
			index, err := p.Choice("Approve new SSH host keys yourself, or trust the first key seen?", options, 0)
			if err != nil {
				return true, wizardError(err)
			}
			plan.policy = values[index]

			return true, nil
		},
	}

	var asked []int
	for i := 0; i < len(steps); {
		did, err := steps[i]()
		switch {
		case errors.Is(err, ui.ErrBack):
			if len(asked) == 0 {
				return clierr.Newf(clierr.ExitFailure, "", "stopped; nothing was changed")
			}
			i, asked = asked[len(asked)-1], asked[:len(asked)-1]
		case err != nil:
			return err
		default:
			if did {
				asked = append(asked, i)
				plan.asked = true
			}
			i++
		}
	}

	return nil
}

func defaultKeyFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(home, ".ssh", "id_ed25519")
	if _, err := os.Stat(path); err != nil {
		return ""
	}

	return "~/.ssh/id_ed25519"
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}

	return path
}

// executeHostsAdd is everything after the last question: the first request is sent here.
func executeHostsAdd(command *cobra.Command, f *cli.Factory, client api.Client, printer *output.Printer,
	plan *hostsAddPlan, o hostsAddOptions, labels map[string]string) error {
	table := printer.Format() == output.FormatTable
	errOut := command.ErrOrStderr()

	entryName := ""
	var accessID string
	if plan.existing != nil {
		accessID, entryName = plan.existing.ID, plan.existing.Name
	} else {
		entryName = strings.TrimSpace(o.accessName)
		if entryName == "" {
			suffix := "key"
			if plan.how == howSSHPassword {
				suffix = "password"
			}
			entryName = plan.user + "-" + suffix
		}
		method := "password"
		if plan.how == howSSHKey {
			method = "private-key"
		}
		profile, err := makeAccessWithSecret(command, f, entryName, "", plan.credential, map[string]any{
			"connectorKind": "ssh", "enabled": true, "username": plan.user, "authenticationMethod": method, "hostKeyPolicy": plan.policy})
		if err != nil {
			return err
		}
		accessID, entryName = profile.ID, profile.Name
		if table {
			if err := announceAccess(command, f, profile); err != nil {
				return err
			}
		}
	}

	host, err := client.CreateHost(command.Context(), api.HostInput{Name: plan.name, Labels: labels, Spec: api.HostSpec{
		Presence: "Managed", AccessProfileID: accessID, AccessTarget: api.HostAccessTarget{Endpoint: plan.endpoint}}})
	if err != nil {
		var apiErr *api.Error
		if errors.As(err, &apiErr) && apiErr.Status == 409 {
			return clierr.Detailed(clierr.ExitConflict, fmt.Errorf("a host named %s already exists", sanitize.Line(plan.name)),
				"nothing was changed about that host", "machinarchy hosts show "+plan.name)
		}

		return fmt.Errorf("add host: %w", err)
	}
	name := sanitize.Line(plan.name)
	if err := printMessageTo(errOut, f, msg.Get("host.registered").With(msg.Vars{"host": name})); err != nil {
		return err
	}

	start := time.Now()
	waiter := hostWaiter{ctx: command.Context(), client: client, id: host.Metadata.ID, deadline: start.Add(o.wait)}
	final := *host
	if o.wait > 0 {
		if final, err = followNewHost(command, f, &waiter, final, plan, o); err != nil {
			return err
		}
	}
	if !table {
		return printer.Host(final)
	}
	if err := reportNewHost(command, f, final, plan.name, plan.endpoint, time.Since(start)); err != nil {
		return err
	}

	return printTwin(command, plan, entryName, o)
}

// hostWaiter polls one host until a condition holds or the time is up.
type hostWaiter struct {
	ctx      context.Context
	client   api.Client
	id       string
	deadline time.Time
}

func (w *hostWaiter) until(done func(api.Host) bool) (api.Host, error) {
	for {
		host, err := w.client.GetHost(w.ctx, w.id)
		if err != nil {
			return api.Host{}, fmt.Errorf("check the host: %w", err)
		}
		remaining := time.Until(w.deadline)
		if done(*host) || remaining <= 0 {
			return *host, nil
		}
		select {
		case <-w.ctx.Done():
			return api.Host{}, w.ctx.Err()
		case <-time.After(min(hostsAddPoll, remaining)):
		}
	}
}

// followNewHost does the waiting and the key question after the host is registered.
func followNewHost(command *cobra.Command, f *cli.Factory, w *hostWaiter, host api.Host, plan *hostsAddPlan, o hostsAddOptions) (api.Host, error) {
	errOut := command.ErrOrStderr()
	name := sanitize.Line(plan.name)
	seconds := int(time.Until(w.deadline).Round(time.Second) / time.Second)
	settled := func(h api.Host) bool {
		status, _ := hostReadiness(h)
		return status == statusReady || status == statusNoConnect
	}
	var err error
	if plan.policy != policyTOFU {
		if err := printMessageTo(errOut, f, msg.Get("host.waiting_key").With(msg.Vars{"host": name, "seconds": strconv.Itoa(seconds)})); err != nil {
			return host, err
		}
		if host, err = w.until(func(h api.Host) bool { return len(h.Status.ObservedHostKeys) > 0 || settled(h) }); err != nil {
			return host, err
		}
		if status, _ := hostReadiness(host); status == statusKey && len(host.Status.ObservedHostKeys) > 0 {
			trusted, err := trustNewHost(command, f, w.client, &host, plan, o)
			if err != nil || !trusted {
				return host, err
			}
		}
	} else if err := printMessageTo(errOut, f, msg.Get("host.waiting_tofu").With(msg.Vars{"host": name, "seconds": strconv.Itoa(seconds)})); err != nil {
		return host, err
	}

	return w.until(func(h api.Host) bool {
		status, _ := hostReadiness(h)
		return settled(h) || status == statusKeyChange
	})
}

// trustNewHost shows the keys the server saw and trusts them after the check.
// trusted is false when the person skipped the question or there is nothing to ask.
func trustNewHost(command *cobra.Command, f *cli.Factory, client api.Client, host *api.Host, plan *hostsAddPlan, o hostsAddOptions) (bool, error) {
	observed, check, err := keysToConfirm(*host, false)
	if err != nil {
		return false, err
	}
	name := sanitize.Line(plan.name)
	later := "machinarchy hosts trust " + plan.name
	if o.trustFingerprint != "" && !slices.Contains(check, o.trustFingerprint) {
		return false, clierr.Detailed(clierr.ExitUsage,
			fmt.Errorf("--trust-fingerprint %s is not a key the server saw on %q", sanitize.Line(o.trustFingerprint), name),
			"the host is registered and waiting for key approval; the server saw: "+sanitize.Line(strings.Join(check, ", ")), later)
	}
	if o.trustFingerprint == "" && !f.Prompter().Interactive {
		return false, nil
	}
	if err := showKeys(command, *host, check, false); err != nil {
		return false, err
	}
	if o.trustFingerprint == "" {
		if err := printMessageTo(command.ErrOrStderr(), f, msg.Get("host.fingerprint_note").With(msg.Vars{"endpoint": sanitize.Line(plan.endpoint)})); err != nil {
			return false, err
		}
		skipped, err := askKeyPrefix(f, *host, check[0], false)
		if err != nil {
			return false, err
		}
		if skipped {
			return false, nil
		}
	}
	trusted, err := client.TrustHostKeys(command.Context(), host.Metadata.ID, host.Metadata.Generation, observed, false)
	if err != nil {
		return false, fmt.Errorf("trust host %q: %w", name, staleHost(err))
	}
	*host = *trusted
	err = printMessageTo(command.ErrOrStderr(), f, msg.Get("host.waiting_ready").With(msg.Vars{"host": name}))

	return true, err
}

// reportNewHost says where the new host stands.
func reportNewHost(command *cobra.Command, f *cli.Factory, host api.Host, rawName, endpoint string, elapsed time.Duration) error {
	name := sanitize.Line(rawName)
	status, _ := hostReadiness(host)
	seconds := fmt.Sprint(int(elapsed.Round(time.Second) / time.Second))
	switch status {
	case statusReady:
		return printMessage(command, f, msg.Get("host.added").With(msg.Vars{"host": name, "seconds": seconds}))
	case statusKey:
		next := "machinarchy hosts trust " + rawName
		if observed, err := observedFingerprints(host); err == nil && len(observed) > 0 {
			next += " --fingerprint " + observed[0]
		}

		return printMessage(command, f, msg.Get("host.added_waiting_key").With(msg.Vars{"host": name, "next": next}))
	case statusNoConnect:
		reason := ""
		for _, condition := range host.Status.Conditions {
			if condition.Type == "Ready" && condition.Message != "" {
				reason = ": " + sanitize.Line(condition.Message)
			}
		}

		return printMessage(command, f, msg.Get("host.added_no_connect").With(msg.Vars{"host": name, "reason": reason, "endpoint": sanitize.Line(endpoint)}))
	default:
		return printMessage(command, f, msg.Get("host.added_checking").With(msg.Vars{"host": name, "status": status, "seconds": seconds}))
	}
}

// printTwin prints the command that repeats the wizard without questions.
func printTwin(command *cobra.Command, plan *hostsAddPlan, entryName string, o hostsAddOptions) error {
	if !plan.asked {
		return nil
	}
	parts := []string{"machinarchy", "hosts", "add", shellWord(plan.name), shellWord(plan.endpoint), "--access", shellWord(entryName)}
	for _, tag := range o.tags {
		parts = append(parts, "--tag", shellWord(tag))
	}
	parts = append(parts, "--no-input")
	_, err := fmt.Fprintf(command.ErrOrStderr(), "To do this without questions (the access entry now exists):\n  %s\n", strings.Join(parts, " "))

	return err
}

var safeShellWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellWord quotes a value for a POSIX shell.
func shellWord(value string) string {
	value = sanitize.Line(value)
	if safeShellWord.MatchString(value) {
		return value
	}

	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
