-- Append-only audit trail. Each workspace's events form a SHA-256 hash chain
-- (see domain.ComputeAuditHash); occurred_at is stored as the exact text that
-- was hashed (a timestamp column would round it). The triggers make the table
-- immutable for the application: an UPDATE or DELETE raises an error. They do
-- not stop someone who can alter the schema; the hash chain is what makes such
-- tampering detectable afterwards.
CREATE TABLE IF NOT EXISTS audit_events (
    id BIGSERIAL PRIMARY KEY,
    workspace_id BIGINT NOT NULL,
    occurred_at TEXT NOT NULL,
    actor_user_id BIGINT NOT NULL DEFAULT 0,
    actor_username TEXT NOT NULL DEFAULT '',
    actor_token_id BIGINT NOT NULL DEFAULT 0,
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
CREATE INDEX IF NOT EXISTS idx_audit_events_workspace ON audit_events (workspace_id, id);
CREATE INDEX IF NOT EXISTS idx_audit_events_action ON audit_events (workspace_id, action, id);

CREATE OR REPLACE FUNCTION audit_events_immutable() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit events are append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_events_no_update BEFORE UPDATE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_immutable();
CREATE TRIGGER audit_events_no_delete BEFORE DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_immutable();
