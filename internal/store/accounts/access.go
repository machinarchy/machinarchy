package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/store/sqlx"
)

// ---- workspace roles and API tokens ----------------------------------------

// GetWorkspaceRole returns a member's role.
func (s *Store) GetWorkspaceRole(ctx context.Context, userID, workspaceID int) (domain.Role, error) {
	var role string
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT role FROM workspace_memberships WHERE user_id = ? AND workspace_id = ?`),
		userID, workspaceID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.ErrMembershipNotFound
	}
	if err != nil {
		return "", fmt.Errorf("query workspace membership: %w", err)
	}
	if !domain.ValidRole(domain.Role(role)) {
		return "", fmt.Errorf("invalid role %q in workspace membership", role)
	}
	return domain.Role(role), nil
}

// ResolveAPIToken returns the principal of a live token. The effective role
// is the lesser of the token's role and the owner's current membership.
func (s *Store) ResolveAPIToken(ctx context.Context, tokenHash string) (*domain.Principal, error) {
	if len(tokenHash) != 64 {
		return nil, domain.ErrAPITokenNotFound
	}
	var principal domain.Principal
	var tokenRole, memberRole string
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT t.id, t.user_id, t.workspace_id, t.role, u.username, m.role
		FROM api_tokens AS t
		JOIN users AS u ON u.id = t.user_id
		JOIN workspace_memberships AS m ON m.user_id = t.user_id AND m.workspace_id = t.workspace_id
		WHERE t.token_hash = ? AND t.revoked_at IS NULL AND t.expires_at > ?`), tokenHash, sqlx.Time(time.Now())).
		Scan(&principal.APITokenID, &principal.UserID, &principal.WorkspaceID, &tokenRole, &principal.Username, &memberRole)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrAPITokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve API token: %w", err)
	}
	principal.Role = domain.EffectiveRole(domain.Role(tokenRole), domain.Role(memberRole))
	if !domain.ValidRole(principal.Role) {
		return nil, domain.ErrAPITokenNotFound
	}
	return &principal, nil
}

