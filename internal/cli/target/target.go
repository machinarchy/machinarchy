// Package target turns the --on and --except values of `machinarchy run` into
// the controller's target rule (docs/cli/spec/02-commands.md section 2.4).
//
// A single --on value is either host names (web-01,web-*) or tag selector
// terms (tier=web,region in (eu,us)), or the word all; mixing them is a usage
// error. Names with * or ? are expanded by the CLI over the hosts in scope, so
// the preview shows, and its hash binds, the hosts that were actually chosen.
// The active env is added to a selector as environment=ENV; for names it is
// checked against the hosts of that env, so an env can never be left by typing
// a name.
package target

import (
	"fmt"
	"strings"

	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

// AllWord is the --on value that selects every host in scope.
const AllWord = "all"

// AnyEnv is the --env value that means "no env scope".
const AnyEnv = "any"

// Plan is a parsed --on and --except.
type Plan struct {
	// Names are the host names and globs of --on.
	Names []string
	// Terms are the tag selector terms of --on.
	Terms []string
	// All is true for --on all.
	All bool
	// Except are the host names and globs of --except.
	Except []string
	// Env is the active env name; empty when there is no scope.
	Env string
}

// Parse reads --on, --except and the active env. env may be empty or "any".
func Parse(on, except, env string) (Plan, error) {
	plan := Plan{}
	if env = strings.TrimSpace(env); env != "" && !strings.EqualFold(env, AnyEnv) {
		if !safeEnv(env) {
			return plan, clierr.Usagef("the env name %q cannot be used to select hosts; use an env with letters, digits, '.', '_' or '-'", env)
		}
		plan.Env = env
	}

	terms, err := split("--on", on)
	if err != nil {
		return plan, err
	}
	if len(terms) == 1 && terms[0] == AllWord {
		plan.All = true
	} else {
		var names, selectors []string
		for _, term := range terms {
			switch {
			case term == AllWord:
				return plan, clierr.Newf(clierr.ExitUsage, "to run on every host use --on all alone, for example: machinarchy run --on all -- uptime",
					"--on %s cannot be combined with other targets", AllWord)
			case isSelector(term):
				selectors = append(selectors, term)
			case strings.ContainsAny(term, " \t"):
				return plan, clierr.Usagef("%q is not a host name (names have no spaces) and not a tag selector (for example tier=web)", term)
			default:
				names = append(names, term)
			}
		}
		if len(names) > 0 && len(selectors) > 0 {
			return plan, clierr.Newf(clierr.ExitUsage, "use host names (web-01,web-02) or tags (tier=web), not both; remove hosts with --except",
				"--on mixes host names (%s) and tag selectors (%s)", names[0], selectors[0])
		}
		plan.Names, plan.Terms = names, selectors
	}

	if except = strings.TrimSpace(except); except != "" {
		excluded, err := split("--except", except)
		if err != nil {
			return plan, err
		}
		for _, term := range excluded {
			if isSelector(term) || strings.ContainsAny(term, " \t") || term == AllWord {
				return plan, clierr.Usagef("--except takes host names, such as web-02,web-03 (got %q)", term)
			}
		}
		plan.Except = excluded
	}

	if err := plan.checkEnvConflict(); err != nil {
		return plan, err
	}

	return plan, nil
}

// NeedsHosts reports whether Rule needs the names of the hosts in scope: the
// plan has a glob, or an env scope that names must be checked against.
func (p Plan) NeedsHosts() bool {
	if hasGlob(p.Names) || hasGlob(p.Except) {
		return true
	}

	return p.Env != "" && (len(p.Names) > 0 || len(p.Except) > 0)
}

// Rule builds the controller's target rule. hosts are the names of the hosts
// in scope (the env's hosts when an env is active); it is only read when
// NeedsHosts is true.
func (p Plan) Rule(hosts []string) (api.TargetRule, error) {
	var rule api.TargetRule

	switch {
	case p.All && p.Env == "":
		rule.All = true
	case p.All:
		rule.Selector = "environment=" + p.Env
	case len(p.Terms) > 0:
		terms := p.Terms
		if p.Env != "" && !p.namesEnv() {
			terms = append([]string{"environment=" + p.Env}, terms...)
		}
		rule.Selector = strings.Join(terms, ",")
	default:
		include, err := p.expand("--on", p.Names, hosts)
		if err != nil {
			return rule, err
		}
		rule.Include = include
	}

	if len(p.Except) > 0 {
		exclude, err := p.expand("--except", p.Except, hosts)
		if err != nil {
			return rule, err
		}
		rule.Exclude = exclude
	}

	return rule, nil
}

// namesEnv reports whether a selector term already says environment=ENV.
func (p Plan) namesEnv() bool {
	for _, term := range p.Terms {
		if value, ok := environmentTerm(term); ok && strings.EqualFold(value, p.Env) {
			return true
		}
	}

	return false
}

// checkEnvConflict refuses --on environment=X when the active env is not X.
func (p Plan) checkEnvConflict() error {
	if p.Env == "" {
		return nil
	}
	for _, term := range p.Terms {
		if !strings.EqualFold(termKey(term), "environment") {
			continue
		}
		if value, ok := environmentTerm(term); ok && strings.EqualFold(value, p.Env) {
			continue
		}

		return clierr.Newf(clierr.ExitUsage, "use one of them: drop --on's environment term, or run with --env any",
			"--on says %q but the active env is %q", term, p.Env)
	}

	return nil
}

// expand resolves names and globs against the hosts in scope.
func (p Plan) expand(flag string, entries, hosts []string) ([]string, error) {
	var out []string
	seen := make(map[string]bool)
	add := func(name string) {
		if key := strings.ToLower(name); !seen[key] {
			seen[key] = true
			out = append(out, name)
		}
	}

	for _, entry := range entries {
		if strings.ContainsAny(entry, "*?") {
			matched := 0
			for _, host := range hosts {
				if Glob(entry, host) {
					add(host)
					matched++
				}
			}
			if matched == 0 {
				return nil, clierr.Newf(clierr.ExitNotFound, "machinarchy hosts", "%s %q matches no host%s", flag, entry, p.scope())
			}

			continue
		}
		if p.Env != "" && !contains(hosts, entry) {
			return nil, clierr.Newf(clierr.ExitNotFound, "check the name with: machinarchy hosts; to leave this env use --env any",
				"host %q is not in env %q", entry, p.Env)
		}
		add(entry)
	}

	return out, nil
}

func (p Plan) scope() string {
	if p.Env == "" {
		return ""
	}

	return fmt.Sprintf(" in env %q", p.Env)
}

// Glob reports whether name matches pattern, where * matches any run of
// characters and ? one character. Host names ignore case.
func Glob(pattern, name string) bool {
	pattern, name = strings.ToLower(pattern), strings.ToLower(name)
	var p, n, star, mark int
	star = -1
	for n < len(name) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == name[n]):
			p++
			n++
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, n
			p++
		case star >= 0:
			p = star + 1
			mark++
			n = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}

	return p == len(pattern)
}

