// Package management persists the global runtime configuration and encrypted
// Secret Store records using the selected Machinarchy SQL database.
package management

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/eventing"
	"github.com/machinarchy/machinarchy/internal/runtimeconfig"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/utils"
)

type Dialect uint8

const (
	SQLite Dialect = iota + 1
	PostgreSQL
)

type Store struct {
	db      *sql.DB
	dialect Dialect
	logger  *slog.Logger
}

func New(db *sql.DB, d Dialect, logger *slog.Logger) (*Store, error) {
	if db == nil || (d != SQLite && d != PostgreSQL) {
		return nil, errors.New("management store requires SQL database and supported dialect")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{db: db, dialect: d, logger: logger}, nil
}
func (s *Store) bind(q string) string {
	if s.dialect == SQLite {
		return q
	}
	var b strings.Builder
	n := 1
	for _, r := range q {
		if r == '?' {
			fmt.Fprintf(&b, "$%d", n)
			n++
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func (s *Store) Seed(ctx context.Context, c runtimeconfig.Config) (runtimeconfig.Revision, error) {
	if err := c.Validate(); err != nil {
		return runtimeconfig.Revision{}, err
	}
	doc, err := json.Marshal(c)
	if err != nil {
		return runtimeconfig.Revision{}, err
	}
	sum, err := c.Checksum()
	if err != nil {
		return runtimeconfig.Revision{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return runtimeconfig.Revision{}, err
	}
	defer utils.RollbackTx(tx, s.logger, "Seed")
	var rev uint64
	err = tx.QueryRowContext(ctx, s.bind("SELECT revision FROM runtime_config_head WHERE id = 1")).Scan(&rev)
	if err == nil {
		var r runtimeconfig.Revision
		err = scanRevision(tx.QueryRowContext(ctx, s.bind("SELECT revision,schema_version,document,checksum,created_at,created_by,change_note FROM runtime_config_revisions WHERE revision = ?"), rev), &r)
		if err != nil {
			return r, err
		}
		if e := tx.Commit(); e != nil {
			return r, e
		}
		return r, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return runtimeconfig.Revision{}, err
	}
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, s.bind("INSERT INTO runtime_config_revisions(revision,schema_version,document,checksum,created_at,created_by,change_note) VALUES(1,?,?,?,?,0,'bootstrap defaults')"), c.SchemaVersion, string(doc), sum, ts(now)); err != nil {
		return runtimeconfig.Revision{}, err
	}
	if _, err = tx.ExecContext(ctx, s.bind("INSERT INTO runtime_config_head(id,revision) VALUES(1,1)")); err != nil {
		return runtimeconfig.Revision{}, err
	}
	if err = tx.Commit(); err != nil {
		return runtimeconfig.Revision{}, err
	}
	return runtimeconfig.Revision{Revision: 1, SchemaVersion: c.SchemaVersion, Config: c, Checksum: sum, CreatedAt: now, ChangeNote: "bootstrap defaults"}, nil
}
func (s *Store) Current(ctx context.Context) (runtimeconfig.Revision, error) {
	var r runtimeconfig.Revision
	err := scanRevision(s.db.QueryRowContext(ctx, s.bind(`SELECT r.revision,r.schema_version,r.document,r.checksum,r.created_at,r.created_by,r.change_note FROM runtime_config_head h JOIN runtime_config_revisions r ON r.revision=h.revision WHERE h.id=1`)), &r)
	return r, err
}
func (s *Store) GetRevision(ctx context.Context, revision uint64) (runtimeconfig.Revision, error) {
	var r runtimeconfig.Revision
	err := scanRevision(s.db.QueryRowContext(ctx, s.bind("SELECT revision,schema_version,document,checksum,created_at,created_by,change_note FROM runtime_config_revisions WHERE revision=?"), revision), &r)
	return r, err
}
func (s *Store) ListRevisions(ctx context.Context, limit int, before uint64) ([]runtimeconfig.Revision, error) {
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("revision list limit must be between 1 and 200")
	}
	q := `SELECT revision,schema_version,document,checksum,created_at,created_by,change_note FROM runtime_config_revisions`
	args := []any{}
	if before > 0 {
		q += ` WHERE revision < ?`
		args = append(args, before)
	}
	q += ` ORDER BY revision DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.bind(q), args...)
	if err != nil {
		return nil, err
	}
	defer utils.CloseRows(rows, s.logger, "query revision list")
	out := make([]runtimeconfig.Revision, 0, limit)
	for rows.Next() {
		var r runtimeconfig.Revision
		if err = scanRevision(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) Publish(ctx context.Context, expected uint64, c runtimeconfig.Config, actor int, note string) (runtimeconfig.Revision, error) {
	if err := c.Validate(); err != nil {
		return runtimeconfig.Revision{}, err
	}
	if len(note) > 512 {
		return runtimeconfig.Revision{}, fmt.Errorf("change note exceeds 512 characters")
	}
	doc, err := json.Marshal(c)
	if err != nil {
		return runtimeconfig.Revision{}, err
	}
	sum, err := c.Checksum()
	if err != nil {
		return runtimeconfig.Revision{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return runtimeconfig.Revision{}, err
	}
	defer utils.RollbackTx(tx, s.logger, "Publish")

	if s.dialect == PostgreSQL {
		var lock uint64
		if err = tx.QueryRowContext(ctx, s.bind("SELECT revision FROM runtime_config_head WHERE id=1 FOR UPDATE")).Scan(&lock); err != nil {
			return runtimeconfig.Revision{}, err
		}
	}
	var current uint64
	if err = tx.QueryRowContext(ctx, s.bind("SELECT revision FROM runtime_config_head WHERE id=1")).Scan(&current); err != nil {
		return runtimeconfig.Revision{}, err
	}
	if current != expected {
		return runtimeconfig.Revision{}, runtimeconfig.ErrConflict
	}
	next := current + 1
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, s.bind("INSERT INTO runtime_config_revisions(revision,schema_version,document,checksum,created_at,created_by,change_note) VALUES(?,?,?,?,?,?,?)"), next, c.SchemaVersion, string(doc), sum, ts(now), actor, note); err != nil {
		return runtimeconfig.Revision{}, err
	}
	res, err := tx.ExecContext(ctx, s.bind("UPDATE runtime_config_head SET revision=? WHERE id=1 AND revision=?"), next, expected)
	if err != nil {
		return runtimeconfig.Revision{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return runtimeconfig.Revision{}, fmt.Errorf("read updated configuration head count: %w", err)
	}
	if n != 1 {
		return runtimeconfig.Revision{}, runtimeconfig.ErrConflict
	}
	if err = s.audit(ctx, tx, actor, "runtime_config.published", "runtime_config", fmt.Sprint(next), map[string]any{"revision": next, "schemaVersion": c.SchemaVersion, "checksum": sum}); err != nil {
		return runtimeconfig.Revision{}, err
	}
	if err = s.configEvent(ctx, tx, next, now, actor); err != nil {
		return runtimeconfig.Revision{}, err
	}
	if err = tx.Commit(); err != nil {
		return runtimeconfig.Revision{}, err
	}
	return runtimeconfig.Revision{Revision: next, SchemaVersion: c.SchemaVersion, Config: c, Checksum: sum, CreatedAt: now, CreatedBy: actor, ChangeNote: note}, nil
}
func (s *Store) configEvent(ctx context.Context, tx *sql.Tx, rev uint64, now time.Time, actor int) error {
	eventID, err := resource.NewResourceID()
	if err != nil {
		return err
	}
	subjectID := resource.ID("00000000-0000-4000-8000-000000000001")
	payload, err := json.Marshal(map[string]uint64{"revision": rev})
	if err != nil {
		return fmt.Errorf("marshal revision payload: %w", err)
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO outbox_events(event_id,event_type,schema_version,occurred_at,producer,workspace_id,subject_kind,subject_id,subject_generation,subject_revision,correlation_id,causation_id,actor_user_id,actor_token_id,payload,created_at) VALUES(?,?,?,?,?,1,?,?,?,?,?,?,?,?,?,?)`), string(eventID), eventing.ConfigurationChanged, 1, ts(now), "machinarchy.configuration", "runtime-config", string(subjectID), rev, rev, "", "", actor, nil, string(payload), ts(now))
	if err != nil {
		return err
	}
	nowNS := now.UTC().UnixNano()
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO outbox_deliveries(event_id,consumer_group,attempts,available_at_ns,delivered_at_ns) VALUES(?,?,0,?,?)`), string(eventID), "runtime-config-notification", nowNS, nowNS)
	return err
}

func (s *Store) RecordProcessStatus(ctx context.Context, v runtimeconfig.ProcessStatus) error {
	if v.ProcessID == "" || v.Role == "" {
		return errors.New("process status identity is required")
	}
	var success any
	if v.LastSuccess != nil {
		success = ts(*v.LastSuccess)
	}
	_, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO runtime_config_process_status(process_id,role,applied_revision,attempted_revision,last_success,last_error_class,heartbeat) VALUES(?,?,?,?,?,?,?) ON CONFLICT(process_id) DO UPDATE SET role=excluded.role,applied_revision=excluded.applied_revision,attempted_revision=excluded.attempted_revision,last_success=COALESCE(excluded.last_success,runtime_config_process_status.last_success),last_error_class=excluded.last_error_class,heartbeat=excluded.heartbeat`), v.ProcessID, v.Role, v.AppliedRevision, v.AttemptedRevision, success, v.LastErrorClass, ts(v.Heartbeat))
	return err
}
func (s *Store) ProcessStatuses(ctx context.Context, since time.Time) ([]runtimeconfig.ProcessStatus, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT process_id,role,applied_revision,attempted_revision,last_success,last_error_class,heartbeat FROM runtime_config_process_status WHERE heartbeat>=? ORDER BY role,process_id`), ts(since))
	if err != nil {
		return nil, err
	}
	defer utils.CloseRows(rows, s.logger, "query process statuses")

	out := []runtimeconfig.ProcessStatus{}
	for rows.Next() {
		var v runtimeconfig.ProcessStatus
		var success sql.NullString
		var heartbeat string
		if err = rows.Scan(&v.ProcessID, &v.Role, &v.AppliedRevision, &v.AttemptedRevision, &success, &v.LastErrorClass, &heartbeat); err != nil {
			return nil, err
		}
		//nolint:errcheck // Timestamp is generated internally in RFC3339Nano format.
		v.Heartbeat, _ = time.Parse(time.RFC3339Nano, heartbeat)
		if success.Valid {
			//nolint:errcheck // Timestamp is generated internally in RFC3339Nano format.
			t, _ := time.Parse(time.RFC3339Nano, success.String)
			v.LastSuccess = &t
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) ConfigChangedAfter(ctx context.Context, revision uint64) (bool, error) {
	var changed bool
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT EXISTS(SELECT 1 FROM outbox_events WHERE event_type=? AND subject_kind='runtime-config' AND subject_revision>?)`), eventing.ConfigurationChanged, revision).Scan(&changed)
	return changed, err
}

