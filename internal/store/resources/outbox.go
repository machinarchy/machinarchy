package resources

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/eventing"
)

func (s *Store) ListHostKeys(ctx context.Context, cursor resource.HostKeyCursor) ([]resource.HostKey, error) {
	limit := cursor.Limit
	if limit < 1 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT workspace_id, id FROM managed_hosts
		WHERE workspace_id > ? OR (workspace_id = ? AND id > ?)
		ORDER BY workspace_id, id LIMIT ?`), cursor.AfterWorkspaceID, cursor.AfterWorkspaceID, cursor.AfterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list managed Host keys: %w", err)
	}
	defer rows.Close()
	keys := make([]resource.HostKey, 0, limit)
	for rows.Next() {
		var key resource.HostKey
		if err := rows.Scan(&key.WorkspaceID, &key.ID); err != nil {
			return nil, fmt.Errorf("scan managed Host key: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed Host keys: %w", err)
	}
	return keys, nil
}

func (s *Store) Claim(ctx context.Context, consumer, owner string, now time.Time, lease time.Duration, limit int) ([]eventing.ClaimedEvent, error) {
	if ctx == nil || consumer == "" || owner == "" || lease <= 0 {
		return nil, errors.New("outbox claim requires context, consumer, owner, and positive lease")
	}
	if limit < 1 || limit > 256 {
		limit = 32
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin outbox claim: %w", err)
	}
	defer rollback(tx, s.logger, "outbox claim")
	query := `SELECT e.event_id, e.event_type, e.schema_version, e.occurred_at, e.producer,
		e.workspace_id, e.subject_kind, e.subject_id, e.subject_generation, e.subject_revision,
		e.correlation_id, e.causation_id, e.actor_user_id, e.actor_token_id, e.payload, d.attempts
		FROM outbox_events e JOIN outbox_deliveries d ON d.event_id = e.event_id
		WHERE d.consumer_group = ? AND d.delivered_at_ns = 0 AND d.quarantined_at_ns = 0
		AND d.available_at_ns <= ? AND (d.lease_expires_at_ns <= ? OR d.lease_owner = ?)
		ORDER BY d.available_at_ns, e.event_id LIMIT ?`
	if s.dialect == PostgreSQL {
		query += ` FOR UPDATE OF d SKIP LOCKED`
	}
	rows, err := tx.QueryContext(ctx, s.bind(query), consumer, now.UnixNano(), now.UnixNano(), owner, limit)
	if err != nil {
		return nil, fmt.Errorf("select pending outbox deliveries: %w", err)
	}
	events := make([]eventing.ClaimedEvent, 0)
	for rows.Next() {
		var event eventing.Envelope
		var eventID, eventType, subjectKind, subjectID, payload, occurred string
		var schemaVersion int
		var actorUser, actorToken sql.NullInt64
		var attempts int
		if err := rows.Scan(&eventID, &eventType, &schemaVersion, &occurred, &event.Producer,
			&event.WorkspaceID, &subjectKind, &subjectID, &event.Subject.Generation, &event.Subject.Revision,
			&event.CorrelationID, &event.CausationID, &actorUser, &actorToken, &payload, &attempts); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan outbox event: %w", err)
		}
		event.ID, event.Type, event.SchemaVersion = resource.ResourceID(eventID), eventType, uint16(schemaVersion)
		event.Subject.Kind, event.Subject.ID = subjectKind, resource.ResourceID(subjectID)
		event.Data = []byte(payload)
		event.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("parse outbox event timestamp: %w", err)
		}
		if actorUser.Valid || actorToken.Valid {
			event.Actor = &eventing.Actor{}
			if actorUser.Valid {
				event.Actor.UserID = int(actorUser.Int64)
			}
			if actorToken.Valid {
				event.Actor.APITokenID = int(actorToken.Int64)
			}
		}
		events = append(events, eventing.ClaimedEvent{Envelope: event, Attempt: attempts + 1})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate pending outbox deliveries: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close pending outbox rows: %w", err)
	}
	for _, claimed := range events {
		result, err := tx.ExecContext(ctx, s.bind(`UPDATE outbox_deliveries SET lease_owner = ?, lease_expires_at_ns = ?, attempts = attempts + 1
			WHERE event_id = ? AND consumer_group = ? AND delivered_at_ns = 0 AND quarantined_at_ns = 0
			AND (lease_expires_at_ns <= ? OR lease_owner = ?)`), owner, now.Add(lease).UnixNano(), string(claimed.Envelope.ID), consumer, now.UnixNano(), owner)
		if err != nil {
			return nil, fmt.Errorf("lease outbox delivery: %w", err)
		}
		if err := s.rowsAffected(result, resource.ErrConflict); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit outbox claim: %w", err)
	}
	return events, nil
}

func (s *Store) Acknowledge(ctx context.Context, consumer, owner string, id resource.ResourceID, now time.Time) error {
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE outbox_deliveries SET delivered_at_ns = ?, lease_owner = '', lease_expires_at_ns = 0, last_error_class = ''
		WHERE event_id = ? AND consumer_group = ? AND lease_owner = ? AND delivered_at_ns = 0 AND quarantined_at_ns = 0`),
		now.UTC().UnixNano(), string(id), consumer, owner)
	if err != nil {
		return fmt.Errorf("acknowledge outbox delivery: %w", err)
	}
	return s.rowsAffected(result, resource.ErrConflict)
}

