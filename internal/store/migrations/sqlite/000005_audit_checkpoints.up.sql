-- Audit retention (ADR-061). A checkpoint records the hash of the newest event
-- removed by an owner's prune, so verification of the retained chain starts
-- from it. Checkpoints are append-only. Events may be deleted only at or below
-- a checkpoint of their workspace, which the prune writes in the same
-- transaction before it deletes.
CREATE TABLE audit_checkpoints (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id INTEGER NOT NULL,
    last_event_id INTEGER NOT NULL,
    last_hash TEXT NOT NULL,
    events_removed INTEGER NOT NULL,
    actor_user_id INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    UNIQUE (workspace_id, last_event_id)
);
CREATE TRIGGER audit_checkpoints_no_update BEFORE UPDATE ON audit_checkpoints
BEGIN
    SELECT RAISE(ABORT, 'audit checkpoints are append-only');
END;
CREATE TRIGGER audit_checkpoints_no_delete BEFORE DELETE ON audit_checkpoints
BEGIN
    SELECT RAISE(ABORT, 'audit checkpoints are append-only');
END;

DROP TRIGGER audit_events_no_delete;
CREATE TRIGGER audit_events_no_delete BEFORE DELETE ON audit_events
WHEN NOT EXISTS (
    SELECT 1 FROM audit_checkpoints c
    WHERE c.workspace_id = OLD.workspace_id AND OLD.id <= c.last_event_id
)
BEGIN
    SELECT RAISE(ABORT, 'audit events are append-only');
END;
