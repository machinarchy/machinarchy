package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

func sampleHosts() []api.Host {
	return []api.Host{
		{ID: "7", Name: "web-01", EnvironmentID: 2, IPAddress: "10.0.0.7", Hostname: "web-01.example", SSH: &api.SSHState{Username: "deploy", Port: 22, Status: "reachable"}},
		{ID: "12", Name: "db", Hostname: "db.example"},
	}
}

func render(t *testing.T, format Format, options Options, call func(*Printer) error) string {
	t.Helper()

	var buffer bytes.Buffer
	printer, err := NewPrinter(&buffer, format, options)
	if err != nil {
		t.Fatalf("NewPrinter: %v", err)
	}
	if err := call(printer); err != nil {
		t.Fatalf("render: %v", err)
	}

	return buffer.String()
}

func TestParseFormat(t *testing.T) {
	valid := map[string]Format{
		"":      FormatTable,
		"table": FormatTable,
		"TABLE": FormatTable,
		"json":  FormatJSON,
		" Json": FormatJSON,
		"yaml":  FormatYAML,
		"yml":   FormatYAML,
	}
	for input, want := range valid {
		got, err := ParseFormat(input)
		if err != nil || got != want {
			t.Errorf("ParseFormat(%q) = %q, %v; want %q", input, got, err, want)
		}
	}

	for _, input := range []string{"xml", "csv", "wide"} {
		_, err := ParseFormat(input)
		if err == nil {
			t.Errorf("ParseFormat(%q) succeeded", input)
			continue
		}
		if clierr.ExitCode(err) != clierr.ExitUsage {
			t.Errorf("ParseFormat(%q) exit code = %d, want %d", input, clierr.ExitCode(err), clierr.ExitUsage)
		}
		want := `unsupported output format "` + input + `"; choose table, json, or yaml`
		if err.Error() != want {
			t.Errorf("ParseFormat(%q) message = %q, want %q", input, err.Error(), want)
		}
	}
}

func TestNewPrinterValidation(t *testing.T) {
	if _, err := NewPrinter(nil, FormatTable, Options{}); err == nil {
		t.Error("NewPrinter accepted a nil writer")
	}

	_, err := NewPrinter(&bytes.Buffer{}, Format("xml"), Options{})
	if err == nil || clierr.ExitCode(err) != clierr.ExitUsage {
		t.Errorf("NewPrinter(xml) = %v (exit %d), want a usage error", err, clierr.ExitCode(err))
	}
}

func TestHostTable(t *testing.T) {
	output := render(t, FormatTable, Options{}, func(p *Printer) error { return p.Hosts(sampleHosts()) })

	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want a header and two rows:\n%s", len(lines), output)
	}

	want := [][]string{
		{"NAME", "ID", "ENVIRONMENT", "ADDRESS", "STATUS"},
		{"web-01", "7", "2", "10.0.0.7", "reachable"},
		{"db", "12", "0", "db.example", "-"},
	}
	for index, line := range lines {
		got := strings.Fields(line)
		if strings.Join(got, "|") != strings.Join(want[index], "|") {
			t.Errorf("line %d = %v, want %v", index, got, want[index])
		}
	}

	// Columns must line up: the ID column starts at the same offset on every row.
	offset := strings.Index(lines[0], "ID")
	for _, line := range lines[1:] {
		if len(line) <= offset || line[offset-2] != ' ' {
			t.Errorf("row %q is not aligned with the header at offset %d", line, offset)
		}
	}
}

func TestHostTableMatchesLegacyBytes(t *testing.T) {
	var legacy, modern bytes.Buffer
	if err := PrintHosts(&legacy, "table", sampleHosts()); err != nil {
		t.Fatal(err)
	}
	printer, err := NewPrinter(&modern, FormatTable, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := printer.Hosts(sampleHosts()); err != nil {
		t.Fatal(err)
	}
	if legacy.String() != modern.String() {
		t.Fatalf("legacy and Printer output differ:\n%q\n%q", legacy.String(), modern.String())
	}
}

func TestEmptyHostTableKeepsOriginalMessage(t *testing.T) {
	output := render(t, FormatTable, Options{}, func(p *Printer) error { return p.Hosts(nil) })

	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "NAME") || !strings.HasPrefix(lines[1], "No resources found.") {
		t.Fatalf("output = %q", output)
	}
}

func TestNoHeaders(t *testing.T) {
	output := render(t, FormatTable, Options{NoHeaders: true}, func(p *Printer) error { return p.Hosts(sampleHosts()) })

	if strings.Contains(output, "NAME") {
		t.Fatalf("header was printed:\n%s", output)
	}
	if lines := strings.Split(strings.TrimRight(output, "\n"), "\n"); len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), output)
	}
}

