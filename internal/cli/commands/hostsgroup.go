package commands

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/msg"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
)

// The `hosts` command (docs/cli/spec/02-commands.md, section 3.8): list, show,
// tag, move and remove. Adding, trusting and enrolling arrive in later steps.

// Plain readiness values (spec 3.8.1).
const (
	statusReady     = "ready"
	statusKey       = "waiting for key approval"
	statusKeyChange = "key changed"
	statusChecking  = "checking"
	statusNoConnect = "cannot connect"
	statusAttention = "needs attention"
)

// labelLimits mirror the controller's label rules; the CLI may not import the
// controller's packages (ADR-035), so they are copied and checked server-side too.
const (
	maxLabelLength = 63
	maxLabels      = 50
)

var (
	labelKeyPattern   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._/-]*[A-Za-z0-9])?$`)
	labelValuePattern = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?)?$`)
	reservedLabelKeys = map[string]bool{"id": true, "name": true, "hostname": true, "address": true,
		"status": true, "environment": true, "connection": true}
)

// hostReadiness maps a host's conditions and phase to a plain status and the
// raw reason of its Ready condition.
func hostReadiness(host api.Host) (status, reason string) {
	var ready *api.Condition
	for i := range host.Status.Conditions {
		if host.Status.Conditions[i].Type == "Ready" {
			ready = &host.Status.Conditions[i]
		}
	}
	if ready == nil {
		return statusChecking, ""
	}
	reason = ready.Reason
	switch {
	case ready.Status == "True":
		return statusReady, reason
	case reason == "HostKeyPendingApproval":
		return statusKey, reason
	case reason == "HostKeyChanged":
		return statusKeyChange, reason
	case host.Status.Phase == "Pending" || reason == "NotObserved":
		return statusChecking, reason
	case reason == "ConnectionFailed" || reason == "TargetNotAllowed":
		return statusNoConnect, reason
	default:
		return statusAttention, reason
	}
}

// NewHostsCommand lists hosts and manages them one by one.
func NewHostsCommand(f *cli.Factory) *cobra.Command {
	var status, selector string
	command := &cobra.Command{
		Use:   "hosts [--status ready|attention|all]",
		Short: "List, show, tag, trust and remove hosts",
		Long: "Lists the machines Machinarchy manages: name, env, address, how it reaches them (ssh or agent), a plain status and tags.\n" +
			"Statuses: ready, waiting for key approval, key changed, checking, cannot connect, needs attention.\n" +
			"Use --env to look at one env.",
		Example: "  machinarchy hosts\n  machinarchy hosts --status attention\n  machinarchy hosts show web-01\n  machinarchy hosts tag web-01 tier=web canary-",
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return listHosts(command, f, status, selector)
		},
	}
	command.Flags().StringVar(&status, "status", "all", "Show ready, attention (everything not ready) or all hosts")
	command.Flags().StringVarP(&selector, "tagged", "l", "", "Only hosts with these tags, for example tier=web")
	command.AddCommand(newHostsAddCommand(f), newHostsShowCommand(f), newHostsTagCommand(f), newHostsMoveCommand(f), newHostsTrustCommand(f), newHostsEnrollCommand(f), newHostsRevokeAgentCommand(f), newHostsRemoveCommand(f))
	completeChildren(command, completeHostNames(f), "show", "tag", "move", "trust", "enroll", "revoke-agent", "remove")

	return command
}

// hostFacts caches what a command looks up about environments and access.
type hostFacts struct {
	envs map[int]string
	how  map[string]string
}

func loadHostFacts(ctx context.Context, client api.Client, hosts []api.Host) (*hostFacts, error) {
	facts := &hostFacts{envs: map[int]string{}, how: map[string]string{}}
	environments, err := client.ListEnvironments(ctx)
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	for _, environment := range environments {
		facts.envs[environment.ID] = environment.Name
	}
	inventory, ok := client.(api.InventoryClient)
	if !ok {
		return facts, nil
	}
	for _, host := range hosts {
		id := host.Spec.AccessProfileID
		if id == "" {
			continue
		}
		if _, done := facts.how[id]; done {
			continue
		}
		profile, err := inventory.GetAccessProfile(ctx, id)
		if err != nil {
			facts.how[id] = "?"

			continue
		}
		facts.how[id] = connectorWord(profile.Spec)
	}

	return facts, nil
}

func connectorWord(spec map[string]any) string {
	if kind := specString(spec, "connectorKind"); kind != "" {
		return kind
	}

	return "?"
}

func (h *hostFacts) howOf(host api.Host) string {
	if how := h.how[host.Spec.AccessProfileID]; how != "" {
		return how
	}

	return "-"
}

