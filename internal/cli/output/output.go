// Package output renders API resources in stable machine-readable or tabular
// formats without coupling commands to a table-rendering library.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/machinarchy/machinarchy/internal/cli/api"
	"go.yaml.in/yaml/v3"
)

// PrintHosts renders a host collection as table, JSON, or YAML.
func PrintHosts(writer io.Writer, format string, hosts []api.Host) error {
	if writer == nil {
		return errors.New("output writer is required")
	}
	switch format {
	case "table", "":
		return printHostTable(writer, hosts)
	case "json":
		return printJSON(writer, hosts)
	case "yaml", "yml":
		return printYAML(writer, hosts)
	default:
		return fmt.Errorf("unsupported output format %q; choose table, json, or yaml", format)
	}
}

// PrintHost renders one host as a row, JSON object, or YAML object.
func PrintHost(writer io.Writer, format string, host api.Host) error {
	if writer == nil {
		return errors.New("output writer is required")
	}
	switch format {
	case "table", "":
		return printHostTable(writer, []api.Host{host})
	case "json":
		return printJSON(writer, host)
	case "yaml", "yml":
		return printYAML(writer, host)
	default:
		return fmt.Errorf("unsupported output format %q; choose table, json, or yaml", format)
	}
}

func printJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printYAML(writer io.Writer, value any) error {
	encoder := yaml.NewEncoder(writer)
	encoder.SetIndent(2)
	if err := encoder.Encode(value); err != nil {
		_ = encoder.Close()
		return err
	}
	return encoder.Close()
}

func printHostTable(writer io.Writer, hosts []api.Host) error {
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "NAME\tID\tENVIRONMENT\tADDRESS\tSTATUS"); err != nil {
		return err
	}
	if len(hosts) == 0 {
		if _, err := fmt.Fprintln(table, "No resources found.\t\t\t\t"); err != nil {
			return err
		}
		return table.Flush()
	}
	for _, host := range hosts {
		address := host.IPAddress
		if address == "" {
			address = host.Hostname
		}
		status := "-"
		if host.SSH != nil && host.SSH.Status != "" {
			status = host.SSH.Status
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%d\t%s\t%s\n",
			tableValue(host.Name), tableValue(host.ID), host.EnvironmentID,
			tableValue(address), tableValue(status)); err != nil {
			return err
		}
	}
	return table.Flush()
}

func tableValue(value string) string {
	return strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(value)
}
