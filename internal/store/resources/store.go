// Package resources implements the persistence shared by the SQLite and
// PostgreSQL adapters for canonical managed resources. Engine-specific SQL
// placeholders are selected by the adapter at construction time; lifecycle
// rules remain in internal/domain/resource.
package resources

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/eventing"
	"github.com/machinarchy/machinarchy/pkg/crypto"
)

type Dialect uint8

const (
	SQLite Dialect = iota + 1
	PostgreSQL
)

type auditAppender func(context.Context, *sql.Tx, domain.AuditEvent) error

type Store struct {
	db          *sql.DB
	dialect     Dialect
	key         []byte
	appendAudit auditAppender
	logger      *slog.Logger
}

func New(db *sql.DB, dialect Dialect, encryptionKey []byte, logger *slog.Logger,
	appendAuditFn func(context.Context, *sql.Tx, domain.AuditEvent) error) (*Store, error) {
	if db == nil || (dialect != SQLite && dialect != PostgreSQL) || len(encryptionKey) != 32 || appendAuditFn == nil {
		return nil, errors.New("resource store requires a database, supported dialect, 32-byte key, and audit appender")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{db: db, dialect: dialect, key: append([]byte(nil), encryptionKey...),
		appendAudit: appendAuditFn, logger: logger}, nil
}

func (s *Store) bind(query string) string {
	if s.dialect == SQLite {
		return query
	}
	var out strings.Builder
	index := 1
	for _, r := range query {
		if r == '?' {
			fmt.Fprintf(&out, "$%d", index)
			index++
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func (s *Store) nowText(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func (s *Store) boolValue(value bool) any {
	if s.dialect == PostgreSQL {
		return value
	}
	if value {
		return 1
	}
	return 0
}

func (s *Store) rowsAffected(result sql.Result, notFound error) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected row count: %w", err)
	}
	if count == 0 {
		return notFound
	}
	return nil
}

func encodeJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode resource data: %w", err)
	}
	return string(data), nil
}

func decodeJSON(text string, value any) error {
	if err := json.Unmarshal([]byte(text), value); err != nil {
		return fmt.Errorf("decode resource data: %w", err)
	}
	return nil
}

func parseTime(text sql.NullString) (*time.Time, error) {
	if !text.Valid || text.String == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, text.String)
	if err != nil {
		return nil, fmt.Errorf("parse resource timestamp: %w", err)
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func (s *Store) audit(ctx context.Context, tx *sql.Tx, workspaceID int, action, targetType string,
	targetID string, details map[string]string) error {
	return s.appendAudit(ctx, tx, domain.AuditEvent{WorkspaceID: workspaceID, Action: action,
		TargetType: targetType, TargetID: targetID, Details: details})
}

func (s *Store) CreateHost(ctx context.Context, host *resource.Host) error {
	if host == nil {
		return fmt.Errorf("%w: Host is required", resource.ErrInvalidResource)
	}
	if err := host.Validate(); err != nil {
		return err
	}
	labels, err := encodeJSON(host.Metadata.Labels)
	if err != nil {
		return err
	}
	conditions, err := encodeJSON(host.Status.Conditions)
	if err != nil {
		return err
	}
	if host.Spec.Presence == "" {
		host.Spec.Presence = resource.PresenceManaged
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin managed Host creation: %w", err)
	}
	defer rollback(tx, s.logger, "managed Host creation")
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO managed_hosts (
		id, workspace_id, environment_id, name, labels, desired_presence, access_profile_id,
		access_profile_revision, generation, revision, observed_generation, phase, conditions,
		last_observed_at, deletion_timestamp, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		string(host.Metadata.ID), host.Metadata.WorkspaceID, nullablePositiveID(host.Metadata.EnvironmentID), host.Metadata.Name,
		labels, string(host.Spec.Presence), string(host.Spec.AccessProfileID), host.Spec.ProfileRevision,
		host.Metadata.Generation, host.Metadata.Revision, host.Status.ObservedGeneration, string(host.Status.Phase),
		conditions, nullableTimeText(s, host.Status.LastObservedAt), nullableTimeText(s, host.Metadata.DeletionTimestamp),
		s.nowText(host.Metadata.CreatedAt), s.nowText(host.Metadata.UpdatedAt))
	if err != nil {
		if isUniqueError(err) {
			return fmt.Errorf("%w: %v", domain.ErrHostAlreadyExists, err)
		}
		return fmt.Errorf("insert managed Host: %w", err)
	}
	if err := s.audit(ctx, tx, host.Metadata.WorkspaceID, domain.AuditResourceHostCreated,
		domain.AuditTargetResourceHost, string(host.Metadata.ID), map[string]string{"name": host.Metadata.Name}); err != nil {
		return err
	}
	if err := s.appendHostEvent(ctx, tx, host, eventing.HostCreated, "created"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit managed Host creation: %w", err)
	}
	return nil
}

func (s *Store) GetHost(ctx context.Context, workspaceID int, id resource.ResourceID) (*resource.Host, error) {
	if workspaceID <= 0 || id.Validate() != nil {
		return nil, fmt.Errorf("%w: workspace and canonical Host ID are required", resource.ErrInvalidResource)
	}
	return s.getHost(ctx, s.db, workspaceID, id)
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) getHost(ctx context.Context, q queryRower, workspaceID int, id resource.ResourceID) (*resource.Host, error) {
	const query = `SELECT id, workspace_id, environment_id, name, labels, desired_presence, access_profile_id,
		access_profile_revision, generation, revision, observed_generation, phase, conditions,
		last_observed_at, deletion_timestamp, created_at, updated_at
		FROM managed_hosts WHERE workspace_id = ? AND id = ?`
	var host resource.Host
	var labelsText, conditionsText, presence, profileID, phase string
	var environmentID sql.NullInt64
	var lastObserved, deletion sql.NullString
	var created, updated string
	err := q.QueryRowContext(ctx, s.bind(query), workspaceID, string(id)).Scan(
		&host.Metadata.ID, &host.Metadata.WorkspaceID, &environmentID, &host.Metadata.Name,
		&labelsText, &presence, &profileID, &host.Spec.ProfileRevision, &host.Metadata.Generation,
		&host.Metadata.Revision, &host.Status.ObservedGeneration, &phase, &conditionsText,
		&lastObserved, &deletion, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, resource.ErrHostNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read managed Host: %w", err)
	}
	if environmentID.Valid {
		host.Metadata.EnvironmentID = int(environmentID.Int64)
	}
	host.Spec.Presence = resource.DesiredPresence(presence)
	host.Spec.AccessProfileID = resource.ResourceID(profileID)
	host.Status.Phase = resource.HostPhase(phase)
	if err := decodeJSON(labelsText, &host.Metadata.Labels); err != nil {
		return nil, err
	}
	if err := decodeJSON(conditionsText, &host.Status.Conditions); err != nil {
		return nil, err
	}
	if host.Status.LastObservedAt, err = parseTime(lastObserved); err != nil {
		return nil, err
	}
	if host.Metadata.DeletionTimestamp, err = parseTime(deletion); err != nil {
		return nil, err
	}
	if host.Metadata.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return nil, fmt.Errorf("parse managed Host creation time: %w", err)
	}
	if host.Metadata.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return nil, fmt.Errorf("parse managed Host update time: %w", err)
	}
	if err := host.Validate(); err != nil {
		return nil, fmt.Errorf("stored managed Host violates domain invariants: %w", err)
	}
	return &host, nil
}