func TestQuietPrintsOnlyIdentifiers(t *testing.T) {
	output := render(t, FormatTable, Options{Quiet: true}, func(p *Printer) error { return p.Hosts(sampleHosts()) })
	if output != "7\n12\n" {
		t.Fatalf("quiet list = %q", output)
	}

	output = render(t, FormatTable, Options{Quiet: true}, func(p *Printer) error { return p.Host(sampleHosts()[1]) })
	if output != "12\n" {
		t.Fatalf("quiet single = %q", output)
	}

	output = render(t, FormatTable, Options{Quiet: true}, func(p *Printer) error { return p.Hosts(nil) })
	if output != "" {
		t.Fatalf("quiet empty = %q, want nothing", output)
	}

	// Quiet only concerns human-readable output; documents stay complete.
	output = render(t, FormatJSON, Options{Quiet: true}, func(p *Printer) error { return p.Hosts(sampleHosts()) })
	if !strings.Contains(output, `"name": "web-01"`) {
		t.Fatalf("quiet must not strip JSON documents: %s", output)
	}
}

func TestJSONOutput(t *testing.T) {
	output := render(t, FormatJSON, Options{}, func(p *Printer) error { return p.Hosts(sampleHosts()) })

	var decoded []map[string]any
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, output)
	}
	if len(decoded) != 2 || decoded[0]["name"] != "web-01" || decoded[0]["ip_address"] != "10.0.0.7" {
		t.Fatalf("decoded = %v", decoded)
	}
	if !strings.Contains(output, "\n  {") {
		t.Errorf("JSON is not indented with two spaces:\n%s", output)
	}

	single := render(t, FormatJSON, Options{}, func(p *Printer) error { return p.Host(sampleHosts()[0]) })
	var object map[string]any
	if err := json.Unmarshal([]byte(single), &object); err != nil || object["id"] != "7" {
		t.Fatalf("single = %s, err = %v", single, err)
	}
}

func TestYAMLOutput(t *testing.T) {
	output := render(t, FormatYAML, Options{}, func(p *Printer) error { return p.Hosts(sampleHosts()) })

	for _, want := range []string{"name: web-01", "ip_address: 10.0.0.7", "environmentId: 2", "status: reachable"} {
		if !strings.Contains(output, want) {
			t.Errorf("YAML output lacks %q:\n%s", want, output)
		}
	}
}

func TestOutputNeverContainsCredentialFields(t *testing.T) {
	for _, format := range []Format{FormatJSON, FormatYAML} {
		output := strings.ToLower(render(t, format, Options{}, func(p *Printer) error { return p.Hosts(sampleHosts()) }))
		for _, forbidden := range []string{"password", "secret", "private", "passphrase", "token"} {
			if strings.Contains(output, forbidden) {
				t.Errorf("%s output of a host contains %q", format, forbidden)
			}
		}
	}
}

func TestTableCellsCannotBreakRows(t *testing.T) {
	hosts := []api.Host{{ID: "1", Name: "evil\tname\nwith\rbreaks", Hostname: "h"}}
	output := render(t, FormatTable, Options{}, func(p *Printer) error { return p.Hosts(hosts) })

	if lines := strings.Split(strings.TrimRight(output, "\n"), "\n"); len(lines) != 2 {
		t.Fatalf("a host name produced %d lines, want header + 1 row:\n%q", len(lines), output)
	}
}

func TestDocumentRefusesTableFormat(t *testing.T) {
	printer, err := NewPrinter(&bytes.Buffer{}, FormatTable, Options{})
	if err != nil {
		t.Fatal(err)
	}

	err = printer.Document(map[string]string{"a": "b"})
	if err == nil || clierr.ExitCode(err) != clierr.ExitUsage {
		t.Fatalf("Document on a table printer = %v (exit %d), want a usage error", err, clierr.ExitCode(err))
	}
}

func TestGenericTable(t *testing.T) {
	output := render(t, FormatTable, Options{}, func(p *Printer) error {
		return p.Table([]string{"A", "B"}, [][]string{{"1", "2"}, {"3", "4"}})
	})
	if lines := strings.Split(strings.TrimRight(output, "\n"), "\n"); len(lines) != 3 {
		t.Fatalf("output = %q", output)
	}

	output = render(t, FormatTable, Options{NoHeaders: true}, func(p *Printer) error {
		return p.Table([]string{"A", "B"}, [][]string{{"1", "2"}})
	})
	if strings.Contains(output, "A") || !strings.Contains(output, "1") {
		t.Fatalf("output = %q", output)
	}
}

func TestLegacyWrappers(t *testing.T) {
	var buffer bytes.Buffer
	if err := PrintHosts(&buffer, "yml", sampleHosts()); err != nil || !strings.Contains(buffer.String(), "name: web-01") {
		t.Fatalf("PrintHosts(yml) = %v\n%s", err, buffer.String())
	}

	buffer.Reset()
	if err := PrintHost(&buffer, "json", sampleHosts()[0]); err != nil || !strings.Contains(buffer.String(), `"id": "7"`) {
		t.Fatalf("PrintHost(json) = %v\n%s", err, buffer.String())
	}

	err := PrintHosts(&buffer, "xml", nil)
	if err == nil || err.Error() != `unsupported output format "xml"; choose table, json, or yaml` {
		t.Fatalf("PrintHosts(xml) = %v", err)
	}

	if err := PrintHosts(nil, "table", nil); err == nil {
		t.Fatal("PrintHosts accepted a nil writer")
	}
}
