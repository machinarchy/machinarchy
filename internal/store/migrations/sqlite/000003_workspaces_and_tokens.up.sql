CREATE TABLE workspaces (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    owner_user_id INTEGER UNIQUE REFERENCES users(id),
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Existing inventory is deterministically assigned to the earliest existing
-- user. If the database has no users yet, the first account claims this row.
INSERT INTO workspaces (id, name, owner_user_id, created_at)
VALUES (1, 'initial-workspace', NULL, CURRENT_TIMESTAMP);

ALTER TABLE hosts ADD COLUMN workspace_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE jobs ADD COLUMN workspace_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE metrics_history ADD COLUMN workspace_id INTEGER NOT NULL DEFAULT 1;

-- Rebuild environments to replace its legacy global name constraint with a
-- workspace-local constraint while preserving all environment IDs and data.
CREATE TABLE environments_scoped (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id INTEGER NOT NULL DEFAULT 1,
    name TEXT NOT NULL COLLATE NOCASE,
    description TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE (workspace_id, name)
);
INSERT INTO environments_scoped (id, workspace_id, name, description, created_at)
SELECT id, 1, name, description, created_at FROM environments;
DROP TABLE environments;
ALTER TABLE environments_scoped RENAME TO environments;

CREATE TABLE workspace_memberships (
    workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('owner', 'operator', 'reader')),
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
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
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    workspace_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('operator', 'reader')),
    token_hash TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    revoked_at TEXT,
    UNIQUE (user_id, name),
    FOREIGN KEY (workspace_id, user_id)
        REFERENCES workspace_memberships(workspace_id, user_id) ON DELETE CASCADE
);

CREATE INDEX idx_api_tokens_workspace_user ON api_tokens(workspace_id, user_id);
