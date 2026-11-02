-- Command runs: one row per run and one row per targeted host. The target hosts
-- are pinned when the run is submitted (host_name, address, and port are
-- snapshots). The command's arguments may contain sensitive values, so they are
-- stored encrypted; only the program name, the argument count, and a digest are
-- stored in the clear. Timestamps are fixed-width UTC text so they sort
-- correctly as strings.
CREATE TABLE IF NOT EXISTS runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id INTEGER NOT NULL,
    status TEXT NOT NULL,
    created_by_user_id INTEGER NOT NULL DEFAULT 0,
    created_by_username TEXT NOT NULL DEFAULT '',
    created_by_token_id INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    program TEXT NOT NULL,
    arg_count INTEGER NOT NULL,
    argv_encrypted TEXT NOT NULL,
    argv_sha256 TEXT NOT NULL,
    timeout_seconds INTEGER NOT NULL,
    concurrency INTEGER NOT NULL,
    targets_hash TEXT NOT NULL,
    rule TEXT NOT NULL DEFAULT '{}',
    cancel_requested INTEGER NOT NULL DEFAULT 0,
    owner_id TEXT NOT NULL DEFAULT '',
    heartbeat_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_runs_workspace ON runs(workspace_id, id);
CREATE INDEX IF NOT EXISTS idx_runs_liveness ON runs(status, heartbeat_at);

CREATE TABLE IF NOT EXISTS run_hosts (
    run_id INTEGER NOT NULL,
    host_id TEXT NOT NULL,
    host_name TEXT NOT NULL,
    address TEXT NOT NULL,
    port INTEGER NOT NULL,
    status TEXT NOT NULL,
    exit_code INTEGER,
    output TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    started_at TEXT,
    finished_at TEXT,
    PRIMARY KEY (run_id, host_id)
);
