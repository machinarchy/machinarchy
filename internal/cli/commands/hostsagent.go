package commands

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/msg"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
)

// The agent path of `hosts` (docs/cli/spec/02-commands.md, section 3.8):
// `hosts add --agent`, `hosts enroll` and `hosts revoke-agent`.
//
// Agents reach the controller only through its gateway (ADR-059): the install
// blob carries the server URL, the controller key and the one-time token, and
// nothing about any broker.

const (
	minEnrollTTL = time.Minute
	maxEnrollTTL = 24 * time.Hour
	// sharedAgentEntry is the access entry `hosts add --agent` reuses or creates.
	sharedAgentEntry = "agents"
)

var agentNameSafe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// agentIssue is what `hosts enroll` and `hosts add --agent` print with -o json|yaml.
// The token is shown once and is left out when it went to a file.
type agentIssue struct {
	Host                 string    `json:"host" yaml:"host"`
	ServerURL            string    `json:"serverUrl" yaml:"serverUrl"`
	HostID               string    `json:"hostId" yaml:"hostId"`
	TokenID              string    `json:"tokenId" yaml:"tokenId"`
	ExpiresAt            time.Time `json:"expiresAt" yaml:"expiresAt"`
	ControllerPublicKeys []string  `json:"controllerPublicKeys" yaml:"controllerPublicKeys"`
	InstallBlob          string    `json:"installBlob,omitempty" yaml:"installBlob,omitempty"`
	InstallBlobFile      string    `json:"installBlobFile,omitempty" yaml:"installBlobFile,omitempty"`
}

func validateEnrollTTL(ttl time.Duration) error {
	if ttl < minEnrollTTL || ttl > maxEnrollTTL {
		return clierr.Usagef("--ttl must be between 1m and 24h")
	}

	return nil
}

// agentGatewayKeys reads the controller's public keys; a server without an agent gateway says so.
func agentGatewayKeys(command *cobra.Command, inventory api.InventoryClient) ([]string, error) {
	key, err := inventory.ControllerKey(command.Context())
	if err != nil {
		var apiErr *api.Error
		if errors.As(err, &apiErr) && apiErr.Status == 503 {
			return nil, clierr.Detailed(clierr.ExitFailure, errors.New("this server has no agent gateway"),
				"it cannot enrol agents, so nothing was changed", "machinarchy hosts add NAME ADDRESS")
		}

		return nil, fmt.Errorf("read the controller key: %w", err)
	}
	keys := key.PublicKeys
	if len(keys) == 0 && key.PublicKey != "" {
		keys = []string{key.PublicKey}
	}
	if len(keys) == 0 {
		return nil, clierr.Detailed(clierr.ExitFailure, errors.New("the server reported no controller key"), "an agent cannot be installed without it", "")
	}

	return keys, nil
}

// installBlob is the agent's environment settings, base64 so it stays one short line.
// It holds the one-time token: it is shown once, never put in a command line.
func installBlob(hostName, serverURL, token string, keys []string) string {
	var b strings.Builder
	b.WriteString("# Machinarchy agent settings; the token is single use.\n")
	if agentNameSafe.MatchString(hostName) {
		fmt.Fprintf(&b, "MACHINARCHY_AGENT_NAME=%s\n", hostName)
	}
	fmt.Fprintf(&b, "MACHINARCHY_AGENT_SERVER_URL=%s\n", serverURL)
	fmt.Fprintf(&b, "MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY=%s\n", strings.Join(keys, ","))
	fmt.Fprintf(&b, "MACHINARCHY_AGENT_ENROLLMENT_TOKEN=%s\n", token)

	return base64.StdEncoding.EncodeToString([]byte(b.String()))
}

// enrollOptions are the flags `hosts enroll` and `hosts add --agent` share.
type enrollOptions struct {
	ttl       time.Duration
	tokenOut  string
	revokeOld bool
	// retired holds the removed broker flags; any of them being set is refused.
	retired retiredBrokerFlags
}

// retiredBrokerFlags are the broker flags of the old agent path. They stay
// defined (hidden) only so that using one is refused with the replacement
// named instead of a bare "unknown flag".
type retiredBrokerFlags struct {
	natsURL, credsFile string
	remember           bool
}

const brokerFlagsRetired = "agents no longer connect to a broker: the install blob holds the server URL, the controller key and the one-time token, nothing else"

