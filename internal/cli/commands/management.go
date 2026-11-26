package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/msg"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/spf13/cobra"
)

const maxManagementInput = 2 << 20

func NewRuntimeConfigCommand(factory *cli.Factory) *cobra.Command {
	root := &cobra.Command{Use: "runtime-config", Short: "Read and manage persisted global runtime configuration", Args: cobra.NoArgs}
	root.AddCommand(
		newConfigGet(factory),
		newConfigHistory(factory), newConfigStatus(factory), newConfigValidate(factory), newConfigPublish(factory), newConfigRollback(factory),
	)
	return root
}

func newConfigGet(f *cli.Factory) *cobra.Command {
	return managementRead(f, &cobra.Command{Use: "get", Short: "Read active runtime configuration", Args: cobra.NoArgs},
		func(ctx context.Context, c api.ManagementClient, _ []string) (json.RawMessage, error) {
			return c.RuntimeConfig(ctx)
		})
}
func newConfigHistory(f *cli.Factory) *cobra.Command {
	var before uint64
	c := managementRead(f, &cobra.Command{Use: "history", Short: "List immutable configuration revisions", Args: cobra.NoArgs},
		func(ctx context.Context, c api.ManagementClient, _ []string) (json.RawMessage, error) {
			return c.ConfigHistory(ctx, before)
		})
	c.Flags().Uint64Var(&before, "before", 0, "Return revisions older than this revision")
	return c
}
func newConfigStatus(f *cli.Factory) *cobra.Command {
	return managementRead(f, &cobra.Command{Use: "status", Short: "Show per-process applied configuration revisions", Args: cobra.NoArgs},
		func(ctx context.Context, c api.ManagementClient, _ []string) (json.RawMessage, error) {
			return c.ConfigStatus(ctx)
		})
}
func newConfigValidate(f *cli.Factory) *cobra.Command {
	var file string
	c := &cobra.Command{Use: "validate", Short: "Validate a runtime configuration JSON document", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		doc, e := readManagementInput(cmd, file)
		if e != nil {
			return e
		}
		client, e := f.NewManagementClient(cmd.Context())
		if e != nil {
			return e
		}
		raw, e := client.ValidateRuntimeConfig(cmd.Context(), doc)
		if e != nil {
			return e
		}
		return writeJSON(cmd, raw)
	}}
	c.Flags().StringVar(&file, "file", "", "JSON document file; defaults to stdin")
	return c
}
func newConfigPublish(f *cli.Factory) *cobra.Command {
	var file, note string
	var expected uint64
	c := &cobra.Command{Use: "publish", Short: "Publish a new immutable runtime configuration revision", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if expected == 0 {
			return clierr.Usagef("--expected-revision is required")
		}
		doc, e := readManagementInput(cmd, file)
		if e != nil {
			return e
		}
		client, e := f.NewManagementClient(cmd.Context())
		if e != nil {
			return e
		}
		raw, e := client.PublishRuntimeConfig(cmd.Context(), expected, doc, note)
		if e != nil {
			return e
		}
		return writeJSON(cmd, raw)
	}}
	c.Flags().Uint64Var(&expected, "expected-revision", 0, "Revision to compare-and-swap")
	c.Flags().StringVar(&file, "file", "", "JSON document file; defaults to stdin")
	c.Flags().StringVar(&note, "change-note", "", "Non-secret change note")
	return c
}
func newConfigRollback(f *cli.Factory) *cobra.Command {
	var expected, target uint64
	var note string
	c := &cobra.Command{Use: "rollback", Short: "Create a new revision from an earlier configuration revision", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if expected == 0 || target == 0 {
			return clierr.Usagef("--expected-revision and --target-revision are required")
		}
		if e := f.RequireConfirmation(fmt.Sprintf("Replace the active runtime configuration with revision %d?", target)); e != nil {
			return e
		}
		client, e := f.NewManagementClient(cmd.Context())
		if e != nil {
			return e
		}
		raw, e := client.RollbackRuntimeConfig(cmd.Context(), expected, target, note)
		if e != nil {
			return e
		}
		return writeJSON(cmd, raw)
	}}
	c.Flags().Uint64Var(&expected, "expected-revision", 0, "Current revision precondition")
	c.Flags().Uint64Var(&target, "target-revision", 0, "Historical revision to copy")
	c.Flags().StringVar(&note, "change-note", "", "Non-secret change note")
	return c
}

