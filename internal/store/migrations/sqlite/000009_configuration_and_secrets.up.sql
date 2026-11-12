CREATE TABLE runtime_config_revisions (
    revision INTEGER PRIMARY KEY CHECK (revision > 0),
    schema_version INTEGER NOT NULL CHECK (schema_version > 0),
    document TEXT NOT NULL,
    checksum TEXT NOT NULL,
    created_at TEXT NOT NULL,
    created_by INTEGER NOT NULL DEFAULT 0,
    change_note TEXT NOT NULL DEFAULT ''
);
CREATE TABLE runtime_config_head (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    revision INTEGER NOT NULL REFERENCES runtime_config_revisions(revision)
);
CREATE TABLE runtime_config_process_status (
    process_id TEXT PRIMARY KEY,
    role TEXT NOT NULL,
    applied_revision INTEGER NOT NULL DEFAULT 0,
    attempted_revision INTEGER NOT NULL DEFAULT 0,
    last_success TEXT,
    last_error_class TEXT NOT NULL DEFAULT '',
    heartbeat TEXT NOT NULL
);
CREATE INDEX idx_runtime_config_status_heartbeat ON runtime_config_process_status(heartbeat);

CREATE TABLE secret_metadata (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    purpose TEXT NOT NULL,
    current_version INTEGER NOT NULL CHECK (current_version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by INTEGER NOT NULL,
    retired_at TEXT
);
CREATE TABLE secret_versions (
    secret_id TEXT NOT NULL REFERENCES secret_metadata(id),
    version INTEGER NOT NULL CHECK (version > 0),
    key_id TEXT NOT NULL,
    wrapped_dek BLOB NOT NULL,
    ciphertext BLOB NOT NULL,
    created_at TEXT NOT NULL,
    created_by INTEGER NOT NULL,
    retired_at TEXT,
    PRIMARY KEY (secret_id, version)
);
CREATE INDEX idx_secret_versions_created ON secret_versions(secret_id, created_at DESC);
CREATE TABLE management_audit_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at TEXT NOT NULL,
    actor_user_id INTEGER NOT NULL DEFAULT 0,
    action TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id TEXT NOT NULL DEFAULT '',
    details TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_management_audit_target ON management_audit_events(target_type, target_id, id DESC);
