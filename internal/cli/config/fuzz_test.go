package config

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzReadDocument feeds arbitrary bytes to the v1/v2 config reader: it must
// never panic, and an accepted document must hold only valid server names and
// URLs (T3, T10).
func FuzzReadDocument(f *testing.F) {
	for _, seed := range []string{
		"",
		"version: 2\nservers:\n  lab:\n    url: https://lab.example.com\n",
		"apiVersion: v1\ncontexts: []\n",
		"version: 99\n",
		"servers:\n  \"bad name\":\n    url: https://x\n",
		"servers:\n  a:\n    url: \"ht\\x00tp://\"\n",
		"\x00\xff[: {",
		"&a [*a]",
	} {
		f.Add([]byte(seed))
	}

	dir := f.TempDir()
	path := filepath.Join(dir, "config.yaml")

	f.Fuzz(func(t *testing.T, data []byte) {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}

		doc, err := readDocument(path)
		if err != nil {
			return
		}
		for name, server := range doc.Servers {
			if !serverNamePattern.MatchString(name) {
				t.Fatalf("accepted server name %q", name)
			}
			if ValidateServer(server.URL) != nil {
				t.Fatalf("accepted server URL %q", server.URL)
			}
		}
	})
}
