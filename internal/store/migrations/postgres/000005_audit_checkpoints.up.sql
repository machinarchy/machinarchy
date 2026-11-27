-- Audit retention (ADR-061). A checkpoint records the hash of the newest event
-- removed by an owner's prune, so verification of the retained chain starts
-- from it. Checkpoints are append-only. Events may be deleted only at or below
-- a checkpoint of their workspace, which the prune writes in the same
-- transaction before it deletes.
CREATE TABLE audit_checkpoints (
    id BIGSERIAL PRIMARY KEY,
    workspace_id BIGINT NOT NULL,
    last_event_id BIGINT NOT NULL,
    last_hash TEXT NOT NULL,
    events_removed BIGINT NOT NULL,
    actor_user_id BIGINT NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    UNIQUE (workspace_id, last_event_id)
);
CREATE FUNCTION audit_checkpoints_immutable() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit checkpoints are append-only';
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER audit_checkpoints_no_update BEFORE UPDATE ON audit_checkpoints
    FOR EACH ROW EXECUTE FUNCTION audit_checkpoints_immutable();
CREATE TRIGGER audit_checkpoints_no_delete BEFORE DELETE ON audit_checkpoints
    FOR EACH ROW EXECUTE FUNCTION audit_checkpoints_immutable();

CREATE FUNCTION audit_events_guard_delete() RETURNS trigger AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM audit_checkpoints c
               WHERE c.workspace_id = OLD.workspace_id AND OLD.id <= c.last_event_id) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'audit events are append-only';
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER audit_events_no_delete ON audit_events;
CREATE TRIGGER audit_events_no_delete BEFORE DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_guard_delete();
