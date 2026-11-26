package commands

import (
	"errors"
	"fmt"
	"regexp"
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

// fingerprintShape is what an SSH key fingerprint looks like. The typed check
// compares what the person read with the key, so a key that does not look like
// this is refused instead of being shown in a form that could mislead.
var fingerprintShape = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/=]{8,}$`)

// fingerprintPrefixLength is how many characters after "SHA256:" are typed.
const fingerprintPrefixLength = 8

func newHostsTrustCommand(f *cli.Factory) *cobra.Command {
	var replace, allPending bool
	var selector string
	var fingerprints []string
	command := &cobra.Command{
		Use:   "trust NAME [--replace] [--fingerprint SHA256:...] | --all-pending [-l TAGS]",
		Short: "Approve the SSH host keys a host presents (owner only)",
		Long: "Shows the SSH host keys the server saw on a host and trusts them after you check them.\n" +
			"On a terminal you compare the key with the machine and type yes; --yes does not answer this question.\n" +
			"In a script, pass --fingerprint SHA256:... of a key you checked on the machine; it must be one the server saw.\n" +
			"A host whose key changed is replaced with --replace: you also type the host's name (--confirm NAME in a script).\n" +
			"--all-pending goes through the hosts waiting for approval one by one and needs a terminal.\n" +
			"All keys the server saw on the host are trusted together, because the server refuses an incomplete set.",
		Example: "  machinarchy hosts trust web-01\n" +
			"  machinarchy hosts trust web-01 --fingerprint SHA256:Qm3vL0xk2G7c9R1tYp8aWuHn5dEoZf4sBjIiK6cXe9k\n" +
			"  machinarchy hosts trust web-01 --replace\n" +
			"  machinarchy hosts trust --all-pending -l tier=web",
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if allPending == (len(args) == 1) {
				return clierr.Usagef("give a host NAME, or --all-pending")
			}
			if allPending && (replace || len(fingerprints) > 0) {
				return clierr.Usagef("--all-pending asks about each host in turn; --replace and --fingerprint need one host NAME")
			}
			for _, fingerprint := range fingerprints {
				if !fingerprintShape.MatchString(fingerprint) {
					return clierr.Usagef("--fingerprint %q is not a SHA256 fingerprint (SHA256:...)", sanitize.Line(fingerprint))
				}
			}
			if allPending && !f.Prompter().Interactive {
				return clierr.Detailed(clierr.ExitUsage,
					errors.New("--all-pending needs a terminal: it would trust keys nobody looked at"),
					"approving every waiting host at once is not possible without looking at each key",
					"machinarchy hosts trust NAME --fingerprint SHA256:...")
			}
			client, err := f.NewAPIClient(command.Context())
			if err != nil {
				return err
			}
			if allPending {
				return trustAllPending(command, f, client, selector)
			}
			host, err := findHost(command.Context(), f, client, args[0])
			if err != nil {
				return err
			}

			return trustHost(command, f, client, host, replace, fingerprints)
		},
	}
	command.Flags().BoolVar(&replace, "replace", false, "Replace the key the host trusts with the key it presents now")
	command.Flags().BoolVar(&allPending, "all-pending", false, "Go through every host waiting for key approval (needs a terminal)")
	command.Flags().StringVarP(&selector, "tagged", "l", "", "Narrow --all-pending to hosts with these tags, for example tier=web")
	command.Flags().StringArrayVar(&fingerprints, "fingerprint", nil, "The fingerprint you checked on the machine (repeatable)")

	return command
}

// observedFingerprints returns the keys the server saw, refusing odd ones.
func observedFingerprints(host api.Host) ([]string, error) {
	var keys []string
	for _, key := range host.Status.ObservedHostKeys {
		if !fingerprintShape.MatchString(key.Fingerprint) {
			return nil, clierr.Detailed(clierr.ExitFailure,
				fmt.Errorf("the server sent a host key for %q that does not look like a SHA256 fingerprint", sanitize.Line(host.Metadata.Name)),
				"a key that cannot be checked is never trusted", "machinarchy hosts show "+host.Metadata.Name)
		}
		if !slices.Contains(keys, key.Fingerprint) {
			keys = append(keys, key.Fingerprint)
		}
	}

	return keys, nil
}

// keysToConfirm says which keys the person must check: all observed ones for a
// first trust, the ones that are not trusted yet for a replacement.
func keysToConfirm(host api.Host, replace bool) (observed, check []string, err error) {
	observed, err = observedFingerprints(host)
	if err != nil {
		return nil, nil, err
	}
	name := host.Metadata.Name
	trusted := host.Spec.AccessTarget.Trust
	switch {
	case len(observed) == 0:
		return nil, nil, clierr.Detailed(clierr.ExitUsage,
			fmt.Errorf("the server has not seen an SSH key on %q yet", sanitize.Line(name)),
			"it records the key when it first connects", "machinarchy hosts show "+name)
	case replace && len(trusted) == 0:
		return nil, nil, clierr.Detailed(clierr.ExitUsage,
			fmt.Errorf("%q trusts no key yet, so there is nothing to replace", sanitize.Line(name)),
			"", "machinarchy hosts trust "+name)
	case !replace && len(trusted) > 0:
		return nil, nil, clierr.Detailed(clierr.ExitUsage,
			fmt.Errorf("%q already trusts %s", sanitize.Line(name), sanitize.Line(strings.Join(trusted, ", "))),
			"changing a trusted key is a separate, stricter step; check the new key out of band first",
			"machinarchy hosts trust "+name+" --replace")
	}
	if !replace {
		return observed, observed, nil
	}
	for _, key := range observed {
		if !slices.Contains(trusted, key) {
			check = append(check, key)
		}
	}
	if len(check) == 0 {
		return nil, nil, clierr.Detailed(clierr.ExitUsage,
			fmt.Errorf("%q presents only keys it already trusts", sanitize.Line(name)),
			"nothing was changed", "machinarchy hosts show "+name)
	}

	return observed, check, nil
}

// showKeys prints what is about to be trusted, always, so a person or a log
// can see it.
func showKeys(command *cobra.Command, host api.Host, check []string, replace bool) error {
	out := command.OutOrStdout()
	name := sanitize.Line(host.Metadata.Name)
	if replace {
		if _, err := fmt.Fprintf(out, "Replace the trusted key of %s (%s)\n", name, sanitize.Line(host.Spec.AccessTarget.Endpoint)); err != nil {
			return err
		}
		for _, key := range host.Spec.AccessTarget.Trust {
			if _, err := fmt.Fprintf(out, "  Old   %s\n", sanitize.Line(key)); err != nil {
				return err
			}
		}
		for _, key := range check {
			if _, err := fmt.Fprintf(out, "  New   %s%s\n", sanitize.Line(key), keyKind(host, key)); err != nil {
				return err
			}
		}

		return nil
	}
	if _, err := fmt.Fprintf(out, "%s (%s) presents:\n", name, sanitize.Line(host.Spec.AccessTarget.Endpoint)); err != nil {
		return err
	}
	for _, key := range check {
		if _, err := fmt.Fprintf(out, "  %s%s\n", sanitize.Line(key), keyKind(host, key)); err != nil {
			return err
		}
	}

	return nil
}

// keyKind labels a fingerprint with its key type, so the person knows which
// ssh-keygen line on the machine to compare it with.
func keyKind(host api.Host, fingerprint string) string {
	for _, key := range host.Status.ObservedHostKeys {
		if key.Fingerprint == fingerprint && key.Algorithm != "" {
			return "  (" + sanitize.Line(key.Algorithm) + ")"
		}
	}

	return ""
}

func confirmationPrompt(key string) (question, expected string) {
	expected = strings.TrimPrefix(key, "SHA256:")[:fingerprintPrefixLength]

	return "Compare with the key on the machine. Trust it? Type yes to trust it, or press Enter to skip", expected
}

// askKeyPrefix asks for the first characters of key. skipped is true when the
// person pressed Enter or typed "back"; a wrong answer is an error.
func askKeyPrefix(f *cli.Factory, host api.Host, key string, replace bool) (skipped bool, err error) {
	question, expected := confirmationPrompt(key)
	if replace {
		question = fmt.Sprintf("Did you confirm the new key with whoever runs %s, by a channel other than Machinarchy?\n"+
			"Type the first %d characters of the NEW key (after SHA256:), or press Enter to cancel", sanitize.Line(host.Metadata.Name), fingerprintPrefixLength)
	}
	answer, err := f.Prompter().Line(question, "")
	switch {
	case errors.Is(err, ui.ErrBack):
		return true, nil
	case err != nil:
		return false, err
	}
	answer = strings.TrimSpace(answer)
	if !replace {
		// A first key is approved by typing yes; the typed prefix is kept for a
		// replaced (changed) key, which is the case worth slowing down for.
		switch strings.ToLower(answer) {
		case "y", "yes":
			return false, nil
		case "", "n", "no":
			return true, nil
		}

		return false, clierr.Newf(clierr.ExitFailure, "", "answer yes to trust the key, or press Enter to skip; nothing was trusted")
	}
	if answer == "" {
		return true, nil
	}
	if answer != expected {
		return false, clierr.Newf(clierr.ExitFailure, "", "that is not the start of the key; nothing was trusted")
	}

	return false, nil
}

// needFingerprint is the error of a run that cannot ask.
func needFingerprint(host api.Host, replace bool, check []string) error {
	next := "machinarchy hosts trust " + host.Metadata.Name
	if replace {
		next += " --replace"
	}
	next += " --fingerprint " + check[0]
	if replace {
		next += " --confirm " + host.Metadata.Name
	}

	return clierr.Detailed(clierr.ExitUsage,
		errors.New("nothing can be asked here, and --yes does not approve an SSH key"),
		"a key is trusted only after you compared it with the one on the machine", next)
}

// trustHost approves (or, with replace, replaces) the keys of one host.
func trustHost(command *cobra.Command, f *cli.Factory, client api.Client, host api.Host, replace bool, fingerprints []string) error {
	observed, check, err := keysToConfirm(host, replace)
	if err != nil {
		return err
	}
	env, protected, err := hostEnvName(command.Context(), f, client, host)
	if err != nil {
		return err
	}
	if err := checkProtectedConfirmation(f, env, protected); err != nil {
		return err
	}
	for _, fingerprint := range fingerprints {
		if !slices.Contains(check, fingerprint) {
			return clierr.Detailed(clierr.ExitUsage,
				fmt.Errorf("--fingerprint %s is not a key the server saw on %q", sanitize.Line(fingerprint), sanitize.Line(host.Metadata.Name)),
				"the server saw: "+sanitize.Line(strings.Join(check, ", ")), "machinarchy hosts show "+host.Metadata.Name)
		}
	}
	if err := showKeys(command, host, check, replace); err != nil {
		return err
	}
	interactive := f.Prompter().Interactive
	if len(fingerprints) == 0 && !interactive {
		return needFingerprint(host, replace, check)
	}
	if err := confirmProtectedEnv(f, env, protected); err != nil {
		return err
	}
	if len(fingerprints) == 0 {
		skipped, err := askKeyPrefix(f, host, check[0], replace)
		if err != nil {
			return err
		}
		if skipped {
			return clierr.Newf(clierr.ExitFailure, "", "skipped; nothing was changed")
		}
	}
	name := sanitize.Line(host.Metadata.Name)
	if replace {
		what := fmt.Sprintf("Replace the trusted key of %s. Commands to it are accepted again once the new key is trusted.", name)
		if err := f.ConfirmTyped(what, host.Metadata.Name); err != nil {
			return err
		}
	}
	if _, err := client.TrustHostKeys(command.Context(), host.Metadata.ID, host.Metadata.Generation, observed, replace); err != nil {
		return fmt.Errorf("trust host %q: %w", name, staleHost(err))
	}
	id := "host.trusted"
	if replace {
		id = "host.key_replaced"
	}

	return printMessage(command, f, msg.Get(id).With(msg.Vars{"host": name}))
}

// trustAllPending goes through the waiting hosts one at a time. Each key is
// typed in; nothing is approved in bulk.
func trustAllPending(command *cobra.Command, f *cli.Factory, client api.Client, selector string) error {
	all, err := listAllHosts(command.Context(), client, api.HostQuery{Selector: selector})
	if err != nil {
		return fmt.Errorf("list hosts: %w", err)
	}
	var pending []api.Host
	for _, host := range all {
		if readyReason(host) == "HostKeyPendingApproval" && len(host.Status.ObservedHostKeys) > 0 && len(host.Spec.AccessTarget.Trust) == 0 {
			pending = append(pending, host)
		}
	}
	out := command.OutOrStdout()
	if len(pending) == 0 {
		return printMessageTo(out, f, msg.Get("host.no_pending"))
	}
	trusted, skipped := 0, 0
	for _, host := range pending {
		observed, check, err := keysToConfirm(host, false)
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
		if err := showKeys(command, host, check, false); err != nil {
			return err
		}
		skip, err := askKeyPrefix(f, host, check[0], false)
		if err != nil {
			return err
		}
		if skip {
			skipped++

			continue
		}
		if _, err := client.TrustHostKeys(command.Context(), host.Metadata.ID, host.Metadata.Generation, observed, false); err != nil {
			return fmt.Errorf("trust host %q: %w", sanitize.Line(host.Metadata.Name), staleHost(err))
		}
		trusted++
		if err := printMessageTo(out, f, msg.Get("host.key_trusted").With(msg.Vars{"host": sanitize.Line(host.Metadata.Name)})); err != nil {
			return err
		}
	}
	return printMessageTo(out, f, msg.Get("host.trust_summary").With(msg.Vars{
		"trusted": strconv.Itoa(trusted), "skipped": strconv.Itoa(skipped)}))
}

// readyReason is the reason of the host's Ready condition.
func readyReason(host api.Host) string {
	for _, condition := range host.Status.Conditions {
		if condition.Type == "Ready" {
			return condition.Reason
		}
	}

	return ""
}