func (h *hostFacts) envOf(host api.Host) string {
	if name := h.envs[host.Metadata.EnvironmentID]; name != "" {
		return name
	}

	return "-"
}

func formatTags(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
	}

	return strings.Join(parts, ",")
}

func listHosts(command *cobra.Command, f *cli.Factory, status, selector string) error {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != "ready" && status != "attention" && status != "all" {
		return clierr.Usagef("--status must be ready, attention or all")
	}
	printer, err := f.Printer(command.OutOrStdout(), output.FormatTable)
	if err != nil {
		return err
	}
	client, err := f.NewAPIClient(command.Context())
	if err != nil {
		return err
	}
	query := api.HostQuery{Selector: selector}
	if query.EnvironmentID, err = resolveEnvironment(command.Context(), client, f.Config().Environment()); err != nil {
		return err
	}
	all, err := listAllHosts(command.Context(), client, query)
	if err != nil {
		return fmt.Errorf("list hosts: %w", err)
	}
	hosts := all[:0:0]
	for _, host := range all {
		plain, _ := hostReadiness(host)
		if status == "all" || (status == "ready") == (plain == statusReady) {
			hosts = append(hosts, host)
		}
	}

	if printer.Format() != output.FormatTable || f.Config().Quiet() {
		if f.Config().Quiet() && printer.Format() == output.FormatTable {
			names := make([]string, 0, len(hosts))
			for _, host := range hosts {
				names = append(names, host.Metadata.Name)
			}

			return printer.IDs(names)
		}

		return printer.Document(hosts)
	}
	if len(hosts) == 0 {
		return printMessage(command, f, msg.Get("list.no_hosts"))
	}
	facts, err := loadHostFacts(command.Context(), client, hosts)
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(hosts))
	for _, host := range hosts {
		plain, _ := hostReadiness(host)
		address := host.Spec.AccessTarget.Endpoint
		if address == "" {
			address = "-"
		}
		rows = append(rows, []string{sanitize.Line(host.Metadata.Name), sanitize.Line(facts.envOf(host)), sanitize.Line(address),
			facts.howOf(host), plain, sanitize.Line(formatTags(host.Metadata.Labels))})
	}

	return printer.Table([]string{"NAME", "ENV", "ADDRESS", "HOW", "STATUS", "TAGS"}, rows)
}

func newHostsShowCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "show NAME",
		Short:   "Show a host: status, address, access, env, tags, keys and recent runs",
		Example: "  machinarchy hosts show web-01\n  machinarchy hosts show web-01 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			printer, err := f.Printer(command.OutOrStdout(), output.FormatTable)
			if err != nil {
				return err
			}
			client, err := f.NewAPIClient(command.Context())
			if err != nil {
				return err
			}
			found, err := findHost(command.Context(), f, client, args[0])
			if err != nil {
				return err
			}
			host, err := client.GetHost(command.Context(), found.Metadata.ID)
			if err != nil {
				return fmt.Errorf("read host: %w", err)
			}
			if printer.Format() != output.FormatTable {
				return printer.Document(host)
			}
			facts, err := loadHostFacts(command.Context(), client, []api.Host{*host})
			if err != nil {
				return err
			}
			runs, runsErr := client.ListOperations(command.Context(), api.OperationQuery{HostID: host.Metadata.ID, Limit: 5})

			_, err = fmt.Fprint(command.OutOrStdout(), renderHost(*host, facts, runs.Items, runsErr))

			return err
		},
	}
}

// renderHost shows the separate things a person needs to tell apart.
func renderHost(host api.Host, facts *hostFacts, runs []api.OperationSummary, runsErr error) string {
	var b strings.Builder
	plain, reason := hostReadiness(host)
	line := func(label, value string) { fmt.Fprintf(&b, "%-14s %s\n", label+":", value) }

	line("Name", sanitize.Line(host.Metadata.Name))
	statusText := plain
	if reason != "" && plain != statusReady {
		statusText += " (" + sanitize.Line(reason) + ")"
	}
	line("Status", statusText)
	line("Env", sanitize.Line(facts.envOf(host)))
	address := host.Spec.AccessTarget.Endpoint
	if address == "" {
		address = "-"
	}
	line("Address", sanitize.Line(address))
	line("How", facts.howOf(host))
	line("Tags", valueOrDash(sanitize.Line(formatTags(host.Metadata.Labels))))
	for _, condition := range host.Status.Conditions {
		text := condition.Status
		if condition.Reason != "" {
			text += " (" + sanitize.Line(condition.Reason) + ")"
		}
		if condition.Message != "" {
			text += ": " + sanitize.Line(condition.Message)
		}
		line(sanitize.Line(condition.Type), text)
	}
	trusted := make(map[string]bool, len(host.Spec.AccessTarget.Trust))
	for _, fingerprint := range host.Spec.AccessTarget.Trust {
		trusted[fingerprint] = true
	}
	if len(host.Status.ObservedHostKeys) == 0 {
		line("Host keys", "none observed yet")
	}
	for _, key := range host.Status.ObservedHostKeys {
		mark := "not trusted"
		if trusted[key.Fingerprint] {
			mark = "trusted"
		}
		line("Host key", fmt.Sprintf("%s %s (%s)", sanitize.Line(key.Algorithm), sanitize.Line(key.Fingerprint), mark))
	}
	switch {
	case runsErr != nil:
		line("Recent runs", "could not be read")
	case len(runs) == 0:
		line("Recent runs", "none")
	default:
		for _, run := range runs {
			line("Run", fmt.Sprintf("%s %s", sanitize.Line(run.ID), sanitize.Line(run.State)))
		}
	}

	return b.String()
}

