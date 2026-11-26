package target_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/target"
)

var fleet = []string{"web-01", "web-02", "web-10", "db-01"}

func rule(t *testing.T, on, except, env string) api.TargetRule {
	t.Helper()
	plan, err := target.Parse(on, except, env)
	if err != nil {
		t.Fatalf("Parse(%q, %q, %q): %v", on, except, env, err)
	}
	var hosts []string
	if plan.NeedsHosts() {
		hosts = fleet
	}
	got, err := plan.Rule(hosts)
	if err != nil {
		t.Fatalf("Rule(%q, %q, %q): %v", on, except, env, err)
	}

	return got
}

// O-25: every --on form maps to the right rule.
func TestRuleForms(t *testing.T) {
	tests := []struct {
		name, on, except, env string
		want                  api.TargetRule
	}{
		{"names", "web-01,web-02", "", "", api.TargetRule{Include: []string{"web-01", "web-02"}}},
		{"glob expands over the hosts", "web-*", "", "", api.TargetRule{Include: []string{"web-01", "web-02", "web-10"}}},
		{"glob is case-insensitive", "WEB-0?", "", "", api.TargetRule{Include: []string{"web-01", "web-02"}}},
		{"tag", "tier=web", "", "", api.TargetRule{Selector: "tier=web"}},
		{"tags with a set", "tier=web,region in (eu,us)", "", "", api.TargetRule{Selector: "tier=web,region in (eu,us)"}},
		{"all", "all", "", "", api.TargetRule{All: true}},
		{"except", "tier=web", "web-02, web-03", "", api.TargetRule{Selector: "tier=web", Exclude: []string{"web-02", "web-03"}}},
		{"except glob", "all", "web-1*", "", api.TargetRule{All: true, Exclude: []string{"web-10"}}},
		{"not-exists", "!canary", "", "", api.TargetRule{Selector: "!canary"}},
		// O-23: the env is part of the selector.
		{"env joins a selector", "tier=web", "", "production", api.TargetRule{Selector: "environment=production,tier=web"}},
		{"all in an env", "all", "", "production", api.TargetRule{Selector: "environment=production"}},
		{"names in an env", "web-01", "", "production", api.TargetRule{Include: []string{"web-01"}}},
		{"same env named twice", "environment=production,tier=web", "", "production", api.TargetRule{Selector: "environment=production,tier=web"}},
		{"any env is no scope", "tier=web", "", "any", api.TargetRule{Selector: "tier=web"}},
		// O-26: env= is a tag called env, never rewritten.
		{"env= is a tag", "env=production", "", "", api.TargetRule{Selector: "env=production"}},
		{"environment without env", "environment=production", "", "", api.TargetRule{Selector: "environment=production"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := rule(t, test.on, test.except, test.env); !reflect.DeepEqual(got, test.want) {
				t.Errorf("rule = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestParseRefusals(t *testing.T) {
	tests := []struct {
		name, on, except, env, want string
	}{
		{"empty on", "", "", "", "needs a target"},
		{"names mixed with tags", "web-01,tier=web", "", "", "mixes host names"},
		{"all with others", "all,web-01", "", "", "cannot be combined"},
		{"trailing comma", "web-01,", "", "", "empty entry"},
		{"unbalanced", "region in (eu", "", "", "unbalanced"},
		{"selector in except", "all", "tier=web", "", "--except takes host names"},
		{"spaces in a name", "web 01", "", "", "no spaces"},
		// O-24: a different env in --on is a conflict, never picked silently.
		{"conflicting env", "environment=production", "", "staging", `active env is "staging"`},
		{"negated env", "environment!=production", "", "staging", "active env"},
		{"unusable env name", "tier=web", "", "bad env", "cannot be used"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := target.Parse(test.on, test.except, test.env)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want it to contain %q", err, test.want)
			}
			if code := clierr.ExitCode(err); code != clierr.ExitUsage {
				t.Errorf("exit = %d, want usage", code)
			}
		})
	}
}

func TestRuleChecksNamesAgainstTheEnv(t *testing.T) {
	plan, err := target.Parse("web-01,ghost", "", "production")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.NeedsHosts() {
		t.Fatal("names in an env need the env's hosts")
	}
	_, err = plan.Rule([]string{"web-01"})
	if err == nil || !strings.Contains(err.Error(), `"ghost" is not in env "production"`) || clierr.ExitCode(err) != clierr.ExitNotFound {
		t.Fatalf("err = %v", err)
	}

	plan, _ = target.Parse("nothing-*", "", "")
	if _, err := plan.Rule(fleet); err == nil || !strings.Contains(err.Error(), "matches no host") {
		t.Fatalf("empty glob err = %v", err)
	}

	plan, _ = target.Parse("web-01", "", "")
	if plan.NeedsHosts() {
		t.Error("plain names without an env need no host list")
	}
}

func TestGlob(t *testing.T) {
	for _, test := range []struct {
		pattern, name string
		want          bool
	}{
		{"web-*", "web-01", true}, {"web-*", "db-01", false}, {"*-01", "web-01", true}, {"w*b-0?", "web-01", true},
		{"web-?", "web-01", false}, {"*", "", true}, {"a*b*c", "aXbYc", true}, {"a*b*c", "aXbY", false}, {"**x", "x", true},
	} {
		if got := target.Glob(test.pattern, test.name); got != test.want {
			t.Errorf("Glob(%q, %q) = %v", test.pattern, test.name, got)
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, seed := range []string{"web-*", "tier=web,region in (eu,us)", "all", ",", "((", "a b"} {
		f.Add(seed, seed, "production")
	}
	f.Fuzz(func(_ *testing.T, on, except, env string) {
		plan, err := target.Parse(on, except, env)
		if err != nil {
			return
		}
		_, _ = plan.Rule(fleet) // must not panic
	})
}
