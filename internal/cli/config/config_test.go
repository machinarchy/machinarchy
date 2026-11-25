package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

func flagSet(t *testing.T, args ...string) *pflag.FlagSet {
	t.Helper()

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.StringP("output", "o", "", "")
	flags.String("server", "", "")
	flags.String("env", "", "")
	flags.String("request-timeout", "", "")
	flags.Bool("quiet", false, "")
	flags.Bool("no-headers", false, "")
	flags.Bool("no-input", false, "")
	flags.Bool("verbose", false, "")
	if err := flags.Parse(args); err != nil {
		t.Fatal(err)
	}

	return flags
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"SERVER", "ENV", "OUTPUT", "TOKEN", "REQUEST_TIMEOUT", "QUIET", "NO_HEADERS", "NO_INPUT", "VERBOSE", "PLAIN"} {
		t.Setenv("MACHINARCHY_"+name, "")
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func load(t *testing.T, content string, args ...string) *Store {
	t.Helper()
	clearEnv(t)

	store, err := Load(writeFile(t, content), flagSet(t, args...))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	return store
}

func TestLoadMissingEmptyAndValidFiles(t *testing.T) {
	clearEnv(t)

	missing, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), flagSet(t))
	if err != nil || len(missing.ServerNames()) != 0 || missing.ActiveName() != "" {
		t.Fatalf("a missing file is an empty config: %+v, %v", missing, err)
	}

	empty := load(t, "  \n")
	if len(empty.ServerNames()) != 0 {
		t.Fatal("an empty file is an empty config")
	}

	store := load(t, "version: 2\nactive: lab\nservers:\n  lab:\n    url: https://lab.example/\n    env: production\n  Prod.EU:\n    url: http://localhost:8080\n    insecure-http: true\n")
	if got := strings.Join(store.ServerNames(), ","); got != "Prod.EU,lab" {
		t.Fatalf("names = %q", got)
	}
	lab, ok := store.Server("lab")
	if !ok || lab.URL != "https://lab.example/" || lab.Env != "production" || store.ActiveName() != "lab" {
		t.Fatalf("lab = %+v, active %q", lab, store.ActiveName())
	}
}

func TestLoadRefusesWhatItCannotTrust(t *testing.T) {
	clearEnv(t)

	tests := map[string]struct {
		content string
		want    string
	}{
		"not yaml":       {"servers: [unterminated", "it is not valid YAML"},
		"future version": {"version: 9\n", "has version 9; this CLI reads version 2"},
		"bad URL":        {"version: 2\nservers:\n  lab:\n    url: localhost\n", `server "lab" in the CLI config has an invalid URL`},
		"bad name":       {"version: 2\nservers:\n  \"-bad name\":\n    url: https://x.example\n", "is not a valid server name"},
		"bad default":    {"defaults:\n  server: https://x.example\n", "defaults.server cannot be saved"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeFile(t, tc.content), flagSet(t))
			if err == nil || !strings.Contains(err.Error()+clierr.Why(err), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if code, _ := clierr.Classify(err); code != clierr.ExitUsage {
				t.Fatalf("exit code = %d", code)
			}
		})
	}

	directory := t.TempDir()
	if _, err := Load(directory, flagSet(t)); err == nil {
		t.Fatal("a directory was accepted")
	}
}

// The older format is named and refused; nothing is converted.
func TestLoadRefusesTheOlderFormat(t *testing.T) {
	clearEnv(t)

	path := writeFile(t, "apiVersion: machinarchy.io/v1\ncurrent-context: lab\nclusters:\n  lab:\n    server: https://x.example\nusers:\n  lab:\n    token: OLD-TOKEN\ncontexts:\n  lab: {cluster: lab, user: lab}\n")
	_, err := Load(path, flagSet(t))
	if err == nil {
		t.Fatal("the older format was accepted")
	}
	for _, want := range []string{"older format that this version no longer reads"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want %q", err, want)
		}
	}
	if !strings.Contains(clierr.Hint(err), "mv \""+path+"\" \""+path+".old\"") || strings.Contains(err.Error()+clierr.Why(err)+clierr.Hint(err), "OLD-TOKEN") {
		t.Errorf("next = %q", clierr.Hint(err))
	}
}

func TestSaveServerActivateRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	clearEnv(t)
	store, err := Load(path, flagSet(t))
	if err != nil {
		t.Fatal(err)
	}

	if err := store.SaveServer("lab", Server{URL: "https://lab.example/"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveServer("Prod.EU", Server{URL: "https://eu.example", InsecureHTTP: false, CAFile: "ca/Prod.EU.pem", Env: "eu"}); err != nil {
		t.Fatal(err)
	}
	if store.ActiveName() != "Prod.EU" {
		t.Fatalf("a saved server becomes the active one, got %q", store.ActiveName())
	}
	if err := store.Activate("lab"); err != nil {
		t.Fatal(err)
	}
	if err := store.Activate("nope"); err == nil || !strings.Contains(clierr.Why(err), "saved servers: Prod.EU, lab") {
		t.Fatalf("Activate(nope) = %v", err)
	}

	// The file is private, keeps names exactly, and trims the trailing slash.
	reloaded, err := Load(path, flagSet(t))
	if err != nil {
		t.Fatal(err)
	}
	if server, _ := reloaded.Server("lab"); server.URL != "https://lab.example" || reloaded.ActiveName() != "lab" {
		t.Fatalf("reloaded = %+v, active %q", server, reloaded.ActiveName())
	}
	if server, _ := reloaded.Server("Prod.EU"); server.CAFile != "ca/Prod.EU.pem" || server.Env != "eu" {
		t.Fatalf("Prod.EU = %+v", server)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, %v", info, err)
		}
		dir, err := os.Stat(filepath.Dir(path))
		if err != nil || dir.Mode().Perm() != 0o700 {
			t.Fatalf("directory mode = %v, %v", dir, err)
		}
	}

	if err := reloaded.RemoveServer("lab"); err != nil {
		t.Fatal(err)
	}
	if reloaded.ActiveName() != "" || len(reloaded.ServerNames()) != 1 {
		t.Fatalf("after removing the active server: %q, %v", reloaded.ActiveName(), reloaded.ServerNames())
	}
	if err := reloaded.RemoveServer("never-saved"); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []struct{ name, url string }{{"", "https://x.example"}, {"has space", "https://x.example"}, {"ok", "not a url"}} {
		if err := store.SaveServer(bad.name, Server{URL: bad.url}); err == nil {
			t.Errorf("SaveServer(%q, %q) was accepted", bad.name, bad.url)
		}
	}
}