func valueOrDash(s string) string {
	if s == "" {
		return "-"
	}

	return s
}

// hostEnvName returns the name of the host's env and whether this computer
// guards it.
func hostEnvName(ctx context.Context, f *cli.Factory, client api.Client, host api.Host) (string, bool, error) {
	if host.Metadata.EnvironmentID == 0 {
		return "", false, nil
	}
	environments, err := client.ListEnvironments(ctx)
	if err != nil {
		return "", false, fmt.Errorf("list environments: %w", err)
	}
	for _, environment := range environments {
		if environment.ID == host.Metadata.EnvironmentID {
			return environment.Name, f.Config().LocalProtected(environment.Name), nil
		}
	}

	return "", false, nil
}

// confirmProtectedEnv applies the local guard: the env's name is typed on a
// terminal or given with --confirm-env. Nothing is asked for an unprotected env.
func confirmProtectedEnv(f *cli.Factory, env string, protected bool) error {
	if err := checkProtectedConfirmation(f, env, protected); err != nil {
		return err
	}
	if !protected || f.ConfirmEnv() == env {
		return nil
	}
	answer, err := f.Prompter().Line(fmt.Sprintf("%q is a protected env. Type its name to continue", sanitize.Line(env)), "")
	if err != nil || strings.TrimSpace(answer) != env {
		return clierr.Newf(clierr.ExitFailure, "", "aborted; nothing was changed")
	}

	return nil
}

func validateTags(set map[string]string, remove []string) error {
	for key, value := range set {
		switch {
		case len(key) > maxLabelLength || !labelKeyPattern.MatchString(key):
			return clierr.Usagef("tag key %q must be 1-%d letters, digits, '.', '_', '-' or '/', starting and ending with a letter or digit", sanitize.Line(key), maxLabelLength)
		case reservedLabelKeys[strings.ToLower(key)]:
			return clierr.Usagef("tag key %q is reserved; choose another name", key)
		case len(value) > maxLabelLength || !labelValuePattern.MatchString(value):
			return clierr.Usagef("the value of tag %q must be at most %d letters, digits, '.', '_' or '-', starting and ending with a letter or digit", key, maxLabelLength)
		}
	}
	for _, key := range remove {
		if _, clash := set[key]; clash {
			return clierr.Usagef("tag %q is both set and removed", key)
		}
	}

	return nil
}

func newHostsTagCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "tag NAME KEY=VALUE... KEY-...",
		Short:   "Set tags with KEY=VALUE and remove them with KEY-",
		Example: "  machinarchy hosts tag web-01 tier=web canary-",
		Args:    cobra.MinimumNArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			set, remove, err := parseLabels(args[1:])
			if err != nil {
				return err
			}
			if err := validateTags(set, remove); err != nil {
				return err
			}
			client, err := f.NewAPIClient(command.Context())
			if err != nil {
				return err
			}
			host, err := findHost(command.Context(), f, client, args[0])
			if err != nil {
				return err
			}
			labels := maps.Clone(host.Metadata.Labels)
			if labels == nil {
				labels = map[string]string{}
			}
			maps.Copy(labels, set)
			for _, key := range remove {
				delete(labels, key)
			}
			if len(labels) > maxLabels {
				return clierr.Usagef("a host may have at most %d tags", maxLabels)
			}
			env, protected, err := hostEnvName(command.Context(), f, client, host)
			if err != nil {
				return err
			}
			if err := confirmProtectedEnv(f, env, protected); err != nil {
				return err
			}
			if _, err := client.SetHostLabels(command.Context(), host.Metadata.ID, host.Metadata.Revision, labels); err != nil {
				return staleHost(fmt.Errorf("tag host: %w", err))
			}
			tags := valueOrDash(sanitize.Line(formatTags(labels)))

			return printMessage(command, f, msg.Get("host.tagged").With(msg.Vars{"host": sanitize.Line(host.Metadata.Name), "tags": tags}))
		},
	}
}

