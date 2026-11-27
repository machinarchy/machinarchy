package commands

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
)

// `hosts import FILE`: registers many SSH hosts from a YAML or CSV file in one
// batch request. The file is checked here, then checked again by the server
// (validate-only) and shown before anything is written. It never runs a
// command, never trusts a host key and never changes an existing host.

// maxImportFile is the largest file read. 100 hosts need a few KiB.
const maxImportFile = 1 << 20

// importRecord is one host of the file. Where names the entry in messages.
type importRecord struct {
	Where   string
	Name    string
	Address string
	Env     string
	Access  string
	Tags    map[string]string
}

// importYAML is the YAML shape: `hosts:` holding a list of entries.
type importYAML struct {
	Hosts []struct {
		Name    string            `yaml:"name"`
		Address string            `yaml:"address"`
		Env     string            `yaml:"env"`
		Access  string            `yaml:"access"`
		Tags    map[string]string `yaml:"tags"`
	} `yaml:"hosts"`
}

var importCSVColumns = map[string]bool{"name": true, "address": true, "env": true, "access": true, "tags": true}

// importFormat picks yaml or csv from --format or the file extension.
func importFormat(file, flag string) (string, error) {
	choice := strings.ToLower(strings.TrimSpace(flag))
	if choice == "" {
		switch strings.ToLower(filepath.Ext(file)) {
		case ".yaml", ".yml":
			choice = "yaml"
		case ".csv":
			choice = "csv"
		}
	}
	switch choice {
	case "yaml", "yml":
		return "yaml", nil
	case "csv":
		return "csv", nil
	}

	return "", clierr.Usagef("cannot tell whether %q is YAML or CSV; name it .yaml, .yml or .csv, or pass --format yaml|csv", sanitize.Line(file))
}

// parseHostImport turns the file's bytes into records and checks each one the
// way `hosts add` checks its arguments. It returns every problem at once.
func parseHostImport(data []byte, format string) ([]importRecord, error) {
	if len(data) > maxImportFile {
		return nil, clierr.Usagef("the file is larger than %d bytes", maxImportFile)
	}
	var (
		records []importRecord
		err     error
	)
	if format == "csv" {
		records, err = parseImportCSV(data)
	} else {
		records, err = parseImportYAML(data)
	}
	if err != nil {
		return nil, err
	}
	switch {
	case len(records) == 0:
		return nil, clierr.Usagef("the file lists no hosts")
	case len(records) > api.MaxHostBatch:
		return nil, clierr.Detailed(clierr.ExitUsage, fmt.Errorf("the file lists %d hosts; one import takes at most %d", len(records), api.MaxHostBatch),
			"the server registers hosts in batches of 100", "split the file and import each part")
	}

	var problems []string
	seen := map[string]string{}
	for i := range records {
		r := &records[i]
		fail := func(err error) {
			problems = append(problems, fmt.Sprintf("%s: %s", r.Where, sanitize.Line(err.Error())))
		}
		if err := validateHostName(r.Name); err != nil {
			fail(err)

			continue
		}
		r.Name = strings.TrimSpace(r.Name)
		if r.Address == "" {
			fail(errors.New("the address is empty (hosts reached through an agent are added with 'hosts add NAME --agent')"))

			continue
		}
		if r.Address, err = normaliseAddress(r.Address); err != nil {
			fail(err)

			continue
		}
		if err := validateTags(r.Tags, nil); err != nil {
			fail(err)

			continue
		}
		key := strings.ToLower(strings.TrimSpace(r.Env)) + "\x00" + r.Name
		if first, dup := seen[key]; dup {
			fail(fmt.Errorf("the name %q repeats %s", r.Name, first))

			continue
		}
		seen[key] = r.Where
	}
	if len(problems) > 0 {
		const shown = 20
		more := ""
		if len(problems) > shown {
			more = fmt.Sprintf("\n... and %d more", len(problems)-shown)
			problems = problems[:shown]
		}

		return nil, clierr.Detailed(clierr.ExitUsage, errors.New("the file has errors; nothing was sent:\n  "+strings.Join(problems, "\n  ")+more),
			"every entry is checked before anything is sent", "")
	}

	return records, nil
}

