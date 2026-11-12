CREATE TABLE runtime_config_revisions (
    revision BIGINT PRIMARY KEY CHECK (revision > 0),
    schema_version INTEGER NOT NULL CHECK (schema_version > 0),
    document TEXT NOT NULL,
    checksum VARCHAR(64) NOT NULL,
    created_at TEXT NOT NULL,
    created_by BIGINT NOT NULL DEFAULT 0,
    change_note VARCHAR(512) NOT NULL DEFAULT ''
);
CREATE TABLE runtime_config_head (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    revision BIGINT NOT NULL REFERENCES runtime_config_revisions(revision)
);
CREATE TABLE runtime_config_process_status (
    process_id VARCHAR(128) PRIMARY KEY,
    role VARCHAR(64) NOT NULL,
    applied_revision BIGINT NOT NULL DEFAULT 0,
    attempted_revision BIGINT NOT NULL DEFAULT 0,
    last_success TEXT,
    last_error_class VARCHAR(128) NOT NULL DEFAULT '',
    heartbeat TEXT NOT NULL
);
CREATE INDEX idx_runtime_config_status_heartbeat ON runtime_config_process_status(heartbeat);

CREATE TABLE secret_metadata (
    id TEXT PRIMARY KEY,
    name VARCHAR(128) NOT NULL UNIQUE,
    description VARCHAR(512) NOT NULL DEFAULT '',
    purpose VARCHAR(64) NOT NULL,
    current_version BIGINT NOT NULL CHECK (current_version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by BIGINT NOT NULL,
    retired_at TEXT
);
CREATE TABLE secret_versions (
    secret_id TEXT NOT NULL REFERENCES secret_metadata(id),
    version BIGINT NOT NULL CHECK (version > 0),
    key_id VARCHAR(128) NOT NULL,
    wrapped_dek BYTEA NOT NULL,
    ciphertext BYTEA NOT NULL,
    created_at TEXT NOT NULL,
    created_by BIGINT NOT NULL,
    retired_at TEXT,
    PRIMARY KEY (secret_id, version)
);
CREATE INDEX idx_secret_versions_created ON secret_versions(secret_id, created_at DESC);
CREATE TABLE management_audit_events (
    id BIGSERIAL PRIMARY KEY,
    occurred_at TEXT NOT NULL,
    actor_user_id BIGINT NOT NULL DEFAULT 0,
    action VARCHAR(128) NOT NULL,
    target_type VARCHAR(64) NOT NULL,
    target_id TEXT NOT NULL DEFAULT '',
    details TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_management_audit_target ON management_audit_events(target_type, target_id, id DESC);
