package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

// auditLockClass namespaces the advisory lock that serializes appends per
// workspace ("AU" in ASCII).
const auditLockClass = 0x4155

type auditExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// appendAudit records event inside the caller's transaction. The actor comes
// from ctx (domain.WithAuditActor); without one the event is attributed to the
// system. A transaction-scoped advisory lock serializes appends per workspace so
// concurrent writers cannot fork the hash chain; the lock is released when the
// caller's transaction ends.
func appendAudit(ctx context.Context, q auditExecer, event domain.AuditEvent) error {
	if event.WorkspaceID <= 0 || event.Action == "" || event.TargetType == "" {
		return fmt.Errorf("%w: audit events need a workspace, action, and target type", domain.ErrInvalidInput)
	}
	if actor, ok := domain.AuditActorFrom(ctx); ok {
		event.ActorUserID, event.ActorUsername, event.ActorTokenID = actor.UserID, actor.Username, actor.APITokenID
		event.RequestID, event.Source = actor.RequestID, actor.Source
	}
	if event.Outcome == "" {
		event.Outcome = domain.AuditOutcomeSuccess
	}
	details, err := domain.SanitizeAuditDetails(event.Details)
	if err != nil {
		return err
	}
	event.Details = details

	if _, err := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, auditLockClass, event.WorkspaceID); err != nil {
		return fmt.Errorf("lock audit chain: %w", err)
	}
	var prev string
	err = q.QueryRowContext(ctx, `SELECT hash FROM audit_events WHERE workspace_id = $1 ORDER BY id DESC LIMIT 1`,
		event.WorkspaceID).Scan(&prev)
	if errors.Is(err, sql.ErrNoRows) {
		prev = domain.AuditGenesisHash
	} else if err != nil {
		return fmt.Errorf("read audit chain head: %w", err)
	}
	occurredAt := time.Now().UTC().Format(time.RFC3339Nano)
	hash := domain.ComputeAuditHash(prev, occurredAt, event)
	encoded, err := json.Marshal(event.Details)
	if err != nil {
		return fmt.Errorf("marshal audit details: %w", err)
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO audit_events (workspace_id, occurred_at, actor_user_id,
		actor_username, actor_token_id, request_id, source, action, target_type, target_id, outcome,
		details, prev_hash, hash) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		event.WorkspaceID, occurredAt, event.ActorUserID, event.ActorUsername, event.ActorTokenID,
		event.RequestID, event.Source, event.Action, event.TargetType, event.TargetID, event.Outcome,
		string(encoded), prev, hash); err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

const auditColumns = `id, workspace_id, occurred_at, actor_user_id, actor_username, actor_token_id,
	request_id, source, action, target_type, target_id, outcome, details, prev_hash, hash`

type rowScanner interface{ Scan(dest ...any) error }

func scanAuditEvent(row rowScanner) (domain.StoredAuditEvent, error) {
	var stored domain.StoredAuditEvent
	var details string
	if err := row.Scan(&stored.ID, &stored.WorkspaceID, &stored.OccurredAtText, &stored.ActorUserID,
		&stored.ActorUsername, &stored.ActorTokenID, &stored.RequestID, &stored.Source, &stored.Action,
		&stored.TargetType, &stored.TargetID, &stored.Outcome, &details, &stored.PrevHash, &stored.Hash); err != nil {
		return stored, err
	}
	stored.Details = map[string]string{}
	if err := json.Unmarshal([]byte(details), &stored.Details); err != nil {
		return stored, fmt.Errorf("decode audit details of event %d: %w", stored.ID, err)
	}
	if parsed, err := time.Parse(time.RFC3339Nano, stored.OccurredAtText); err == nil {
		stored.OccurredAt = parsed
	}
	return stored, nil
}

func (s *Store) ListAuditEvents(ctx context.Context, workspaceID int, filter domain.AuditFilter) ([]domain.AuditEvent, error) {
	filter, err := domain.NormalizeAuditFilter(filter)
	if err != nil {
		return nil, err
	}
	query := `SELECT ` + auditColumns + ` FROM audit_events WHERE workspace_id = $1`
	args := []any{workspaceID}
	if filter.Action != "" {
		args = append(args, filter.Action)
		query += fmt.Sprintf(` AND action = $%d`, len(args))
	}
	if filter.BeforeID > 0 {
		args = append(args, filter.BeforeID)
		query += fmt.Sprintf(` AND id < $%d`, len(args))
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(` ORDER BY id DESC LIMIT $%d`, len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	events := make([]domain.AuditEvent, 0, filter.Limit)
	for rows.Next() {
		stored, err := scanAuditEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, stored.AuditEvent)
	}
	return events, rows.Err()
}

func (s *Store) VerifyAuditChain(ctx context.Context, workspaceID int) (*domain.AuditVerification, error) {
	verifier := domain.NewAuditChainVerifier()
	var after int64
	for {
		rows, err := s.db.QueryContext(ctx, `SELECT `+auditColumns+` FROM audit_events
			WHERE workspace_id = $1 AND id > $2 ORDER BY id ASC LIMIT 1000`, workspaceID, after)
		if err != nil {
			return nil, fmt.Errorf("read audit events for verification: %w", err)
		}
		count := 0
		for rows.Next() {
			stored, err := scanAuditEvent(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			count++
			after = stored.ID
			if !verifier.Add(stored) {
				rows.Close()
				result := verifier.Result()
				return &result, nil
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("iterate audit events: %w", err)
		}
		if count < 1000 {
			result := verifier.Result()
			return &result, nil
		}
	}
}

// keyList renders map keys as a sorted, comma-separated list for audit details.
func keyList(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// sortedList renders values as a sorted, comma-separated list for audit details.
func sortedList(values []string) string {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}
