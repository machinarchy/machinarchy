package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteAtomicCreatesPrivateFileAndDirectory(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "dir", "secret.yaml")
	if err := WriteAtomic(path, []byte("one"), 0o700, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(path, []byte("two"), 0o700, 0o600); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path) // #nosec G304 -- test path
	if err != nil || string(data) != "two" {
		t.Fatalf("content = %q, %v", data, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, %v", info.Mode().Perm(), err)
		}
		dir, err := os.Stat(filepath.Dir(path))
		if err != nil || dir.Mode().Perm() != 0o700 {
			t.Fatalf("directory mode = %v, %v", dir.Mode().Perm(), err)
		}
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("a temporary file was left behind: %v, %v", entries, err)
	}
}

func TestWriteAtomicFailsWhenTheDirectoryCannotBeCreated(t *testing.T) {
	t.Parallel()

	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(filepath.Join(blocker, "sub", "x"), []byte("x"), 0o700, 0o600); err == nil {
		t.Fatal("writing below a regular file must fail")
	}
}
