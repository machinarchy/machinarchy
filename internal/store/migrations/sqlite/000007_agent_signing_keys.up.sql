-- Controller signing keys for the agent gateway (AUDIT M-3, Phase 16). The
-- private seed is a Secret Store secret (secret_id, secret_version); this
-- table holds only public data and the rotation state. At most one key is
-- 'active' (signs) and at most one is 'next' (announced to agents).
-- Times are Unix nanoseconds.
CREATE TABLE agent_signing_keys (
    id TEXT PRIMARY KEY,
    state TEXT NOT NULL CHECK (state IN ('next', 'active', 'retired')),
    public_key TEXT NOT NULL UNIQUE,
    secret_id TEXT NOT NULL,
    secret_version INTEGER NOT NULL,
    origin TEXT NOT NULL CHECK (origin IN ('generated', 'imported')),
    created_by INTEGER NOT NULL DEFAULT 0,
    created_at_ns BIGINT NOT NULL,
    activated_at_ns BIGINT NOT NULL DEFAULT 0,
    retired_at_ns BIGINT NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX agent_signing_keys_one_active ON agent_signing_keys (state) WHERE state = 'active';
CREATE UNIQUE INDEX agent_signing_keys_one_next ON agent_signing_keys (state) WHERE state = 'next';
