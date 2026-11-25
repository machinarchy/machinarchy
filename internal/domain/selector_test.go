package domain

import (
	"errors"
	"strings"
	"testing"
)

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
		if err := rule.Validate(); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: Validate() = %v, want ErrInvalidInput", name, err)
		}
	}
}

// TestMatchesFieldsSemantics covers the operators against a field map; the
// managed-host field set is tested in the resource package.
func TestMatchesFieldsSemantics(t *testing.T) {
	subject := func(labels map[string]string, name string) SelectorField {
		return func(key string) (string, bool, bool) {
			if key == "name" {
				return name, name != "", true
			}
			value, ok := labels[key]
			return value, ok, false
		}
	}
	web := subject(map[string]string{"role": "web", "canary": ""}, "Web-01")
	for expr, want := range map[string]bool{
		"role=web": true, "role!=db": true, "role in (db,web)": true, "role notin (web)": false,
		"canary": true, "!canary": false, "name=web-*": true, "role=WEB": false, "missing!=x": true,
	} {
		selector, err := ParseSelector(expr)
		if err != nil {
			t.Fatalf("ParseSelector(%q): %v", expr, err)
		}
		if got := selector.MatchesFields(web); got != want {
			t.Errorf("%q = %v, want %v", expr, got, want)
		}
	}
	var none *Selector
	if none.MatchesFields(web) {
		t.Error("a nil selector must match nothing")
	}
}
