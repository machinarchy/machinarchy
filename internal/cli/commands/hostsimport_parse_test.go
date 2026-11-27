package commands

import (
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

const importYAMLFile = `hosts:
  - name: web-1
    address: 10.0.0.5
    env: staging
    access: deploy
    tags: {tier: web}
  - name: web-2
    address: web2.example.com:2222
`

func TestParseHostImportYAML(t *testing.T) {
	records, err := parseHostImport([]byte(importYAMLFile), "yaml")
	if err != nil || len(records) != 2 {
		t.Fatalf("records=%v err=%v", records, err)
	}
	if records[0].Address != "10.0.0.5:22" || records[0].Env != "staging" || records[0].Access != "deploy" || records[0].Tags["tier"] != "web" {
		t.Errorf("record 0 = %+v", records[0])
	}
	if records[1].Address != "web2.example.com:2222" {
		t.Errorf("record 1 = %+v", records[1])
	}
}

func TestParseHostImportCSV(t *testing.T) {
	data := "\ufeffName, Address ,env,tags\nweb-1,10.0.0.5,staging,tier=web;zone=a\nweb-2,10.0.0.6,,\n"
	records, err := parseHostImport([]byte(data), "csv")
	if err != nil || len(records) != 2 {
		t.Fatalf("records=%v err=%v", records, err)
	}
	if records[0].Tags["zone"] != "a" || records[0].Env != "staging" || records[1].Env != "" || records[1].Where != "row 3" {
		t.Errorf("records = %+v", records)
	}
}

func TestParseHostImportRejects(t *testing.T) {
	many := "hosts:\n" + strings.Repeat("  - {name: h, address: 10.0.0.1}\n", api.MaxHostBatch+1)
	cases := map[string]struct{ data, format, want string }{
		"empty":            {"", "yaml", "no hosts"},
		"unknown field":    {"hosts:\n  - {name: a, address: 1.2.3.4, password: x}\n", "yaml", "not valid"},
		"two documents":    {importYAMLFile + "---\nhosts: []\n", "yaml", "single document"},
		"no address":       {"hosts:\n  - {name: a}\n", "yaml", "address is empty"},
		"bad address":      {"hosts:\n  - {name: a, address: 'x y'}\n", "yaml", "HOST or HOST:PORT"},
		"bad name":         {"hosts:\n  - {name: 'a b', address: 1.2.3.4}\n", "yaml", "host name"},
		"reserved tag":     {"hosts:\n  - {name: a, address: 1.2.3.4, tags: {id: x}}\n", "yaml", "reserved"},
		"duplicate":        {"hosts:\n  - {name: a, address: 1.2.3.4}\n  - {name: a, address: 1.2.3.5}\n", "yaml", "repeats host 1"},
		"over limit":       {many, "yaml", "at most 100"},
		"csv column":       {"name,address,password\na,1.2.3.4,x\n", "csv", "unknown column"},
		"csv no address":   {"name\na\n", "csv", "address column"},
		"csv bad tag":      {"name,address,tags\na,1.2.3.4,oops\n", "csv", "KEY=VALUE"},
		"csv extra fields": {"name,address\na,1.2.3.4,zzz\n", "csv", "more fields"},
		"too big":          {strings.Repeat("#", maxImportFile+1), "yaml", "larger than"},
	}
	for name, c := range cases {
		_, err := parseHostImport([]byte(c.data), c.format)
		if err == nil || !strings.Contains(err.Error(), c.want) || clierr.ExitCode(err) != clierr.ExitUsage {
			t.Errorf("%s: err = %v, want usage error containing %q", name, err, c.want)
		}
	}
}

func FuzzParseHostImport(f *testing.F) {
	f.Add([]byte(importYAMLFile), "yaml")
	f.Add([]byte("name,address,tags\nweb-1,10.0.0.5,a=b\n"), "csv")
	f.Add([]byte("hosts: &a [*a]"), "yaml")
	f.Add([]byte("\"unterminated,address\n"), "csv")
	f.Fuzz(func(t *testing.T, data []byte, format string) {
		if format != "csv" {
			format = "yaml"
		}
		records, err := parseHostImport(data, format)
		if err == nil && (len(records) == 0 || len(records) > api.MaxHostBatch) {
			t.Fatalf("accepted %d records", len(records))
		}
		for _, r := range records {
			if strings.ContainsAny(r.Name, " \t\r\n") || !strings.Contains(r.Address, ":") {
				t.Fatalf("accepted bad record %+v", r)
			}
		}
	})
}