func addRetiredBrokerFlags(command *cobra.Command, r *retiredBrokerFlags) {
	flags := command.Flags()
	flags.StringVar(&r.natsURL, "nats-url", "", "Removed")
	flags.StringVar(&r.credsFile, "nats-creds-file", "", "Removed")
	flags.BoolVar(&r.remember, "remember", false, "Removed")
	for _, name := range []string{"nats-url", "nats-creds-file", "remember"} {
		if err := flags.MarkHidden(name); err != nil {
			panic(err.Error()) // the flag was defined just above
		}
	}
}

// refuseRetiredBrokerFlags stops before anything is sent when a removed flag was given.
func refuseRetiredBrokerFlags(command *cobra.Command) error {
	for _, name := range []string{"nats-url", "nats-creds-file", "remember"} {
		if command.Flags().Changed(name) {
			return clierr.Detailed(clierr.ExitUsage, fmt.Errorf("--%s was removed", name), brokerFlagsRetired,
				"machinarchy hosts enroll NAME")
		}
	}

	return nil
}

// issueAgentEnrolment issues a one-time token for host and prints the install blob.
func issueAgentEnrolment(command *cobra.Command, f *cli.Factory, access api.AccessClient, host api.Host, keys []string, o enrollOptions) error {
	printer, err := f.Printer(command.OutOrStdout(), output.FormatTable)
	if err != nil {
		return err
	}
	if o.tokenOut != "" {
		if _, err := os.Lstat(o.tokenOut); err == nil {
			return clierr.Usagef("%s already exists; --token-out never overwrites a file", sanitize.Line(o.tokenOut))
		}
	}
	target, err := f.ResolveTarget()
	if err != nil {
		return err
	}
	name := host.Metadata.Name
	issued, err := access.CreateEnrollmentToken(command.Context(), api.EnrollmentTokenInput{
		HostID: host.Metadata.ID, TTLSeconds: int(o.ttl.Seconds())})
	if err != nil {
		return fmt.Errorf("issue the enrolment token: %w", err)
	}
	blob := installBlob(name, target.Server, issued.Token, keys)
	document := agentIssue{Host: name, ServerURL: target.Server, HostID: host.Metadata.ID, TokenID: issued.ID, ExpiresAt: issued.ExpiresAt, ControllerPublicKeys: keys}
	if o.tokenOut != "" {
		if err := writeNewPrivateFile(o.tokenOut, blob+"\n"); err != nil {
			revokeErr := access.RevokeEnrollmentToken(command.Context(), issued.ID)
			if revokeErr != nil {
				err = errors.Join(err, fmt.Errorf("the issued token %s could not be revoked either: %w", issued.ID, revokeErr))
			}

			return fmt.Errorf("save the install blob: %w", err)
		}
		document.InstallBlobFile = o.tokenOut
	} else {
		document.InstallBlob = blob
	}
	if o.revokeOld {
		if err := revokeOldTokens(command, access, host.Metadata.ID, issued.ID); err != nil {
			return err
		}
	}

	if printer.Format() != output.FormatTable {
		return printer.Document(document)
	}
	if err := printInstallSteps(command, f, name, issued.ExpiresAt, o.tokenOut); err != nil {
		return err
	}
	if o.tokenOut != "" {
		return nil
	}
	_, err = fmt.Fprintln(command.OutOrStdout(), blob)

	return err
}

// writeNewPrivateFile creates path with mode 0600 and fails if it exists.
func writeNewPrivateFile(path, content string) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- the path is the user's own --token-out
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	_, err = file.WriteString(content)

	return err
}

// revokeOldTokens revokes this host's earlier tokens that are still unused.
func revokeOldTokens(command *cobra.Command, access api.AccessClient, hostID, keep string) error {
	tokens, err := access.ListEnrollmentTokens(command.Context())
	if err != nil {
		return fmt.Errorf("list enrolment tokens: %w", err)
	}
	for _, token := range tokens {
		if token.ID == keep || token.HostID != hostID || token.UsedAt != nil || token.RevokedAt != nil || time.Now().After(token.ExpiresAt) {
			continue
		}
		if err := access.RevokeEnrollmentToken(command.Context(), token.ID); err != nil {
			return fmt.Errorf("revoke the earlier token %s (the new one was issued): %w", token.ID, err)
		}
	}

	return nil
}

