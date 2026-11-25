package config

import (
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/pflag"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

// Global options resolve with one precedence everywhere
// (docs/cli/spec/02-commands.md section 2.1):
//
//	flag given on the command line
//	> environment variable MACHINARCHY_<NAME>
//	> the old environment variable MACHINARCHY_CLI_<NAME> (deprecated, warned)
//	> saved value in the config file ("defaults:")
//	> the flag's own default
//
// An environment variable that is set but empty counts as not set, so
// "MACHINARCHY_OUTPUT= machinarchy ..." falls through to the next layer.
//
// A few options are flag-only: --yes, --confirm, --confirm-env and --dry-run
// can never come from the environment or the config file, because each is a
// deliberate answer to one invocation and a standing "yes" would defeat the
// confirmation it answers.

const (
	envPrefix       = "MACHINARCHY"
	legacyEnvPrefix = "MACHINARCHY_CLI"
)

// flagOnly lists the options that only a flag on the command line can set.
var flagOnly = map[string]bool{"yes": true, "confirm": true, "confirm-env": true, "dry-run": true}

// savedDefaults lists the options that may be saved under "defaults:" in the
// config file, and whether each is a boolean. Server, env and credentials are
// not here: they belong to saved servers, not to global defaults.
var savedDefaults = map[string]bool{ // value: is boolean
	"output": false, "request-timeout": false,
	"plain": true, "quiet": true, "verbose": true, "no-headers": true, "no-input": true,
}

// envKeys are the options that have an environment variable.
var envKeys = []string{
	"server", "env", "output", "token", "request-timeout",
	"verbose", "quiet", "no-headers", "no-input", "plain",
}

// Source says where a setting's value came from.
type Source int

// Sources, from strongest to weakest.
const (
	SourceFlag Source = iota
	SourceEnv
	SourceLegacyEnv
	SourceConfig
	SourceDefault
)

// String names the source for `config show`.
func (s Source) String() string {
	switch s {
	case SourceFlag:
		return "flag"
	case SourceEnv:
		return "environment"
	case SourceLegacyEnv:
		return "environment (deprecated name)"
	case SourceConfig:
		return "config file"
	case SourceDefault:
		return "default"
	}

	return "unknown"
}

// LegacyEnv is a deprecated environment variable that was used because its
// replacement was not set.
type LegacyEnv struct {
	Old, New string
}

// settings resolves a global setting. It replaces viper, which the CLI used
// only for this lookup (AUDIT P-02); the config file's contexts are read
// separately by readDocument.
type settings struct {
	flags    *pflag.FlagSet
	defaults map[string]string
	lookup   func(string) (string, bool)
}

func newSettings(flags *pflag.FlagSet, defaults map[string]string) *settings {
	return &settings{flags: flags, defaults: defaults, lookup: os.LookupEnv}
}

// envNames returns the variable that sets key and the deprecated one it
// replaces. The context option predates the new names and keeps only the old
// one until servers replace contexts.
func envNames(key string) (current, legacy string) {
	if flagOnly[key] {
		return "", ""
	}

	suffix := strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(key))
	if key == "context" {
		return "", legacyEnvPrefix + "_" + suffix
	}

	return envPrefix + "_" + suffix, legacyEnvPrefix + "_" + suffix
}

// resolve returns the value of key and where it came from.
func (s *settings) resolve(key string) (string, Source) {
	var flag *pflag.Flag
	if s != nil && s.flags != nil {
		flag = s.flags.Lookup(key)
	}
	if flag != nil && flag.Changed {
		return flag.Value.String(), SourceFlag
	}
	if s == nil {
		return "", SourceDefault
	}

	if current, legacy := envNames(key); current != "" || legacy != "" {
		if current != "" {
			if value, ok := s.lookup(current); ok && value != "" {
				return value, SourceEnv
			}
		}
		if legacy != "" {
			if value, ok := s.lookup(legacy); ok && value != "" {
				return value, SourceLegacyEnv
			}
		}
	}
	if !flagOnly[key] {
		if value, ok := s.defaults[key]; ok {
			return value, SourceConfig
		}
	}
	if flag != nil {
		return flag.DefValue, SourceDefault
	}

	return "", SourceDefault
}

// GetString returns the value of key.
func (s *settings) GetString(key string) string {
	value, _ := s.resolve(key)

	return value
}

// GetBool returns key as a boolean. A value that is not a boolean counts as
// false; saved defaults are checked when the config file is read.
func (s *settings) GetBool(key string) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(s.GetString(key)))

	return err == nil && value
}

// legacyUsed lists the deprecated variables that are in effect: set while the
// new name is not.
func (s *settings) legacyUsed() []LegacyEnv {
	if s == nil {
		return nil
	}

	var used []LegacyEnv
	for _, key := range envKeys {
		current, legacy := envNames(key)
		if value, newSet := s.lookup(current); newSet && value != "" {
			continue
		}
		if value, oldSet := s.lookup(legacy); oldSet && value != "" {
			used = append(used, LegacyEnv{Old: legacy, New: current})
		}
	}

	return used
}

// validateDefaults checks the "defaults:" section of the config file.
func validateDefaults(defaults map[string]string) error {
	keys := make([]string, 0, len(defaults))
	for key := range defaults {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		isBool, allowed := savedDefaults[key]
		if !allowed {
			return clierr.Newf(clierr.ExitUsage,
				"Remove it from the config file, or give it as a flag or an environment variable. "+
					"Saved defaults can set: "+strings.Join(savedDefaultNames(), ", ")+".",
				"defaults.%s cannot be saved in the CLI config", key)
		}
		if isBool {
			if _, err := strconv.ParseBool(strings.TrimSpace(defaults[key])); err != nil {
				return clierr.Newf(clierr.ExitUsage, "Use true or false.",
					"defaults.%s must be true or false, not %q", key, defaults[key])
			}
		}
	}

	return nil
}

func savedDefaultNames() []string {
	names := make([]string, 0, len(savedDefaults))
	for name := range savedDefaults {
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}

// Plain reports whether plain output was asked for: --plain,
// MACHINARCHY_PLAIN or a saved default.
func (s *Store) Plain() bool {
	return s != nil && s.v != nil && s.v.GetBool("plain")
}

// DryRun reports whether --dry-run was given. It can only come from the flag.
func (s *Store) DryRun() bool {
	return s != nil && s.v != nil && s.v.GetBool("dry-run")
}

// ConfirmName returns the value of --confirm, the non-interactive answer to a
// type-the-name confirmation. It can only come from the flag.
func (s *Store) ConfirmName() string {
	if s == nil || s.v == nil {
		return ""
	}

	return strings.TrimSpace(s.v.GetString("confirm"))
}

// ConfirmEnv returns the value of --confirm-env, the non-interactive answer to
// a protected-environment confirmation. It can only come from the flag.
func (s *Store) ConfirmEnv() string {
	if s == nil || s.v == nil {
		return ""
	}

	return strings.TrimSpace(s.v.GetString("confirm-env"))
}

// LegacyEnv returns the deprecated environment variables in effect.
func (s *Store) LegacyEnv() []LegacyEnv {
	if s == nil {
		return nil
	}

	return s.v.legacyUsed()
}

// Source reports where the effective value of the option key came from.
func (s *Store) Source(key string) Source {
	if s == nil || s.v == nil {
		return SourceDefault
	}
	_, source := s.v.resolve(key)

	return source
}