func (s *Store) Retry(ctx context.Context, consumer, owner string, id resource.ResourceID, availableAt time.Time, failureClass string) error {
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE outbox_deliveries SET available_at_ns = ?, lease_owner = '', lease_expires_at_ns = 0, last_error_class = ?
		WHERE event_id = ? AND consumer_group = ? AND lease_owner = ? AND delivered_at_ns = 0 AND quarantined_at_ns = 0`),
		availableAt.UnixNano(), boundedFailure(failureClass), string(id), consumer, owner)
	if err != nil {
		return fmt.Errorf("schedule outbox retry: %w", err)
	}
	return s.rowsAffected(result, resource.ErrConflict)
}

func (s *Store) Quarantine(ctx context.Context, consumer, owner string, id resource.ResourceID, now time.Time, failureClass string) error {
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE outbox_deliveries SET quarantined_at_ns = ?, lease_owner = '', lease_expires_at_ns = 0, last_error_class = ?
		WHERE event_id = ? AND consumer_group = ? AND lease_owner = ? AND delivered_at_ns = 0 AND quarantined_at_ns = 0`),
		now.UTC().UnixNano(), boundedFailure(failureClass), string(id), consumer, owner)
	if err != nil {
		return fmt.Errorf("quarantine outbox delivery: %w", err)
	}
	return s.rowsAffected(result, resource.ErrConflict)
}

