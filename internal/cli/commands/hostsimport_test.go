package commands_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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

// importServer answers the batch route: a name starting with "dup" is a
// conflict. It records every batch body.
func importServer(t *testing.T, batches *[]struct {
	ValidateOnly bool
	Hosts        []api.HostInput
}) *apiServer {
	t.Helper()
	var server *apiServer
	server = newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/environments":
			_, _ = w.Write([]byte(`[{"id":1,"name":"production","description":""},{"id":2,"name":"staging","description":""}]`))
		case "GET /api/v1/managed/access-profiles":
			_, _ = w.Write([]byte(`{"items":[{"id":"prof-2","name":"deploy","currentRevision":1}]}`))
		case "POST /api/v1/managed/hosts/batch":
			var body struct {
				ValidateOnly bool
				Hosts        []api.HostInput
			}
			server.mu.Lock()
			raw := server.requests[len(server.requests)-1].Body
			server.mu.Unlock()
			_ = json.Unmarshal(raw, &body)
			*batches = append(*batches, body)
			result := api.HostBatchResult{ValidateOnly: body.ValidateOnly}
			for i, h := range body.Hosts {
				out := api.HostBatchOutcome{Index: i, Name: h.Name, Status: "valid"}
				switch {
				case strings.HasPrefix(h.Name, "dup"):
					out.Status, out.Error = "conflict", "already exists"
					result.Failed++
				case !body.ValidateOnly:
					out.Status, out.ID = "created", "h-"+h.Name
					result.Created++
				}
				result.Results = append(result.Results, out)
			}
			_ = json.NewEncoder(w).Encode(result)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no route","code":"not_found"}`))
		}
	})

	return server
}

type batchBody = struct {
	ValidateOnly bool
	Hosts        []api.HostInput
}

func writeImport(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestHostsImportApplies(t *testing.T) {
	var batches []batchBody
	server := importServer(t, &batches)
	file := writeImport(t, "fleet.yaml", importYAMLFile)

	got := addRun(t, server, false, "", "hosts", "import", file, "--yes")
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stderr)
	}
	if len(batches) != 2 || !batches[0].ValidateOnly || batches[1].ValidateOnly {
		t.Fatalf("want a validate-only call, then the real one: %+v", batches)
	}
	first := batches[1].Hosts[0]
	if first.EnvironmentID != 2 || first.Spec.AccessProfileID != "prof-2" || first.Spec.AccessTarget.Endpoint != "10.0.0.5:22" || first.Labels["tier"] != "web" {
		t.Errorf("host sent = %+v", first)
	}
	if !strings.Contains(got.stdout, "created") || !strings.Contains(got.stderr, "valid") {
		t.Errorf("stdout=%q stderr=%q", got.stdout, got.stderr)
	}
}

func TestHostsImportDryRunWritesNothing(t *testing.T) {
	var batches []batchBody
	server := importServer(t, &batches)
	file := writeImport(t, "fleet.yaml", importYAMLFile)

	got := addRun(t, server, false, "", "hosts", "import", file, "--dry-run")
	if got.code != 0 || len(batches) != 1 || !batches[0].ValidateOnly {
		t.Fatalf("exit %d batches %+v: %s", got.code, batches, got.stderr)
	}
}

func TestHostsImportNeedsConfirmation(t *testing.T) {
	var batches []batchBody
	server := importServer(t, &batches)
	file := writeImport(t, "fleet.yaml", importYAMLFile)

	got := addRun(t, server, false, "", "hosts", "import", file)
	if got.code != clierr.ExitUsage {
		t.Fatalf("exit %d, want 2: %s", got.code, got.stderr)
	}
	for _, b := range batches {
		if !b.ValidateOnly {
			t.Errorf("a real write was sent without confirmation")
		}
	}
}

func TestHostsImportStopsOnInvalidUnlessSkipped(t *testing.T) {
	var batches []batchBody
	server := importServer(t, &batches)
	file := writeImport(t, "fleet.csv", "name,address\nweb-1,10.0.0.5\ndup-1,10.0.0.6\n")

	got := addRun(t, server, false, "", "hosts", "import", file, "--yes")
	if got.code != clierr.ExitFailure || !strings.Contains(got.stderr, "--skip-invalid") || !strings.Contains(got.stderr, "conflict") {
		t.Fatalf("exit %d: %s", got.code, got.stderr)
	}
	if len(batches) != 1 {
		t.Fatalf("nothing may be written: %+v", batches)
	}

	got = addRun(t, server, false, "", "hosts", "import", file, "--yes", "--skip-invalid")
	if got.code != 0 || len(batches) != 3 || len(batches[2].Hosts) != 1 || batches[2].Hosts[0].Name != "web-1" {
		t.Fatalf("exit %d batches %+v: %s", got.code, batches, got.stderr)
	}
}

func TestHostsImportRefusesBadFileBeforeAnyRequest(t *testing.T) {
	var batches []batchBody
	server := importServer(t, &batches)
	for name, content := range map[string]string{
		"bad.yaml":      "hosts:\n  - {name: a}\n",
		"unknown.yaml":  "hosts:\n  - {name: a, address: 1.2.3.4, env: nowhere}\n",
		"noaccess.yaml": "hosts:\n  - {name: a, address: 1.2.3.4, access: nope}\n",
		"notes.txt":     "x",
	} {
		got := addRun(t, server, false, "", "hosts", "import", writeImport(t, name, content), "--yes")
		if got.code == 0 {
			t.Errorf("%s: exit 0", name)
		}
	}
	if len(batches) != 0 || server.countOf("POST", "/api/v1/managed/hosts/batch") != 0 {
		t.Errorf("a batch was sent: %+v", batches)
	}
	if got := addRun(t, server, false, "", "hosts", "import", filepath.Join(t.TempDir(), "missing.yaml")); got.code != clierr.ExitUsage {
		t.Errorf("missing file: exit %d", got.code)
	}
}

func TestHostsImportReadsStdin(t *testing.T) {
	var batches []batchBody
	server := importServer(t, &batches)
	got := addRun(t, server, false, importYAMLFile, "hosts", "import", "-", "--format", "yaml", "--yes")
	if got.code != 0 || len(batches) != 2 {
		t.Fatalf("exit %d: %s", got.code, got.stderr)
	}
	if got := addRun(t, server, false, importYAMLFile, "hosts", "import", "-", "--yes"); got.code != clierr.ExitUsage {
		t.Errorf("stdin without --format: exit %d", got.code)
	}
}