func hasGlob(entries []string) bool {
	for _, entry := range entries {
		if strings.ContainsAny(entry, "*?") {
			return true
		}
	}

	return false
}

func contains(hosts []string, name string) bool {
	for _, host := range hosts {
		if strings.EqualFold(host, name) {
			return true
		}
	}

	return false
}

// split cuts a value at the commas that are not inside parentheses.
func split(flag, value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, clierr.Newf(clierr.ExitUsage, "for example: machinarchy run --on tier=web -- uptime", "%s needs a target", flag)
	}
	var terms []string
	depth, start := 0, 0
	add := func(end int) error {
		term := strings.TrimSpace(value[start:end])
		if term == "" {
			return clierr.Usagef("%s has an empty entry; check for a doubled or trailing comma", flag)
		}
		terms = append(terms, term)

		return nil
	}
	for i, r := range value {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, clierr.Usagef("%s has an unexpected ')'", flag)
			}
		case ',':
			if depth == 0 {
				if err := add(i); err != nil {
					return nil, err
				}
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, clierr.Usagef("%s has unbalanced parentheses", flag)
	}
	if err := add(len(value)); err != nil {
		return nil, err
	}

	return terms, nil
}

// isSelector reports whether a term is a tag selector term rather than a host
// name: it has an operator, or is a set test (key in (...), key notin (...)).
func isSelector(term string) bool {
	if strings.ContainsAny(term, "=!()") {
		return true
	}
	words := strings.Fields(term)

	return len(words) >= 2 && (strings.EqualFold(words[1], "in") || strings.EqualFold(words[1], "notin"))
}

// termKey returns the key a selector term tests.
func termKey(term string) string {
	term = strings.TrimLeft(term, "! \t")
	if i := strings.IndexAny(term, " \t=!("); i >= 0 {
		return term[:i]
	}

	return term
}

// environmentTerm reads an environment=VALUE or environment==VALUE term.
func environmentTerm(term string) (value string, ok bool) {
	rest := strings.TrimSpace(term)
	if len(rest) < len("environment") || !strings.EqualFold(rest[:len("environment")], "environment") {
		return "", false
	}
	rest = strings.TrimSpace(rest[len("environment"):])
	switch {
	case strings.HasPrefix(rest, "=="):
		return strings.TrimSpace(rest[2:]), true
	case strings.HasPrefix(rest, "="):
		return strings.TrimSpace(rest[1:]), true
	}

	return "", false
}

func safeEnv(env string) bool {
	for i, r := range env {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case i > 0 && (r == '.' || r == '_' || r == '-'):
		default:
			return false
		}
	}

	return env != ""
}
