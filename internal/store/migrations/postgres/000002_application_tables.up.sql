CREATE SEQUENCE IF NOT EXISTS hosts_id_seq;
SELECT setval(
    'hosts_id_seq',
    COALESCE((SELECT MAX(id::BIGINT) FROM hosts WHERE id ~ '^[0-9]+$'), 0) + 1,
    false
);

CREATE TABLE IF NOT EXISTS users (
    id BIGSERIAL PRIMARY KEY,
    username VARCHAR(255) NOT NULL UNIQUE,
    email VARCHAR(320) UNIQUE,
    password_hash TEXT NOT NULL,
    reset_token TEXT,
    reset_token_expires TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS environments (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS jobs (
    id BIGSERIAL PRIMARY KEY,
    host_id BIGINT NOT NULL,
    command TEXT NOT NULL,
    status VARCHAR(32) NOT NULL,
    result_log TEXT NOT NULL DEFAULT '',
    triggered_by VARCHAR(255) NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS metrics_history (
    id BIGSERIAL PRIMARY KEY,
    timestamp TIMESTAMPTZ NOT NULL,
    cpu_percent DOUBLE PRECISION NOT NULL,
    mem_percent DOUBLE PRECISION NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_jobs_host_id_started_at ON jobs(host_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_metrics_history_timestamp ON metrics_history(timestamp DESC);
