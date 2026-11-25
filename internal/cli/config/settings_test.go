package config

import (
	"testing"

	"github.com/spf13/pflag"
)

func fakeEnv(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]

		return value, ok
	}
}

func newTestSettings(env map[string]string, defaults map[string]string, args ...string) *settings {
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.StringP("output", "o", "", "")
	flags.Bool("plain", false, "")
	flags.Bool("yes", false, "")
	flags.Bool("dry-run", false, "")
	if err := flags.Parse(args); err != nil {
		panic(err)
	}

	s := newSettings(flags, defaults)
	s.lookup = fakeEnv(env)

	return s
}

func TestResolveOrder(t *testing.T) {
	t.Parallel()

	env := map[string]string{"MACHINARCHY_OUTPUT": "json", "MACHINARCHY_CLI_OUTPUT": "yaml"}
	defaults := map[string]string{"output": "table"}

	tests := []struct {
		name       string
		env        map[string]string
		defaults   map[string]string
		args       []string
		want       string
		wantSource Source
	}{
		{"flag wins", env, defaults, []string{"-o", "x"}, "x", SourceFlag},
		{"environment next", env, defaults, nil, "json", SourceEnv},
		{"deprecated name next", map[string]string{"MACHINARCHY_CLI_OUTPUT": "yaml"}, defaults, nil, "yaml", SourceLegacyEnv},
		{"saved default next", nil, defaults, nil, "table", SourceConfig},
		{"flag default last", nil, nil, nil, "", SourceDefault},
		{"empty environment is not set", map[string]string{"MACHINARCHY_OUTPUT": ""}, defaults, nil, "table", SourceConfig},
		{"empty deprecated name is not set", map[string]string{"MACHINARCHY_CLI_OUTPUT": ""}, nil, nil, "", SourceDefault},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, source := newTestSettings(tc.env, tc.defaults, tc.args...).resolve("output")
			if got != tc.want || source != tc.wantSource {
				t.Fatalf("resolve = %q from %s, want %q from %s", got, source, tc.want, tc.wantSource)
			}
		})
	}
}

func TestFlagOnlyKeysIgnoreEnvironmentAndDefaults(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"MACHINARCHY_YES": "1", "MACHINARCHY_CLI_YES": "1", "MACHINARCHY_DRY_RUN": "1", "MACHINARCHY_CLI_DRY_RUN": "1",
	}
	s := newTestSettings(env, map[string]string{"yes": "true", "dry-run": "true"})
	if s.GetBool("yes") || s.GetBool("dry-run") {
		t.Fatal("flag-only options must not come from the environment or the config file")
	}

	s = newTestSettings(nil, nil, "--yes", "--dry-run")
	if !s.GetBool("yes") || !s.GetBool("dry-run") {
		t.Fatal("flag-only options must come from the flag")
	}
}

func TestEnvNames(t *testing.T) {
	t.Parallel()

	tests := []struct{ key, current, legacy string }{
		{"output", "MACHINARCHY_OUTPUT", "MACHINARCHY_CLI_OUTPUT"},
		{"request-timeout", "MACHINARCHY_REQUEST_TIMEOUT", "MACHINARCHY_CLI_REQUEST_TIMEOUT"},
		{"no-input", "MACHINARCHY_NO_INPUT", "MACHINARCHY_CLI_NO_INPUT"},
		{"context", "", "MACHINARCHY_CLI_CONTEXT"},
		{"yes", "", ""},
		{"confirm-env", "", ""},
	}
	for _, tc := range tests {
		if current, legacy := envNames(tc.key); current != tc.current || legacy != tc.legacy {
			t.Errorf("envNames(%q) = %q, %q; want %q, %q", tc.key, current, legacy, tc.current, tc.legacy)
		}
	}
}

func TestLegacyUsed(t *testing.T) {
	t.Parallel()

	s := newTestSettings(map[string]string{
		"MACHINARCHY_CLI_OUTPUT": "json",                               // used: the new name is not set
		"MACHINARCHY_CLI_QUIET":  "true", "MACHINARCHY_QUIET": "false", // not used: the new name wins
		"MACHINARCHY_CLI_ENV":    "", // empty: ignored
		"MACHINARCHY_CLI_SERVER": "https://x",
	}, nil)

	got := s.legacyUsed()
	want := []LegacyEnv{
		{Old: "MACHINARCHY_CLI_SERVER", New: "MACHINARCHY_SERVER"},
		{Old: "MACHINARCHY_CLI_OUTPUT", New: "MACHINARCHY_OUTPUT"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("legacyUsed = %+v, want %+v", got, want)
	}
}

func TestValidateDefaults(t *testing.T) {
	t.Parallel()

	if err := validateDefaults(map[string]string{"output": "json", "plain": "true", "request-timeout": "10s"}); err != nil {
		t.Fatalf("valid defaults rejected: %v", err)
	}
	if err := validateDefaults(nil); err != nil {
		t.Fatalf("no defaults rejected: %v", err)
	}
	for name, defaults := range map[string]map[string]string{
		"not allowed": {"server": "https://x"},
		"flag only":   {"yes": "true"},
		"bad boolean": {"quiet": "sometimes"},
	} {
		if err := validateDefaults(defaults); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestSourceNames(t *testing.T) {
	t.Parallel()

	for source, want := range map[Source]string{
		SourceFlag: "flag", SourceEnv: "environment", SourceLegacyEnv: "environment (deprecated name)",
		SourceConfig: "config file", SourceDefault: "default", Source(99): "unknown",
	} {
		if got := source.String(); got != want {
			t.Errorf("Source(%d) = %q, want %q", source, got, want)
		}
	}
}

func TestNilStoreAccessorsAreSafe(t *testing.T) {
	t.Parallel()

	var store *Store
	if store.Plain() || store.DryRun() || store.ConfirmName() != "" || store.ConfirmEnv() != "" ||
		store.LegacyEnv() != nil || store.Source("output") != SourceDefault {
		t.Fatal("a nil store must read as defaults")
	}
}
