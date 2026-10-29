package domain

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func testFleetHost(id int, name string, status HostStatus, labels map[string]string) *Host {
	return &Host{
		ID: strconv.Itoa(id), Name: name, Hostname: name + ".example.test", IPAddress: "10.0.0." + strconv.Itoa(id),
		Connection: MethodSSH, Labels: labels, EnvironmentID: 1,
		SSH: &SSHAccess{Status: status},
	}
}

func TestParseSelectorAcceptsDocumentedForms(t *testing.T) {
	for _, expr := range []string{
		"role=web",
		"role==web",
		"role!=web",
		"role in (web,db)",
		"role in ( web , db )",
		"role notin (batch)",
		"canary",
		"!canary",
		"environment=production,role in (web,db),!canary,tier!=legacy",
		"name=web-*",
		"address=10.0.?.*",
		"owner=",
		"  role = web  ,  tier = 1  ",
		"team/squad=blue",
	} {
		if _, err := ParseSelector(expr); err != nil {
			t.Errorf("ParseSelector(%q) error = %v", expr, err)
		}
	}
}

func TestParseSelectorRejectsMalformedInput(t *testing.T) {
	tooLong := strings.Repeat("a", MaxSelectorLength+1)
	manyReqs := strings.TrimSuffix(strings.Repeat("a,", MaxSelectorRequirements+1), ",")
	manyValues := "k in (" + strings.TrimSuffix(strings.Repeat("v,", MaxSelectorValues+1), ",") + ")"
	for name, expr := range map[string]string{
		"empty":                 "",
		"blank":                 "   ",
		"trailing comma":        "role=web,",
		"leading comma":         ",role=web",
		"doubled comma":         "a=b,,c=d",
		"unbalanced open":       "role in (web",
		"unbalanced close":      "role in web)",
		"nested parentheses":    "role in ((web))",
		"missing set keyword":   "role (web)",
		"unknown set keyword":   "role within (web)",
		"empty set member":      "role in (web,,db)",
		"empty set":             "role in ()",
		"double equals value":   "a==b==c",
		"value with paren":      "a=b(c",
		"key with space":        "my key=v",
		"bare phrase":           "this is not valid",
		"bad key characters":    "ro$le=web",
		"bad value characters":  "role=we$b",
		"regex-looking value":   "name=web-[0-9]+",
		"key starts with dash":  "-role=web",
		"bang without key":      "!",
		"too long":              tooLong,
		"too many requirements": manyReqs,
		"too many set values":   manyValues,
	} {
		_, err := ParseSelector(expr)
		if !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: ParseSelector(%q) error = %v, want ErrInvalidInput", name, truncateForError(expr), err)
		}
	}
}

