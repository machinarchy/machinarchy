CREATE TABLE workspaces (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    owner_user_id BIGINT UNIQUE REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Existing inventory is deterministically assigned to the earliest existing
-- user. If the database has no users yet, the first account claims this row.
INSERT INTO workspaces (id, name, owner_user_id, created_at)
VALUES (1, 'initial-workspace', NULL, CURRENT_TIMESTAMP);
SELECT setval(pg_get_serial_sequence('workspaces', 'id'),
    GREATEST(COALESCE((SELECT MAX(id) FROM workspaces), 1), 1), true);

ALTER TABLE hosts ADD COLUMN workspace_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE environments ADD COLUMN workspace_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE jobs ADD COLUMN workspace_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE metrics_history ADD COLUMN workspace_id BIGINT NOT NULL DEFAULT 1 REFERENCES workspaces(id);
ALTER TABLE environments DROP CONSTRAINT environments_name_key;
CREATE UNIQUE INDEX idx_environments_workspace_name ON environments(workspace_id, name);

CREATE TABLE workspace_memberships (
    workspace_id BIGINT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('owner', 'operator', 'reader')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (workspace_id, user_id)
);

INSERT INTO workspaces (name, owner_user_id, created_at)
SELECT 'workspace-' || id, id, CURRENT_TIMESTAMP
FROM users
WHERE id <> (SELECT MIN(id) FROM users);

UPDATE workspaces
SET name = 'workspace-' || (SELECT MIN(id) FROM users),
    owner_user_id = (SELECT MIN(id) FROM users)
WHERE id = 1 AND EXISTS (SELECT 1 FROM users);

INSERT INTO workspace_memberships (workspace_id, user_id, role)
SELECT CASE
        WHEN users.id = (SELECT MIN(id) FROM users) THEN 1
        ELSE (SELECT id FROM workspaces WHERE owner_user_id = users.id)
    END,
    users.id,
    'owner'
FROM users;

CREATE INDEX idx_hosts_workspace_id ON hosts(workspace_id);
CREATE INDEX idx_environments_workspace_id ON environments(workspace_id);
CREATE INDEX idx_jobs_workspace_id_started_at ON jobs(workspace_id, started_at DESC);
CREATE INDEX idx_metrics_history_workspace_timestamp ON metrics_history(workspace_id, timestamp DESC);

CREATE TABLE api_tokens (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL,
    workspace_id BIGINT NOT NULL,
    name VARCHAR(100) NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('operator', 'reader')),
    token_hash CHAR(64) NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    UNIQUE (user_id, name),
    FOREIGN KEY (workspace_id, user_id)
        REFERENCES workspace_memberships(workspace_id, user_id) ON DELETE CASCADE
);

CREATE INDEX idx_api_tokens_workspace_user ON api_tokens(workspace_id, user_id);
