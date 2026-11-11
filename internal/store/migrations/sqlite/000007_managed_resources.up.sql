-- Canonical connector-neutral resources are introduced alongside the legacy
-- SSH-shaped hosts/runs tables. Existing API rows are not rewritten here.
CREATE UNIQUE INDEX idx_environments_workspace_id_id ON environments(workspace_id, id);
CREATE TABLE managed_hosts (
    id TEXT PRIMARY KEY,
    workspace_id INTEGER NOT NULL REFERENCES workspaces(id),
    environment_id INTEGER,
    name TEXT NOT NULL,
    labels TEXT NOT NULL DEFAULT '{}',
    desired_presence TEXT NOT NULL CHECK (desired_presence IN ('Managed', 'Absent')),
    access_profile_id TEXT NOT NULL DEFAULT '',
    access_profile_revision INTEGER NOT NULL DEFAULT 0,
    generation INTEGER NOT NULL CHECK (generation > 0),
    revision INTEGER NOT NULL CHECK (revision > 0),
    observed_generation INTEGER NOT NULL DEFAULT 0 CHECK (observed_generation >= 0),
    phase TEXT NOT NULL CHECK (phase IN ('Pending', 'Active', 'Deleting')),
    conditions TEXT NOT NULL DEFAULT '[]',
    last_observed_at TEXT,
    deletion_timestamp TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (workspace_id, name),
    FOREIGN KEY (workspace_id, environment_id) REFERENCES environments(workspace_id, id)
);
CREATE INDEX idx_managed_hosts_resync ON managed_hosts(workspace_id, phase, updated_at, id);
CREATE INDEX idx_managed_hosts_generation ON managed_hosts(workspace_id, generation, observed_generation);

CREATE TABLE operations (
    id TEXT PRIMARY KEY,
    workspace_id INTEGER NOT NULL REFERENCES workspaces(id),
    kind TEXT NOT NULL CHECK (kind = 'Run'),
    state TEXT NOT NULL CHECK (state IN ('Queued', 'Running', 'CancelRequested', 'Succeeded', 'PartiallySucceeded', 'Failed', 'Cancelled', 'TimedOut', 'Interrupted')),
    revision INTEGER NOT NULL CHECK (revision > 0),
    actor_user_id INTEGER NOT NULL DEFAULT 0,
    actor_token_id INTEGER NOT NULL DEFAULT 0,
    spec_encrypted TEXT NOT NULL,
    cancel_asked INTEGER NOT NULL DEFAULT 0 CHECK (cancel_asked IN (0, 1)),
    conditions TEXT NOT NULL DEFAULT '[]',
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT
);
CREATE INDEX idx_operations_workspace_created ON operations(workspace_id, created_at DESC, id DESC);
CREATE INDEX idx_operations_state_created ON operations(state, created_at, id);

CREATE TABLE operation_targets (
    operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
    host_id TEXT NOT NULL,
    host_generation INTEGER NOT NULL,
    display_name TEXT NOT NULL,
    access_profile_id TEXT NOT NULL DEFAULT '',
    access_profile_revision INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL CHECK (state IN ('Pending', 'Running', 'Succeeded', 'Failed', 'TimedOut', 'Cancelled', 'Interrupted')),
    exit_code INTEGER,
    output TEXT NOT NULL DEFAULT '',
    error_class TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (operation_id, host_id)
);
CREATE INDEX idx_operation_targets_host ON operation_targets(host_id, operation_id);
