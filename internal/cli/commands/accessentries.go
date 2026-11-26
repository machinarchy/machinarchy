package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/msg"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
	"github.com/machinarchy/machinarchy/internal/cli/ui"
)

// Access entries are the user-facing name of an Access Profile: how the
// controller reaches machines (docs/cli/spec/02-commands.md, section 3.9).

// maxKeyFile bounds a key file or a password read from stdin.
const maxKeyFile = 1 << 20

// Values of --how.
const (
	howSSHKey      = "ssh-key"
	howSSHPassword = "ssh-password"
	howAgent       = "agent"
)

// Host-key policies, in the words of the spec.
const (
	policyApprove = "approve"
	policyTOFU    = "tofu"
	policyPinned  = "pinned"
)

// NewAccessCommand lists, shows, adds and removes access entries.
func NewAccessCommand(f *cli.Factory) *cobra.Command {
	command := &cobra.Command{
		Use:   "access",
		Short: "List, add, show and remove how Machinarchy reaches machines",
		Long: "An access entry says how the controller logs in to machines: with an SSH key, with an SSH password, or through\n" +
			"an installed agent. Keys and passwords are stored encrypted on the server and are never shown again.",
		Example: "  machinarchy access\n  machinarchy access add deploy-key --how ssh-key --user deploy --key-file ~/.ssh/id_ed25519\n  machinarchy access show deploy-key",
		Args:    cobra.NoArgs,
		RunE:    listAccess(f),
	}
	command.AddCommand(newAccessShowCommand(f), newAccessAddCommand(f), newAccessRemoveCommand(f))
	completeChildren(command, completeAccessNames(f, true), "show", "remove")

	return command
}