func (s *Store) ListHosts(ctx context.Context, workspaceID int, limit int) ([]resource.Host, error) {
	if workspaceID <= 0 {
		return nil, fmt.Errorf("%w: workspace ID must be positive", resource.ErrInvalidResource)
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id FROM managed_hosts WHERE workspace_id = ? ORDER BY updated_at DESC, id LIMIT ?`), workspaceID, limit)
	if err != nil {
		return nil, fmt.Errorf("list managed Hosts: %w", err)
	}
	defer rows.Close()
	ids := make([]resource.ResourceID, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, resource.ResourceID(id))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed Hosts: %w", err)
	}
	hosts := make([]resource.Host, 0, len(ids))
	for _, id := range ids {
		host, err := s.GetHost(ctx, workspaceID, id)
		if err != nil {
			return nil, err
		}
		hosts = append(hosts, *host)
	}
	return hosts, nil
}

func (s *Store) UpdateHostSpec(ctx context.Context, workspaceID int, id resource.ResourceID,
	expectedRevision, expectedGeneration uint64, spec resource.HostSpec, now time.Time) error {
	host, err := s.getHost(ctx, s.db, workspaceID, id)
	if err != nil {
		return err
	}
	if host.Metadata.Revision != expectedRevision || host.Metadata.Generation != expectedGeneration {
		return resource.ErrConflict
	}
	if err := host.UpdateSpec(expectedGeneration, spec, now); err != nil {
		return err
	}
	if host.Metadata.Revision == expectedRevision {
		return nil
	}
	eventType, change := eventing.HostSpecChanged, "spec-changed"
	if host.Spec.Presence == resource.PresenceAbsent {
		eventType, change = eventing.HostDeletionRequested, "deletion-requested"
	}
	return s.writeHost(ctx, host, expectedRevision, domain.AuditResourceHostUpdated, eventType, change, nil)
}

func (s *Store) UpdateHostMetadata(ctx context.Context, workspaceID int, id resource.ResourceID,
	expectedRevision uint64, name string, environmentID int, labels map[string]string, now time.Time) error {
	host, err := s.getHost(ctx, s.db, workspaceID, id)
	if err != nil {
		return err
	}
	if host.Metadata.Revision != expectedRevision {
		return resource.ErrConflict
	}
	if err := host.UpdateMetadata(expectedRevision, name, environmentID, labels, now); err != nil {
		return err
	}
	return s.writeHost(ctx, host, expectedRevision, domain.AuditResourceHostUpdated, "", "", nil)
}

func (s *Store) ObserveHost(ctx context.Context, workspaceID int, id resource.ResourceID,
	expectedGeneration uint64, phase resource.HostPhase, conditions []resource.Condition, now time.Time) error {
	host, err := s.getHost(ctx, s.db, workspaceID, id)
	if err != nil {
		return err
	}
	if err := host.Observe(expectedGeneration, phase, conditions, now); err != nil {
		return err
	}
	var fence *resource.LeaseFence
	if value, ok := resource.LeaseFenceFromContext(ctx); ok {
		fence = &value
	}
	if s.dialect == PostgreSQL && fence == nil {
		return resource.ErrLeaseRequired
	}
	return s.writeHost(ctx, host, host.Metadata.Revision-1, domain.AuditResourceHostUpdated, "", "", fence)
}

func (s *Store) writeHost(ctx context.Context, host *resource.Host, expectedRevision uint64, auditAction, eventType, eventChange string, fence *resource.LeaseFence) error {
	labels, err := encodeJSON(host.Metadata.Labels)
	if err != nil {
		return err
	}
	conditions, err := encodeJSON(host.Status.Conditions)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin managed Host update: %w", err)
	}
	defer rollback(tx, s.logger, "managed Host update")
	if fence != nil {
		if err := s.verifyLeaseFence(ctx, tx, *fence, host.Metadata.WorkspaceID, host.Metadata.ID); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE managed_hosts SET environment_id = ?, name = ?, labels = ?,
		desired_presence = ?, access_profile_id = ?, access_profile_revision = ?, generation = ?, revision = ?,
		observed_generation = ?, phase = ?, conditions = ?, last_observed_at = ?, deletion_timestamp = ?, updated_at = ?
		WHERE workspace_id = ? AND id = ? AND revision = ?`), nullablePositiveID(host.Metadata.EnvironmentID), host.Metadata.Name, labels,
		string(host.Spec.Presence), string(host.Spec.AccessProfileID), host.Spec.ProfileRevision,
		host.Metadata.Generation, host.Metadata.Revision, host.Status.ObservedGeneration, string(host.Status.Phase),
		conditions, nullableTimeText(s, host.Status.LastObservedAt), nullableTimeText(s, host.Metadata.DeletionTimestamp),
		s.nowText(host.Metadata.UpdatedAt), host.Metadata.WorkspaceID, string(host.Metadata.ID), expectedRevision)
	if err != nil {
		return fmt.Errorf("update managed Host: %w", err)
	}
	if err := s.rowsAffected(result, resource.ErrConflict); err != nil {
		return err
	}
	if err := s.audit(ctx, tx, host.Metadata.WorkspaceID, auditAction, domain.AuditTargetResourceHost,
		string(host.Metadata.ID), map[string]string{"generation": fmt.Sprint(host.Metadata.Generation)}); err != nil {
		return err
	}
	if eventType != "" {
		if err := s.appendHostEvent(ctx, tx, host, eventType, eventChange); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit managed Host update: %w", err)
	}
	return nil
}