// staleHost explains a revision conflict, which means somebody changed the host.
func staleHost(err error) error {
	var apiErr *api.Error
	if errors.As(err, &apiErr) && apiErr.ExitCode() == clierr.ExitConflict {
		return clierr.Detailed(clierr.ExitFailure, errors.New("the host changed while you were working on it"),
			"someone or something else changed it first, so nothing was changed", "run the same command again")
	}

	return err
}

func newHostsMoveCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "move NAME --env ENV",
		Short: "Move a host to another env",
		Long: "Moves a host to another env. Every host is in exactly one env; the host keeps its name, tags and access.\n" +
			"A locally protected source or target env needs its name typed (or --confirm-env in a script).",
		Example: "  machinarchy hosts move web-01 --env staging",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			target := strings.TrimSpace(f.Config().Environment())
			if target == "" {
				return clierr.Detailed(clierr.ExitUsage, errors.New("say which env to move the host to"), "", "machinarchy hosts move "+args[0]+" --env NAME")
			}
			client, err := f.NewAPIClient(command.Context())
			if err != nil {
				return err
			}
			hosts, err := listAllHosts(command.Context(), client, api.HostQuery{})
			if err != nil {
				return fmt.Errorf("list hosts: %w", err)
			}
			host, err := pickHost(hosts, args[0])
			if err != nil {
				return err
			}
			targetID, err := resolveEnvironment(command.Context(), client, target)
			if err != nil {
				return err
			}
			name := sanitize.Line(host.Metadata.Name)
			if host.Metadata.EnvironmentID == targetID {
				_, err = fmt.Fprintf(command.OutOrStdout(), "%s is already in %s; nothing was changed.\n", name, sanitize.Line(target))
				return err
			}
			source, sourceProtected, err := hostEnvName(command.Context(), f, client, host)
			if err != nil {
				return err
			}
			if sourceProtected {
				if err = confirmProtectedEnv(f, source, true); err != nil {
					return err
				}
			}
			if f.Config().LocalProtected(target) {
				if err = confirmProtectedEnv(f, target, true); err != nil {
					return err
				}
			}
			if err = client.MoveHost(command.Context(), host.Metadata.ID, host.Metadata.Revision, host.Metadata.Generation, host.Spec, targetID); err != nil {
				var apiErr *api.Error
				if errors.As(err, &apiErr) && apiErr.Status == 409 {
					return clierr.Detailed(clierr.ExitConflict, fmt.Errorf("the host %s changed meanwhile", name),
						"nothing was changed", "machinarchy hosts move "+host.Metadata.Name+" --env "+target)
				}
				return fmt.Errorf("move host: %w", err)
			}
			moved, err := client.GetHost(command.Context(), host.Metadata.ID)
			if err != nil {
				return fmt.Errorf("read the host after the move: %w", err)
			}
			if moved.Metadata.EnvironmentID != targetID {
				return clierr.Detailed(clierr.ExitFailure, errors.New("this server cannot move hosts between envs"),
					fmt.Sprintf("host %q is still in its old env; the server ignored the request (it predates envs on hosts)", name),
					"update the server")
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "Moved %s to %s.\n", name, sanitize.Line(target))
			return err
		},
	}
}

func newHostsRemoveCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove a host from the inventory (nothing else)",
		Long: "Removes the host from Machinarchy's inventory and nothing else: an installed agent keeps running and keeps its\n" +
			"identity, the access entry and its credentials stay, and the machine is not touched. You confirm by typing the\n" +
			"host's name (in a script: --confirm NAME); --yes is not enough. A host in a protected env also needs the env's name.\n" +
			"The server refuses while a run targets the host.",
		Example: "  machinarchy hosts remove web-01\n  machinarchy hosts remove web-01 --confirm web-01",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			client, err := f.NewAPIClient(command.Context())
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
			if err := f.ConfirmTyped(fmt.Sprintf("Remove host %q from the inventory? The machine itself is not touched.", name), host.Metadata.Name); err != nil {
				return err
			}
			if err := client.DeleteHost(command.Context(), host.Metadata.ID, host.Metadata.Revision); err != nil {
				return fmt.Errorf("remove host: %w", err)
			}

			return printMessage(command, f, msg.Get("host.removed").With(msg.Vars{"host": name}))
		},
	}
}