func TestSaveWithoutAPath(t *testing.T) {
	store, err := LoadSettings(flagSet(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveServer("lab", Server{URL: "https://x.example"}); err == nil {
		t.Fatal("a store without a file path saved something")
	}
}

func TestNormalizeURLAndDefaultServerName(t *testing.T) {
	for in, want := range map[string]string{
		"machinarchy.example.com":          "https://machinarchy.example.com",
		"https://machinarchy.example.com/": "https://machinarchy.example.com",
		"http://localhost:8080":            "http://localhost:8080",
		"  https://x.example/api/  ":       "https://x.example/api",
	} {
		if got, err := NormalizeURL(in); err != nil || got != want {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://x.example", "https://user:pw@x.example", "https://x.example?q=1", "https://x.example#f"} {
		if _, err := NormalizeURL(bad); err == nil {
			t.Errorf("NormalizeURL(%q) was accepted", bad)
		}
	}

	for in, want := range map[string]string{
		"https://machinarchy.example.com": "machinarchy-example-com",
		"http://localhost:8080":           "localhost-8080",
		"http://127.0.0.1:52566":          "127-0-0-1-52566",
		"https://[::1]:8443":              "1-8443",
		"nonsense":                        "server",
	} {
		if got := DefaultServerName(in); got != want {
			t.Errorf("DefaultServerName(%q) = %q, want %q", in, got, want)
		}
		if got := DefaultServerName(in); !serverNamePattern.MatchString(got) {
			t.Errorf("DefaultServerName(%q) = %q is not a valid server name", in, got)
		}
	}
	if long := DefaultServerName("https://" + strings.Repeat("a", 100) + ".example"); len(long) > 63 {
		t.Errorf("a long name was not cut: %d", len(long))
	}
}

func TestCheckTransport(t *testing.T) {
	for _, ok := range []string{"https://machinarchy.example", "http://localhost:8080", "http://127.0.0.1:1", "http://[::1]:1", "http://app.localhost"} {
		if err := CheckTransport(ok, false); err != nil {
			t.Errorf("CheckTransport(%q) = %v", ok, err)
		}
	}
	err := CheckTransport("http://machinarchy.example", false)
	if err == nil || !strings.Contains(err.Error(), "refusing to send your credentials over plain http:// to machinarchy.example") ||
		!strings.Contains(clierr.Hint(err), "machinarchy login http://machinarchy.example --insecure-http") {
		t.Fatalf("err = %v / %q", err, clierr.Hint(err))
	}
	if err := CheckTransport("http://machinarchy.example", true); err != nil {
		t.Fatalf("an explicit opt-in must be honoured: %v", err)
	}
}

func TestValidateToken(t *testing.T) {
	if err := ValidateToken("mch_pat_abc123"); err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"empty": "", "space": "a b", "newline": "a\nb", "tab": "a\tb", "DEL": "a\x7fb", "oversized": strings.Repeat("x", MaxTokenBytes+1)} {
		err := ValidateToken(token)
		if err == nil {
			t.Errorf("%s: the token was accepted", name)
		} else if strings.Contains(err.Error(), "a b") {
			t.Errorf("%s: the error echoes the token", name)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{"localhost": true, "LOCALHOST.": true, "app.localhost": true, "127.0.0.1": true, "127.9.9.9": true, "::1": true,
		"10.0.0.1": false, "example.com": false, "localhost.example.com": false, "": false} {
		if got := IsLoopbackHost(host); got != want {
			t.Errorf("IsLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestRequestTimeoutAndOutputSettings(t *testing.T) {
	store := load(t, "")
	if timeout, err := store.RequestTimeout(); err != nil || timeout != DefaultRequestTimeout {
		t.Fatalf("default timeout = %v, %v", timeout, err)
	}
	if store.OutputExplicit() != "" || store.OutputFormat() != "table" {
		t.Fatalf("output = %q / %q", store.OutputExplicit(), store.OutputFormat())
	}

	flagged := load(t, "", "-o", " JSON ", "--request-timeout", "7s", "--quiet", "--no-headers", "--no-input", "--verbose")
	if timeout, _ := flagged.RequestTimeout(); timeout.Seconds() != 7 || flagged.OutputExplicit() != "json" || flagged.OutputFormat() != "json" ||
		!flagged.Quiet() || !flagged.NoHeaders() || !flagged.NoInput() || !flagged.Verbose() {
		t.Fatalf("flags were not honoured: %+v", flagged)
	}

	for _, bad := range []string{"banana", "-5s", "0s"} {
		if _, err := load(t, "", "--request-timeout", bad).RequestTimeout(); err == nil {
			t.Errorf("timeout %q was accepted", bad)
		}
	}

	var nilStore *Store
	if timeout, err := nilStore.RequestTimeout(); err != nil || timeout != DefaultRequestTimeout || nilStore.Quiet() || nilStore.OutputExplicit() != "" || nilStore.Path() != "" || nilStore.ActiveName() != "" {
		t.Fatal("a nil store must read as defaults")
	}
}

func TestLoadSettingsIgnoresADamagedFile(t *testing.T) {
	clearEnv(t)
	t.Setenv("MACHINARCHY_OUTPUT", "yaml")

	store, err := LoadSettings(flagSet(t))
	if err != nil {
		t.Fatal(err)
	}
	if store.OutputExplicit() != "yaml" || store.Path() != "" {
		t.Fatalf("output = %q, path = %q", store.OutputExplicit(), store.Path())
	}
}

func TestPaths(t *testing.T) {
	t.Setenv("MACHINARCHY_CONFIG", "")
	if got, err := ResolvePath("  /tmp/explicit.yaml "); err != nil || got != "/tmp/explicit.yaml" {
		t.Fatalf("explicit = %q, %v", got, err)
	}
	t.Setenv("MACHINARCHY_CONFIG", "/tmp/from-env.yaml")
	if got, err := ResolvePath(""); err != nil || got != "/tmp/from-env.yaml" {
		t.Fatalf("env = %q, %v", got, err)
	}
	if got, _ := ResolvePath("/tmp/flag.yaml"); got != "/tmp/flag.yaml" {
		t.Fatalf("a flag beats the variable: %q", got)
	}

	for in, want := range map[string]string{
		"/home/u/.machinarchy/config.yaml": "/home/u/.machinarchy/config.credentials.yaml",
		"/tmp/cli.yml":                     "/tmp/cli.credentials.yml",
		"/tmp/noext":                       "/tmp/noext.credentials",
	} {
		if got := CredentialsPath(in); got != want {
			t.Errorf("CredentialsPath(%q) = %q, want %q", in, got, want)
		}
	}
}
