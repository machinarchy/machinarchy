package commands_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/machinarchy/machinarchy/internal/cli/cred"
)

// updateSchemas rewrites the schema goldens: go test ./internal/cli/commands -run TestOutputSchemas -update
var updateSchemas = flag.Bool("update", false, "rewrite the -o json schema goldens in testdata/schema")

// shape reduces a decoded document to its key paths and value types. Lists
// merge the shapes of their elements under "[]". The same reduction is used
// for JSON and YAML, so the two formats must agree on names and types.
func shape(prefix string, value any, into map[string]map[string]bool) {
	add := func(kind string) {
		if into[prefix] == nil {
			into[prefix] = map[string]bool{}
		}
		into[prefix][kind] = true
	}
	switch v := value.(type) {
	case map[string]any:
		add("object")
		for key, child := range v {
			shape(prefix+"."+key, child, into)
		}
	case []any:
		add("array")
		for _, child := range v {
			shape(prefix+"[]", child, into)
		}
	case string, time.Time:
		add("string")
	case bool:
		add("boolean")
	case nil:
		add("null")
	default: // json float64; yaml int, int64, float64, uint64
		add("number")
	}
}

func shapeLines(t *testing.T, format, text string) []string {
	t.Helper()
	var decoded any
	switch format {
	case "json":
		if err := json.Unmarshal([]byte(text), &decoded); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, text)
		}
	default:
		if err := yaml.Unmarshal([]byte(text), &decoded); err != nil {
			t.Fatalf("not YAML: %v\n%s", err, text)
		}
	}
	paths := map[string]map[string]bool{}
	shape("$", decoded, paths)

	lines := make([]string, 0, len(paths))
	for path, kinds := range paths {
		var names []string
		for kind := range kinds {
			names = append(names, kind)
		}
		sort.Strings(names)
		lines = append(lines, path+" "+strings.Join(names, "|"))
	}
	sort.Strings(lines)

	return lines
}

// schemaCase is one command whose -o output is a compatibility promise.
type schemaCase struct {
	name string
	// run returns stdout of the command for one -o format.
	run func(t *testing.T, format string) string
	// stable says the output must be byte-identical when run twice (N-02).
	stable bool
}

func schemaCases(t *testing.T) []schemaCase {
	t.Helper()
	hostsServer := hostsGroupServer(t)
	hostsConfig := writeConfig(t, hostsServer.URL, "tok")
	accessServer := accessEntriesServer(t, false)
	accessConfig := writeConfig(t, accessServer.URL, "tok")

	api := func(config string, args ...string) func(*testing.T, string) string {
		return func(t *testing.T, format string) string {
			t.Helper()
			got := execute(t, nil, "", append([]string{"--config", config}, append(args, "-o", format)...)...)
			if got.code != 0 {
				t.Fatalf("%v -o %s = %+v", args, format, got)
			}

			return got.stdout
		}
	}

	auth := newAuthServer(t, false)
	auth.addToken("mch_pat_schema-SECRET", "schema", "reader")
	ownerPath := ownerConfig(t, auth)
	fixed := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	auth.addToken("mch_pat_device-0001", "device-laptop", "operator")
	whoamiPath := seed(t, auth.URL, cred.Credential{Kind: cred.KindDeviceToken, Secret: "mch_pat_device-0001", User: "maxim", Name: "device-laptop", Role: "operator", ExpiresAt: &fixed})

	fake := newRunFake(fleet("web", 3, 1)...)
	fake.put(operation(runID("4b2d9e10", 1), "Succeeded", target("web-01", "Succeeded", exit(0), "ok\n", ""), target("web-02", "Succeeded", exit(0), "ok\n", "")))
	fake.put(operation(runID("8d1a0b7c", 2), "PartiallySucceeded", target("web-01", "Succeeded", exit(0), "ok\n", ""), target("web-02", "Failed", exit(1), "", "")))

	homeSrv := homeServer(t, []homeHost{ready("a")}, twoRuns(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)), nil)
	homePath := sessionConfig(t, homeSrv.URL, inHours(5))

	return []schemaCase{
		{"hosts", api(hostsConfig, "hosts"), true},
		{"hosts-show", api(hostsConfig, "hosts", "show", "web-01"), true},
		{"envs", api(hostsConfig, "envs"), true},
		{"access", api(accessConfig, "access"), true},
		{"access-show", api(accessConfig, "access", "show", "deploy"), true},
		// The auth fake lists tokens in map order with the clock, so only the shape is promised here.
		{"tokens", func(t *testing.T, format string) string {
			return runAuth(t, authRun{config: ownerPath}, "tokens", "-o", format).stdout
		}, false},
		{"whoami", func(t *testing.T, format string) string {
			got := runAuth(t, authRun{config: whoamiPath}, "whoami", "-o", format)
			if got.code != 0 {
				t.Fatalf("whoami = %+v", got)
			}

			return got.stdout
		}, true},
		// The run fake stamps the list with the clock, so only the shape is promised here.
		{"run-list", func(t *testing.T, format string) string {
			return runCLI(t, fake, runCfg{}, "run", "-o", format).stdout
		}, false},
		{"run-show", func(t *testing.T, format string) string {
			return runCLI(t, fake, runCfg{}, "run", runID("8d1a0b7c", 2), "-o", format).stdout
		}, true},
		// The home document and version carry the clock and the build, so only their shape is promised.
		{"home", func(t *testing.T, format string) string {
			return runHome(t, homePath, nil, "-o", format).stdout
		}, false},
		{"version", func(t *testing.T, format string) string {
			return execute(t, nil, "", "version", "-o", format).stdout
		}, false},
	}
}

