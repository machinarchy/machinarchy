-- Command runs: one row per run and one row per targeted host. The target hosts
-- are pinned when the run is submitted (host_name, address, and port are
-- snapshots). The command's arguments may contain sensitive values, so they are
-- stored encrypted; only the program name, the argument count, and a digest are
-- stored in the clear.
CREATE TABLE IF NOT EXISTS runs (
    id BIGSERIAL PRIMARY KEY,
    workspace_id BIGINT NOT NULL,
    status TEXT NOT NULL,
    created_by_user_id BIGINT NOT NULL DEFAULT 0,
    created_by_username TEXT NOT NULL DEFAULT '',
    created_by_token_id BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    program TEXT NOT NULL,
    arg_count INTEGER NOT NULL,
    argv_encrypted TEXT NOT NULL,
    argv_sha256 TEXT NOT NULL,
    timeout_seconds INTEGER NOT NULL,
    concurrency INTEGER NOT NULL,
    targets_hash TEXT NOT NULL,
    rule TEXT NOT NULL DEFAULT '{}',
    cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
    owner_id TEXT NOT NULL DEFAULT '',
    heartbeat_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_runs_workspace ON runs (workspace_id, id);
CREATE INDEX IF NOT EXISTS idx_runs_liveness ON runs (status, heartbeat_at);

CREATE TABLE IF NOT EXISTS run_hosts (
    run_id BIGINT NOT NULL,
    host_id TEXT NOT NULL,
    host_name TEXT NOT NULL,
    address TEXT NOT NULL,
    port INTEGER NOT NULL,
    status TEXT NOT NULL,
    exit_code INTEGER,
    output TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    PRIMARY KEY (run_id, host_id)
);