func (s *Store) DeleteHost(ctx context.Context, workspaceID int, id resource.ResourceID, expectedRevision uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin managed Host deletion: %w", err)
	}
	defer rollback(tx, s.logger, "managed Host deletion")
	host, err := s.getHost(ctx, tx, workspaceID, id)
	if err != nil {
		return err
	}
	if host.Metadata.Revision != expectedRevision {
		return resource.ErrConflict
	}
	if fence, ok := resource.LeaseFenceFromContext(ctx); ok {
		if err := s.verifyLeaseFence(ctx, tx, fence, workspaceID, id); err != nil {
			return err
		}
	}
	if err := s.audit(ctx, tx, workspaceID, domain.AuditResourceHostDeleted,
		domain.AuditTargetResourceHost, string(id), map[string]string{"name": host.Metadata.Name}); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM managed_hosts WHERE workspace_id = ? AND id = ? AND revision = ?`),
		workspaceID, string(id), expectedRevision)
	if err != nil {
		return fmt.Errorf("delete managed Host: %w", err)
	}
	if err := s.rowsAffected(result, resource.ErrConflict); err != nil {
		return err
	}
	deleted := *host
	deleted.Metadata.Revision = expectedRevision + 1
	if err := s.appendHostEvent(ctx, tx, &deleted, eventing.HostDeleted, "deleted"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit managed Host deletion: %w", err)
	}
	return nil
}

func (s *Store) appendHostEvent(ctx context.Context, tx *sql.Tx, host *resource.Host, eventType, change string) error {
	var correlationID string
	var actor *eventing.Actor
	if source, ok := domain.AuditActorFrom(ctx); ok {
		correlationID = source.RequestID
		if source.UserID > 0 || source.APITokenID > 0 {
			actor = &eventing.Actor{UserID: source.UserID, APITokenID: source.APITokenID}
		}
	}
	event, err := eventing.NewHostEvent(eventType, host.Metadata.WorkspaceID, eventing.Subject{
		Kind: "host", ID: host.Metadata.ID, Generation: host.Metadata.Generation, Revision: host.Metadata.Revision,
	}, change, host.Metadata.UpdatedAt, correlationID, correlationID, actor)
	if err != nil {
		return fmt.Errorf("construct Host integration event: %w", err)
	}
	payload := string(event.Data)
	var actorUserID, actorTokenID any
	if actor != nil {
		if actor.UserID > 0 {
			actorUserID = actor.UserID
		}
		if actor.APITokenID > 0 {
			actorTokenID = actor.APITokenID
		}
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO outbox_events (
		event_id, event_type, schema_version, occurred_at, producer, workspace_id,
		subject_kind, subject_id, subject_generation, subject_revision, correlation_id,
		causation_id, actor_user_id, actor_token_id, payload, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), string(event.ID), event.Type, event.SchemaVersion,
		s.nowText(event.OccurredAt), event.Producer, event.WorkspaceID, event.Subject.Kind, string(event.Subject.ID),
		event.Subject.Generation, event.Subject.Revision, event.CorrelationID, event.CausationID,
		actorUserID, actorTokenID, payload, s.nowText(event.OccurredAt))
	if err != nil {
		return fmt.Errorf("insert Host outbox event: %w", err)
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO outbox_deliveries (event_id, consumer_group, available_at_ns)
		VALUES (?, ?, ?)`), string(event.ID), eventing.ConsumerControlManager, event.OccurredAt.UnixNano())
	if err != nil {
		return fmt.Errorf("create Host outbox delivery: %w", err)
	}
	return nil
}