func (s *Store) Prune(ctx context.Context, now time.Time) (int64, error) {
	deliveredBefore := now.Add(-7 * 24 * time.Hour).UTC().UnixNano()
	quarantinedBefore := now.Add(-30 * 24 * time.Hour).UTC().UnixNano()
	result, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM outbox_events WHERE EXISTS (
		SELECT 1 FROM outbox_deliveries d WHERE d.event_id = outbox_events.event_id
		AND d.delivered_at_ns = 0 AND d.quarantined_at_ns = 0
	) = FALSE AND NOT EXISTS (
		SELECT 1 FROM outbox_deliveries d WHERE d.event_id = outbox_events.event_id AND d.delivered_at_ns > ?
	) AND NOT EXISTS (
		SELECT 1 FROM outbox_deliveries d WHERE d.event_id = outbox_events.event_id AND d.quarantined_at_ns > ?
	) AND EXISTS (
		SELECT 1 FROM outbox_deliveries d WHERE d.event_id = outbox_events.event_id
	)`), deliveredBefore, quarantinedBefore)
	if err != nil {
		return 0, fmt.Errorf("prune retained outbox events: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read pruned outbox count: %w", err)
	}
	return count, nil
}

func boundedFailure(value string) string {
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

func (s *Store) AcquireResourceLease(ctx context.Context, lease resource.ResourceLease, now time.Time, ttl time.Duration) (resource.ResourceLease, bool, error) {
	if s.dialect != PostgreSQL {
		return lease, false, resource.ErrLeaseUnsupported
	}
	if err := validateLease(lease, ttl); err != nil {
		return lease, false, err
	}
	nowNS, expires := now.UTC().UnixNano(), now.UTC().Add(ttl).UnixNano()
	query := `INSERT INTO resource_leases(resource_kind, workspace_id, resource_id, owner_id, fencing_token, expires_at_ns)
		VALUES (?, ?, ?, ?, 1, ?)
		ON CONFLICT(resource_kind, workspace_id, resource_id) DO UPDATE SET
		owner_id = excluded.owner_id,
		fencing_token = CASE WHEN resource_leases.owner_id = excluded.owner_id THEN resource_leases.fencing_token ELSE resource_leases.fencing_token + 1 END,
		expires_at_ns = excluded.expires_at_ns
		WHERE resource_leases.expires_at_ns <= ? OR resource_leases.owner_id = ?
		RETURNING fencing_token`
	var token uint64
	err := s.db.QueryRowContext(ctx, s.bind(query), lease.Kind, lease.WorkspaceID, lease.ID, lease.Owner, expires, nowNS, lease.Owner).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return lease, false, nil
	}
	if err != nil {
		return lease, false, fmt.Errorf("acquire resource lease: %w", err)
	}
	lease.Token, lease.ExpiresAt = token, time.Unix(0, expires).UTC()
	return lease, true, nil
}

func (s *Store) RenewResourceLease(ctx context.Context, lease resource.ResourceLease, now time.Time, ttl time.Duration) (resource.ResourceLease, bool, error) {
	if s.dialect != PostgreSQL {
		return lease, false, resource.ErrLeaseUnsupported
	}
	if err := validateLease(lease, ttl); err != nil {
		return lease, false, err
	}
	expires := now.UTC().Add(ttl).UnixNano()
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE resource_leases SET expires_at_ns = ? WHERE resource_kind = ? AND workspace_id = ?
		AND resource_id = ? AND owner_id = ? AND fencing_token = ? AND expires_at_ns > ?`), expires, lease.Kind, lease.WorkspaceID,
		lease.ID, lease.Owner, lease.Token, now.UTC().UnixNano())
	if err != nil {
		return lease, false, fmt.Errorf("renew resource lease: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return lease, false, err
	}
	if count == 0 {
		return lease, false, nil
	}
	lease.ExpiresAt = time.Unix(0, expires).UTC()
	return lease, true, nil
}

func (s *Store) ReleaseResourceLease(ctx context.Context, lease resource.ResourceLease) error {
	if s.dialect != PostgreSQL {
		return resource.ErrLeaseUnsupported
	}
	_, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM resource_leases WHERE resource_kind = ? AND workspace_id = ? AND resource_id = ? AND owner_id = ? AND fencing_token = ?`),
		lease.Kind, lease.WorkspaceID, lease.ID, lease.Owner, lease.Token)
	if err != nil {
		return fmt.Errorf("release resource lease: %w", err)
	}
	return nil
}

func validateLease(lease resource.ResourceLease, ttl time.Duration) error {
	if lease.Kind == "" || lease.WorkspaceID <= 0 || lease.ID == "" || lease.Owner == "" || ttl <= 0 || ttl > time.Hour {
		return errors.New("resource lease identity, owner, and bounded positive TTL are required")
	}
	return nil
}

func (s *Store) verifyLeaseFence(ctx context.Context, tx *sql.Tx, fence resource.LeaseFence, workspaceID int, id resource.ResourceID) error {
	if s.dialect != PostgreSQL || fence.Kind != "host" || fence.WorkspaceID != workspaceID || fence.ID != string(id) || fence.Token == 0 {
		return resource.ErrLeaseRequired
	}
	var token, expiresAt int64
	err := tx.QueryRowContext(ctx, `SELECT fencing_token, expires_at_ns FROM resource_leases
		WHERE resource_kind = $1 AND workspace_id = $2 AND resource_id = $3 FOR UPDATE`,
		fence.Kind, fence.WorkspaceID, fence.ID).Scan(&token, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return resource.ErrLeaseRequired
	}
	if err != nil {
		return fmt.Errorf("verify resource lease fence: %w", err)
	}
	if uint64(token) != fence.Token || expiresAt <= time.Now().UTC().UnixNano() {
		return resource.ErrLeaseRequired
	}
	return nil
}
