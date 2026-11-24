package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/store/resources"
)

// AUDIT Q-08: the periodic resync pages through every Host by (workspace,
// ID). Its query must walk an index in that order rather than sort the table
// for every page. The plan is checked with the planner told to avoid sorts
// and full scans, so a missing index shows even on a small table.
func testHostKeysPlan(t *testing.T, f *fixture) {
	ctx := context.Background()
	ws := f.newWorkspace(t, "keyset")
	for i := range 20 {
		host, err := resource.NewHost(ws, 0, fmt.Sprintf("k-%02d", i), nil, resource.HostSpec{}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := f.stores.ResourceHosts.CreateHost(ctx, host); err != nil {
			t.Fatal(err)
		}
	}
	db := f.raw(t)
	query := resources.HostKeysQuery
	var plan []string
	if f.engine == "postgres" {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		for _, setting := range []string{"SET LOCAL enable_seqscan = off", "SET LOCAL enable_sort = off", "SET LOCAL enable_bitmapscan = off"} {
			if _, err := tx.ExecContext(ctx, setting); err != nil {
				t.Fatal(err)
			}
		}
		n := 0
		query = bindDollar(resources.HostKeysQuery, &n)
		rows, err := tx.QueryContext(ctx, "EXPLAIN "+query, ws, "", 50)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, line)
		}
		text := strings.Join(plan, "\n")
		if strings.Contains(text, "Sort") || strings.Contains(text, "Seq Scan") {
			t.Fatalf("the resync keyset sorts or scans the table on PostgreSQL:\n%s", text)
		}
		t.Logf("PostgreSQL plan:\n%s", text)
		return
	}
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, ws, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	text := strings.Join(plan, "\n")
	if strings.Contains(text, "TEMP B-TREE") || !strings.Contains(text, "INDEX") {
		t.Fatalf("the resync keyset sorts the table on SQLite:\n%s", text)
	}
	t.Logf("SQLite plan:\n%s", text)
}

func bindDollar(query string, n *int) string {
	var b strings.Builder
	for _, r := range query {
		if r == '?' {
			*n++
			fmt.Fprintf(&b, "$%d", *n)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
