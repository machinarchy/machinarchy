package resource

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

func targetHost(t *testing.T, name string, environment int, labels map[string]string) Host {
	t.Helper()
	host, err := NewHost(1, environment, name, labels, HostSpec{AccessTarget: HostAccessTarget{Endpoint: name + ":22"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return *host
}

func inventory(t *testing.T) []Host {
	return []Host{
		targetHost(t, "web-01", 1, map[string]string{"role": "web"}),
		targetHost(t, "web-02", 1, map[string]string{"role": "web", "canary": "true"}),
		targetHost(t, "DB-01", 1, map[string]string{"role": "db"}),
		targetHost(t, "cache-01", 0, map[string]string{"role": "cache"}),
	}
}

func names(hosts []Host) string {
	out := make([]string, len(hosts))
	for i, host := range hosts {
		out[i] = host.Metadata.Name
	}
	return strings.Join(out, ",")
}

// TestResolveHostTargets ports the legacy target-rule semantics to managed
// Hosts: selector, include and exclude by ID or name, all, built-in fields.
func TestResolveHostTargets(t *testing.T) {
	hosts := inventory(t)
	environments := map[int]string{1: "production"}
	tests := []struct {
		name     string
		rule     domain.TargetRule
		matched  string
		excluded string
	}{
		{"selector", domain.TargetRule{Selector: "role=web"}, "web-01,web-02", ""},
		{"exclude by name ignores case", domain.TargetRule{Selector: "role=web", Exclude: []string{"WEB-02"}}, "web-01", "web-02"},
		{"label absence", domain.TargetRule{Selector: "role=web,!canary"}, "web-01", ""},
		{"include adds", domain.TargetRule{Selector: "role=web", Include: []string{"db-01"}}, "DB-01,web-01,web-02", ""},
		{"include by ID", domain.TargetRule{Include: []string{string(hosts[3].Metadata.ID)}}, "cache-01", ""},
		{"exclude beats include", domain.TargetRule{Include: []string{"db-01", "web-01"}, Exclude: []string{"db-01"}}, "web-01", "DB-01"},
		{"all", domain.TargetRule{All: true}, "cache-01,DB-01,web-01,web-02", ""},
		{"environment field", domain.TargetRule{Selector: "environment=PRODUCTION,role in (db,cache)"}, "DB-01", ""},
		{"no environment", domain.TargetRule{Selector: "!environment"}, "cache-01", ""},
		{"name glob ignores case", domain.TargetRule{Selector: "name=db-*"}, "DB-01", ""},
		{"label values are case-sensitive", domain.TargetRule{Selector: "role=WEB"}, "", ""},
		{"phase field", domain.TargetRule{Selector: "phase=pending,role=cache"}, "cache-01", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			set, err := ResolveHostTargets(tc.rule, hosts, environments)
			if err != nil {
				t.Fatal(err)
			}
			if got := names(set.Matched); got != tc.matched {
				t.Errorf("matched = %q, want %q", got, tc.matched)
			}
			if got := names(set.Excluded); got != tc.excluded {
				t.Errorf("excluded = %q, want %q", got, tc.excluded)
			}
		})
	}
}

func TestResolveHostTargetsReportsUnknownEntriesAndRejectsEmptyRules(t *testing.T) {
	set, err := ResolveHostTargets(domain.TargetRule{Selector: "role=web", Include: []string{"ghost"}, Exclude: []string{"ghost-2"}}, inventory(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(set.UnknownIncludes, ",") != "ghost" || strings.Join(set.UnknownExcludes, ",") != "ghost-2" {
		t.Fatalf("unknown = %v / %v", set.UnknownIncludes, set.UnknownExcludes)
	}
	for _, rule := range []domain.TargetRule{{}, {Exclude: []string{"web-01"}}, {All: true, Selector: "a"}} {
		if _, err := ResolveHostTargets(rule, inventory(t), nil); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("rule %+v: err = %v, want ErrInvalidInput", rule, err)
		}
	}
}

func TestHostTargetsHashTracksIDsAndGenerations(t *testing.T) {
	hosts := inventory(t)
	reversed := []Host{hosts[3], hosts[2], hosts[1], hosts[0]}
	if HostTargetsHash(hosts) != HostTargetsHash(reversed) {
		t.Fatal("hash must not depend on order")
	}
	bumped := append([]Host(nil), hosts...)
	bumped[0].Metadata.Generation++
	if HostTargetsHash(hosts) == HostTargetsHash(bumped) {
		t.Fatal("hash must change when a host's generation changes")
	}
}
