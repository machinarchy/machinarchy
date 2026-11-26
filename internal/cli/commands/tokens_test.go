package commands_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/cred"
)

func ownerConfig(t *testing.T, server *authServer) string {
	t.Helper()
	server.addSession("sess-owner")

	return seed(t, server.URL, cred.Credential{Kind: cred.KindSession, Secret: "sess-owner", User: "maxim"})
}

func TestTokensCreate(t *testing.T) {
	server := newAuthServer(t, false)
	path := ownerConfig(t, server)

	t.Run("the token is shown once, with its expiry", func(t *testing.T) {
		got := runAuth(t, authRun{config: path}, "tokens", "create", "ci-deploy", "--role", "operator", "--expires", "30d")
		if got.code != 0 {
			t.Fatalf("result = %+v", got)
		}
		secret := "mch_pat_ci-deploy-"
		if strings.Count(got.stdout, secret) != 1 || !strings.Contains(got.stdout, "Token ci-deploy created (operator, expires ") ||
			!strings.Contains(got.stdout, "It is shown only once:") || got.stderr != "" {
			t.Fatalf("stdout = %q, stderr = %q", got.stdout, got.stderr)
		}

		var body map[string]any
		create := server.saw("POST", "/api/v1/tokens")
		if err := json.Unmarshal(create[len(create)-1].Body, &body); err != nil || body["role"] != "operator" || body["expiresInHours"] != float64(720) || body["name"] != "ci-deploy" {
			t.Fatalf("request body = %v, %v", body, err)
		}
		if create[len(create)-1].Auth != "Bearer sess-owner" {
			t.Fatalf("the token must be created with the session: %q", create[len(create)-1].Auth)
		}
	})

	t.Run("-q prints the token alone", func(t *testing.T) {
		got := runAuth(t, authRun{config: path}, "tokens", "create", "ci-quiet", "--role", "reader", "-q")
		if got.code != 0 || !strings.HasPrefix(got.stdout, "mch_pat_ci-quiet-") || strings.Count(got.stdout, "\n") != 1 {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("the default lifetime is 90 days", func(t *testing.T) {
		runAuth(t, authRun{config: path}, "tokens", "create", "ci-default", "--role", "reader", "-q")
		var body map[string]any
		create := server.saw("POST", "/api/v1/tokens")
		_ = json.Unmarshal(create[len(create)-1].Body, &body)
		if body["expiresInHours"] != float64(2160) {
			t.Fatalf("body = %v", body)
		}
	})

	t.Run("--out writes a private file and prints no token", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "ci.token")
		got := runAuth(t, authRun{config: path}, "tokens", "create", "ci-file", "--role", "reader", "--out", file)
		if got.code != 0 || strings.Contains(got.stdout+got.stderr, "mch_pat_") || !strings.Contains(got.stdout, "written to "+file) {
			t.Fatalf("result = %+v", got)
		}
		data, err := os.ReadFile(file) // #nosec G304 -- test path
		if err != nil || !strings.HasPrefix(string(data), "mch_pat_ci-file-") {
			t.Fatalf("file = %q, %v", data, err)
		}
		if runtime.GOOS != "windows" {
			if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("mode = %v, %v", info, err)
			}
		}

		again := runAuth(t, authRun{config: path}, "tokens", "create", "ci-file2", "--role", "reader", "--out", file)
		if again.code != clierr.ExitUsage || !strings.Contains(again.stderr, "already exists") || strings.Contains(again.stderr, "mch_pat_") {
			t.Fatalf("an existing file must never be overwritten: %+v", again)
		}
		if after, _ := os.ReadFile(file); string(after) != string(data) { // #nosec G304 -- test path
			t.Fatal("the existing file was changed")
		}
	})

	t.Run("json and yaml include the token, once", func(t *testing.T) {
		for _, format := range []string{"json", "yaml"} {
			got := runAuth(t, authRun{config: path}, "tokens", "create", "ci-"+format, "--role", "reader", "-o", format)
			if got.code != 0 || strings.Count(got.stdout, "mch_pat_ci-"+format+"-") != 1 {
				t.Fatalf("%s result = %+v", format, got)
			}
		}
	})

	t.Run("bad input is refused before anything is sent", func(t *testing.T) {
		server.reset()
		for name, args := range map[string][]string{
			"an owner token":     {"tokens", "create", "x", "--role", "owner"},
			"no role":            {"tokens", "create", "x"},
			"a year and a day":   {"tokens", "create", "x", "--role", "reader", "--expires", "366d"},
			"not a lifetime":     {"tokens", "create", "x", "--role", "reader", "--expires", "soon"},
			"zero days":          {"tokens", "create", "x", "--role", "reader", "--expires", "0d"},
			"--out with -o json": {"tokens", "create", "x", "--role", "reader", "--out", filepath.Join(t.TempDir(), "t"), "-o", "json"},
		} {
			if got := runAuth(t, authRun{config: path}, args...); got.code != clierr.ExitUsage {
				t.Errorf("%s: result = %+v", name, got)
			}
		}
		for _, r := range server.saw("POST", "/api/v1/tokens") {
			t.Errorf("a refused command sent %s", r.Body)
		}
	})
}

