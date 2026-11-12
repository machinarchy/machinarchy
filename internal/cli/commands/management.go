package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/utils"
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

func newConfigGet(factory *cli.Factory) *cobra.Command {
	return &cobra.Command{Use: "get", Short: "Read active runtime configuration", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := factory.NewManagementClient(cmd.Context())
		if err != nil {
			return err
		}
		raw, err := client.RuntimeConfig(cmd.Context())
		if err != nil {
			return err
		}
		return writeJSON(cmd, raw)
	}}
}
func newConfigHistory(f *cli.Factory) *cobra.Command {
	var before uint64
	c := &cobra.Command{Use: "history", Short: "List immutable configuration revisions", RunE: func(cmd *cobra.Command, _ []string) error {
		client, e := f.NewManagementClient(cmd.Context())
		if e != nil {
			return e
		}
		raw, e := client.ConfigHistory(cmd.Context(), before)
		if e != nil {
			return e
		}
		return writeJSON(cmd, raw)
	}}
	c.Flags().Uint64Var(&before, "before", 0, "Return revisions older than this revision")
	return c
}
func newConfigStatus(f *cli.Factory) *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Show per-process applied configuration revisions", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, e := f.NewManagementClient(cmd.Context())
		if e != nil {
			return e
		}
		raw, e := client.ConfigStatus(cmd.Context())
		if e != nil {
			return e
		}
		return writeJSON(cmd, raw)
	}}
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
			return fmt.Errorf("--expected-revision is required")
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
			return fmt.Errorf("--expected-revision and --target-revision are required")
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
func newSecretList(f *cli.Factory) *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List secret metadata", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, e := f.NewManagementClient(cmd.Context())
		if e != nil {
			return e
		}
		raw, e := c.ListSecrets(cmd.Context())
		if e != nil {
			return e
		}
		return writeJSON(cmd, raw)
	}}
}
func newSecretCreate(f *cli.Factory) *cobra.Command {
	var description string
	var stdin bool
	c := &cobra.Command{Use: "create NAME --value-stdin", Short: "Create an encrypted secret version from stdin", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !stdin {
			return fmt.Errorf("--value-stdin is required; secret values are not accepted as flags")
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
		return writeJSON(cmd, raw)
	}}
	c.Flags().StringVar(&description, "description", "", "Non-secret description")
	c.Flags().BoolVar(&stdin, "value-stdin", false, "Read secret value from stdin")
	return c
}
func newSecretVersions(f *cli.Factory) *cobra.Command {
	return &cobra.Command{Use: "versions ID", Short: "List secret version metadata", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, e := f.NewManagementClient(cmd.Context())
		if e != nil {
			return e
		}
		raw, e := c.SecretVersions(cmd.Context(), args[0])
		if e != nil {
			return e
		}
		return writeJSON(cmd, raw)
	}}
}
func newSecretRotate(f *cli.Factory) *cobra.Command {
	var expected uint64
	var stdin bool
	c := &cobra.Command{Use: "rotate ID --expected-version N --value-stdin", Short: "Add a new encrypted secret version from stdin", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if expected == 0 || !stdin {
			return fmt.Errorf("--expected-version and --value-stdin are required")
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
	c := &cobra.Command{Use: "rewrap --old-key-id ID", Short: "Rewrap stored DEKs under the active external key", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(oldKeyID) == "" {
			return fmt.Errorf("--old-key-id is required")
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
	c.Flags().StringVar(&oldKeyID, "old-key-id", "", "KEK identifier to rewrap from")
	c.Flags().IntVar(&batchSize, "batch-size", 100, "Maximum secret versions to rewrap in this request")
	return c
}
func newSecretDelete(f *cli.Factory) *cobra.Command {
	return &cobra.Command{Use: "delete ID", Short: "Retire a secret without immediately removing retained ciphertext", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, e := f.NewManagementClient(cmd.Context())
		if e != nil {
			return e
		}
		if e = client.DeleteSecret(cmd.Context(), args[0]); e != nil {
			return e
		}
		_, e = fmt.Fprintln(cmd.OutOrStdout(), "Secret retired.")
		return e
	}}
}

func readManagementInput(cmd *cobra.Command, file string) (json.RawMessage, error) {
	var r io.Reader = cmd.InOrStdin()
	if file != "" {
		f, e := os.Open(file)
		if e != nil {
			return nil, e
		}
		defer utils.CloseWithLog(f, file)
		r = f
	}
	b, e := io.ReadAll(io.LimitReader(r, maxManagementInput+1))
	if e != nil {
		return nil, e
	}
	if len(b) > maxManagementInput {
		return nil, fmt.Errorf("input exceeds %d bytes", maxManagementInput)
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("input must be valid JSON")
	}
	return json.RawMessage(b), nil
}
func readSecretInput(cmd *cobra.Command) (string, error) {
	b, e := io.ReadAll(io.LimitReader(cmd.InOrStdin(), (1<<20)+1))
	if e != nil {
		return "", e
	}
	if len(b) > 1<<20 {
		return "", fmt.Errorf("secret input exceeds 1 MiB")
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	if value == "" {
		return "", fmt.Errorf("secret value cannot be empty")
	}
	return value, nil
}
func writeJSON(cmd *cobra.Command, raw json.RawMessage) error {
	var value any
	if e := json.Unmarshal(raw, &value); e != nil {
		return e
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
