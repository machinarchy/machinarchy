package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// preflightChecks run before a pending migration is applied. They exist for
// migrations whose SQL would fail on existing data with an unhelpful message:
// the check reports exactly which rows conflict and leaves the database
// untouched so an operator can resolve them deliberately.
var preflightChecks = map[int]func(ctx context.Context, db *sql.DB, engine string) error{
	4: checkHostUniqueness,
}

const maxReportedConflicts = 20

// checkHostUniqueness verifies that no workspace already contains two hosts with
// the same name (ignoring case) or the same address and port, which migration 4
// turns into unique indexes.
func checkHostUniqueness(ctx context.Context, db *sql.DB, engine string) error {
	var nameQuery, addressQuery string
	switch engine {
	case "postgres":
		nameQuery = `SELECT workspace_id, lower(name), COUNT(*), string_agg(id, ', ' ORDER BY id)
			FROM hosts GROUP BY workspace_id, lower(name) HAVING COUNT(*) > 1
			ORDER BY workspace_id, lower(name) LIMIT $1`
		addressQuery = `SELECT workspace_id, lower(ip_address) || ':' || port::TEXT, COUNT(*), string_agg(id, ', ' ORDER BY id)
			FROM hosts GROUP BY workspace_id, lower(ip_address), port HAVING COUNT(*) > 1
			ORDER BY workspace_id, lower(ip_address), port LIMIT $1`
	default:
		nameQuery = `SELECT workspace_id, lower(name), COUNT(*), group_concat(COALESCE(id, rowid), ', ')
			FROM hosts GROUP BY workspace_id, lower(name) HAVING COUNT(*) > 1
			ORDER BY workspace_id, lower(name) LIMIT ?`
		addressQuery = `SELECT workspace_id, lower(ip_address) || ':' || port, COUNT(*), group_concat(COALESCE(id, rowid), ', ')
			FROM hosts GROUP BY workspace_id, lower(ip_address), port HAVING COUNT(*) > 1
			ORDER BY workspace_id, lower(ip_address), port LIMIT ?`
	}

	var conflicts []string
	for _, spec := range []struct{ label, query string }{{"name", nameQuery}, {"address", addressQuery}} {
		found, err := hostConflicts(ctx, db, spec.label, spec.query)
		if err != nil {
			return fmt.Errorf("check existing hosts for duplicate %ss: %w", spec.label, err)
		}
		conflicts = append(conflicts, found...)
	}
	if len(conflicts) == 0 {
		return nil
	}
	return fmt.Errorf("migration 4 cannot add host uniqueness constraints because existing hosts conflict. "+
		"Rename or delete the duplicate hosts (a host name must be unique per workspace ignoring case, "+
		"and so must an address:port pair), then restart. Nothing has been changed. Conflicts (up to %d per kind):\n  %s",
		maxReportedConflicts, strings.Join(conflicts, "\n  "))
}

func hostConflicts(ctx context.Context, db *sql.DB, label, query string) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, maxReportedConflicts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var conflicts []string
	for rows.Next() {
		var workspaceID, count int
		var key, ids string
		if err := rows.Scan(&workspaceID, &key, &count, &ids); err != nil {
			return nil, err
		}
		conflicts = append(conflicts, fmt.Sprintf("workspace %d: %s %q is used by %d hosts (IDs: %s)",
			workspaceID, label, key, count, ids))
	}
	return conflicts, rows.Err()
}