// accessClients returns the plain, access and inventory halves of the API client.
func accessClients(ctx context.Context, f *cli.Factory) (api.Client, api.AccessClient, api.InventoryClient, error) {
	client, err := f.NewAPIClient(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	access, ok := client.(api.AccessClient)
	if !ok {
		return nil, nil, nil, errors.New("API client does not support Access Profiles")
	}
	inventory, ok := client.(api.InventoryClient)
	if !ok {
		return nil, nil, nil, errors.New("API client does not support Access Profiles")
	}

	return client, access, inventory, nil
}

// accessRow is one access entry as `access -o json` prints it. It holds
// secret references never values.
type accessRow struct {
	ID          string `json:"id" yaml:"id"`
	Name        string `json:"name" yaml:"name"`
	How         string `json:"how" yaml:"how"`
	User        string `json:"user,omitempty" yaml:"user,omitempty"`
	HostKey     string `json:"hostKeyPolicy,omitempty" yaml:"hostKeyPolicy,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Hosts       int    `json:"hosts" yaml:"hosts"`
}

func listAccess(f *cli.Factory) func(*cobra.Command, []string) error {
	return func(command *cobra.Command, _ []string) error {
		client, _, inventory, err := accessClients(command.Context(), f)
		if err != nil {
			return err
		}
		profiles, err := collect(func(cursor string) (api.Page[api.AccessProfile], error) {
			return client.ListAccessProfiles(command.Context(), cursor)
		})
		if err != nil {
			return fmt.Errorf("list access entries: %w", err)
		}
		hosts, err := listAllHosts(command.Context(), client, api.HostQuery{})
		if err != nil {
			return fmt.Errorf("list hosts: %w", err)
		}
		uses := hostsPerProfile(hosts)

		documents := make([]accessRow, 0, len(profiles))
		rows := make([][]string, 0, len(profiles))
		names := make([]string, 0, len(profiles))
		for _, profile := range profiles {
			detail, err := inventory.GetAccessProfile(command.Context(), profile.ID)
			if err != nil {
				return fmt.Errorf("read access entry %q: %w", sanitize.Line(profile.Name), err)
			}
			row := newAccessRow(detail, uses[profile.ID])
			documents = append(documents, row)
			names = append(names, profile.Name)
			rows = append(rows, []string{sanitize.Line(row.Name), row.How, sanitize.Line(row.User), fmt.Sprint(row.Hosts)})
		}

		return printList(command, f, documents, names, []string{"NAME", "HOW", "USER", "HOSTS"}, rows)
	}
}

func hostsPerProfile(hosts []api.Host) map[string]int {
	uses := make(map[string]int)
	for _, host := range hosts {
		if id := host.Spec.AccessProfileID; id != "" {
			uses[id]++
		}
	}

	return uses
}

func newAccessRow(detail *api.AccessProfileDetail, hosts int) accessRow {
	row := accessRow{ID: detail.ID, Name: detail.Name, Description: detail.Description, Hosts: hosts,
		How: describeHow(detail.Spec), User: specString(detail.Spec, "username"), HostKey: specString(detail.Spec, "hostKeyPolicy")}

	return row
}

func specString(spec map[string]any, key string) string {
	value, _ := spec[key].(string)

	return value
}

// describeHow turns an Access Profile spec into the words of --how.
func describeHow(spec map[string]any) string {
	switch specString(spec, "connectorKind") {
	case "agent":
		return howAgent
	case "ssh":
		if specString(spec, "authenticationMethod") == "private-key" {
			return howSSHKey
		}

		return howSSHPassword
	default:
		return "unknown"
	}
}

// policyWords explains a host-key policy to a person.
func policyWords(policy string) string {
	switch policy {
	case policyApprove:
		return "ask me to approve each host key"
	case policyTOFU:
		return "trust the first key seen (a lab setting)"
	case policyPinned:
		return "only keys I pin"
	case "ca":
		return "keys signed by a certificate authority"
	case "":
		return "the server's default (ask me to approve each host key)"
	default:
		return sanitize.Line(policy)
	}
}

func newAccessShowCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "show NAME",
		Short:   "Show how an access entry works and which hosts use it",
		Long:    "Shows how an access entry works. The key or password is never shown: it cannot be read back from the server.",
		Example: "  machinarchy access show deploy-key",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			client, _, inventory, err := accessClients(command.Context(), f)
			if err != nil {
				return err
			}
			id, err := resolveProfile(command, client, args[0])
			if err != nil {
				return err
			}
			detail, err := inventory.GetAccessProfile(command.Context(), id)
			if err != nil {
				return fmt.Errorf("read access entry: %w", err)
			}
			hosts, err := listAllHosts(command.Context(), client, api.HostQuery{})
			if err != nil {
				return fmt.Errorf("list hosts: %w", err)
			}
			var users []string
			for _, host := range hosts {
				if host.Spec.AccessProfileID == id {
					users = append(users, host.Metadata.Name)
				}
			}
			row := newAccessRow(detail, len(users))

			printer, err := f.Printer(command.OutOrStdout(), output.FormatTable)
			if err != nil {
				return err
			}
			if printer.Format() != output.FormatTable {
				return printer.Document(struct {
					accessRow `yaml:",inline"`
					HostNames []string `json:"hostNames" yaml:"hostNames"`
				}{row, nonNil(users)})
			}

			out := command.OutOrStdout()
			lines := []string{"Name:  " + sanitize.Line(row.Name), "How:   " + accessHowWords(row.How)}
			if row.User != "" {
				lines = append(lines, "User:  "+sanitize.Line(row.User))
			}
			if row.How != howAgent {
				lines = append(lines, "Host keys: "+policyWords(row.HostKey), "Secret: stored encrypted on the server; it is never shown")
			}
			if row.Description != "" {
				lines = append(lines, "Note:  "+sanitize.Line(row.Description))
			}
			if len(users) == 0 {
				lines = append(lines, "Hosts using it: none")
			} else {
				quoted := make([]string, 0, len(users))
				for _, name := range users {
					quoted = append(quoted, sanitize.Line(name))
				}
				lines = append(lines, fmt.Sprintf("Hosts using it (%d): %s", len(users), strings.Join(quoted, ", ")))
			}
			_, err = fmt.Fprintln(out, strings.Join(lines, "\n"))

			return err
		},
	}
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}

	return values
}

func accessHowWords(how string) string {
	switch how {
	case howSSHKey:
		return "ssh with a key"
	case howSSHPassword:
		return "ssh with a password"
	case howAgent:
		return "through an installed agent"
	default:
		return how
	}
}

type accessAddOptions struct {
	how, user, keyFile, hostKeyPolicy, description string
	passwordStdin                                  bool
}

func newAccessAddCommand(f *cli.Factory) *cobra.Command {
	var o accessAddOptions
	command := &cobra.Command{
		Use:   "add NAME [--how ssh-key|ssh-password|agent] [--user USER] [--key-file FILE | --password-stdin]",
		Short: "Add an access entry (owner)",
		Long: "Adds an access entry. Without flags it asks what it needs. The key or password is typed without echo, read from\n" +
			"--key-file or from stdin with --password-stdin; it is never a flag value, is stored encrypted on the server\n" +
			"and is never printed. Host keys: approve (default, you approve each key), tofu (the first key seen is trusted:\n" +
			"a lab setting, keys only) or pinned (only keys you pin).",
		Example: "  machinarchy access add deploy-key --how ssh-key --user deploy --key-file ~/.ssh/id_ed25519\n" +
			"  printf '%s' \"$PW\" | machinarchy access add lab --how ssh-password --user ops --password-stdin\n" +
			"  machinarchy access add agents --how agent",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runAccessAdd(command, f, strings.TrimSpace(args[0]), o)
		},
	}
	flags := command.Flags()
	flags.StringVar(&o.how, "how", "", "ssh-key, ssh-password or agent")
	flags.StringVar(&o.user, "user", "", "SSH login user")
	flags.StringVar(&o.keyFile, "key-file", "", "File with the SSH private key")
	flags.BoolVar(&o.passwordStdin, "password-stdin", false, "Read the SSH password from stdin")
	flags.StringVar(&o.hostKeyPolicy, "host-key-policy", "", "approve (default), tofu (keys only) or pinned")
	flags.StringVar(&o.description, "description", "", "What the entry is for")

	return command
}

func runAccessAdd(command *cobra.Command, f *cli.Factory, name string, o accessAddOptions) error {
	if name == "" {
		return clierr.Usagef("the access entry needs a name")
	}
	prompter := f.Prompter()
	how, err := chooseHow(prompter, o.how)
	if err != nil {
		return err
	}
	if how == howAgent {
		if o.user != "" || o.keyFile != "" || o.passwordStdin || o.hostKeyPolicy != "" {
			return clierr.Usagef("an agent entry takes no --user, --key-file, --password-stdin or --host-key-policy")
		}

		return createAccess(command, f, name, o.description, map[string]any{"connectorKind": "agent", "enabled": true})
	}

	user, err := askUser(prompter, o.user, name)
	if err != nil {
		return err
	}
	policy, err := chooseHostKeyPolicy(prompter, o.hostKeyPolicy, how)
	if err != nil {
		return err
	}
	value, err := readCredential(command, prompter, how, o)
	if err != nil {
		return err
	}

	method := "password"
	if how == howSSHKey {
		method = "private-key"
	}
	return createAccessWithSecret(command, f, name, o.description, value, map[string]any{
		"connectorKind": "ssh", "enabled": true, "username": user, "authenticationMethod": method, "hostKeyPolicy": policy})
}

func chooseHow(p *ui.Prompter, given string) (string, error) {
	options := []string{howSSHKey, howSSHPassword, howAgent}
	if given != "" {
		for _, option := range options {
			if given == option {
				return given, nil
			}
		}

		return "", clierr.Usagef("--how must be ssh-key, ssh-password or agent")
	}
	index, err := p.Choice("How does Machinarchy reach your machines?",
		[]string{"ssh with a key", "ssh with a password", "an installed agent"}, 0)
	if errors.Is(err, ui.ErrNoInput) {
		return "", inputRequired("--how", "machinarchy access add NAME --how ssh-key|ssh-password|agent")
	}
	if err != nil {
		return "", wizardStopped(err)
	}

	return options[index], nil
}

// wizardStopped turns a closed or "back" answer into a calm stop.
func wizardStopped(err error) error {
	if errors.Is(err, ui.ErrBack) || errors.Is(err, io.EOF) {
		return clierr.Newf(clierr.ExitFailure, "", "stopped; nothing was changed")
	}

	return err
}

func askUser(p *ui.Prompter, given, name string) (string, error) {
	if strings.TrimSpace(given) != "" {
		return strings.TrimSpace(given), nil
	}
	user, err := p.Line("SSH login user", "")
	if errors.Is(err, ui.ErrNoInput) {
		return "", inputRequired("--user", "machinarchy access add "+name+" --user USER")
	}
	if err != nil {
		return "", wizardStopped(err)
	}
	if strings.TrimSpace(user) == "" {
		return "", clierr.Usagef("the SSH login user cannot be empty")
	}

	return strings.TrimSpace(user), nil
}

// chooseHostKeyPolicy applies the host-key policy; tofu is offered only with a key.
func chooseHostKeyPolicy(p *ui.Prompter, given, how string) (string, error) {
	switch given {
	case policyApprove, policyPinned:
		return given, nil
	case policyTOFU:
		if how != howSSHKey {
			return "", clierr.Detailed(clierr.ExitUsage, errors.New("tofu works only with an SSH key"),
				"trusting the first key seen is allowed only with key logins", "--host-key-policy approve")
		}

		return given, nil
	case "":
	default:
		return "", clierr.Usagef("--host-key-policy must be approve, tofu or pinned")
	}
	if !p.Interactive {
		return policyApprove, nil
	}
	options, values := []string{"ask me to approve each host key (recommended)", "only keys I pin"}, []string{policyApprove, policyPinned}
	if how == howSSHKey {
		options = append(options, "trust the first key seen (a lab setting)")
		values = append(values, policyTOFU)
	}
	index, err := p.Choice("Which SSH host keys does Machinarchy accept?", options, 0)
	if err != nil {
		return "", wizardStopped(err)
	}

	return values[index], nil
}

// readCredential gets the key or password from a file, stdin or a hidden prompt.
func readCredential(command *cobra.Command, p *ui.Prompter, how string, o accessAddOptions) (string, error) {
	switch {
	case how == howSSHKey && o.passwordStdin, how == howSSHPassword && o.keyFile != "":
		return "", clierr.Usagef("--key-file goes with --how ssh-key and --password-stdin with --how ssh-password")
	case o.keyFile != "" && o.passwordStdin:
		return "", clierr.Usagef("use one of --key-file and --password-stdin")
	case how == howSSHKey && o.keyFile != "":
		return readKeyFile(o.keyFile)
	case how == howSSHPassword && o.passwordStdin:
		return readLimitedStdin(command.InOrStdin(), "the password")
	}

	if how == howSSHKey {
		path, err := p.Line("Path of the SSH private key file", "")
		if errors.Is(err, ui.ErrNoInput) {
			return "", inputRequired("--key-file", "machinarchy access add NAME --how ssh-key --key-file FILE")
		}
		if err != nil {
			return "", wizardStopped(err)
		}

		return readKeyFile(strings.TrimSpace(path))
	}
	password, err := p.Secret("SSH password (not shown)")
	if errors.Is(err, ui.ErrNoInput) {
		return "", inputRequired("--password-stdin", "printf '%s' \"$PASSWORD\" | machinarchy access add NAME --how ssh-password --user USER --password-stdin")
	}
	if err != nil {
		return "", wizardStopped(err)
	}
	if password == "" {
		return "", clierr.Usagef("the password cannot be empty")
	}

	return password, nil
}

func readKeyFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", clierr.Newf(clierr.ExitNotFound, "Check the path of the private key file.", "cannot read the key file %q", sanitize.Line(path))
	}
	if !info.Mode().IsRegular() || info.Size() > maxKeyFile {
		return "", clierr.Usagef("%q is not a regular key file of at most 1 MiB", sanitize.Line(path))
	}
	data, err := os.ReadFile(path) // #nosec G304 -- the path is the user's own --key-file.
	if err != nil {
		return "", clierr.Newf(clierr.ExitFailure, "Check the file's permissions.", "cannot read the key file %q", sanitize.Line(path))
	}
	text := strings.TrimSpace(string(data))
	if !strings.Contains(text, "PRIVATE KEY") {
		return "", clierr.Detailed(clierr.ExitUsage, fmt.Errorf("%q does not look like an SSH private key", sanitize.Line(path)),
			"a private key file starts with -----BEGIN ... PRIVATE KEY-----; a .pub file is the public half and cannot log in",
			"machinarchy access add NAME --how ssh-key --key-file PATH-OF-THE-PRIVATE-KEY")
	}

	return string(data), nil
}

func readLimitedStdin(in io.Reader, what string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(in, maxKeyFile+1))
	if err != nil {
		return "", fmt.Errorf("read %s from stdin: %w", what, err)
	}
	if len(data) > maxKeyFile {
		return "", clierr.Usagef("%s on stdin is longer than 1 MiB", what)
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if value == "" {
		return "", clierr.Usagef("%s on stdin is empty", what)
	}

	return value, nil
}

// createAccessWithSecret stores the credential as a secret, then the entry. If
// the entry cannot be created the new secret is retired again.
func createAccessWithSecret(command *cobra.Command, f *cli.Factory, name, description, value string, spec map[string]any) error {
	profile, err := makeAccessWithSecret(command, f, name, description, value, spec)
	if err != nil {
		return err
	}

	return announceAccess(command, f, profile)
}

// makeAccessWithSecret is createAccessWithSecret without the success line.
func makeAccessWithSecret(command *cobra.Command, f *cli.Factory, name, description, value string, spec map[string]any) (*api.AccessProfile, error) {
	management, err := f.NewManagementClient(command.Context())
	if err != nil {
		return nil, err
	}
	document, err := json.Marshal(map[string]string{"name": name, "description": "Credential of access entry " + name,
		"purpose": "ssh-credential", "value": value})
	if err != nil {
		return nil, err
	}
	raw, err := management.CreateSecret(command.Context(), document)
	if err != nil {
		return nil, fmt.Errorf("store the credential: %w", err)
	}
	var created secretMetadata
	if err := json.Unmarshal(raw, &created); err != nil {
		return nil, fmt.Errorf("decode the stored credential: %w", err)
	}
	spec["credential"] = map[string]any{"id": created.ID, "version": created.CurrentVersion}

	profile, err := makeAccess(command, f, name, description, spec)
	if err != nil {
		if retireErr := management.DeleteSecret(command.Context(), created.ID); retireErr != nil {
			return nil, fmt.Errorf("%w (the stored credential %s could not be removed again: %w; remove it with 'machinarchy secret delete %s')",
				err, created.ID, retireErr, created.ID)
		}

		return nil, err
	}

	return profile, nil
}

func createAccess(command *cobra.Command, f *cli.Factory, name, description string, spec map[string]any) error {
	profile, err := makeAccess(command, f, name, description, spec)
	if err != nil {
		return err
	}

	return announceAccess(command, f, profile)
}

func makeAccess(command *cobra.Command, f *cli.Factory, name, description string, spec map[string]any) (*api.AccessProfile, error) {
	_, access, _, err := accessClients(command.Context(), f)
	if err != nil {
		return nil, err
	}
	profile, err := access.CreateAccessProfile(command.Context(), api.AccessProfileInput{Name: name, Description: description, Spec: spec})
	if err != nil {
		return nil, fmt.Errorf("add access entry: %w", err)
	}

	return profile, nil
}

func announceAccess(command *cobra.Command, f *cli.Factory, profile *api.AccessProfile) error {
	if f.Config().Quiet() {
		_, err := fmt.Fprintln(command.OutOrStdout(), profile.ID)

		return err
	}

	return printMessage(command, f, msg.Get("access.created").With(msg.Vars{"name": sanitize.Line(profile.Name)}))
}

func newAccessRemoveCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove an access entry (owner)",
		Long: "Removes an access entry from the server. You confirm by typing its name (in a script: --confirm NAME); --yes is\n" +
			"not enough. The server refuses while hosts use the entry. The stored key or password is NOT retired: remove it\n" +
			"with 'machinarchy secret delete' when it is no longer needed.",
		Example: "  machinarchy access remove old-key\n  machinarchy access remove old-key --confirm old-key",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			client, access, _, err := accessClients(command.Context(), f)
			if err != nil {
				return err
			}
			id, err := resolveProfile(command, client, args[0])
			if err != nil {
				return err
			}
			if err := f.ConfirmTyped(fmt.Sprintf("Remove access entry %q? Hosts that use it must be moved first.", sanitize.Line(args[0])), args[0]); err != nil {
				return err
			}
			if err := access.DeleteAccessProfile(command.Context(), id); err != nil {
				return fmt.Errorf("remove access entry: %w", err)
			}

			return printMessage(command, f, msg.Get("access.removed").With(msg.Vars{"name": sanitize.Line(args[0])}))
		},
	}
}