// ListAPITokens lists a user's tokens in a workspace, newest first.
func (s *Store) ListAPITokens(ctx context.Context, userID, workspaceID int) ([]domain.APIToken, error) {
	if userID <= 0 || workspaceID <= 0 {
		return nil, fmt.Errorf("%w: user and workspace IDs must be positive", domain.ErrInvalidInput)
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, user_id, workspace_id, name, role, created_at, expires_at, revoked_at
		FROM api_tokens WHERE user_id = ? AND workspace_id = ? ORDER BY id DESC`), userID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list API tokens: %w", err)
	}
	defer sqlx.CloseRows(rows, s.logger, "list API tokens")
	tokens := make([]domain.APIToken, 0)
	for rows.Next() {
		var token domain.APIToken
		var role, createdAt, expiresAt string
		var revokedAt sql.NullString
		if err := rows.Scan(&token.ID, &token.UserID, &token.WorkspaceID, &token.Name, &role, &createdAt, &expiresAt, &revokedAt); err != nil {
			return nil, fmt.Errorf("scan API token: %w", err)
		}
		token.Role = domain.Role(role)
		if token.CreatedAt, err = sqlx.ParseTime(createdAt); err != nil {
			return nil, err
		}
		expiry, err := sqlx.ParseTime(expiresAt)
		if err != nil {
			return nil, err
		}
		token.ExpiresAt = &expiry
		if token.RevokedAt, err = sqlx.ParseNullTime(revokedAt); err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate API tokens: %w", err)
	}
	return tokens, nil
}

// CreateAPIToken stores a token hash. Only its name, role and expiry reach the
// audit trail; the secret and its hash never do.
func (s *Store) CreateAPIToken(ctx context.Context, token *domain.APIToken) error {
	if token == nil || token.UserID <= 0 || token.WorkspaceID <= 0 ||
		(token.Role != domain.RoleOperator && token.Role != domain.RoleReader) || len(token.TokenHash) != 64 {
		return fmt.Errorf("%w: invalid API token record", domain.ErrInvalidInput)
	}
	if token.CreatedAt.IsZero() || token.ExpiresAt == nil || token.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: API token timestamps are required", domain.ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create API token transaction: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "create API token")
	id, err := insertID(ctx, tx, s.bind(`INSERT INTO api_tokens (user_id, workspace_id, name, role, token_hash, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`), token.UserID, token.WorkspaceID, token.Name, string(token.Role), token.TokenHash,
		sqlx.Time(token.CreatedAt), sqlx.Time(*token.ExpiresAt))
	if err != nil {
		if sqlx.IsUniqueViolation(err) {
			return fmt.Errorf("%w: %w", domain.ErrAPITokenAlreadyExists, err)
		}
		return fmt.Errorf("insert API token: %w", err)
	}
	if err := s.appendAudit(ctx, tx, domain.AuditEvent{
		WorkspaceID: token.WorkspaceID, Action: domain.AuditAPITokenCreated,
		TargetType: domain.AuditTargetAPIToken, TargetID: fmt.Sprint(id),
		Details: map[string]string{"name": token.Name, "role": string(token.Role),
			"expires_at": token.ExpiresAt.UTC().Format(time.RFC3339)},
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create API token transaction: %w", err)
	}
	token.ID = id
	return nil
}

// RevokeAPIToken revokes a live token owned by userID.
func (s *Store) RevokeAPIToken(ctx context.Context, id, userID, workspaceID int) error {
	if id <= 0 || userID <= 0 || workspaceID <= 0 {
		return fmt.Errorf("%w: token, user, and workspace IDs must be positive", domain.ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin revoke API token transaction: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "revoke API token")
	var name string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT name FROM api_tokens
		WHERE id = ? AND user_id = ? AND workspace_id = ? AND revoked_at IS NULL`), id, userID, workspaceID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrAPITokenNotFound
	}
	if err != nil {
		return fmt.Errorf("read API token before revoke: %w", err)
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE api_tokens SET revoked_at = ?
		WHERE id = ? AND user_id = ? AND workspace_id = ? AND revoked_at IS NULL`), sqlx.Time(time.Now()), id, userID, workspaceID)
	if err != nil {
		return fmt.Errorf("revoke API token: %w", err)
	}
	if err := sqlx.RowsAffected(result, domain.ErrAPITokenNotFound); err != nil {
		return err
	}
	if err := s.appendAudit(ctx, tx, domain.AuditEvent{
		WorkspaceID: workspaceID, Action: domain.AuditAPITokenRevoked,
		TargetType: domain.AuditTargetAPIToken, TargetID: fmt.Sprint(id),
		Details: map[string]string{"name": name},
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit revoke API token transaction: %w", err)
	}
	return nil
}

// ---- environments ----------------------------------------------------------

// CreateEnvironment inserts an environment; names are unique per workspace
// ignoring case.
func (s *Store) CreateEnvironment(ctx context.Context, environment *domain.Environment) error {
	if environment == nil || environment.WorkspaceID <= 0 {
		return fmt.Errorf("%w: environment and workspace are required", domain.ErrInvalidInput)
	}
	now := time.Now().UTC()
	id, err := insertID(ctx, s.db, s.bind(`INSERT INTO environments (workspace_id, name, description, protected, created_at) VALUES (?, ?, ?, ?, ?)`),
		environment.WorkspaceID, environment.Name, environment.Description, environment.Protected, sqlx.Time(now))
	if err != nil {
		if sqlx.IsUniqueViolation(err) {
			return fmt.Errorf("%w: %w", domain.ErrEnvironmentAlreadyExists, err)
		}
		return fmt.Errorf("insert environment: %w", err)
	}
	environment.ID, environment.CreatedAt = id, now
	return nil
}

func scanEnvironment(row interface{ Scan(...any) error }) (*domain.Environment, error) {
	environment := new(domain.Environment)
	var createdAt string
	if err := row.Scan(&environment.ID, &environment.WorkspaceID, &environment.Name, &environment.Description, &environment.Protected, &createdAt); err != nil {
		return nil, err
	}
	var err error
	if environment.CreatedAt, err = sqlx.ParseTime(createdAt); err != nil {
		return nil, err
	}
	return environment, nil
}

// GetEnvironmentByID returns one environment of a workspace.
func (s *Store) GetEnvironmentByID(ctx context.Context, workspaceID, id int) (*domain.Environment, error) {
	if workspaceID <= 0 {
		return nil, fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	environment, err := scanEnvironment(s.db.QueryRowContext(ctx, s.bind(`SELECT id, workspace_id, name, description, protected, created_at
		FROM environments WHERE id = ? AND workspace_id = ?`), id, workspaceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrEnvironmentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query environment %d: %w", id, err)
	}
	return environment, nil
}

// ListEnvironments lists a workspace's environments by ID.
func (s *Store) ListEnvironments(ctx context.Context, workspaceID int) ([]*domain.Environment, error) {
	if workspaceID <= 0 {
		return nil, fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, workspace_id, name, description, protected, created_at
		FROM environments WHERE workspace_id = ? ORDER BY id`), workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	defer sqlx.CloseRows(rows, s.logger, "list environments")
	environments := make([]*domain.Environment, 0)
	for rows.Next() {
		environment, err := scanEnvironment(rows)
		if err != nil {
			return nil, fmt.Errorf("scan environment: %w", err)
		}
		environments = append(environments, environment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate environments: %w", err)
	}
	return environments, nil
}

// UpdateEnvironment renames, re-describes, protects or unprotects an
// environment. A change of the protection is audited in the same transaction.
func (s *Store) UpdateEnvironment(ctx context.Context, environment *domain.Environment) error {
	if environment == nil || environment.WorkspaceID <= 0 {
		return fmt.Errorf("%w: environment and workspace are required", domain.ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin update environment transaction: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "update environment")
	var current string
	var wasProtected bool
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT name, protected FROM environments WHERE id = ? AND workspace_id = ?`),
		environment.ID, environment.WorkspaceID).Scan(&current, &wasProtected); errors.Is(err, sql.ErrNoRows) {
		return domain.ErrEnvironmentNotFound
	} else if err != nil {
		return fmt.Errorf("read environment %d: %w", environment.ID, err)
	}
	if strings.EqualFold(current, domain.DefaultEnvironmentName) && !strings.EqualFold(environment.Name, domain.DefaultEnvironmentName) {
		return domain.ErrDefaultEnvironment
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE environments SET name = ?, description = ?, protected = ? WHERE id = ? AND workspace_id = ?`),
		environment.Name, environment.Description, environment.Protected, environment.ID, environment.WorkspaceID)
	if err != nil {
		if sqlx.IsUniqueViolation(err) {
			return fmt.Errorf("%w: %w", domain.ErrEnvironmentAlreadyExists, err)
		}
		return fmt.Errorf("update environment %d: %w", environment.ID, err)
	}
	if err := sqlx.RowsAffected(result, domain.ErrEnvironmentNotFound); err != nil {
		return err
	}
	if wasProtected != environment.Protected {
		if err := s.appendAudit(ctx, tx, domain.AuditEvent{WorkspaceID: environment.WorkspaceID, Action: domain.AuditEnvironmentProtection,
			TargetType: domain.AuditTargetEnvironment, TargetID: fmt.Sprint(environment.ID),
			Details: map[string]string{"name": environment.Name, "protected": fmt.Sprint(environment.Protected)}}); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit update environment transaction: %w", err)
	}
	return nil
}

