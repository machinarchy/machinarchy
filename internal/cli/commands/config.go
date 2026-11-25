package commands

import (
	"fmt"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/config"
	"github.com/machinarchy/machinarchy/internal/cli/cred"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
)

// NewConfigCommand registers the hidden `config path` and `config show`. They
// are for support and scripts; nothing on the beginner path needs them, and
// they change nothing.
func NewConfigCommand(factory *cli.Factory) *cobra.Command {
	command := &cobra.Command{
		Use:    "config",
		Short:  "Show where the CLI keeps its settings",
		Hidden: true,
		Args:   cobra.NoArgs,
	}
	command.AddCommand(newConfigPathCommand(factory), newConfigShowCommand(factory))

	return command
}

func newConfigPathCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the config file and the credentials file",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			path := f.Config().Path()
			if _, err := fmt.Fprintln(command.OutOrStdout(), path); err != nil {
				return err
			}
			if f.Config().Quiet() {
				return nil
			}
			_, err := fmt.Fprintln(command.OutOrStdout(), config.CredentialsPath(path))

			return err
		},
	}
}

type showCredential struct {
	Kind      cred.Kind  `json:"kind" yaml:"kind"`
	Secret    string     `json:"secret" yaml:"secret"`
	User      string     `json:"user,omitempty" yaml:"user,omitempty"`
	Name      string     `json:"name,omitempty" yaml:"name,omitempty"`
	Role      string     `json:"role,omitempty" yaml:"role,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty" yaml:"expiresAt,omitempty"`
	Expired   bool       `json:"expired" yaml:"expired"`
}

type showServer struct {
	Name         string          `json:"name" yaml:"name"`
	URL          string          `json:"url" yaml:"url"`
	Active       bool            `json:"active" yaml:"active"`
	InsecureHTTP bool            `json:"insecureHttp" yaml:"insecureHttp"`
	CAFile       string          `json:"caFile,omitempty" yaml:"caFile,omitempty"`
	Env          string          `json:"env,omitempty" yaml:"env,omitempty"`
	Session      *showCredential `json:"session,omitempty" yaml:"session,omitempty"`
	Token        *showCredential `json:"token,omitempty" yaml:"token,omitempty"`
}

type showOption struct {
	Value  string `json:"value" yaml:"value"`
	Source string `json:"source" yaml:"source"`
}

type showDocument struct {
	ConfigFile      string                `json:"configFile" yaml:"configFile"`
	CredentialsFile string                `json:"credentialsFile" yaml:"credentialsFile"`
	CredentialsIn   string                `json:"credentialsIn" yaml:"credentialsIn"`
	Active          string                `json:"active,omitempty" yaml:"active,omitempty"`
	Servers         []showServer          `json:"servers" yaml:"servers"`
	Options         map[string]showOption `json:"options" yaml:"options"`
}

// newConfigShowCommand prints everything the CLI has saved and every effective
// option with its source. Every credential is shown as ***.
func newConfigShowCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the saved servers and the effective options, with secrets hidden",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			printer, err := f.Printer(command.OutOrStdout(), output.FormatTable)
			if err != nil {
				return err
			}
			doc, err := buildShow(f)
			if err != nil {
				return err
			}
			if printer.Format() != output.FormatTable {
				return printer.Document(doc)
			}

			return printShow(command, doc)
		},
	}
}

func buildShow(f *cli.Factory) (showDocument, error) {
	store := f.Config()
	doc := showDocument{
		ConfigFile: store.Path(), CredentialsFile: config.CredentialsPath(store.Path()), CredentialsIn: f.Credentials().Where(),
		Active: store.ActiveName(), Servers: []showServer{}, Options: map[string]showOption{},
	}

	now := f.Now()
	for _, name := range store.ServerNames() {
		server, _ := store.Server(name)
		entry, err := f.Credentials().Get(name)
		if err != nil {
			return showDocument{}, err
		}
		doc.Servers = append(doc.Servers, showServer{
			Name: name, URL: server.URL, Active: name == store.ActiveName(), InsecureHTTP: server.InsecureHTTP,
			CAFile: server.CAFile, Env: server.Env, Session: redact(entry.Session, now), Token: redact(entry.Token, now),
		})
	}

	for _, key := range []string{"server", "env", "output", "plain", "quiet", "verbose", "no-headers", "no-input", "request-timeout", "token"} {
		value, source := store.Setting(key)
		if key == "token" && value != "" {
			value = "***"
		}
		doc.Options[key] = showOption{Value: value, Source: source.String()}
	}

	return doc, nil
}

// redact copies a credential for display. The secret is replaced; nothing else
// about it is secret.
func redact(c *cred.Credential, now time.Time) *showCredential {
	if c == nil {
		return nil
	}

	return &showCredential{Kind: c.Kind, Secret: "***", User: c.User, Name: c.Name, Role: c.Role, ExpiresAt: c.ExpiresAt, Expired: c.Expired(now)}
}

func printShow(command *cobra.Command, doc showDocument) error {
	out := command.OutOrStdout()
	line := func(format string, args ...any) error {
		_, err := fmt.Fprintf(out, format+"\n", args...)

		return err
	}

	if err := line("Config file:       %s", sanitize.Line(doc.ConfigFile)); err != nil {
		return err
	}
	if err := line("Credentials file:  %s (%s)", sanitize.Line(doc.CredentialsFile), sanitize.Line(doc.CredentialsIn)); err != nil {
		return err
	}
	active := doc.Active
	if active == "" {
		active = "none"
	}
	if err := line("Active server:     %s", sanitize.Line(active)); err != nil {
		return err
	}

	for _, server := range doc.Servers {
		if err := line("\nServer %s  %s", sanitize.Line(server.Name), sanitize.Line(server.URL)); err != nil {
			return err
		}
		if server.InsecureHTTP {
			if err := line("  plain http allowed"); err != nil {
				return err
			}
		}
		if server.CAFile != "" {
			if err := line("  CA file: %s", sanitize.Line(server.CAFile)); err != nil {
				return err
			}
		}
		for _, c := range []struct {
			label string
			value *showCredential
		}{{"session", server.Session}, {"token", server.Token}} {
			if c.value == nil {
				continue
			}
			expiry := "no expiry known"
			if c.value.ExpiresAt != nil {
				expiry = "expires " + c.value.ExpiresAt.Local().Format("2006-01-02 15:04")
			}
			if c.value.Expired {
				expiry = "expired"
			}
			if err := line("  %-8s %s (%s) %s, %s", c.label+":", c.value.Kind, c.value.Secret, sanitize.Line(c.value.Name), expiry); err != nil {
				return err
			}
		}
	}

	if err := line("\nOptions"); err != nil {
		return err
	}
	keys := make([]string, 0, len(doc.Options))
	for key := range doc.Options {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		option := doc.Options[key]
		if err := line("  %-16s %-24s from %s", key, sanitize.Line(option.Value), option.Source); err != nil {
			return err
		}
	}

	return nil
}