func (s *Store) CreateOperation(ctx context.Context, operation *resource.Operation) error {
	if operation == nil {
		return fmt.Errorf("%w: Operation is required", resource.ErrInvalidResource)
	}
	if err := operation.Validate(); err != nil {
		return err
	}
	specJSON, err := json.Marshal(operation.Spec)
	if err != nil {
		return fmt.Errorf("encode Operation spec: %w", err)
	}
	encrypted, err := crypto.Encrypt(string(specJSON), s.key)
	if err != nil {
		return fmt.Errorf("encrypt Operation spec: %w", err)
	}
	conditionsJSON, err := encodeJSON(operation.Status.Conditions)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Operation creation: %w", err)
	}
	defer rollback(tx, s.logger, "Operation creation")
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO operations (
		id, workspace_id, kind, state, revision, actor_user_id, actor_token_id, spec_encrypted,
		cancel_asked, conditions, created_at, started_at, finished_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), string(operation.ID), operation.WorkspaceID,
		string(operation.Kind), string(operation.Status.State), operation.Revision, operation.CreatedBy.UserID,
		operation.CreatedBy.APITokenID, encrypted, s.boolValue(operation.Status.CancelAsked),
		conditionsJSON, s.nowText(operation.CreatedAt), nullableTimeText(s, operation.StartedAt), nullableTimeText(s, operation.FinishedAt))
	if err != nil {
		if isUniqueError(err) {
			return fmt.Errorf("%w: %v", resource.ErrConflict, err)
		}
		return fmt.Errorf("insert Operation: %w", err)
	}
	for _, target := range operation.Status.Targets {
		output, err := s.encryptTargetOutput(target.Result)
		if err != nil {
			return fmt.Errorf("encrypt Operation target output: %w", err)
		}
		_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO operation_targets (
			operation_id, host_id, host_generation, display_name, access_profile_id,
			access_profile_revision, state, exit_code, output, error_class
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), string(operation.ID), string(target.Spec.HostID),
			target.Spec.HostGeneration, target.Spec.DisplayName, string(target.Spec.AccessProfileID),
			target.Spec.AccessProfileRevision, string(target.State), nullableInt(target.Result, func(r *resource.TargetResult) *int { return r.ExitCode }),
			output, targetErrorClass(target.Result))
		if err != nil {
			return fmt.Errorf("insert Operation target: %w", err)
		}
	}
	if err := s.audit(ctx, tx, operation.WorkspaceID, domain.AuditOperationCreated,
		domain.AuditTargetOperation, string(operation.ID), map[string]string{"kind": string(operation.Kind)}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Operation creation: %w", err)
	}
	return nil
}

