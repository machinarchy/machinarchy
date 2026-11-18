-- Machinarchy baseline schema (squashed during development, 2026-11-18).
--
-- It replaces migrations 000001-000013 and contains only the managed resource
-- model and the shared account tables; the legacy hosts/jobs/runs tables and
-- workspaces.owner_user_id are gone. A database created before this baseline
-- must be recreated: the migrator refuses to run against it.
--
-- Every timestamp is RFC 3339 text in UTC, identical in both dialects.

-- Accounts, environments and the audit trail.
CREATE TABLE workspaces (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    created_at TEXT NOT NULL
);
-- The single shared workspace (ADR-0003).
INSERT INTO workspaces (id, name, created_at) VALUES (1, 'default', '2026-01-01T00:00:00Z');
SELECT setval(pg_get_serial_sequence('workspaces', 'id'), 1);

CREATE TABLE users (
    id BIGSERIAL PRIMARY KEY,
    username VARCHAR(255) NOT NULL,
    email VARCHAR(320),
    password_hash TEXT NOT NULL,
    reset_token TEXT,
    reset_token_expires TEXT,
    created_at TEXT NOT NULL
);
-- Usernames and emails are unique ignoring case, as on SQLite (NOCASE).
CREATE UNIQUE INDEX idx_users_username ON users(lower(username));
CREATE UNIQUE INDEX idx_users_email ON users(lower(email));

CREATE TABLE workspace_memberships (
    workspace_id BIGINT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('owner', 'operator', 'reader')),
    created_at TEXT NOT NULL,
    PRIMARY KEY (workspace_id, user_id)
);

CREATE TABLE api_tokens (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL,
    workspace_id BIGINT NOT NULL,
    name VARCHAR(100) NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('operator', 'reader')),
    token_hash CHAR(64) NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    revoked_at TEXT,
    UNIQUE (user_id, name),
    FOREIGN KEY (workspace_id, user_id) REFERENCES workspace_memberships(workspace_id, user_id) ON DELETE CASCADE
);
CREATE INDEX idx_api_tokens_workspace_user ON api_tokens(workspace_id, user_id);