// printInstallSteps tells what to do on the machine. It never repeats the token.
func printInstallSteps(command *cobra.Command, f *cli.Factory, rawName string, expires time.Time, tokenFile string) error {
	if f.Config().Quiet() {
		return nil
	}
	where := "The install blob is printed below. It holds a one-time token that is shown only now."
	if tokenFile != "" {
		where = fmt.Sprintf("The install blob was saved to %s (mode 0600). It holds a one-time token.", sanitize.Line(tokenFile))
	}
	_, err := fmt.Fprintf(command.ErrOrStderr(), "Agent enrolment for %s (token valid until %s).\n%s\n\n"+
		"On the machine, as root:\n"+
		"  1. Install machinarchy-agent from its release (scripts/install-systemd.sh in the package).\n"+
		"  2. Append the settings to /etc/machinarchy-agent/agent.env:  echo BLOB | base64 -d >> /etc/machinarchy-agent/agent.env\n"+
		"     Pass the blob on standard input or from the file, not typed into a shared shell history.\n"+
		"  3. Start the service, then delete the token line from agent.env once the agent has registered.\n"+
		"Then: machinarchy hosts show %s\n\n",
		sanitize.Line(rawName), expires.Format(time.RFC3339), where, shellWord(rawName))

	return err
}

func newHostsEnrollCommand(f *cli.Factory) *cobra.Command {
	var o enrollOptions
	var showKey bool
	command := &cobra.Command{
		Use:   "enroll NAME",
		Short: "Issue a one-time install blob for an agent host (owner)",
		Long: "Issues a new one-time enrolment token for an existing host and prints the install blob: the agent's name, the controller's\n" +
			"public key, the server URL and the token, as base64 settings. The token is shown once. Nothing else is needed on the machine.",
		Example: "  machinarchy hosts enroll web-01\n  machinarchy hosts enroll web-01 --ttl 30m --token-out web-01.blob\n  machinarchy hosts enroll web-01 --revoke-old",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := refuseRetiredBrokerFlags(command); err != nil {
				return err
			}
			if err := validateEnrollTTL(o.ttl); err != nil {
				return err
			}
			client, access, inventory, err := accessClients(command.Context(), f)
			if err != nil {
				return err
			}
			host, err := findHost(command.Context(), f, client, args[0])
			if err != nil {
				return err
			}
			keys, err := agentGatewayKeys(command, inventory)
			if err != nil {
				return err
			}
			if showKey {
				if _, err := fmt.Fprintf(command.ErrOrStderr(), "Controller public key: %s\n", sanitize.Line(strings.Join(keys, ","))); err != nil {
					return err
				}
			}

			return issueAgentEnrolment(command, f, access, host, keys, o)
		},
	}
	command.Flags().DurationVar(&o.ttl, "ttl", time.Hour, "How long the token stays valid (1m to 24h)")
	command.Flags().StringVar(&o.tokenOut, "token-out", "", "Write the install blob to this new file (mode 0600, never overwrites)")
	command.Flags().BoolVar(&o.revokeOld, "revoke-old", false, "Also revoke this host's earlier unused tokens")
	addRetiredBrokerFlags(command, &o.retired)
	command.Flags().BoolVar(&showKey, "show-controller-key", false, "Also print the controller public key")

	return command
}

func newHostsRevokeAgentCommand(f *cli.Factory) *cobra.Command {
	var agentID string
	command := &cobra.Command{
		Use:   "revoke-agent NAME --agent-id ID",
		Short: "Revoke an agent's identity so it can no longer serve commands (owner)",
		Long: "Revokes the agent identity, so the agent can no longer serve commands. The host stays in the inventory and the machine is\n" +
			"not touched. You confirm by typing the host's name (in a script: --confirm NAME); --yes is not enough. A host in a protected\n" +
			"env also needs the env's name. The agent ID is the one 'machinarchy get agent-tokens' shows as 'used by ID'; the server does\n" +
			"not yet say which agent belongs to a host.",
		Example: "  machinarchy hosts revoke-agent web-01 --agent-id agent-3f9\n  machinarchy hosts revoke-agent web-01 --agent-id agent-3f9 --confirm web-01",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if strings.TrimSpace(agentID) == "" {
				return clierr.Detailed(clierr.ExitUsage, errors.New("--agent-id is required"),
					"the server does not yet say which agent belongs to a host; 'get agent-tokens' shows the ID as 'used by ID'", "machinarchy get agent-tokens")
			}
			client, _, inventory, err := accessClients(command.Context(), f)
			if err != nil {
				return err
			}
			host, err := findHost(command.Context(), f, client, args[0])
			if err != nil {
				return err
			}
			env, protected, err := hostEnvName(command.Context(), f, client, host)
			if err != nil {
				return err
			}
			if err := confirmProtectedEnv(f, env, protected); err != nil {
				return err
			}
			name := sanitize.Line(host.Metadata.Name)
			if err := f.ConfirmTyped(fmt.Sprintf("Revoke agent %s of host %q? It can no longer serve commands.", sanitize.Line(agentID), name), host.Metadata.Name); err != nil {
				return err
			}
			if err := inventory.RevokeAgent(command.Context(), strings.TrimSpace(agentID)); err != nil {
				return fmt.Errorf("revoke agent: %w", err)
			}

			return printMessage(command, f, msg.Get("host.agent_revoked").With(msg.Vars{"host": name, "agent": sanitize.Line(agentID)}))
		},
	}
	command.Flags().StringVar(&agentID, "agent-id", "", "ID of the agent to revoke")

	return command
}

