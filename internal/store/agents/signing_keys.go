package agents

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/store/sqlx"
)

var _ agentgateway.SigningKeyStore = (*Store)(nil)

const signingKeyColumns = `id, state, public_key, secret_id, secret_version, origin, created_by, created_at_ns, activated_at_ns, retired_at_ns`

func scanSigningKey(row interface{ Scan(...any) error }) (agentgateway.SigningKey, error) {
	var key agentgateway.SigningKey
	var public string
	var created, activated, retired int64
	if err := row.Scan(&key.ID, &key.State, &public, &key.SecretID, &key.SecretVersion, &key.Origin, &key.CreatedBy,
		&created, &activated, &retired); err != nil {
		return key, err
	}
	raw, err := base64.StdEncoding.DecodeString(public)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return key, fmt.Errorf("stored signing key %s has an invalid public key", key.ID)
	}
	key.PublicKey = ed25519.PublicKey(raw)
	key.CreatedAt = time.Unix(0, created).UTC()
	if activated > 0 {
		t := time.Unix(0, activated).UTC()
		key.ActivatedAt = &t
	}
	if retired > 0 {
		t := time.Unix(0, retired).UTC()
		key.RetiredAt = &t
	}
	return key, nil
}

// ListSigningKeys implements agentgateway.SigningKeyStore.
func (s *Store) ListSigningKeys(ctx context.Context) ([]agentgateway.SigningKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+signingKeyColumns+` FROM agent_signing_keys ORDER BY created_at_ns DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list signing keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var keys []agentgateway.SigningKey
	for rows.Next() {
		key, err := scanSigningKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// CreateSigningKey implements agentgateway.SigningKeyStore. The partial
// unique indexes refuse a second active or next key, also between replicas.
func (s *Store) CreateSigningKey(ctx context.Context, key agentgateway.SigningKey, actor domain.Principal) error {
	if key.State != agentgateway.SigningKeyActive && key.State != agentgateway.SigningKeyNext {
		return fmt.Errorf("%w: a new signing key is active or next", domain.ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin signing key: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "signing key")
	var activated int64
	if key.State == agentgateway.SigningKeyActive {
		activated = key.CreatedAt.UnixNano()
	}
	public := base64.StdEncoding.EncodeToString(key.PublicKey)
	if _, err := tx.ExecContext(ctx, s.dialect.Bind(`INSERT INTO agent_signing_keys (id, state, public_key, secret_id, secret_version,
		origin, created_by, created_at_ns, activated_at_ns) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		key.ID, string(key.State), public, key.SecretID, key.SecretVersion, string(key.Origin), actor.UserID,
		key.CreatedAt.UnixNano(), activated); err != nil {
		if sqlx.IsUniqueViolation(err) {
			if key.State == agentgateway.SigningKeyNext {
				return agentgateway.ErrSigningKeyStaged
			}
			return agentgateway.ErrActiveSigningKeyExists
		}
		return fmt.Errorf("create signing key: %w", err)
	}
	if err := s.audit(ctx, tx, domain.AuditEvent{WorkspaceID: domain.DefaultWorkspaceID, ActorUserID: actor.UserID,
		Action: domain.AuditAgentSigningKeyCreated, TargetType: domain.AuditTargetSigningKey, TargetID: key.ID,
		Details: map[string]string{"state": string(key.State), "origin": string(key.Origin), "public_key": public}}); err != nil {
		return err
	}
	return tx.Commit()
}

// ActivateSigningKey implements agentgateway.SigningKeyStore.
func (s *Store) ActivateSigningKey(ctx context.Context, actor domain.Principal, now time.Time) (agentgateway.SigningKey, error) {
	return s.transition(ctx, actor, now, true)
}

// DiscardSigningKey implements agentgateway.SigningKeyStore.
func (s *Store) DiscardSigningKey(ctx context.Context, actor domain.Principal, now time.Time) (agentgateway.SigningKey, error) {
	return s.transition(ctx, actor, now, false)
}

// transition activates (retiring the active key) or discards the next key.
func (s *Store) transition(ctx context.Context, actor domain.Principal, now time.Time, activate bool) (agentgateway.SigningKey, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return agentgateway.SigningKey{}, fmt.Errorf("begin signing key change: %w", err)
	}
	defer sqlx.Rollback(tx, s.logger, "signing key change")
	next, err := scanSigningKey(tx.QueryRowContext(ctx, s.dialect.Bind(`SELECT `+signingKeyColumns+` FROM agent_signing_keys WHERE state = ?`),
		string(agentgateway.SigningKeyNext)))
	if errors.Is(err, sql.ErrNoRows) {
		return agentgateway.SigningKey{}, agentgateway.ErrNoStagedSigningKey
	}
	if err != nil {
		return agentgateway.SigningKey{}, fmt.Errorf("read next signing key: %w", err)
	}
	at := now.UnixNano()
	action := domain.AuditAgentSigningKeyDiscarded
	if activate {
		action = domain.AuditAgentSigningKeyActivated
		// Retire first: the unique index allows one active key.
		if _, err := tx.ExecContext(ctx, s.dialect.Bind(`UPDATE agent_signing_keys SET state = ?, retired_at_ns = ? WHERE state = ?`),
			string(agentgateway.SigningKeyRetired), at, string(agentgateway.SigningKeyActive)); err != nil {
			return agentgateway.SigningKey{}, fmt.Errorf("retire active signing key: %w", err)
		}
		if _, err := tx.ExecContext(ctx, s.dialect.Bind(`UPDATE agent_signing_keys SET state = ?, activated_at_ns = ? WHERE id = ? AND state = ?`),
			string(agentgateway.SigningKeyActive), at, next.ID, string(agentgateway.SigningKeyNext)); err != nil {
			return agentgateway.SigningKey{}, fmt.Errorf("activate signing key: %w", err)
		}
		next.State = agentgateway.SigningKeyActive
		t := now.UTC()
		next.ActivatedAt = &t
	} else {
		if _, err := tx.ExecContext(ctx, s.dialect.Bind(`UPDATE agent_signing_keys SET state = ?, retired_at_ns = ? WHERE id = ? AND state = ?`),
			string(agentgateway.SigningKeyRetired), at, next.ID, string(agentgateway.SigningKeyNext)); err != nil {
			return agentgateway.SigningKey{}, fmt.Errorf("discard signing key: %w", err)
		}
		next.State = agentgateway.SigningKeyRetired
		t := now.UTC()
		next.RetiredAt = &t
	}
	if err := s.audit(ctx, tx, domain.AuditEvent{WorkspaceID: domain.DefaultWorkspaceID, ActorUserID: actor.UserID,
		Action: action, TargetType: domain.AuditTargetSigningKey, TargetID: next.ID,
		Details: map[string]string{"public_key": base64.StdEncoding.EncodeToString(next.PublicKey)}}); err != nil {
		return agentgateway.SigningKey{}, err
	}
	if err := tx.Commit(); err != nil {
		return agentgateway.SigningKey{}, fmt.Errorf("commit signing key change: %w", err)
	}
	return next, nil
}
