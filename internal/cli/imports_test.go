package cli_test

import (
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/machinarchy/machinarchy/internal/cli/"

// U-11: the new building blocks have a one-way dependency order, so none of
// them can grow a dependency on the commands or the API
// (docs/cli/spec/05-architecture.md section 2). Each package may import only
// the CLI packages listed here, besides the standard library and the few
// third-party packages named.
func TestPackageDependencyRules(t *testing.T) {
	allowed := map[string][]string{
		"sanitize":  {},
		"msg":       {},
		"ui":        {"msg", "sanitize"},
		"group":     {"msg", "sanitize", "ui"},
		"deprecate": {"msg"},
		"clierr":    {},
	}
	thirdParty := map[string]map[string]bool{
		"ui":        {"golang.org/x/term": true},
		"deprecate": {"github.com/spf13/cobra": true},
	}

	for pkg, cliImports := range allowed {
		allowedSet := map[string]bool{}
		for _, name := range cliImports {
			allowedSet[modulePath+name] = true
		}

		files, err := filepath.Glob(filepath.Join(pkg, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no files for %s: %v", pkg, err)
		}

		var problems []string
		fset := token.NewFileSet()
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(fset, file, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, spec := range parsed.Imports {
				path, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				switch {
				case strings.HasPrefix(path, "github.com/machinarchy/"):
					if !allowedSet[path] {
						problems = append(problems, file+" imports "+path)
					}
				case strings.Contains(strings.SplitN(path, "/", 2)[0], "."): // a third-party module
					if !thirdParty[pkg][path] {
						problems = append(problems, file+" imports third-party "+path)
					}
				}
			}
		}

		sort.Strings(problems)
		if len(problems) > 0 {
			t.Errorf("package %s breaks the dependency rules:\n  %s", pkg, strings.Join(problems, "\n  "))
		}
	}
}

// The CLI binary depends on four third-party modules and nothing else; M1
// adds none (docs/cli/spec/05-architecture.md section 10). go.mod also lists
// the controller's own dependencies, so the check is on what the CLI binary
// actually links.
func TestCLIBinaryHasNoNewThirdPartyDependencies(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the go tool is not available")
	}

	cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./cmd/machinarchy") // #nosec G204 -- fixed arguments
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}

	allowed := []string{
		"github.com/machinarchy/machinarchy",
		"github.com/spf13/cobra", "github.com/spf13/pflag",
		"go.yaml.in/yaml/v3",
		"golang.org/x/term", "golang.org/x/sys",
	}
	for _, path := range strings.Fields(string(out)) {
		ok := false
		for _, prefix := range allowed {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				ok = true

				break
			}
		}
		if !ok {
			t.Errorf("the CLI binary links %s, which is not one of the approved dependencies", path)
		}
	}
}
