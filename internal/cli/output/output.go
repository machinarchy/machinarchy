// Package output renders API resources in stable machine-readable or tabular
// formats without coupling commands to a table-rendering library.
//
// Commands obtain a Printer (usually through cli.Factory.Printer) instead of
// formatting output themselves, so format selection, header suppression and
// quiet mode behave the same everywhere.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"go.yaml.in/yaml/v3"

	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

// Format is a supported output format.
type Format string

// Supported output formats.
const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
	FormatYAML  Format = "yaml"
)

// ParseFormat validates a user-supplied format. An empty value selects the
// table format and "yml" is accepted as an alias for "yaml". An unsupported
// value is a usage error.
func ParseFormat(value string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "table", "":
		return FormatTable, nil
	case "json":
		return FormatJSON, nil
	case "yaml", "yml":
		return FormatYAML, nil
	default:
		return "", clierr.Usagef("unsupported output format %q; choose table, json, or yaml", value)
	}
}

// Options adjust how a Printer renders human-readable output.
type Options struct {
	// NoHeaders omits the header row of tables.
	NoHeaders bool
	// Quiet prints only resource identifiers, one per line, in table mode.
	Quiet bool
}

// Printer renders resources to one writer in one format.
type Printer struct {
	writer  io.Writer
	format  Format
	options Options
}

// NewPrinter creates a Printer. It fails for a nil writer or unknown format.
func NewPrinter(writer io.Writer, format Format, options Options) (*Printer, error) {
	if writer == nil {
		return nil, errors.New("output writer is required")
	}
	switch format {
	case FormatTable, FormatJSON, FormatYAML:
	default:
		return nil, clierr.Usagef("unsupported output format %q; choose table, json, or yaml", string(format))
	}

	return &Printer{writer: writer, format: format, options: options}, nil
}

// Format returns the format this Printer renders.
func (p *Printer) Format() Format {
	return p.format
}

// Document renders a value as JSON or YAML using the API's own field names.
// It is an error to call it on a table Printer: tables need column definitions
// (see Table) and silently falling back would change a script's input format.
func (p *Printer) Document(value any) error {
	switch p.format {
	case FormatJSON:
		return printJSON(p.writer, value)
	case FormatYAML:
		return printYAML(p.writer, value)
	default:
		return clierr.Usagef("table output is not available for this resource; use -o json or -o yaml")
	}
}

// Table renders rows under headers using the shared column layout. Cell
// values are stripped of tabs and line breaks so one record is always one
// line. The header row is omitted when Options.NoHeaders is set.
func (p *Printer) Table(headers []string, rows [][]string) error {
	table := tabwriter.NewWriter(p.writer, 0, 4, 2, ' ', 0)

	if !p.options.NoHeaders {
		if _, err := fmt.Fprintln(table, strings.Join(headers, "\t")); err != nil {
			return err
		}
	}
	for _, row := range rows {
		cells := make([]string, len(row))
		for index, cell := range row {
			cells[index] = tableValue(cell)
		}
		if _, err := fmt.Fprintln(table, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}

	return table.Flush()
}

// IDs prints one identifier per line.
func (p *Printer) IDs(ids []string) error {
	for _, id := range ids {
		if _, err := fmt.Fprintln(p.writer, tableValue(id)); err != nil {
			return err
		}
	}

	return nil
}

// Hosts renders a host collection.
func (p *Printer) Hosts(hosts []api.Host) error {
	if p.format != FormatTable {
		return p.Document(hosts)
	}
	if p.options.Quiet {
		ids := make([]string, 0, len(hosts))
		for _, host := range hosts {
			ids = append(ids, host.Metadata.ID)
		}

		return p.IDs(ids)
	}

	return p.hostTable(hosts)
}

// Host renders one host.
func (p *Printer) Host(host api.Host) error {
	if p.format != FormatTable {
		return p.Document(host)
	}
	if p.options.Quiet {
		return p.IDs([]string{host.Metadata.ID})
	}

	return p.hostTable([]api.Host{host})
}

func (p *Printer) hostTable(hosts []api.Host) error {
	headers := []string{"NAME", "ID", "ENVIRONMENT", "ENDPOINT", "PHASE", "READY"}

	if len(hosts) == 0 {
		return p.Table(headers, [][]string{{"No resources found.", "", "", "", "", ""}})
	}

	rows := make([][]string, 0, len(hosts))
	for _, host := range hosts {
		environment := "-"
		if host.Metadata.EnvironmentID > 0 {
			environment = fmt.Sprintf("%d", host.Metadata.EnvironmentID)
		}
		rows = append(rows, []string{host.Metadata.Name, host.Metadata.ID, environment,
			orDash(host.Spec.AccessTarget.Endpoint), orDash(host.Status.Phase), condition(host.Status.Conditions, "Ready")})
	}

	return p.Table(headers, rows)
}

// condition returns the status of the named condition, or "-".
func condition(conditions []api.Condition, kind string) string {
	for _, c := range conditions {
		if c.Type == kind {
			return c.Status
		}
	}
	return "-"
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func printJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printYAML(writer io.Writer, value any) (err error) {
	encoder := yaml.NewEncoder(writer)
	encoder.SetIndent(2)

	defer func() {
		if closeErr := encoder.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close YAML encoder: %w", closeErr))
		}
	}()

	return encoder.Encode(value)
}

func tableValue(value string) string {
	return strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(value)
}