func (s *Store) GetOperation(ctx context.Context, workspaceID int, id resource.ResourceID) (*resource.Operation, error) {
	if workspaceID <= 0 || id.Validate() != nil {
		return nil, fmt.Errorf("%w: workspace and Operation ID are required", resource.ErrInvalidResource)
	}
	var operation resource.Operation
	var kind, state, encrypted, conditionsText string
	var cancelAsked bool
	var created string
	var started, finished sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, workspace_id, kind, state, revision,
		actor_user_id, actor_token_id, spec_encrypted, cancel_asked, conditions, created_at, started_at, finished_at
		FROM operations WHERE workspace_id = ? AND id = ?`), workspaceID, string(id)).Scan(
		&operation.ID, &operation.WorkspaceID, &kind, &state, &operation.Revision,
		&operation.CreatedBy.UserID, &operation.CreatedBy.APITokenID, &encrypted, &cancelAsked, &conditionsText,
		&created, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, resource.ErrOperationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read Operation: %w", err)
	}
	plain, err := crypto.Decrypt(encrypted, s.key)
	if err != nil {
		return nil, fmt.Errorf("decrypt Operation spec: %w", err)
	}
	if err := json.Unmarshal([]byte(plain), &operation.Spec); err != nil {
		return nil, fmt.Errorf("decode Operation spec: %w", err)
	}
	if err := decodeJSON(conditionsText, &operation.Status.Conditions); err != nil {
		return nil, err
	}
	operation.Kind, operation.Status.State, operation.Status.CancelAsked = resource.OperationKind(kind), resource.OperationState(state), cancelAsked
	if operation.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return nil, fmt.Errorf("parse Operation creation time: %w", err)
	}
	if operation.StartedAt, err = parseTime(started); err != nil {
		return nil, err
	}
	if operation.FinishedAt, err = parseTime(finished); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT host_id, host_generation, display_name,
		access_profile_id, access_profile_revision, state, exit_code, output, error_class
		FROM operation_targets WHERE operation_id = ? ORDER BY host_id`), string(id))
	if err != nil {
		return nil, fmt.Errorf("read Operation targets: %w", err)
	}
	defer rows.Close()
	byHost := make(map[resource.ResourceID]resource.OperationTargetSpec, len(operation.Spec.Targets))
	for _, target := range operation.Spec.Targets {
		byHost[target.HostID] = target
	}
	targetsByHost := make(map[resource.ResourceID]resource.OperationTarget, len(operation.Spec.Targets))
	for rows.Next() {
		var hostID, displayName, profileID, targetState, output, errorClass string
		var generation, profileRevision uint64
		var exitCode sql.NullInt64
		if err := rows.Scan(&hostID, &generation, &displayName, &profileID, &profileRevision, &targetState, &exitCode, &output, &errorClass); err != nil {
			return nil, fmt.Errorf("scan Operation target: %w", err)
		}
		if output != "" {
			output, err = crypto.Decrypt(output, s.key)
			if err != nil {
				return nil, fmt.Errorf("decrypt Operation target output: %w", err)
			}
		}
		spec, ok := byHost[resource.ResourceID(hostID)]
		if !ok || spec.HostGeneration != generation || spec.DisplayName != displayName || spec.AccessProfileID != resource.ResourceID(profileID) || spec.AccessProfileRevision != profileRevision {
			return nil, fmt.Errorf("stored Operation target does not match immutable spec")
		}
		target := resource.OperationTarget{Spec: spec, State: resource.TargetState(targetState)}
		if exitCode.Valid || output != "" || errorClass != "" {
			target.Result = &resource.TargetResult{Output: output, ErrorClass: errorClass}
			if exitCode.Valid {
				value := int(exitCode.Int64)
				target.Result.ExitCode = &value
			}
		}
		targetsByHost[spec.HostID] = target
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Operation targets: %w", err)
	}
	for _, spec := range operation.Spec.Targets {
		target, ok := targetsByHost[spec.HostID]
		if !ok {
			return nil, fmt.Errorf("stored Operation target %s is missing", spec.HostID)
		}
		operation.Status.Targets = append(operation.Status.Targets, target)
	}
	if err := operation.Validate(); err != nil {
		return nil, fmt.Errorf("stored Operation violates domain invariants: %w", err)
	}
	return &operation, nil
}

