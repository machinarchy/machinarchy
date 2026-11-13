package resources

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/eventing"
)

func (s *Store) CreateAccessProfile(ctx context.Context, profile *resource.AccessProfile, revision resource.AccessProfileRevision) error {
	if profile == nil || profile.Validate() != nil || revision.Validate() != nil || revision.ProfileID != profile.ID || revision.WorkspaceID != profile.WorkspaceID || revision.Revision != 1 {
		return fmt.Errorf("%w: invalid initial Access Profile", resource.ErrInvalidResource)
	}
	doc, checksum, err := profileDocument(revision.Spec)
	if err != nil {
		return err
	}
	revision.Checksum = checksum
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Access Profile creation: %w", err)
	}
	defer rollback(tx, s.logger, "Access Profile creation")
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO access_profiles(id,workspace_id,name,description,current_revision,created_at,updated_at,created_by) VALUES(?,?,?,?,?,?,?,?)`), string(profile.ID), profile.WorkspaceID, profile.Name, profile.Description, 1, s.nowText(profile.CreatedAt), s.nowText(profile.UpdatedAt), profile.CreatedBy)
	if err != nil {
		return fmt.Errorf("insert Access Profile: %w", err)
	}
	if _, err = tx.ExecContext(ctx, s.bind(`INSERT INTO access_profile_revisions(profile_id,workspace_id,revision,document,checksum,created_at,created_by) VALUES(?,?,?,?,?,?,?)`), string(profile.ID), profile.WorkspaceID, 1, doc, checksum, s.nowText(revision.CreatedAt), revision.CreatedBy); err != nil {
		return fmt.Errorf("insert initial Access Profile revision: %w", err)
	}
	if err = s.accessProfileAuditAndEvent(ctx, tx, profile.WorkspaceID, *profile, revision, true); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetAccessProfile(ctx context.Context, workspaceID int, id resource.ID) (*resource.AccessProfile, error) {
	if workspaceID <= 0 || id.Validate() != nil {
		return nil, resource.ErrAccessProfileNotFound
	}
	var p resource.AccessProfile
	var created, updated string
	var deleted sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id,workspace_id,name,description,current_revision,created_at,updated_at,created_by,deleted_at FROM access_profiles WHERE workspace_id=? AND id=?`), workspaceID, string(id)).Scan(&p.ID, &p.WorkspaceID, &p.Name, &p.Description, &p.CurrentRevision, &created, &updated, &p.CreatedBy, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, resource.ErrAccessProfileNotFound
	}
	if err != nil {
		return nil, err
	}
	if p.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return nil, err
	}
	if p.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return nil, err
	}
	if deleted.Valid {
		t, e := time.Parse(time.RFC3339Nano, deleted.String)
		if e != nil {
			return nil, e
		}
		p.DeletedAt = &t
	}
	if err = p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) GetAccessProfileRevision(ctx context.Context, workspaceID int, id resource.ID, revision uint64) (*resource.AccessProfileRevision, error) {
	var r resource.AccessProfileRevision
	var doc, created string
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT profile_id,workspace_id,revision,document,checksum,created_at,created_by FROM access_profile_revisions WHERE workspace_id=? AND profile_id=? AND revision=?`), workspaceID, string(id), revision).Scan(&r.ProfileID, &r.WorkspaceID, &r.Revision, &doc, &r.Checksum, &created, &r.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, resource.ErrAccessProfileNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(doc), &r.Spec); err != nil {
		return nil, fmt.Errorf("decode Access Profile revision: %w", err)
	}
	if r.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return nil, err
	}
	if err = r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) UpdateAccessProfile(ctx context.Context, workspaceID int, id resource.ID, expected uint64, spec resource.AccessProfileSpec, actor int, now time.Time) (*resource.AccessProfileRevision, error) {
	p, err := s.GetAccessProfile(ctx, workspaceID, id)
	if err != nil {
		return nil, err
	}
	if p.DeletedAt != nil {
		return nil, resource.ErrAccessProfileNotFound
	}
	if p.CurrentRevision != expected {
		return nil, resource.ErrConflict
	}
	r, err := p.NextRevision(expected, spec, actor, now)
	if err != nil {
		return nil, err
	}
	doc, sum, err := profileDocument(r.Spec)
	if err != nil {
		return nil, err
	}
	r.Checksum = sum
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx, s.logger, "Access Profile revision")
	query := `SELECT current_revision FROM access_profiles WHERE workspace_id=? AND id=?`
	if s.dialect == PostgreSQL {
		query += " FOR UPDATE"
	}
	var current uint64
	if err = tx.QueryRowContext(ctx, s.bind(query), workspaceID, string(id)).Scan(&current); err != nil {
		return nil, resource.ErrAccessProfileNotFound
	}
	if current != expected {
		return nil, resource.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, s.bind(`INSERT INTO access_profile_revisions(profile_id,workspace_id,revision,document,checksum,created_at,created_by) VALUES(?,?,?,?,?,?,?)`), string(id), workspaceID, r.Revision, doc, sum, s.nowText(now), actor); err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, s.bind(`UPDATE access_profiles SET current_revision=?,updated_at=? WHERE workspace_id=? AND id=? AND current_revision=? AND deleted_at IS NULL`), r.Revision, s.nowText(now), workspaceID, string(id), expected)
	if err != nil {
		return nil, err
	}
	if err = s.rowsAffected(res, resource.ErrConflict); err != nil {
		return nil, err
	}
	if err = s.accessProfileAuditAndEvent(ctx, tx, workspaceID, *p, r, false); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) DisableAccessProfile(ctx context.Context, workspaceID int, id resource.ID, expected uint64, actor int, now time.Time) (*resource.AccessProfileRevision, error) {
	r, err := s.GetAccessProfileRevision(ctx, workspaceID, id, expected)
	if err != nil {
		return nil, err
	}
	if !r.Spec.Enabled {
		return r, nil
	}
	r.Spec.Enabled = false
	return s.UpdateAccessProfile(ctx, workspaceID, id, expected, r.Spec, actor, now)
}

func (s *Store) ListHostsByAccessProfile(ctx context.Context, workspaceID int, id resource.ID) ([]resource.HostKey, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT workspace_id,id FROM managed_hosts WHERE workspace_id=? AND access_profile_id=? ORDER BY id`), workspaceID, string(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []resource.HostKey{}
	for rows.Next() {
		var k resource.HostKey
		if err = rows.Scan(&k.WorkspaceID, &k.ID); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAccessProfile(ctx context.Context, workspaceID int, id resource.ID, actor int, now time.Time) error {
	_ = actor
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx, s.logger, "Access Profile deletion")
	var refs int
	if err = tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM managed_hosts WHERE workspace_id=? AND access_profile_id=?`), workspaceID, string(id)).Scan(&refs); err != nil {
		return err
	}
	if refs > 0 {
		return resource.ErrConflict
	}
	res, err := tx.ExecContext(ctx, s.bind(`UPDATE access_profiles SET deleted_at=?,updated_at=? WHERE workspace_id=? AND id=? AND deleted_at IS NULL`), s.nowText(now), s.nowText(now), workspaceID, string(id))
	if err != nil {
		return err
	}
	if err = s.rowsAffected(res, resource.ErrAccessProfileNotFound); err != nil {
		return err
	}
	if err = s.audit(ctx, tx, workspaceID, domain.AuditResourceHostUpdated, "access-profile", string(id), map[string]string{"action": "tombstoned"}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) accessProfileAuditAndEvent(ctx context.Context, tx *sql.Tx, workspace int, p resource.AccessProfile, r resource.AccessProfileRevision, created bool) error {
	action := "access_profile.revised"
	if created {
		action = "access_profile.created"
	}
	if err := s.audit(ctx, tx, workspace, action, "access-profile", string(p.ID), map[string]string{"revision": fmt.Sprint(r.Revision), "checksum": r.Checksum}); err != nil {
		return err
	}
	id, err := resource.NewResourceID()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"profileId": p.ID,
		"revision":  r.Revision,
	})
	if err != nil {
		return fmt.Errorf("marshal profile revision payload: %w", err)
	}
	now := s.nowText(r.CreatedAt)
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO outbox_events(event_id,event_type,schema_version,occurred_at,producer,workspace_id,subject_kind,subject_id,subject_generation,subject_revision,correlation_id,causation_id,actor_user_id,actor_token_id,payload,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), string(id), eventing.AccessProfileChanged, 1, now, "machinarchy.access-profile", workspace, "access-profile", string(p.ID), r.Revision, r.Revision, "", "", r.CreatedBy, nil, string(payload), now)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO outbox_deliveries(event_id,consumer_group,attempts,available_at_ns) VALUES(?,?,0,?)`), string(id), eventing.ConsumerControlManager, r.CreatedAt.UTC().UnixNano())
	return err
}

func profileDocument(spec resource.AccessProfileSpec) (string, string, error) {
	b, err := json.Marshal(spec)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(b)
	return string(b), hex.EncodeToString(sum[:]), nil
}

func (s *Store) validateHostAccessProfile(ctx context.Context, q queryRower, workspaceID int, id resource.ID) error {
	var exists int
	err := q.QueryRowContext(ctx, s.bind(`SELECT 1 FROM access_profiles WHERE workspace_id=? AND id=? AND deleted_at IS NULL`), workspaceID, string(id)).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return resource.ErrAccessProfileNotFound
	}
	return err
}

var _ resource.AccessProfileRepository = (*Store)(nil)