// runHostsAddAgent registers a host reached through an agent and issues its install blob.
func runHostsAddAgent(command *cobra.Command, f *cli.Factory, args []string, o hostsAddOptions) error {
	if err := refuseRetiredBrokerFlags(command); err != nil {
		return err
	}
	if len(args) > 1 {
		return clierr.Usagef("an agent host takes only a NAME, no address")
	}
	if o.user != "" || o.keyFile != "" || o.passwordStdin || o.hostKeyPolicy != "" || o.accessName != "" || o.trustFingerprint != "" {
		return clierr.Usagef("--agent takes no --user, --key-file, --password-stdin, --host-key-policy, --access-name or --trust-fingerprint")
	}
	if err := validateEnrollTTL(o.ttl); err != nil {
		return err
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
	name := ""
	if len(args) == 1 {
		name = strings.TrimSpace(args[0])
	} else {
		var err error
		if name, err = askLine(f.Prompter(), "Name for this machine", "", "NAME", "machinarchy hosts add NAME --agent"); err != nil {
			return err
		}
	}
	if err := validateHostName(name); err != nil {
		return err
	}

	client, access, inventory, err := accessClients(command.Context(), f)
	if err != nil {
		return err
	}
	keys, err := agentGatewayKeys(command, inventory)
	if err != nil {
		return err
	}
	entryID, err := agentEntry(command, f, client, inventory, o.access)
	if err != nil {
		return err
	}
	host, err := client.CreateHost(command.Context(), api.HostInput{Name: name, Labels: labels, Spec: api.HostSpec{
		Presence: "Managed", AccessProfileID: entryID}})
	if err != nil {
		var apiErr *api.Error
		if errors.As(err, &apiErr) && apiErr.Status == 409 {
			return clierr.Detailed(clierr.ExitConflict, fmt.Errorf("a host named %s already exists", sanitize.Line(name)),
				"nothing was changed about that host", "machinarchy hosts show "+name)
		}

		return fmt.Errorf("add host: %w", err)
	}
	if err := printMessageTo(command.ErrOrStderr(), f, msg.Get("host.registered").With(msg.Vars{"host": sanitize.Line(name)})); err != nil {
		return err
	}

	return issueAgentEnrolment(command, f, access, *host, keys, enrollOptions{ttl: o.ttl, tokenOut: o.tokenOut})
}

// agentEntry returns the id of the agent access entry: the one named, else the shared entry "agents" (created if missing).
func agentEntry(command *cobra.Command, f *cli.Factory, client api.Client, inventory api.InventoryClient, given string) (string, error) {
	if given != "" {
		id, err := resolveProfile(command, client, given)
		if err != nil {
			return "", err
		}
		detail, err := inventory.GetAccessProfile(command.Context(), id)
		if err != nil {
			return "", fmt.Errorf("read access entry: %w", err)
		}
		if describeHow(detail.Spec) != howAgent {
			return "", clierr.Detailed(clierr.ExitUsage, fmt.Errorf("access entry %q reaches machines over SSH", sanitize.Line(detail.Name)),
				"an agent host needs an agent entry", "machinarchy access")
		}

		return id, nil
	}
	profiles, err := collect(func(cursor string) (api.Page[api.AccessProfile], error) {
		return client.ListAccessProfiles(command.Context(), cursor)
	})
	if err != nil {
		return "", fmt.Errorf("list access entries: %w", err)
	}
	for _, profile := range profiles {
		if !strings.EqualFold(profile.Name, sharedAgentEntry) {
			continue
		}
		detail, err := inventory.GetAccessProfile(command.Context(), profile.ID)
		if err != nil {
			return "", fmt.Errorf("read access entry: %w", err)
		}
		if describeHow(detail.Spec) != howAgent {
			return "", clierr.Detailed(clierr.ExitUsage, fmt.Errorf("the access entry %q is not an agent entry", sharedAgentEntry),
				"name an agent entry with --access", "machinarchy access")
		}

		return profile.ID, nil
	}
	created, err := makeAccess(command, f, sharedAgentEntry, "Agent-reached hosts", map[string]any{"connectorKind": "agent", "enabled": true})
	if err != nil {
		return "", err
	}

	return created.ID, nil
}
