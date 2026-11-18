-- Owner-issued, single-use agent enrolment tokens (AUDIT M-4). Only the
-- SHA-256 of the secret is stored. A token is consumed in the same
-- transaction that registers the agent.
CREATE TABLE agent_enrollment_tokens (
    id TEXT PRIMARY KEY,
    workspace_id BIGINT NOT NULL REFERENCES workspaces(id),
    token_hash TEXT NOT NULL UNIQUE,
    hostname TEXT,
    created_by BIGINT NOT NULL REFERENCES users(id),
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at TEXT,
    used_request_id TEXT,
    used_by_agent TEXT,
    revoked_at TEXT
);
CREATE INDEX idx_agent_enrollment_tokens_workspace ON agent_enrollment_tokens(workspace_id, created_at);