func parseImportYAML(data []byte) ([]importRecord, error) {
	var doc importYAML
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}

		return nil, clierr.Usagef("the YAML file is not valid: %s", sanitize.Line(err.Error()))
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, clierr.Usagef("the YAML file must hold a single document")
	}
	records := make([]importRecord, len(doc.Hosts))
	for i, h := range doc.Hosts {
		records[i] = importRecord{Where: "host " + strconv.Itoa(i+1), Name: h.Name, Address: strings.TrimSpace(h.Address),
			Env: strings.TrimSpace(h.Env), Access: strings.TrimSpace(h.Access), Tags: h.Tags}
	}

	return records, nil
}

func parseImportCSV(data []byte) ([]importRecord, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, clierr.Usagef("the CSV file is not valid: %s", sanitize.Line(err.Error()))
	}
	if len(rows) == 0 {
		return nil, nil
	}
	column := map[string]int{}
	for i, name := range rows[0] {
		name = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "\ufeff")))
		if !importCSVColumns[name] {
			return nil, clierr.Usagef("the CSV header has an unknown column %q; use name, address, env, access, tags", sanitize.Line(name))
		}
		if _, dup := column[name]; dup {
			return nil, clierr.Usagef("the CSV header repeats the column %q", name)
		}
		column[name] = i
	}
	if _, ok := column["name"]; !ok {
		return nil, clierr.Usagef("the CSV header needs a name column")
	}
	if _, ok := column["address"]; !ok {
		return nil, clierr.Usagef("the CSV header needs an address column")
	}
	cell := func(row []string, name string) string {
		if i, ok := column[name]; ok && i < len(row) {
			return strings.TrimSpace(row[i])
		}

		return ""
	}
	var records []importRecord
	for i, row := range rows[1:] {
		where := "row " + strconv.Itoa(i+2)
		if len(row) > len(rows[0]) {
			return nil, clierr.Usagef("%s has more fields than the header", where)
		}
		tags, err := parseCSVTags(cell(row, "tags"))
		if err != nil {
			return nil, clierr.Usagef("%s: %s", where, err.Error())
		}
		records = append(records, importRecord{Where: where, Name: cell(row, "name"), Address: cell(row, "address"),
			Env: cell(row, "env"), Access: cell(row, "access"), Tags: tags})
	}

	return records, nil
}

// parseCSVTags reads "k=v;k2=v2".
func parseCSVTags(cell string) (map[string]string, error) {
	if cell == "" {
		return nil, nil
	}
	tags := map[string]string{}
	for _, pair := range strings.Split(cell, ";") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, value, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("the tag %q must be KEY=VALUE (separate tags with ';')", sanitize.Line(pair))
		}
		tags[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}

	return tags, nil
}

type hostsImportOptions struct {
	format      string
	skipInvalid bool
}

func newHostsImportCommand(f *cli.Factory) *cobra.Command {
	var o hostsImportOptions
	command := &cobra.Command{
		Use:   "import FILE",
		Short: "Register many SSH hosts from a YAML or CSV file (owner only)",
		Long: "Reads up to 100 hosts from FILE (use - for standard input), checks them, asks the server to validate them and shows what would\n" +
			"happen. Only then, after you confirm (or with --yes), are the hosts registered. Nothing is written before that.\n" +
			"With --dry-run it stops after the check. The import only adds: a name that already exists is reported and left as it is. It runs\n" +
			"no commands and trusts no SSH key; approve keys afterwards with 'hosts trust --all-pending'.\n\n" +
			"YAML: a 'hosts:' list of entries with name, address and optionally env, access (an access entry name) and tags.\n" +
			"CSV: a header row with name, address and optionally env, access, tags (tags as k=v;k2=v2).\n" +
			"An entry without env goes to the active env. If the check finds a problem the import stops, unless --skip-invalid.",
		Example: "  machinarchy hosts import fleet.yaml --dry-run\n  machinarchy hosts import fleet.csv --yes\n  cat fleet.yaml | machinarchy hosts import - --format yaml --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runHostsImport(command, f, args[0], o)
		},
		Annotations: map[string]string{cli.AnnotationDryRun: "true"},
	}
	command.Flags().StringVar(&o.format, "format", "", "yaml or csv (default: from the file name)")
	command.Flags().BoolVar(&o.skipInvalid, "skip-invalid", false, "Register the hosts that pass the check and skip the others")

	return command
}