func TestSelectorSemantics(t *testing.T) {
	web := testFleetHost(1, "web-01", HostStatusActive, map[string]string{"role": "web", "tier": "1", "canary": ""})
	db := testFleetHost(2, "DB-01", HostStatusPending, map[string]string{"role": "db", "tier": "legacy"})
	bare := testFleetHost(3, "bare-01", HostStatusActive, nil)
	bare.EnvironmentID = 0

	tests := []struct {
		expr string
		want map[*Host]bool
	}{
		{"role=web", map[*Host]bool{web: true}},
		{"role==db", map[*Host]bool{db: true}},
		{"role!=web", map[*Host]bool{db: true, bare: true}}, // != includes hosts without the label
		{"role in (web,db)", map[*Host]bool{web: true, db: true}},
		{"role notin (web)", map[*Host]bool{db: true, bare: true}},
		{"canary", map[*Host]bool{web: true}}, // present even with an empty value
		{"!canary", map[*Host]bool{db: true, bare: true}},
		{"canary=", map[*Host]bool{web: true}},
		{"role=web,tier=1", map[*Host]bool{web: true}},
		{"role=web,tier=2", map[*Host]bool{}},
		{"role in (web,db),tier!=legacy", map[*Host]bool{web: true}},
		{"name=web-*", map[*Host]bool{web: true}},
		{"name=db-01", map[*Host]bool{db: true}}, // built-ins ignore case
		{"name=DB-*", map[*Host]bool{db: true}},
		{"name=*-01", map[*Host]bool{web: true, db: true, bare: true}},
		{"name=?eb-01", map[*Host]bool{web: true}},
		{"environment=production", map[*Host]bool{web: true, db: true}},
		{"environment=PRODUCTION", map[*Host]bool{web: true, db: true}},
		{"!environment", map[*Host]bool{bare: true}},
		{"status=active", map[*Host]bool{web: true, bare: true}},
		{"status!=active", map[*Host]bool{db: true}},
		{"address=10.0.0.2", map[*Host]bool{db: true}},
		{"address=10.0.0.*", map[*Host]bool{web: true, db: true, bare: true}},
		{"role=WEB", map[*Host]bool{}}, // label values are case-sensitive
		{"ROLE=web", map[*Host]bool{}}, // so are label keys
		{"id=3", map[*Host]bool{bare: true}},
	}
	environments := map[int]string{1: "production"}
	for _, tc := range tests {
		selector, err := ParseSelector(tc.expr)
		if err != nil {
			t.Errorf("ParseSelector(%q) error = %v", tc.expr, err)
			continue
		}
		for _, host := range []*Host{web, db, bare} {
			if got := selector.Matches(host, environments[host.EnvironmentID]); got != tc.want[host] {
				t.Errorf("%q matches %s = %v, want %v", tc.expr, host.Name, got, tc.want[host])
			}
		}
	}
}

func TestNilSelectorAndNilHostMatchNothing(t *testing.T) {
	var none *Selector
	if none.Matches(testFleetHost(1, "a", HostStatusActive, nil), "") {
		t.Error("a nil selector must match nothing")
	}
	selector, _ := ParseSelector("a")
	if selector.Matches(nil, "") {
		t.Error("a nil host must not match")
	}
}

func TestGlobMatchIsLinearAndCorrect(t *testing.T) {
	for _, tc := range []struct {
		pattern, value string
		want           bool
	}{
		{"*", "", true}, {"*", "anything", true}, {"", "", true}, {"", "x", false},
		{"a*b", "ab", true}, {"a*b", "axxb", true}, {"a*b", "axxbc", false},
		{"a?c", "abc", true}, {"a?c", "ac", false}, {"*a*b*c*", "xxaxxbxxcxx", true},
		{"**x", "x", true}, {"web-*", "web-", true}, {"web-*", "web", false},
	} {
		if got := globMatch(tc.pattern, tc.value); got != tc.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
	// A hostile pattern against a long value must finish promptly (no exponential backtracking).
	if globMatch(strings.Repeat("a*", 100)+"b", strings.Repeat("a", 5000)) {
		t.Error("pathological pattern should not match")
	}
}

func fleetInventory() []*Host {
	return []*Host{
		testFleetHost(1, "web-01", HostStatusActive, map[string]string{"role": "web"}),
		testFleetHost(2, "web-02", HostStatusActive, map[string]string{"role": "web", "canary": "true"}),
		testFleetHost(3, "db-01", HostStatusActive, map[string]string{"role": "db"}),
		testFleetHost(10, "cache-01", HostStatusActive, map[string]string{"role": "cache"}),
	}
}

func hostNames(hosts []*Host) []string {
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = h.Name
	}
	return out
}