func NewSecretCommand(factory *cli.Factory) *cobra.Command {
	root := &cobra.Command{Use: "secret", Short: "Manage encrypted secrets (values are write-only)", Args: cobra.NoArgs}
	root.AddCommand(newSecretList(factory), newSecretCreate(factory), newSecretVersions(factory), newSecretRotate(factory), newSecretRewrap(factory), newSecretDelete(factory))
	return root
}

// secretMetadata is what the CLI shows of a secret; values are never returned.
type secretMetadata struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Purpose        string `json:"purpose"`
	CurrentVersion uint64 `json:"currentVersion"`
	UpdatedAt      string `json:"updatedAt"`
}

func newSecretList(f *cli.Factory) *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List secret metadata", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := f.NewManagementClient(cmd.Context())
		if err != nil {
			return err
		}
		raw, err := client.ListSecrets(cmd.Context())
		if err != nil {
			return err
		}
		printer, err := f.Printer(cmd.OutOrStdout(), output.FormatTable)
		if err != nil {
			return err
		}
		if printer.Format() != output.FormatTable {
			return printDocument(printer, raw)
		}
		var secrets []secretMetadata
		if err := json.Unmarshal(raw, &secrets); err != nil {
			return fmt.Errorf("decode secrets: %w", err)
		}
		if f.Config().Quiet() {
			ids := make([]string, 0, len(secrets))
			for _, secret := range secrets {
				ids = append(ids, secret.ID)
			}
			return printer.IDs(ids)
		}
		if len(secrets) == 0 {
			return printMessage(cmd, f, msg.Get("list.empty"))
		}
		rows := make([][]string, 0, len(secrets))
		for _, secret := range secrets {
			rows = append(rows, []string{secret.Name, secret.ID, secret.Purpose, fmt.Sprint(secret.CurrentVersion), secret.UpdatedAt})
		}
		return printer.Table([]string{"NAME", "ID", "PURPOSE", "VERSION", "UPDATED"}, rows)
	}}
}

// printDocument prints a raw JSON response in the printer's format.
func printDocument(printer *output.Printer, raw json.RawMessage) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	return printer.Document(value)
}
func newSecretCreate(f *cli.Factory) *cobra.Command {
	var description string
	var stdin bool
	c := &cobra.Command{Use: "create NAME --value-stdin", Short: "Create an encrypted secret version from stdin", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !stdin {
			return clierr.Usagef("--value-stdin is required; secret values are not accepted as flags")
		}
		value, e := readSecretInput(cmd)
		if e != nil {
			return e
		}
		doc, err := json.Marshal(map[string]string{"name": args[0], "description": description, "purpose": "ssh-credential", "value": value})
		if err != nil {
			return err
		}
		client, e := f.NewManagementClient(cmd.Context())
		if e != nil {
			return e
		}
		raw, e := client.CreateSecret(cmd.Context(), doc)
		if e != nil {
			return e
		}
		printer, e := f.Printer(cmd.OutOrStdout(), output.FormatTable)
		if e != nil {
			return e
		}
		if printer.Format() != output.FormatTable {
			return printDocument(printer, raw)
		}
		var created secretMetadata
		if e := json.Unmarshal(raw, &created); e != nil {
			return fmt.Errorf("decode created secret: %w", e)
		}
		if f.Config().Quiet() {
			return printer.IDs([]string{created.ID})
		}
		return printMessage(cmd, f, msg.Get("secret.created").With(msg.Vars{
			"name": created.Name, "id": created.ID, "version": strconv.FormatUint(created.CurrentVersion, 10)}))
	}}
	c.Flags().StringVar(&description, "description", "", "Non-secret description")
	c.Flags().BoolVar(&stdin, "value-stdin", false, "Read secret value from stdin")
	return c
}
func newSecretVersions(f *cli.Factory) *cobra.Command {
	return managementRead(f, &cobra.Command{Use: "versions ID", Short: "List secret version metadata", Args: cobra.ExactArgs(1)},
		func(ctx context.Context, c api.ManagementClient, args []string) (json.RawMessage, error) {
			return c.SecretVersions(ctx, args[0])
		})
}
func newSecretRotate(f *cli.Factory) *cobra.Command {
	var expected uint64
	var stdin bool
	c := &cobra.Command{Use: "rotate ID --expected-version N --value-stdin", Short: "Add a new encrypted secret version from stdin", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if expected == 0 || !stdin {
			return clierr.Usagef("--expected-version and --value-stdin are required")
		}
		value, e := readSecretInput(cmd)
		if e != nil {
			return e
		}
		client, e := f.NewManagementClient(cmd.Context())
		if e != nil {
			return e
		}
		raw, e := client.RotateSecret(cmd.Context(), args[0], expected, value)
		if e != nil {
			return e
		}
		return writeJSON(cmd, raw)
	}}
	c.Flags().Uint64Var(&expected, "expected-version", 0, "Current secret version precondition")
	c.Flags().BoolVar(&stdin, "value-stdin", false, "Read new secret value from stdin")
	return c
}
func newSecretRewrap(f *cli.Factory) *cobra.Command {
	var oldKeyID string
	var batchSize int
	c := &cobra.Command{Use: "rewrap --old-key-id ID", Short: "Move secrets and run data off an old encryption key",
		Long: "Rewraps the data keys of secrets and run data (specs, output, progress) from --old-key-id to the current key, " +
			"--batch-size of each per call, and reseals run data written before key IDs existed. Repeat until the result " +
			"says \"done\": true, then remove the old key from the keyring (keep it until database backups made before are expired).",
		Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(oldKeyID) == "" {
				return clierr.Usagef("--old-key-id is required")
			}
			if err := f.RequireConfirmation(fmt.Sprintf("Move secrets and Operation data from key %q to the current key?", oldKeyID)); err != nil {
				return err
			}
			client, err := f.NewManagementClient(cmd.Context())
			if err != nil {
				return err
			}
			raw, err := client.RewrapSecrets(cmd.Context(), oldKeyID, batchSize)
			if err != nil {
				return err
			}
			return writeJSON(cmd, raw)
		}}
	c.Flags().StringVar(&oldKeyID, "old-key-id", "", "Key ID to move off (e.g. bootstrap-v1 or local:bootstrap-v1)")
	c.Flags().IntVar(&batchSize, "batch-size", 100, "Maximum values of each kind to rewrap in this call (1-256)")
	return c
}
func newSecretDelete(f *cli.Factory) *cobra.Command {
	return &cobra.Command{Use: "delete ID", Short: "Retire a secret without immediately removing retained ciphertext", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if e := f.RequireConfirmation(fmt.Sprintf("Retire secret %q?", args[0])); e != nil {
			return e
		}
		client, e := f.NewManagementClient(cmd.Context())
		if e != nil {
			return e
		}
		if e = client.DeleteSecret(cmd.Context(), args[0]); e != nil {
			return e
		}
		return printMessage(cmd, f, msg.Get("secret.retired"))
	}}
}