func (s *Store) UpdateOperation(ctx context.Context, operation *resource.Operation, expectedRevision uint64) error {
	if operation == nil {
		return fmt.Errorf("%w: Operation is required", resource.ErrInvalidResource)
	}
	if err := operation.Validate(); err != nil {
		return err
	}
	current, err := s.GetOperation(ctx, operation.WorkspaceID, operation.ID)
	if err != nil {
		return err
	}
	if current.Revision != expectedRevision || operation.Revision <= expectedRevision {
		return resource.ErrConflict
	}
	if !sameOperationSpec(current.Spec, operation.Spec) {
		return fmt.Errorf("%w: Operation spec and targets are immutable", resource.ErrInvalidResource)
	}
	conditionsJSON, err := encodeJSON(operation.Status.Conditions)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Operation update: %w", err)
	}
	defer rollback(tx, s.logger, "Operation update")
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE operations SET state = ?, revision = ?, cancel_asked = ?, conditions = ?, started_at = ?, finished_at = ?
		WHERE workspace_id = ? AND id = ? AND revision = ?`), string(operation.Status.State), operation.Revision,
		s.boolValue(operation.Status.CancelAsked), conditionsJSON, nullableTimeText(s, operation.StartedAt), nullableTimeText(s, operation.FinishedAt),
		operation.WorkspaceID, string(operation.ID), expectedRevision)
	if err != nil {
		return fmt.Errorf("update Operation: %w", err)
	}
	if err := s.rowsAffected(result, resource.ErrConflict); err != nil {
		return err
	}
	for _, target := range operation.Status.Targets {
		output, err := s.encryptTargetOutput(target.Result)
		if err != nil {
			return fmt.Errorf("encrypt Operation target output: %w", err)
		}
		result, err := tx.ExecContext(ctx, s.bind(`UPDATE operation_targets SET state = ?, exit_code = ?, output = ?, error_class = ?
			WHERE operation_id = ? AND host_id = ?`), string(target.State), nullableInt(target.Result, func(r *resource.TargetResult) *int { return r.ExitCode }),
			output, targetErrorClass(target.Result), string(operation.ID), string(target.Spec.HostID))
		if err != nil {
			return fmt.Errorf("update Operation target: %w", err)
		}
		if err := s.rowsAffected(result, resource.ErrConflict); err != nil {
			return err
		}
	}
	if err := s.audit(ctx, tx, operation.WorkspaceID, domain.AuditOperationUpdated, domain.AuditTargetOperation,
		string(operation.ID), map[string]string{"state": string(operation.Status.State)}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Operation update: %w", err)
	}
	return nil
}

func sameOperationSpec(a, b resource.OperationSpec) bool {
	x, errX := json.Marshal(a)
	y, errY := json.Marshal(b)
	return errX == nil && errY == nil && string(x) == string(y)
}

func nullableInt(result *resource.TargetResult, value func(*resource.TargetResult) *int) any {
	if result == nil || value(result) == nil {
		return nil
	}
	return *value(result)
}
func (s *Store) encryptTargetOutput(result *resource.TargetResult) (string, error) {
	if result == nil || result.Output == "" {
		return "", nil
	}
	return crypto.Encrypt(result.Output, s.key)
}
func targetErrorClass(result *resource.TargetResult) string {
	if result == nil {
		return ""
	}
	return result.ErrorClass
}

func nullableTimeText(s *Store, value *time.Time) any {
	if value == nil {
		return nil
	}
	return s.nowText(*value)
}

func nullablePositiveID(value int) any {
	if value <= 0 {
		return nil
	}
	return value
}

func rollback(tx *sql.Tx, logger *slog.Logger, operation string) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		logger.Warn("rollback persistence transaction", "operation", operation, "error", err)
	}
}

func isUniqueError(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique") || strings.Contains(text, "duplicate key")
}

var _ resource.HostRepository = (*Store)(nil)
var _ resource.OperationRepository = (*Store)(nil)
var _ = crypto.Encrypt