func TestResolveTargetsCombinesSelectorIncludeAndExclude(t *testing.T) {
	envs := map[int]string{1: "production"}
	tests := []struct {
		name     string
		rule     TargetRule
		matched  string
		excluded string
	}{
		{"selector only", TargetRule{Selector: "role=web"}, "web-01,web-02", ""},
		{"selector minus exclude by name", TargetRule{Selector: "role=web", Exclude: []string{"WEB-02"}}, "web-01", "web-02"},
		{"selector minus label-style exclusion", TargetRule{Selector: "role=web,!canary"}, "web-01", ""},
		{"include adds regardless of selector", TargetRule{Selector: "role=web", Include: []string{"db-01"}}, "db-01,web-01,web-02", ""},
		{"include by ID", TargetRule{Include: []string{"10"}}, "cache-01", ""},
		{"exclude beats include", TargetRule{Include: []string{"db-01", "web-01"}, Exclude: []string{"db-01"}}, "web-01", "db-01"},
		{"exclude beats selector and include", TargetRule{Selector: "role=db", Include: []string{"db-01"}, Exclude: []string{"1", "db-01"}}, "", "db-01"},
		{"all", TargetRule{All: true}, "cache-01,db-01,web-01,web-02", ""},
		{"all minus exclude", TargetRule{All: true, Exclude: []string{"cache-01"}}, "db-01,web-01,web-02", "cache-01"},
		{"environment built-in", TargetRule{Selector: "environment=production,role in (db,cache)"}, "cache-01,db-01", ""},
		{"selector matching nothing", TargetRule{Selector: "role=none"}, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			set, err := ResolveTargets(tc.rule, fleetInventory(), envs)
			if err != nil {
				t.Fatalf("ResolveTargets() error = %v", err)
			}
			if got := strings.Join(hostNames(set.Matched), ","); got != tc.matched {
				t.Errorf("matched = %q, want %q", got, tc.matched)
			}
			if got := strings.Join(hostNames(set.Excluded), ","); got != tc.excluded {
				t.Errorf("excluded = %q, want %q", got, tc.excluded)
			}
		})
	}
}

func TestResolveTargetsReportsUnknownEntries(t *testing.T) {
	set, err := ResolveTargets(TargetRule{
		Selector: "role=web", Include: []string{"ghost-1", " db-01 "}, Exclude: []string{"ghost-2"},
	}, fleetInventory(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.UnknownIncludes) != 1 || set.UnknownIncludes[0] != "ghost-1" ||
		len(set.UnknownExcludes) != 1 || set.UnknownExcludes[0] != "ghost-2" {
		t.Errorf("unknown entries = includes %v, excludes %v", set.UnknownIncludes, set.UnknownExcludes)
	}
	if got := strings.Join(hostNames(set.Matched), ","); got != "db-01,web-01,web-02" {
		t.Errorf("matched = %q; entries are trimmed and unknown ones ignored", got)
	}
}

func TestTargetRuleNeverDefaultsToTheWholeFleet(t *testing.T) {
	for name, rule := range map[string]TargetRule{
		"empty":                  {},
		"exclude only":           {Exclude: []string{"web-01"}},
		"blank selector":         {Selector: "   "},
		"all with selector":      {All: true, Selector: "role=web"},
		"all with include":       {All: true, Include: []string{"web-01"}},
		"bad selector":           {Selector: "role in ("},
		"blank include entry":    {Include: []string{"  "}},
		"too many includes":      {Include: make([]string, MaxTargetRuleEntries+1)},
		"overlong exclude entry": {Selector: "a", Exclude: []string{strings.Repeat("x", 256)}},
	} {
		if _, err := ResolveTargets(rule, fleetInventory(), nil); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: error = %v, want ErrInvalidInput", name, err)
		}
		if err := rule.Validate(); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: Validate() = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestResolveTargetsIsDeterministic(t *testing.T) {
	first, _ := ResolveTargets(TargetRule{All: true}, fleetInventory(), nil)
	for i := 0; i < 20; i++ {
		again, _ := ResolveTargets(TargetRule{All: true}, fleetInventory(), nil)
		if strings.Join(hostNames(first.Matched), ",") != strings.Join(hostNames(again.Matched), ",") {
			t.Fatal("target order must not depend on map iteration")
		}
	}
}