func readImportFile(command *cobra.Command, file string) ([]byte, error) {
	var reader io.Reader
	if file == "-" {
		reader = command.InOrStdin()
	} else {
		// #nosec G304 -- the path is the file the person named on the command line.
		handle, err := os.Open(file)
		if err != nil {
			return nil, clierr.Usagef("cannot read %q: %s", sanitize.Line(file), sanitize.Line(err.Error()))
		}
		defer func() { _ = handle.Close() }() // read-only: a close error cannot lose data
		reader = handle
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxImportFile+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", sanitize.Line(file), err)
	}
	if len(data) > maxImportFile {
		return nil, clierr.Usagef("the file is larger than %d bytes", maxImportFile)
	}

	return data, nil
}

func runHostsImport(command *cobra.Command, f *cli.Factory, file string, o hostsImportOptions) error {
	format, err := importFormat(file, o.format)
	if err != nil {
		return err
	}
	data, err := readImportFile(command, file)
	if err != nil {
		return err
	}
	records, err := parseHostImport(data, format)
	if err != nil {
		return err
	}
	printer, err := f.Printer(command.OutOrStdout(), output.FormatTable)
	if err != nil {
		return err
	}
	client, err := f.NewAPIClient(command.Context())
	if err != nil {
		return err
	}
	inputs, err := resolveImport(command, f, client, records)
	if err != nil {
		return err
	}

	preview, err := client.CreateHostsBatch(command.Context(), inputs, true)
	if err != nil {
		return importAPIError(err)
	}
	if len(preview.Results) != len(records) {
		return fmt.Errorf("the server checked %d of %d hosts; nothing was written", len(preview.Results), len(records))
	}
	if f.DryRun() {
		if err := renderImport(printer, records, preview); err != nil {
			return err
		}
		_, err = fmt.Fprintln(command.ErrOrStderr(), "Nothing was written (--dry-run).")

		return err
	}
	if printer.Format() == output.FormatTable {
		errPrinter, err := output.NewPrinter(command.ErrOrStderr(), output.FormatTable, output.Options{})
		if err != nil {
			return err
		}
		if err := renderImport(errPrinter, records, preview); err != nil {
			return err
		}
	}

	var apply []api.HostInput
	var applied []importRecord
	for i, outcome := range preview.Results {
		if outcome.Status == "valid" {
			apply = append(apply, inputs[i])
			applied = append(applied, records[i])
		}
	}
	if skipped := len(records) - len(apply); skipped > 0 && !o.skipInvalid {
		return clierr.Detailed(clierr.ExitFailure, fmt.Errorf("%d of %d hosts did not pass the check; nothing was written", skipped, len(records)),
			"fix the entries above, or skip them", "machinarchy hosts import "+file+" --skip-invalid")
	}
	if len(apply) == 0 {
		return clierr.Newf(clierr.ExitFailure, "", "no host passed the check; nothing was written")
	}
	if err := f.RequireConfirmation("Register " + plural(len(apply), "host") + "?"); err != nil {
		return err
	}

	result, err := client.CreateHostsBatch(command.Context(), apply, false)
	if err != nil {
		return importAPIError(err)
	}
	if len(result.Results) != len(apply) {
		return fmt.Errorf("the server reported %d of %d hosts; check with 'machinarchy hosts'", len(result.Results), len(apply))
	}
	if err := renderImport(printer, applied, result); err != nil {
		return err
	}
	if result.Failed > 0 {
		code := clierr.ExitPartial
		if result.Created == 0 {
			code = clierr.ExitAllFailed
		}

		return clierr.Detailed(code, fmt.Errorf("%d of %d hosts were not registered", result.Failed, len(apply)),
			"the others were registered; nothing is rolled back", "machinarchy hosts")
	}
	if result.Created > 0 {
		_, _ = fmt.Fprintln(command.ErrOrStderr(), "No SSH key is trusted yet: 'machinarchy hosts trust --all-pending' asks about each host.")
	}

	return nil
}

