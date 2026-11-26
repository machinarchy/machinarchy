package agentgateway

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBrokerImportsStayInAdapters keeps the broker library out of the gateway
// core (ADR-059): only the NATS adapter and natsjwt.go may import nats-io.
func TestBrokerImportsStayInAdapters(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		slash := filepath.ToSlash(path)
		if strings.HasPrefix(slash, "bus/nats/") || slash == "natsjwt.go" {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			if strings.Contains(spec.Path.Value, "nats-io/") {
				t.Errorf("%s imports %s; only bus/nats and natsjwt.go may", path, spec.Path.Value)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
