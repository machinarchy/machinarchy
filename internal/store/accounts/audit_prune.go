package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/store/sqlx"
)

// rowQuerier is the read side shared by *sql.DB and *sql.Tx.
type rowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// latestAuditCheckpoint returns the workspace's newest prune checkpoint, or nil
// when nothing was ever pruned.
func (s *Store) latestAuditCheckpoint(ctx context.Context, q rowQuerier, workspaceID int) (*domain.AuditCheckpoint, error) {
	var (
		cp      domain.AuditCheckpoint
		created string
	)
	err := q.QueryRowContext(ctx, s.bind(`SELECT id, last_event_id, last_hash, events_removed, actor_user_id, created_at
		FROM audit_checkpoints WHERE workspace_id = ? ORDER BY last_event_id DESC LIMIT 1`), workspaceID).
		Scan(&cp.ID, &cp.LastEventID, &cp.LastHash, &cp.EventsRemoved, &cp.ActorUserID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read audit checkpoint: %w", err)
	}
	if parsed, perr := time.Parse(time.RFC3339Nano, created); perr == nil {
		cp.CreatedAt = parsed
	}
	return &cp, nil
}

// PruneAudit removes the workspace's events with an ID below req.BeforeID and
// leaves a checkpoint (ADR-061). It first re-walks the range it would remove,
// from the previous checkpoint, and refuses unless the chain is intact there,
// the newest removed event is older than domain.MinAuditPruneAge, and the
// caller's archive hash and count match what is about to go. The checkpoint,
// the deletion and the audit.pruned event commit together or not at all.
func (s *Store) PruneAudit(ctx context.Context, workspaceID int, req domain.AuditPruneRequest, now time.Time) (*domain.AuditCheckpoint, error) {
	if workspaceID <= 0 {
		return nil, fmt.Errorf("%w: a workspace is required", domain.ErrInvalidInput)
	}
	if err := domain.ValidateAuditPrune(req); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin audit prune transaction: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "prune audit")
	if s.dialect == sqlx.PostgreSQL {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, auditLockClass, workspaceID); err != nil {
			return nil, fmt.Errorf("lock audit chain: %w", err)
		}
	}
	previous, err := s.latestAuditCheckpoint(ctx, tx, workspaceID)
	if err != nil {
		return nil, err
	}
	verifier := domain.NewAuditChainVerifier()
	var after, lowest int64
	if previous != nil {
		verifier = domain.NewAuditChainVerifierFrom(previous.LastHash)
		after, lowest = previous.LastEventID, previous.LastEventID
	}

	// Walk the range in pages, checking the chain and keeping only the last event.
	var (
		count int
		last  domain.StoredAuditEvent
	)
	const pageSize = 1000
	for {
		rows, err := tx.QueryContext(ctx, s.bind(`SELECT `+auditColumns+` FROM audit_events
			WHERE workspace_id = ? AND id > ? AND id < ? ORDER BY id LIMIT ?`), workspaceID, after, req.BeforeID, pageSize)
		if err != nil {
			return nil, fmt.Errorf("read audit events to prune: %w", err)
		}
		page := 0
		for rows.Next() {
			stored, serr := scanAuditEvent(rows)
			if serr != nil {
				_ = rows.Close()
				return nil, serr
			}
			page++
			after = stored.ID
			if !verifier.Add(stored) {
				_ = rows.Close()
				return nil, fmt.Errorf("%w: the audit chain is broken at event %d, so nothing was pruned (run audit verify)",
					domain.ErrInvalidInput, stored.ID)
			}
			count++
			last = stored
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("iterate audit events to prune: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close audit events to prune: %w", err)
		}
		if page < pageSize {
			break
		}
	}

	if count == 0 {
		return nil, fmt.Errorf("%w: there are no events below %d to prune", domain.ErrInvalidInput, req.BeforeID)
	}
	if last.OccurredAt.IsZero() || last.OccurredAt.After(now.Add(-domain.MinAuditPruneAge)) {
		return nil, fmt.Errorf("%w: the newest event to prune (%d) is younger than %d days", domain.ErrInvalidInput,
			last.ID, int(domain.MinAuditPruneAge/(24*time.Hour)))
	}
	if count != req.ArchiveCount || last.Hash != req.ArchiveHeadHash {
		return nil, fmt.Errorf("%w: the server would remove %d events ending at event %d with hash %s",
			domain.ErrAuditArchiveMismatch, count, last.ID, last.Hash)
	}

	actorID := 0
	if actor, ok := domain.AuditActorFrom(ctx); ok {
		actorID = actor.UserID
	}
	checkpoint := domain.AuditCheckpoint{LastEventID: last.ID, LastHash: last.Hash, EventsRemoved: count, ActorUserID: actorID, CreatedAt: now.UTC()}
	// The checkpoint goes first: the delete trigger permits removal only at or
	// below a checkpoint of the same workspace.
	if err := tx.QueryRowContext(ctx, s.bind(`INSERT INTO audit_checkpoints
		(workspace_id, last_event_id, last_hash, events_removed, actor_user_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?) RETURNING id`),
		workspaceID, last.ID, last.Hash, count, actorID, sqlx.Time(now)).Scan(&checkpoint.ID); err != nil {
		return nil, fmt.Errorf("record audit checkpoint: %w", err)
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM audit_events WHERE workspace_id = ? AND id > ? AND id <= ?`),
		workspaceID, lowest, last.ID)
	if err != nil {
		return nil, fmt.Errorf("delete pruned audit events: %w", err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("count pruned audit events: %w", err)
	}
	if removed != int64(count) {
		return nil, fmt.Errorf("pruned %d audit events, expected %d; nothing was removed", removed, count)
	}
	if err := s.appendAudit(ctx, tx, domain.AuditEvent{
		WorkspaceID: workspaceID, Action: domain.AuditPruned, TargetType: domain.AuditTargetTrail,
		TargetID: fmt.Sprint(last.ID),
		Details:  map[string]string{"removed": fmt.Sprint(count), "last_event_id": fmt.Sprint(last.ID), "last_hash": last.Hash},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit audit prune: %w", err)
	}
	return &checkpoint, nil
}