// importAPIError explains the whole-request refusals.
func importAPIError(err error) error {
	var apiErr *api.Error
	if errors.As(err, &apiErr) && (apiErr.Status == 403) {
		return clierr.Detailed(clierr.ExitAuth, fmt.Errorf("the server refused the import: %s", sanitize.Line(apiErr.Message)),
			"registering hosts needs an owner or admin login", "machinarchy whoami")
	}

	return fmt.Errorf("import hosts: %w", err)
}

// resolveImport turns env and access names into IDs, one lookup per distinct
// name, and reports every unknown name at once.
func resolveImport(command *cobra.Command, f *cli.Factory, client api.Client, records []importRecord) ([]api.HostInput, error) {
	envs := map[string]int{}
	profiles := map[string]string{}
	var problems []string
	inputs := make([]api.HostInput, len(records))
	for i, r := range records {
		envName := r.Env
		if envName == "" {
			envName = f.Config().Environment()
		}
		id, ok := envs[envName]
		if !ok {
			var err error
			if id, err = resolveEnvironment(command.Context(), client, envName); err != nil {
				if !isNotFoundOrUsage(err) {
					return nil, err
				}
				problems = append(problems, fmt.Sprintf("%s: %s", r.Where, sanitize.Line(err.Error())))

				continue
			}
			envs[envName] = id
		}
		profile := ""
		if r.Access != "" {
			if profile, ok = profiles[r.Access]; !ok {
				var err error
				if profile, err = resolveProfile(command, client, r.Access); err != nil {
					if !isNotFoundOrUsage(err) {
						return nil, err
					}
					problems = append(problems, fmt.Sprintf("%s: %s", r.Where, sanitize.Line(err.Error())))

					continue
				}
				profiles[r.Access] = profile
			}
		}
		inputs[i] = api.HostInput{EnvironmentID: id, Name: r.Name, Labels: r.Tags,
			Spec: api.HostSpec{Presence: "Managed", AccessProfileID: profile, AccessTarget: api.HostAccessTarget{Endpoint: r.Address}}}
	}
	if len(problems) > 0 {
		sort.Strings(problems)

		return nil, clierr.Usagef("the file names things that do not exist; nothing was sent:\n  %s", strings.Join(problems, "\n  "))
	}

	return inputs, nil
}

func isNotFoundOrUsage(err error) bool {
	code := clierr.ExitCode(err)

	return code == clierr.ExitNotFound || code == clierr.ExitUsage
}

// renderImport prints the per-host outcomes: a table, or the result document
// for -o json|yaml.
func renderImport(printer *output.Printer, records []importRecord, result *api.HostBatchResult) error {
	if printer.Format() != output.FormatTable {
		return printer.Document(result)
	}
	rows := make([][]string, 0, len(result.Results))
	for i, outcome := range result.Results {
		name, address := sanitize.Line(outcome.Name), ""
		if i < len(records) {
			address = sanitize.Line(records[i].Address)
		}
		detail := outcome.Status
		if outcome.Error != "" {
			detail += ": " + sanitize.Line(outcome.Error)
		}
		rows = append(rows, []string{name, address, detail})
	}
	if err := printer.Table([]string{"NAME", "ADDRESS", "RESULT"}, rows); err != nil {
		return err
	}

	return nil
}
