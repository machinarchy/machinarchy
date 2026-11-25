package store_test

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

// AUDIT A-12: label selectors run in the database. The
// SQL conditions must select exactly what the Go selector selects, follow
// label changes and deletions, and stay inside the workspace.
func testHostLabelSelectors(t *testing.T, f *fixture) {
	ctx := context.Background()
	ws := f.newWorkspace(t, "labels")
	other := f.newWorkspace(t, "labels-other")
	now := time.Now().UTC()
	create := func(workspace int, name string, labels map[string]string) *resource.Host {
		t.Helper()
		host, err := resource.NewHost(workspace, 0, name, labels, resource.HostSpec{}, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.stores.ResourceHosts.CreateHost(ctx, host); err != nil {
			t.Fatal(err)
		}
		return host
	}
	hosts := map[string]*resource.Host{
		"web-1":  create(ws, "web-1", map[string]string{"tier": "web", "zone": "a", "canary": "yes"}),
		"web-2":  create(ws, "web-2", map[string]string{"tier": "web", "zone": "b"}),
		"db-1":   create(ws, "db-1", map[string]string{"tier": "db", "zone": "a"}),
		"bare":   create(ws, "bare", nil),
		"legacy": create(ws, "legacy", map[string]string{"tier": "legacy"}),
	}
	create(other, "web-other", map[string]string{"tier": "web"}) // never visible from ws

	list := func(filter resource.HostListFilter) []string {
		t.Helper()
		filter.WorkspaceID, filter.Limit = ws, resource.MaxPageSize
		page, err := f.stores.ResourceHosts.ListHosts(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(page))
		for _, host := range page {
			names = append(names, host.Metadata.Name)
		}
		sort.Strings(names)
		return names
	}
	everyHost := list(resource.HostListFilter{})
	for _, expr := range []string{
		"tier=web", "tier in (web,db)", "tier!=web", "tier notin (web,legacy)", "canary", "!canary",
		"tier=web,zone=a", "tier=w*", "tier!=w*", "zone", "!zone,tier", "name=web-1", "tier=web,name=WEB-2", "tier=none",
	} {
		selector, err := domain.ParseSelector(expr)
		if err != nil {
			t.Fatal(err)
		}
		// What the Go selector selects from the whole inventory...
		var want []string
		for _, name := range everyHost {
			if selector.MatchesFields(hosts[name].SelectorField("")) {
				want = append(want, name)
			}
		}
		// ...must survive the database's label conditions.
		candidates := list(resource.HostListFilter{Labels: selector.LabelConstraints(resource.IsHostBuiltinField)})
		var got []string
		for _, name := range candidates {
			if selector.MatchesFields(hosts[name].SelectorField("")) {
				got = append(got, name)
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: database candidates %v select %v, the selector alone %v", expr, candidates, got, want)
		}
	}
	// Exact label conditions need no further filtering.
	if got := list(resource.HostListFilter{Labels: []domain.LabelConstraint{{Key: "tier", Op: domain.LabelNotIn, Values: []string{"web"}}}}); !slices.Equal(got, []string{"bare", "db-1", "legacy"}) {
		t.Errorf("tier notin (web) = %v", got)
	}

	// The index follows label changes and deletions.
	web2 := hosts["web-2"]
	if err := f.stores.ResourceHosts.UpdateHostMetadata(ctx, ws, web2.Metadata.ID, web2.Metadata.Revision, web2.Metadata.Name, 0,
		map[string]string{"tier": "db"}, now); err != nil {
		t.Fatal(err)
	}
	tierWeb := []domain.LabelConstraint{{Key: "tier", Op: domain.LabelIn, Values: []string{"web"}}}
	if got := list(resource.HostListFilter{Labels: tierWeb}); !slices.Equal(got, []string{"web-1"}) {
		t.Errorf("after relabelling web-2: tier=web = %v", got)
	}
	if got := list(resource.HostListFilter{Labels: []domain.LabelConstraint{{Key: "zone", Op: domain.LabelExists}}}); slices.Contains(got, "web-2") {
		t.Errorf("a removed label still matches: %v", got)
	}
	web1 := hosts["web-1"]
	if err := f.stores.ResourceHosts.DeleteHost(ctx, ws, web1.Metadata.ID, web1.Metadata.Revision); err != nil {
		t.Fatal(err)
	}
	if got := list(resource.HostListFilter{Labels: tierWeb}); len(got) != 0 {
		t.Errorf("a deleted host still matches: %v", got)
	}
	var rows int
	if err := f.raw(t).QueryRowContext(ctx, f.pick(`SELECT count(*) FROM managed_host_labels WHERE host_id = ?`, `SELECT count(*) FROM managed_host_labels WHERE host_id = $1`),
		string(web1.Metadata.ID)).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("label rows of a deleted host: %d, %v", rows, err)
	}

	// Lookup finds hosts by ID or by name, ignoring case.
	if got := list(resource.HostListFilter{Lookup: []string{"DB-1", string(hosts["bare"].Metadata.ID), "ghost"}}); !slices.Equal(got, []string{"bare", "db-1"}) {
		t.Errorf("lookup = %v", got)
	}

	// Paging with label conditions keeps going past non-matching IDs.
	for i := range 7 {
		create(ws, fmt.Sprintf("bulk-%d", i), map[string]string{"batch": fmt.Sprint(i % 2)})
	}
	filter := resource.HostListFilter{WorkspaceID: ws, Limit: 2, Labels: []domain.LabelConstraint{{Key: "batch", Op: domain.LabelIn, Values: []string{"1"}}}}
	var paged []string
	for {
		page, err := f.stores.ResourceHosts.ListHosts(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		for _, host := range page {
			paged = append(paged, host.Metadata.Name)
		}
		if len(page) < filter.Limit {
			break
		}
		filter.After = page[len(page)-1].Metadata.ID
	}
	sort.Strings(paged)
	if !slices.Equal(paged, []string{"bulk-1", "bulk-3", "bulk-5"}) {
		t.Errorf("paged batch=1 = %v", paged)
	}
}
