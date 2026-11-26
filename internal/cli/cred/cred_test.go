package cred

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

const secret = "mch_pat_SUPER-SECRET-VALUE-1234567890"

func newStore(t *testing.T) (*File, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "credentials.yaml")

	return NewFile(path), path
}

// B-22 at the type level: no formatting verb prints the secret.
func TestCredentialNeverPrintsItsSecret(t *testing.T) {
	t.Parallel()

	expires := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	c := Credential{Kind: KindToken, Secret: secret, Name: "ci", ExpiresAt: &expires}
	entry := Entry{Token: &c}

	for _, text := range []string{
		fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprintf("%s", c), fmt.Sprint(c),
		fmt.Sprintf("%v", &c), fmt.Sprintf("%+v", entry), fmt.Sprintf("%#v", entry), fmt.Sprintf("%v", []Credential{c}),
		fmt.Sprintf("%v", map[string]Credential{"x": c}), fmt.Errorf("failed: %v", c).Error(), fmt.Errorf("failed: %w", fmt.Errorf("%v", c)).Error(),
	} {
		if strings.Contains(text, secret) || strings.Contains(text, "SECRET") {
			t.Fatalf("a formatting verb printed the secret: %q", text)
		}
	}
	if got := c.String(); got != "***" {
		t.Fatalf("String = %q", got)
	}
}

func TestPutGetDeleteRoundTrip(t *testing.T) {
	t.Parallel()

	store, path := newStore(t)
	expires := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)

	if entry, err := store.Get("lab"); err != nil || !entry.Empty() {
		t.Fatalf("a missing file is an empty store: %+v, %v", entry, err)
	}

	session := Credential{Kind: KindSession, Secret: "session-secret", User: "maxim", ExpiresAt: &expires}
	device := Credential{Kind: KindDeviceToken, Secret: secret, Name: "device-laptop", Role: "operator", TokenID: 7}
	if err := store.Put("lab", session); err != nil {
		t.Fatal(err)
	}
	if err := store.Put("lab", device); err != nil {
		t.Fatal(err)
	}
	if err := store.Put("other", Credential{Kind: KindToken, Secret: "other-secret"}); err != nil {
		t.Fatal(err)
	}

	entry, err := store.Get("lab")
	if err != nil || entry.Session == nil || entry.Token == nil {
		t.Fatalf("both slots must be kept: %+v, %v", entry, err)
	}
	if entry.Session.Secret != "session-secret" || entry.Session.User != "maxim" || !entry.Session.ExpiresAt.Equal(expires) {
		t.Fatalf("session = %+v", *entry.Session)
	}
	if entry.Token.Secret != secret || entry.Token.TokenID != 7 || entry.Token.Role != "operator" || entry.Token.Kind != KindDeviceToken {
		t.Fatalf("token = %+v", *entry.Token)
	}

	// Replacing one slot keeps the other.
	if err := store.Put("lab", Credential{Kind: KindSession, Secret: "new-session"}); err != nil {
		t.Fatal(err)
	}
	entry, _ = store.Get("lab")
	if entry.Session.Secret != "new-session" || entry.Token.Secret != secret {
		t.Fatalf("Put changed the other slot: %+v / %+v", *entry.Session, *entry.Token)
	}

	if err := store.Delete("lab", SlotSession); err != nil {
		t.Fatal(err)
	}
	if entry, _ = store.Get("lab"); entry.Session != nil || entry.Token == nil {
		t.Fatalf("Delete(session) = %+v", entry)
	}
	if err := store.Delete("lab"); err != nil {
		t.Fatal(err)
	}
	if entry, _ = store.Get("lab"); !entry.Empty() {
		t.Fatalf("Delete() must remove everything: %+v", entry)
	}
	if entry, _ = store.Get("other"); entry.Token == nil {
		t.Fatal("another server's credential was removed")
	}
	if err := store.Delete("never-saved"); err != nil {
		t.Fatalf("deleting an unknown server must succeed: %v", err)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, %v", info.Mode().Perm(), err)
		}
	}
}

func TestPutRejectsEmptyValues(t *testing.T) {
	t.Parallel()

	store, _ := newStore(t)
	if err := store.Put("", Credential{Kind: KindToken, Secret: "x"}); err == nil {
		t.Error("an empty server name was accepted")
	}
	if err := store.Put("lab", Credential{Kind: KindToken}); err == nil {
		t.Error("an empty secret was accepted")
	}
}

// S-27: a credentials file that other users can read is refused, with the fix.
func TestLaxPermissionsAreRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes do not apply on Windows")
	}
	t.Parallel()

	store, path := newStore(t)
	if err := store.Put("lab", Credential{Kind: KindToken, Secret: secret}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	for name, call := range map[string]func() error{
		"Get":    func() error { _, err := store.Get("lab"); return err },
		"Put":    func() error { return store.Put("lab", Credential{Kind: KindToken, Secret: "x"}) },
		"Delete": func() error { return store.Delete("lab") },
	} {
		err := call()
		if err == nil {
			t.Fatalf("%s accepted a file readable by other users", name)
		}
		if code, _ := clierr.Classify(err); code != clierr.ExitUsage {
			t.Errorf("%s exit code = %d", name, code)
		}
		if !strings.Contains(clierr.Hint(err), "chmod 600 "+path) || strings.Contains(err.Error(), secret) || strings.Contains(clierr.Why(err), secret) {
			t.Errorf("%s: next = %q, error = %q", name, clierr.Hint(err), err)
		}
	}
}

func TestDamagedFilesStopTheCommand(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"not yaml":        "servers: [unterminated",
		"future version":  "version: 9\nservers: {}\n",
		"missing version": "servers:\n  lab:\n    token: {kind: token, secret: x}\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, path := newStore(t)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Get("lab"); err == nil {
				t.Fatal("a damaged file was accepted")
			}
			if err := store.Put("lab", Credential{Kind: KindToken, Secret: "x"}); err == nil {
				t.Fatal("Put overwrote a file it could not read")
			}
			if data, _ := os.ReadFile(path); string(data) != content { // #nosec G304 -- test path
				t.Fatal("a file that could not be read was changed")
			}
		})
	}
}

func TestEmptyAndOversizedAndDirectoryFiles(t *testing.T) {
	t.Parallel()

	store, path := newStore(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if entry, err := store.Get("lab"); err != nil || !entry.Empty() {
		t.Fatalf("an empty file is an empty store: %+v, %v", entry, err)
	}

	if err := os.WriteFile(path, []byte("version: 2\n# "+strings.Repeat("x", maxFileSize)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("lab"); err == nil {
		t.Fatal("an oversized file was accepted")
	}

	dir := filepath.Join(t.TempDir(), "credentials.yaml")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFile(dir).Get("lab"); err == nil {
		t.Fatal("a directory was accepted")
	}
}

func TestExpired(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	if !(Credential{ExpiresAt: &past}).Expired(now) || (Credential{ExpiresAt: &future}).Expired(now) || (Credential{}).Expired(now) {
		t.Fatal("Expired is wrong")
	}
}

func TestSlotOfAndWhere(t *testing.T) {
	t.Parallel()

	if SlotOf(KindSession) != SlotSession || SlotOf(KindToken) != SlotToken || SlotOf(KindDeviceToken) != SlotToken {
		t.Fatal("SlotOf is wrong")
	}
	store, path := newStore(t)
	if store.Where() != "a private file" || strings.Contains(store.Where(), path) {
		t.Fatalf("Where = %q", store.Where())
	}
}