type scanner interface{ Scan(...any) error }

func scanRevision(row scanner, r *runtimeconfig.Revision) error {
	var doc, created string
	if err := row.Scan(&r.Revision, &r.SchemaVersion, &doc, &r.Checksum, &created, &r.CreatedBy, &r.ChangeNote); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return runtimeconfig.ErrNotFound
		}
		return err
	}
	if err := json.Unmarshal([]byte(doc), &r.Config); err != nil {
		return fmt.Errorf("decode runtime configuration revision %d: %w", r.Revision, err)
	}
	//nolint:errcheck // Timestamp is generated internally in RFC3339Nano format.
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return nil
}
func (s *Store) audit(ctx context.Context, tx *sql.Tx, actor int, action, targetType, target string, details map[string]any) error {
	b, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.bind("INSERT INTO management_audit_events(occurred_at,actor_user_id,action,target_type,target_id,details) VALUES(?,?,?,?,?,?)"), ts(time.Now()), actor, action, targetType, target, string(b))
	return err
}

func (s *Store) CreateSecret(ctx context.Context, m secrets.Metadata, v secrets.StoredVersion) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer utils.RollbackTx(tx, s.logger, "create secret")
	if _, err = tx.ExecContext(ctx, s.bind(`INSERT INTO secret_metadata(id,name,description,purpose,current_version,created_at,updated_at,created_by) VALUES(?,?,?,?,?,?,?,?)`), m.ID, m.Name, m.Description, string(m.Purpose), m.CurrentVersion, ts(m.CreatedAt), ts(m.UpdatedAt), v.CreatedBy); err != nil {
		return err
	}
	if err = s.insertSecretVersion(ctx, tx, v); err != nil {
		return err
	}
	if err = s.audit(ctx, tx, v.CreatedBy, "secret.created", "secret", m.ID, map[string]any{"version": v.Ref.Version}); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) AppendSecretVersion(ctx context.Context, id string, expected uint64, v secrets.StoredVersion) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer utils.RollbackTx(tx, s.logger, "append secret version")

	res, err := tx.ExecContext(ctx, s.bind(`UPDATE secret_metadata SET current_version=?,updated_at=? WHERE id=? AND current_version=? AND retired_at IS NULL`), v.Ref.Version, ts(v.CreatedAt), id, expected)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("get sv affected rows: %w", err)
	}
	if n != 1 {
		return secrets.ErrConflict
	}
	if err = s.insertSecretVersion(ctx, tx, v); err != nil {
		return err
	}
	if err = s.audit(ctx, tx, v.CreatedBy, "secret.rotated", "secret", id, map[string]any{"version": v.Ref.Version}); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) insertSecretVersion(ctx context.Context, tx *sql.Tx, v secrets.StoredVersion) error {
	_, err := tx.ExecContext(ctx, s.bind(`INSERT INTO secret_versions(secret_id,version,key_id,data_aad_version,data_aad_key_id,wrapped_dek,ciphertext,created_at,created_by) VALUES(?,?,?,?,?,?,?,?,?)`), v.Ref.ID, v.Ref.Version, v.Ciphertext.KeyID, v.Ciphertext.AADVersion, v.Ciphertext.DataAADKeyID, v.Ciphertext.WrappedDEK, v.Ciphertext.Value, ts(v.CreatedAt), v.CreatedBy)
	return err
}
func (s *Store) GetSecret(ctx context.Context, id string) (secrets.Metadata, error) {
	var m secrets.Metadata
	var created, updated string
	var retired sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id,name,description,purpose,current_version,created_at,updated_at,retired_at FROM secret_metadata WHERE id=?`), id).Scan(&m.ID, &m.Name, &m.Description, &m.Purpose, &m.CurrentVersion, &created, &updated, &retired)
	if errors.Is(err, sql.ErrNoRows) {
		return m, secrets.ErrNotFound
	}
	if err != nil {
		return m, err
	}
	//nolint:errcheck // Timestamp is generated internally in RFC3339Nano format.
	m.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	//nolint:errcheck // Timestamp is generated internally in RFC3339Nano format.
	m.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if retired.Valid {
		//nolint:errcheck // Timestamp is generated internally in RFC3339Nano format.
		t, _ := time.Parse(time.RFC3339Nano, retired.String)
		m.RetiredAt = &t
	}
	return m, nil
}
func (s *Store) ListSecrets(ctx context.Context, limit int) ([]secrets.Metadata, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id,name,description,purpose,current_version,created_at,updated_at,retired_at FROM secret_metadata ORDER BY name LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer utils.CloseRows(rows, s.logger, "query list secrets")
	out := []secrets.Metadata{}
	for rows.Next() {
		var m secrets.Metadata
		var created, updated string
		var retired sql.NullString
		if err = rows.Scan(&m.ID, &m.Name, &m.Description, &m.Purpose, &m.CurrentVersion, &created, &updated, &retired); err != nil {
			return nil, err
		}
		//nolint:errcheck // Timestamp is generated internally in RFC3339Nano format.
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		//nolint:errcheck // Timestamp is generated internally in RFC3339Nano format.
		m.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		if retired.Valid {
			//nolint:errcheck // Timestamp is generated internally in RFC3339Nano format.
			t, _ := time.Parse(time.RFC3339Nano, retired.String)
			m.RetiredAt = &t
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) GetSecretVersion(ctx context.Context, ref secrets.Ref) (secrets.StoredVersion, error) {
	var v secrets.StoredVersion
	var created string
	var retired sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT v.secret_id,v.version,v.created_at,v.created_by,v.retired_at,v.key_id,v.data_aad_version,v.data_aad_key_id,v.wrapped_dek,v.ciphertext FROM secret_versions v JOIN secret_metadata m ON m.id=v.secret_id WHERE v.secret_id=? AND v.version=? AND m.retired_at IS NULL AND v.retired_at IS NULL`), ref.ID, ref.Version).Scan(&v.Ref.ID, &v.Ref.Version, &created, &v.CreatedBy, &retired, &v.Ciphertext.KeyID, &v.Ciphertext.AADVersion, &v.Ciphertext.DataAADKeyID, &v.Ciphertext.WrappedDEK, &v.Ciphertext.Value)
	if errors.Is(err, sql.ErrNoRows) {
		return v, secrets.ErrNotFound
	}
	if err != nil {
		return v, err
	}
	//nolint:errcheck // Timestamp is generated internally in RFC3339Nano format.
	v.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if retired.Valid {
		//nolint:errcheck // Timestamp is generated internally in RFC3339Nano format.
		t, _ := time.Parse(time.RFC3339Nano, retired.String)
		v.RetiredAt = &t
	}
	return v, nil
}
func (s *Store) ListSecretVersions(ctx context.Context, id string) ([]secrets.Version, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT version,created_at,created_by,retired_at FROM secret_versions WHERE secret_id=? ORDER BY version DESC`), id)
	if err != nil {
		return nil, err
	}
	defer utils.CloseRows(rows, s.logger, "query list secret versions")

	out := []secrets.Version{}
	for rows.Next() {
		var v secrets.Version
		var created string
		var retired sql.NullString
		v.Ref.ID = id
		if err = rows.Scan(&v.Ref.Version, &created, &v.CreatedBy, &retired); err != nil {
			return nil, err
		}
		//nolint:errcheck // Timestamp is generated internally in RFC3339Nano format.
		v.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		if retired.Valid {
			//nolint:errcheck // Timestamp is generated internally in RFC3339Nano format.
			t, _ := time.Parse(time.RFC3339Nano, retired.String)
			v.RetiredAt = &t
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) RetireSecret(ctx context.Context, id string, actor int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer utils.RollbackTx(tx, s.logger, "retire secret")
	now := ts(time.Now())
	res, err := tx.ExecContext(ctx, s.bind(`UPDATE secret_metadata SET retired_at=?,updated_at=? WHERE id=? AND retired_at IS NULL`), now, now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("get rs affected rows: %w", err)
	}
	if n != 1 {
		return secrets.ErrNotFound
	}
	if err = s.audit(ctx, tx, actor, "secret.retired", "secret", id, map[string]any{}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListRewrapCandidates(ctx context.Context, keyID string, limit int) ([]secrets.RewrapCandidate, error) {
	if strings.TrimSpace(keyID) == "" || limit < 1 || limit > 256 {
		return nil, errors.New("secret rewrap query requires a key ID and batch size from 1 to 256")
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT v.secret_id,v.version,m.purpose,v.key_id,v.data_aad_version,v.data_aad_key_id,v.wrapped_dek
		FROM secret_versions v JOIN secret_metadata m ON m.id=v.secret_id
		WHERE v.key_id=? ORDER BY v.secret_id,v.version LIMIT ?`), keyID, limit)
	if err != nil {
		return nil, err
	}
	defer utils.CloseRows(rows, s.logger, "query secret DEK rewrap batch")
	candidates := make([]secrets.RewrapCandidate, 0, limit)
	for rows.Next() {
		var candidate secrets.RewrapCandidate
		if err = rows.Scan(&candidate.Ref.ID, &candidate.Ref.Version, &candidate.Purpose,
			&candidate.Ciphertext.KeyID, &candidate.Ciphertext.AADVersion, &candidate.Ciphertext.DataAADKeyID, &candidate.Ciphertext.WrappedDEK); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func (s *Store) RewrapDEK(ctx context.Context, ref secrets.Ref, oldKeyID, newKeyID string, wrapped []byte, actor int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer utils.RollbackTx(tx, s.logger, "rewrap secret DEK")
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE secret_versions SET key_id=?,wrapped_dek=?
		WHERE secret_id=? AND version=? AND key_id=?`), newKeyID, wrapped, ref.ID, ref.Version, oldKeyID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read rewrapped secret version count: %w", err)
	}
	if affected != 1 {
		return secrets.ErrConflict
	}
	if err = s.audit(ctx, tx, actor, "secret.dek_rewrapped", "secret", ref.ID, map[string]any{
		"version": ref.Version, "oldKeyId": oldKeyID, "newKeyId": newKeyID,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CountVersionsByKey(ctx context.Context, keyID string) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM secret_versions WHERE key_id=?`), keyID).Scan(&count)
	return count, err
}
