-- Append-only audit trail. Each workspace's events form a SHA-256 hash chain
-- (see domain.ComputeAuditHash); occurred_at is stored as the exact text that
-- was hashed. The triggers make the table immutable for the application: an
-- UPDATE or DELETE aborts. They do not stop someone who can alter the schema;
-- the hash chain is what makes such tampering detectable afterwards.
CREATE TABLE IF NOT EXISTS audit_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id INTEGER NOT NULL,
    occurred_at TEXT NOT NULL,
    actor_user_id INTEGER NOT NULL DEFAULT 0,
    actor_username TEXT NOT NULL DEFAULT '',
    actor_token_id INTEGER NOT NULL DEFAULT 0,
    request_id TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL DEFAULT 'success',
    details TEXT NOT NULL DEFAULT '{}',
    prev_hash TEXT NOT NULL,
    hash TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_events_workspace ON audit_events(workspace_id, id);
CREATE INDEX IF NOT EXISTS idx_audit_events_action ON audit_events(workspace_id, action, id);

CREATE TRIGGER IF NOT EXISTS audit_events_no_update BEFORE UPDATE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit events are append-only');
END;
CREATE TRIGGER IF NOT EXISTS audit_events_no_delete BEFORE DELETE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit events are append-only');
END;