// N-01: field names and types of every -o document; keys may be added, never
// removed or retyped. N-02: the same command twice prints the same bytes.
func TestOutputSchemas(t *testing.T) {
	dir := filepath.Join("testdata", "schema")
	for _, c := range schemaCases(t) {
		t.Run(c.name, func(t *testing.T) {
			asJSON := c.run(t, "json")
			asYAML := c.run(t, "yaml")
			got := shapeLines(t, "json", asJSON)

			// -o yaml uses the same names and types as -o json. It may leave out an
			// empty optional value (yaml omitempty drops a zero time that json prints),
			// so every YAML path must exist in the JSON document, not the reverse.
			inJSON := map[string]bool{}
			for _, line := range got {
				inJSON[line] = true
			}
			for _, line := range shapeLines(t, "yaml", asYAML) {
				if !inJSON[line] {
					t.Errorf("-o yaml has %q, which -o json does not", line)
				}
			}
			if c.stable {
				if again := c.run(t, "json"); again != asJSON {
					t.Errorf("-o json is not byte-stable (N-02):\n%s\n---\n%s", asJSON, again)
				}
				if again := c.run(t, "yaml"); again != asYAML {
					t.Errorf("-o yaml is not byte-stable (N-02):\n%s\n---\n%s", asYAML, again)
				}
			}

			path := filepath.Join(dir, c.name+".txt")
			if *updateSchemas {
				if err := os.MkdirAll(dir, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(strings.Join(got, "\n")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}

				return
			}
			golden, err := os.ReadFile(path) //nolint:gosec // fixed test path
			if err != nil {
				t.Fatalf("no golden for %s (run with -update): %v", c.name, err)
			}
			have := map[string]bool{}
			for _, line := range got {
				have[line] = true
			}
			for _, line := range strings.Split(strings.TrimSpace(string(golden)), "\n") {
				if !have[line] {
					t.Errorf("%s: %q was promised but is gone or changed (a key was removed, renamed or retyped)", c.name, line)
				}
			}
		})
	}
}

// The check itself must fail when a key disappears or changes type.
func TestSchemaCheckCatchesBreakage(t *testing.T) {
	promised := shapeLines(t, "json", `{"id":"a","count":1,"tags":[{"k":"v"}]}`)
	for name, doc := range map[string]string{
		"removed": `{"id":"a","tags":[{"k":"v"}]}`,
		"renamed": `{"name":"a","count":1,"tags":[{"k":"v"}]}`,
		"retyped": `{"id":"a","count":"1","tags":[{"k":"v"}]}`,
	} {
		have := map[string]bool{}
		for _, line := range shapeLines(t, "json", doc) {
			have[line] = true
		}
		broken := false
		for _, line := range promised {
			broken = broken || !have[line]
		}
		if !broken {
			t.Errorf("%s: breakage went unnoticed", name)
		}
	}
	added := map[string]bool{}
	for _, line := range shapeLines(t, "json", `{"id":"a","count":1,"tags":[{"k":"v","x":true}],"new":1}`) {
		added[line] = true
	}
	for _, line := range promised {
		if !added[line] {
			t.Errorf("adding keys must stay allowed, but %q failed", line)
		}
	}
}
