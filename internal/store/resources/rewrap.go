package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/machinarchy/machinarchy/internal/store/sqlx"
)

// sealedColumn is one encrypted Operation data column.
type sealedColumn struct {
	name   string // column name in the AAD
	query  string
	update string
	count  string
}

var sealedColumns = []sealedColumn{
	{
		name:   columnSpec,
		query:  `SELECT workspace_id, id, '', spec_encrypted FROM operations WHERE %s LIMIT ?`,
		update: `UPDATE operations SET spec_encrypted = ? WHERE workspace_id = ? AND id = ? AND spec_encrypted = ?`,
		count:  `SELECT COUNT(*) FROM operations WHERE %s`,
	},
	{
		name: columnOutput,
		query: `SELECT o.workspace_id, t.operation_id, t.host_id, t.output FROM operation_targets t
			JOIN operations o ON o.id = t.operation_id WHERE %s LIMIT ?`,
		update: `UPDATE operation_targets SET output = ? WHERE operation_id = ? AND host_id = ? AND output = ?`,
		count:  `SELECT COUNT(*) FROM operation_targets t WHERE %s`,
	},
	{
		name: columnLive,
		query: `SELECT o.workspace_id, t.operation_id, t.host_id, t.live FROM operation_targets t
			JOIN operations o ON o.id = t.operation_id WHERE %s LIMIT ?`,
		update: `UPDATE operation_targets SET live = ? WHERE operation_id = ? AND host_id = ? AND live = ?`,
		count:  `SELECT COUNT(*) FROM operation_targets t WHERE %s`,
	},
}

// rewrapPredicate selects values to move off oldKeyID: those sealed under it
// (any spelling) and every value written before Phase 16.
func rewrapPredicate(column, oldKeyID string) (string, []any, error) {
	escape := strings.NewReplacer(`!`, `!!`, `%`, `!%`, `_`, `!_`)
	clauses := []string{column + ` NOT LIKE ? ESCAPE '!'`}
	args := []any{escape.Replace(sealedPrefix) + "%"}
	prefixes, err := keyPrefixes(oldKeyID)
	if err != nil {
		return "", nil, err
	}
	for _, prefix := range prefixes {
		clauses = append(clauses, column+` LIKE ? ESCAPE '!'`)
		args = append(args, escape.Replace(prefix)+"%")
	}
	return column + ` <> '' AND (` + strings.Join(clauses, " OR ") + `)`, args, nil
}

// RewrapOperationData moves up to batchSize values per column off oldKeyID
// to the current key, and reseals values written before Phase 16. Values are
// replaced only if unchanged since they were read, so a concurrent update
// wins. Repeat until Remaining is 0, then the old key can be dropped.
func (s *Store) RewrapOperationData(ctx context.Context, oldKeyID string, batchSize int) (rewrapped int, remaining int64, err error) {
	var result struct {
		Rewrapped int
		Remaining int64
	}
	defer func() { rewrapped, remaining = result.Rewrapped, result.Remaining }()
	if strings.TrimSpace(oldKeyID) == "" || batchSize < 1 || batchSize > 256 {
		return 0, 0, fmt.Errorf("rewrap Operation data: a key ID and a batch size of 1..256 are required")
	}
	for _, col := range sealedColumns {
		valueColumn := map[string]string{columnSpec: "spec_encrypted", columnOutput: "t.output", columnLive: "t.live"}[col.name]
		where, args, err := rewrapPredicate(valueColumn, oldKeyID)
		if err != nil {
			return 0, 0, err
		}
		rows, err := s.db.QueryContext(ctx, s.bind(fmt.Sprintf(col.query, where)), append(args, batchSize)...)
		if err != nil {
			return 0, 0, fmt.Errorf("list Operation %s to rewrap: %w", col.name, err)
		}
		type candidate struct {
			workspaceID              int
			operationID, hostID, old string
		}
		var candidates []candidate
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.workspaceID, &c.operationID, &c.hostID, &c.old); err != nil {
				sqlx.CloseRows(rows, s.logger, "rewrap Operation data")
				return 0, 0, fmt.Errorf("scan Operation %s to rewrap: %w", col.name, err)
			}
			candidates = append(candidates, c)
		}
		sqlx.CloseRows(rows, s.logger, "rewrap Operation data")
		if err := rows.Err(); err != nil {
			return 0, 0, err
		}
		for _, c := range candidates {
			next, changed, err := s.cipher.Rewrap(ctx, c.old, dataAAD(col.name, c.workspaceID, c.operationID, c.hostID))
			if err != nil {
				return 0, 0, fmt.Errorf("rewrap Operation %s of %s: %w", col.name, c.operationID, err)
			}
			if !changed {
				continue
			}
			args := []any{next}
			if col.name == columnSpec {
				args = append(args, c.workspaceID, c.operationID, c.old)
			} else {
				args = append(args, c.operationID, c.hostID, c.old)
			}
			updated, err := s.db.ExecContext(ctx, s.bind(col.update), args...)
			if err != nil {
				return 0, 0, fmt.Errorf("store rewrapped Operation %s: %w", col.name, err)
			}
			n, err := updated.RowsAffected()
			if err != nil {
				return 0, 0, fmt.Errorf("store rewrapped Operation %s: %w", col.name, err)
			}
			if n == 1 { // 0: changed meanwhile; picked up by the next batch
				result.Rewrapped++
			}
		}
	}
	for _, col := range sealedColumns {
		valueColumn := map[string]string{columnSpec: "spec_encrypted", columnOutput: "t.output", columnLive: "t.live"}[col.name]
		where, args, err := rewrapPredicate(valueColumn, oldKeyID)
		if err != nil {
			return 0, 0, err
		}
		var n int64
		if err := s.db.QueryRowContext(ctx, s.bind(fmt.Sprintf(col.count, where)), args...).Scan(&n); err != nil {
			return 0, 0, fmt.Errorf("count Operation %s to rewrap: %w", col.name, err)
		}
		result.Remaining += n
	}
	return 0, 0, nil
}
