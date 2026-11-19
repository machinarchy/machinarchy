-- Server-side login sessions (AUDIT S-08). The session JWT carries the row's
-- id; a request is accepted only while the row is active. Times are Unix
-- nanoseconds, so comparisons are exact (0 = never revoked).
CREATE TABLE user_sessions (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id INTEGER NOT NULL,
    created_at_ns INTEGER NOT NULL,
    expires_at_ns INTEGER NOT NULL,
    revoked_at_ns INTEGER NOT NULL DEFAULT 0,
    revoke_reason TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    client_ip TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_user_sessions_user ON user_sessions(user_id, revoked_at_ns, expires_at_ns);