func TestTokensListNeverShowsSecrets(t *testing.T) {
	server := newAuthServer(t, false)
	path := ownerConfig(t, server)
	server.addToken("mch_pat_listed-SECRET", "listed", "reader")

	for _, format := range []string{"table", "json", "yaml"} {
		got := runAuth(t, authRun{config: path}, "tokens", "-o", format, "-v")
		if got.code != 0 || !strings.Contains(got.stdout, "listed") || strings.Contains(got.stdout+got.stderr, "SECRET") {
			t.Fatalf("%s result = %+v", format, got)
		}
	}
	got := runAuth(t, authRun{config: path}, "tokens")
	for _, want := range []string{"ID", "NAME", "ROLE", "EXPIRES", "STATE", "active"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the table is missing %q:\n%s", want, got.stdout)
		}
	}
	if q := runAuth(t, authRun{config: path}, "tokens", "-q"); q.code != 0 || strings.TrimSpace(q.stdout) != "1" {
		t.Fatalf("-q = %+v", q)
	}
}

func TestTokensRevoke(t *testing.T) {
	server := newAuthServer(t, false)
	path := ownerConfig(t, server)
	first := server.addToken("mch_pat_ci-SECRET-1", "ci", "operator")

	t.Run("--yes does not confirm it", func(t *testing.T) {
		got := runAuth(t, authRun{config: path}, "tokens", "revoke", "ci", "--yes")
		if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "this needs confirmation, and --yes is not enough here") || !strings.Contains(got.stderr, "Next:  --confirm ci") {
			t.Fatalf("result = %+v", got)
		}
		if first.Revoked {
			t.Fatal("the token was revoked without confirmation")
		}
	})

	t.Run("the wrong name does not confirm it", func(t *testing.T) {
		got := runAuth(t, authRun{config: path}, "tokens", "revoke", "ci", "--confirm", "other")
		if got.code != clierr.ExitUsage || first.Revoked {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("typing the name on a terminal confirms it", func(t *testing.T) {
		wrong := runAuth(t, authRun{config: path, terminal: true, stdin: "nope\n"}, "tokens", "revoke", "ci")
		if wrong.code != clierr.ExitFailure || !strings.Contains(wrong.stderr, `Type "ci" to confirm: `) || !strings.Contains(wrong.stderr, "aborted; nothing was changed") || first.Revoked {
			t.Fatalf("wrong answer = %+v", wrong)
		}
		right := runAuth(t, authRun{config: path, terminal: true, stdin: "ci\n"}, "tokens", "revoke", "ci")
		if right.code != 0 || !strings.Contains(right.stdout, "Token ci revoked.") || !first.Revoked {
			t.Fatalf("right answer = %+v", right)
		}
		if kind, _ := server.identify("mch_pat_ci-SECRET-1"); kind != "" {
			t.Fatal("a revoked token still works")
		}
	})

	t.Run("a script confirms with --confirm", func(t *testing.T) {
		second := server.addToken("mch_pat_ci2-SECRET", "ci2", "reader")
		got := runAuth(t, authRun{config: path}, "tokens", "revoke", "ci2", "--confirm", "ci2")
		if got.code != 0 || !second.Revoked {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("unknown and duplicate names", func(t *testing.T) {
		if got := runAuth(t, authRun{config: path}, "tokens", "revoke", "ghost", "--confirm", "ghost"); got.code != clierr.ExitNotFound || !strings.Contains(got.stderr, "Next:  machinarchy tokens") {
			t.Fatalf("unknown = %+v", got)
		}
		a := server.addToken("mch_pat_dup-SECRET-a", "dup", "reader")
		b := server.addToken("mch_pat_dup-SECRET-b", "dup", "reader")
		got := runAuth(t, authRun{config: path}, "tokens", "revoke", "dup", "--confirm", "dup")
		if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "2 active tokens are called \"dup\"") || a.Revoked || b.Revoked {
			t.Fatalf("duplicates = %+v", got)
		}
		if got := runAuth(t, authRun{config: path}, "tokens", "revoke", "dup", "--id", itoa(b.ID), "--confirm", "dup"); got.code != 0 || !b.Revoked || a.Revoked {
			t.Fatalf("by id = %+v", got)
		}
	})
}