// DeleteEnvironment removes an environment.
func (s *Store) DeleteEnvironment(ctx context.Context, workspaceID, id int) error {
	if workspaceID <= 0 {
		return fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	var name string
	if err := s.db.QueryRowContext(ctx, s.bind(`SELECT name FROM environments WHERE id = ? AND workspace_id = ?`),
		id, workspaceID).Scan(&name); errors.Is(err, sql.ErrNoRows) {
		return domain.ErrEnvironmentNotFound
	} else if err != nil {
		return fmt.Errorf("read environment %d: %w", id, err)
	}
	if strings.EqualFold(name, domain.DefaultEnvironmentName) {
		return domain.ErrDefaultEnvironment
	}
	var hosts int
	if err := s.db.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM managed_hosts WHERE workspace_id = ? AND environment_id = ?`),
		workspaceID, id).Scan(&hosts); err != nil {
		return fmt.Errorf("count hosts of environment %d: %w", id, err)
	}
	if hosts > 0 {
		return domain.ErrEnvironmentInUse
	}
	result, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM environments WHERE id = ? AND workspace_id = ?`), id, workspaceID)
	if err != nil {
		return fmt.Errorf("delete environment %d: %w", id, err)
	}
	return sqlx.RowsAffected(result, domain.ErrEnvironmentNotFound)
}

// ---- audit trail -------------------------------------------------------------

// auditLockClass namespaces the PostgreSQL advisory lock that serialises
// appends per workspace ("AU").
const auditLockClass = 0x4155

// AppendAudit records event inside tx, so it commits or rolls back with the
// change it describes. The managed-resource store uses it too.
func (s *Store) AppendAudit(ctx context.Context, tx *sql.Tx, event domain.AuditEvent) error {
	return s.appendAudit(ctx, tx, event)
}