CREATE TABLE environments (
    id BIGSERIAL PRIMARY KEY,
    workspace_id BIGINT NOT NULL REFERENCES workspaces(id),
    name VARCHAR(255) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_environments_workspace_name ON environments(workspace_id, lower(name));

CREATE TABLE audit_events (
    id BIGSERIAL PRIMARY KEY,
    workspace_id BIGINT NOT NULL,
    occurred_at TEXT NOT NULL,
    actor_user_id BIGINT NOT NULL DEFAULT 0,
    actor_username TEXT NOT NULL DEFAULT '',
    actor_token_id BIGINT NOT NULL DEFAULT 0,
    request_id TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL DEFAULT 'success',
    details TEXT NOT NULL DEFAULT '{}',
    prev_hash TEXT NOT NULL,
    hash TEXT NOT NULL
);
CREATE INDEX idx_audit_events_workspace ON audit_events(workspace_id, id);
CREATE INDEX idx_audit_events_action ON audit_events(workspace_id, action, id);
CREATE FUNCTION audit_events_immutable() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit events are append-only';
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER audit_events_no_update BEFORE UPDATE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_immutable();
CREATE TRIGGER audit_events_no_delete BEFORE DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_immutable();

-- Managed resources: hosts, operations and their targets.
CREATE UNIQUE INDEX idx_environments_workspace_id_id ON environments(workspace_id, id);
CREATE TABLE managed_hosts (
    id TEXT PRIMARY KEY,
    workspace_id BIGINT NOT NULL REFERENCES workspaces(id),
    environment_id BIGINT,
    name VARCHAR(255) NOT NULL,
    labels TEXT NOT NULL DEFAULT '{}',
    desired_presence TEXT NOT NULL CHECK (desired_presence IN ('Managed', 'Absent')),
    access_profile_id TEXT NOT NULL DEFAULT '',
    access_profile_revision BIGINT NOT NULL DEFAULT 0,
    access_target TEXT NOT NULL DEFAULT '{}',
    generation BIGINT NOT NULL CHECK (generation > 0),
    revision BIGINT NOT NULL CHECK (revision > 0),
    observed_generation BIGINT NOT NULL DEFAULT 0 CHECK (observed_generation >= 0),
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
    workspace_id BIGINT NOT NULL REFERENCES workspaces(id),
    kind TEXT NOT NULL CHECK (kind = 'Run'),
    state TEXT NOT NULL CHECK (state IN ('Queued', 'Running', 'CancelRequested', 'Succeeded', 'PartiallySucceeded', 'Failed', 'Cancelled', 'TimedOut', 'Interrupted')),
    revision BIGINT NOT NULL CHECK (revision > 0),
    actor_user_id BIGINT NOT NULL DEFAULT 0,
    actor_token_id BIGINT NOT NULL DEFAULT 0,
    spec_encrypted TEXT NOT NULL,
    cancel_asked BOOLEAN NOT NULL DEFAULT FALSE,
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
    host_generation BIGINT NOT NULL,
    display_name VARCHAR(255) NOT NULL,
    access_profile_id TEXT NOT NULL DEFAULT '',
    access_profile_revision BIGINT NOT NULL DEFAULT 0,
    state TEXT NOT NULL CHECK (state IN ('Pending', 'Running', 'Succeeded', 'Failed', 'TimedOut', 'Cancelled', 'Interrupted')),
    exit_code INTEGER,
    output TEXT NOT NULL DEFAULT '',
    error_class TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (operation_id, host_id)
);
CREATE INDEX idx_operation_targets_host ON operation_targets(host_id, operation_id);

-- Control loop: outbox, deliveries and leases.
CREATE TABLE outbox_events (
    event_id TEXT PRIMARY KEY,
    event_type VARCHAR(128) NOT NULL,
    schema_version SMALLINT NOT NULL CHECK (schema_version > 0),
    occurred_at TEXT NOT NULL,
    producer VARCHAR(128) NOT NULL,
    workspace_id BIGINT NOT NULL REFERENCES workspaces(id),
    subject_kind VARCHAR(64) NOT NULL,
    subject_id TEXT NOT NULL,
    subject_generation BIGINT NOT NULL CHECK (subject_generation > 0),
    subject_revision BIGINT NOT NULL CHECK (subject_revision > 0),
    correlation_id VARCHAR(128) NOT NULL DEFAULT '',
    causation_id VARCHAR(128) NOT NULL DEFAULT '',
    actor_user_id BIGINT,
    actor_token_id BIGINT,
    payload TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_outbox_subject ON outbox_events(workspace_id, subject_kind, subject_id, subject_revision);
CREATE INDEX idx_outbox_created ON outbox_events(created_at, event_id);

CREATE TABLE outbox_deliveries (
    event_id TEXT NOT NULL REFERENCES outbox_events(event_id) ON DELETE CASCADE,
    consumer_group VARCHAR(64) NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at_ns BIGINT NOT NULL,
    lease_owner VARCHAR(128) NOT NULL DEFAULT '',
    lease_expires_at_ns BIGINT NOT NULL DEFAULT 0,
    delivered_at_ns BIGINT NOT NULL DEFAULT 0,
    quarantined_at_ns BIGINT NOT NULL DEFAULT 0,
    last_error_class VARCHAR(128) NOT NULL DEFAULT '',
    PRIMARY KEY (event_id, consumer_group)
);
CREATE INDEX idx_outbox_delivery_poll ON outbox_deliveries(consumer_group, delivered_at_ns, quarantined_at_ns, available_at_ns, lease_expires_at_ns);

CREATE TABLE resource_leases (
    resource_kind VARCHAR(64) NOT NULL,
    workspace_id BIGINT NOT NULL REFERENCES workspaces(id),
    resource_id TEXT NOT NULL,
    owner_id VARCHAR(128) NOT NULL,
    fencing_token BIGINT NOT NULL CHECK (fencing_token > 0),
    expires_at_ns BIGINT NOT NULL,
    PRIMARY KEY (resource_kind, workspace_id, resource_id)
);

-- Runtime configuration and the Secret Store.
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
    data_aad_version INTEGER NOT NULL DEFAULT 1 CHECK (data_aad_version > 0),
    data_aad_key_id VARCHAR(128) NOT NULL DEFAULT '',
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

-- Access Profiles.
CREATE TABLE access_profiles (
    id TEXT PRIMARY KEY,
    workspace_id BIGINT NOT NULL REFERENCES workspaces(id),
    name VARCHAR(128) NOT NULL,
    description VARCHAR(512) NOT NULL DEFAULT '',
    current_revision BIGINT NOT NULL CHECK(current_revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by BIGINT NOT NULL,
    deleted_at TEXT,
    UNIQUE(workspace_id, name),
    UNIQUE(workspace_id, id)
);
CREATE TABLE access_profile_revisions (
    profile_id TEXT NOT NULL REFERENCES access_profiles(id),
    workspace_id BIGINT NOT NULL,
    revision BIGINT NOT NULL CHECK(revision > 0),
    document TEXT NOT NULL,
    checksum VARCHAR(64) NOT NULL,
    created_at TEXT NOT NULL,
    created_by BIGINT NOT NULL,
    PRIMARY KEY(profile_id, revision),
    FOREIGN KEY(workspace_id, profile_id) REFERENCES access_profiles(workspace_id, id)
);
CREATE INDEX idx_managed_hosts_access_profile ON managed_hosts(workspace_id, access_profile_id, id);

-- Access Profile integrity guards.
CREATE FUNCTION machinarchy_guard_managed_host_profile() RETURNS trigger AS $$
BEGIN
    IF NEW.access_profile_id <> '' AND NOT EXISTS (
        SELECT 1 FROM access_profiles p
        WHERE p.workspace_id = NEW.workspace_id AND p.id = NEW.access_profile_id AND p.deleted_at IS NULL
    ) THEN
        RAISE EXCEPTION 'managed Host Access Profile must exist in the same workspace';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER managed_host_profile_guard
BEFORE INSERT OR UPDATE OF workspace_id, access_profile_id ON managed_hosts
FOR EACH ROW EXECUTE FUNCTION machinarchy_guard_managed_host_profile();

CREATE FUNCTION machinarchy_reject_access_profile_revision_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'Access Profile revisions are immutable';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER access_profile_revision_update_guard
BEFORE UPDATE OR DELETE ON access_profile_revisions
FOR EACH ROW EXECUTE FUNCTION machinarchy_reject_access_profile_revision_mutation();

CREATE FUNCTION machinarchy_guard_access_profile_delete() RETURNS trigger AS $$
BEGIN
    IF NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL AND EXISTS (
        SELECT 1 FROM managed_hosts h WHERE h.workspace_id = OLD.workspace_id AND h.access_profile_id = OLD.id
    ) THEN
        RAISE EXCEPTION 'referenced Access Profiles cannot be deleted';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER access_profile_delete_reference_guard
BEFORE UPDATE OF deleted_at ON access_profiles
FOR EACH ROW EXECUTE FUNCTION machinarchy_guard_access_profile_delete();

-- Host agents enrolled through the agent gateway (protocol/agentv1).
CREATE TABLE agents (
    id TEXT PRIMARY KEY,
    workspace_id BIGINT NOT NULL REFERENCES workspaces(id),
    request_id TEXT NOT NULL UNIQUE,
    public_key TEXT NOT NULL UNIQUE,
    hostname TEXT NOT NULL,
    agent_version TEXT NOT NULL,
    os TEXT NOT NULL,
    architecture TEXT NOT NULL,
    capabilities TEXT NOT NULL DEFAULT '[]',
    lease_id TEXT NOT NULL,
    lease_expires_at TEXT NOT NULL,
    registered_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    revoked_at TEXT
);
CREATE INDEX idx_agents_workspace ON agents(workspace_id, id);