func readManagementInput(cmd *cobra.Command, file string) (json.RawMessage, error) {
	r := cmd.InOrStdin()
	if file != "" {
		// #nosec G304 -- file is an operator-configured configuration file path.
		f, e := os.Open(file)
		if e != nil {
			return nil, clierr.Usage(e)
		}
		defer func() { _ = f.Close() }() // read-only: a close error cannot lose data
		r = f
	}
	b, e := io.ReadAll(io.LimitReader(r, maxManagementInput+1))
	if e != nil {
		return nil, e
	}
	if len(b) > maxManagementInput {
		return nil, clierr.Usagef("input exceeds %d bytes", maxManagementInput)
	}
	if !json.Valid(b) {
		return nil, clierr.Usagef("input must be valid JSON")
	}
	return json.RawMessage(b), nil
}
func readSecretInput(cmd *cobra.Command) (string, error) {
	b, e := io.ReadAll(io.LimitReader(cmd.InOrStdin(), (1<<20)+1))
	if e != nil {
		return "", e
	}
	if len(b) > 1<<20 {
		return "", clierr.Usagef("secret input exceeds 1 MiB")
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	if value == "" {
		return "", clierr.Usagef("secret value cannot be empty")
	}
	return value, nil
}
func writeJSON(cmd *cobra.Command, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if e := json.Unmarshal(raw, &value); e != nil {
		return e
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// managementRead completes command as a read-only management call whose JSON
// response is printed as is (AUDIT X-08).
func managementRead(f *cli.Factory, command *cobra.Command,
	call func(context.Context, api.ManagementClient, []string) (json.RawMessage, error)) *cobra.Command {
	command.RunE = func(cmd *cobra.Command, args []string) error {
		client, err := f.NewManagementClient(cmd.Context())
		if err != nil {
			return err
		}
		raw, err := call(cmd.Context(), client, args)
		if err != nil {
			return err
		}
		return writeJSON(cmd, raw)
	}
	return command
}