// appendAudit links event to the workspace's hash chain. The actor comes from
// ctx (domain.WithAuditActor); without one the event is the system's. Appends
// are serialised per workspace: by an advisory lock on PostgreSQL, and by the
// single connection on SQLite.
func (s *Store) appendAudit(ctx context.Context, tx *sql.Tx, event domain.AuditEvent) error {
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
	if s.dialect == sqlx.PostgreSQL {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, auditLockClass, event.WorkspaceID); err != nil {
			return fmt.Errorf("lock audit chain: %w", err)
		}
	}
	var prev string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT hash FROM audit_events WHERE workspace_id = ? ORDER BY id DESC LIMIT 1`),
		event.WorkspaceID).Scan(&prev)
	if errors.Is(err, sql.ErrNoRows) {
		// No retained events: the chain continues from the newest prune
		// checkpoint, or starts at genesis if nothing was ever pruned.
		checkpoint, cerr := s.latestAuditCheckpoint(ctx, tx, event.WorkspaceID)
		switch {
		case cerr != nil:
			return cerr
		case checkpoint != nil:
			prev = checkpoint.LastHash
		default:
			prev = domain.AuditGenesisHash
		}
	} else if err != nil {
		return fmt.Errorf("read audit chain head: %w", err)
	}
	occurredAt := sqlx.Time(time.Now())
	hash := domain.ComputeAuditHash(prev, occurredAt, event)
	encoded, err := json.Marshal(event.Details)
	if err != nil {
		return fmt.Errorf("marshal audit details: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO audit_events (workspace_id, occurred_at, actor_user_id,
		actor_username, actor_token_id, request_id, source, action, target_type, target_id, outcome,
		details, prev_hash, hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		event.WorkspaceID, occurredAt, event.ActorUserID, event.ActorUsername, event.ActorTokenID,
		event.RequestID, event.Source, event.Action, event.TargetType, event.TargetID, event.Outcome,
		string(encoded), prev, hash); err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

const auditColumns = `id, workspace_id, occurred_at, actor_user_id, actor_username, actor_token_id,
	request_id, source, action, target_type, target_id, outcome, details, prev_hash, hash`

func scanAuditEvent(row interface{ Scan(...any) error }) (domain.StoredAuditEvent, error) {
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

// ListAuditEvents lists a workspace's events newest first.
func (s *Store) ListAuditEvents(ctx context.Context, workspaceID int, filter domain.AuditFilter) ([]domain.AuditEvent, error) {
	filter, err := domain.NormalizeAuditFilter(filter)
	if err != nil {
		return nil, err
	}
	query := `SELECT ` + auditColumns + ` FROM audit_events WHERE workspace_id = ?`
	args := []any{workspaceID}
	if filter.Action != "" {
		query += ` AND action = ?`
		args = append(args, filter.Action)
	}
	if filter.BeforeID > 0 {
		query += ` AND id < ?`
		args = append(args, filter.BeforeID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer sqlx.CloseRows(rows, s.logger, "list audit events")
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

// VerifyAuditChain walks a workspace's chain in pages of 1000.
func (s *Store) VerifyAuditChain(ctx context.Context, workspaceID int) (*domain.AuditVerification, error) {
	checkpoint, err := s.latestAuditCheckpoint(ctx, s.db, workspaceID)
	if err != nil {
		return nil, err
	}
	verifier := domain.NewAuditChainVerifier()
	var after int64
	if checkpoint != nil {
		verifier = domain.NewAuditChainVerifierFrom(checkpoint.LastHash)
		after = checkpoint.LastEventID
	}
	for {
		done, err := s.verifyPage(ctx, s.db, workspaceID, &after, verifier)
		if err != nil {
			return nil, err
		}
		if done {
			result := verifier.Result()
			result.Checkpoint = checkpoint
			return &result, nil
		}
	}
}

func (s *Store) verifyPage(ctx context.Context, q rowQuerier, workspaceID int, after *int64, verifier *domain.AuditChainVerifier) (bool, error) {
	const pageSize = 1000
	rows, err := q.QueryContext(ctx, s.bind(`SELECT `+auditColumns+` FROM audit_events
		WHERE workspace_id = ? AND id > ? ORDER BY id LIMIT ?`), workspaceID, *after, pageSize)
	if err != nil {
		return false, fmt.Errorf("read audit events for verification: %w", err)
	}
	defer sqlx.CloseRows(rows, s.logger, "verify audit chain")
	count := 0
	for rows.Next() {
		stored, err := scanAuditEvent(rows)
		if err != nil {
			return false, err
		}
		count++
		*after = stored.ID
		if !verifier.Add(stored) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate audit events: %w", err)
	}
	return count < pageSize, nil
}
