-- Machinarchy baseline schema (squashed during development; last squash
-- 2026-11-25, before the first release).
--
-- It contains the whole current schema: accounts and sessions, the managed
-- resource model (hosts, labels, Operations and their targets, agent commands),
-- agents and their enrolment, signing keys, secrets and the runtime
-- configuration. A database created by an earlier development schema must be
-- recreated: the migrator refuses to run against it.
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
    -- SSH host keys last seen for the Host: kept so an owner can approve them,
    -- or see what changed.
    observed_host_keys TEXT NOT NULL DEFAULT '[]',
    UNIQUE (workspace_id, name),
    FOREIGN KEY (workspace_id, environment_id) REFERENCES environments(workspace_id, id)
);
CREATE INDEX idx_managed_hosts_resync ON managed_hosts(workspace_id, phase, updated_at, id);
CREATE INDEX idx_managed_hosts_generation ON managed_hosts(workspace_id, generation, observed_generation);
-- The periodic resync pages through every Host in (workspace_id, id) order;
-- with this index each page is an index range read.
CREATE INDEX idx_managed_hosts_keyset ON managed_hosts(workspace_id, id);

CREATE TABLE operations (
    id TEXT PRIMARY KEY,
    workspace_id BIGINT NOT NULL REFERENCES workspaces(id),
    kind TEXT NOT NULL CHECK (kind IN ('Run', 'Capability')),
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
    state TEXT NOT NULL CHECK (state IN ('Pending', 'Running', 'Succeeded', 'Failed', 'TimedOut', 'Cancelled', 'Interrupted', 'Lost')),
    exit_code INTEGER,
    output TEXT NOT NULL DEFAULT '',
    error_class TEXT NOT NULL DEFAULT '',
    -- Detached execution: the launch handle (plain JSON: mode, unit, pid, state
    -- root, endpoint, times) and the live progress while running (encrypted
    -- like target output).
    launch TEXT NOT NULL DEFAULT '',
    live TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (operation_id, host_id)
);
CREATE INDEX idx_operation_targets_host ON operation_targets(host_id, operation_id);
CREATE INDEX idx_operation_targets_detached ON operation_targets(state, operation_id) WHERE launch <> '';

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

-- Owner-issued, single-use agent enrolment tokens (AUDIT M-4). Only the
-- SHA-256 of the secret is stored. A token is consumed in the same
-- transaction that registers the agent.
CREATE TABLE agent_enrollment_tokens (
    id TEXT PRIMARY KEY,
    workspace_id BIGINT NOT NULL REFERENCES workspaces(id),
    token_hash TEXT NOT NULL UNIQUE,
    hostname TEXT,
    created_by BIGINT NOT NULL REFERENCES users(id),
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at TEXT,
    used_request_id TEXT,
    used_by_agent TEXT,
    revoked_at TEXT,
    -- A per-host enrolment token binds the agent that uses it to that host.
    host_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_agent_enrollment_tokens_workspace ON agent_enrollment_tokens(workspace_id, created_at);

-- Server-side login sessions (AUDIT S-08). The session JWT carries the row's
-- id; a request is accepted only while the row is active. Times are Unix
-- nanoseconds, so comparisons are exact (0 = never revoked).
CREATE TABLE user_sessions (
    id TEXT PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id BIGINT NOT NULL,
    created_at_ns BIGINT NOT NULL,
    expires_at_ns BIGINT NOT NULL,
    revoked_at_ns BIGINT NOT NULL DEFAULT 0,
    revoke_reason TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    client_ip TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_user_sessions_user ON user_sessions(user_id, revoked_at_ns, expires_at_ns);

-- Failed logins per account and per client network (AUDIT S-09), shared by
-- every replica. scope is 'account' or 'network'. Times are Unix nanoseconds.
CREATE TABLE login_failures (
    scope TEXT NOT NULL CHECK (scope IN ('account', 'network')),
    subject TEXT NOT NULL,
    failures INTEGER NOT NULL,
    window_started_at_ns BIGINT NOT NULL,
    blocked_until_ns BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (scope, subject)
);

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

-- Agent capability Operations (ADR-057).
-- Which agent serves which managed host (one each way).
CREATE TABLE agent_host_bindings (
    host_id TEXT PRIMARY KEY REFERENCES managed_hosts(id) ON DELETE CASCADE,
    workspace_id INTEGER NOT NULL REFERENCES workspaces(id),
    agent_id TEXT NOT NULL UNIQUE REFERENCES agents(id),
    token_id TEXT NOT NULL,
    bound_at_ns BIGINT NOT NULL
);

-- Commands to agents, written with the Operation's targets and published by
-- the relay (PubAck-gated, Nats-Msg-Id = command_id). Times are Unix ns.
CREATE TABLE operation_agent_commands (
    command_id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
    host_id TEXT NOT NULL,
    workspace_id INTEGER NOT NULL,
    agent_id TEXT NOT NULL,
    agent_operation_id TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('operation.request', 'operation.cancel', 'result.ack')),
    state TEXT NOT NULL CHECK (state IN ('pending', 'published', 'expired')),
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at_ns BIGINT NOT NULL,
    created_at_ns BIGINT NOT NULL,
    expires_at_ns BIGINT NOT NULL,
    -- When a target with no terminal result becomes Lost (request rows only).
    result_due_at_ns BIGINT NOT NULL DEFAULT 0,
    published_at_ns BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX idx_operation_agent_commands_due ON operation_agent_commands(state, next_attempt_at_ns);
CREATE INDEX idx_operation_agent_commands_target ON operation_agent_commands(operation_id, host_id);
CREATE INDEX idx_operation_agent_commands_agent_op ON operation_agent_commands(agent_operation_id);

-- Host labels as rows, so label selectors run in the database with an index
-- instead of a bounded scan in Go (AUDIT A-12). managed_hosts.labels stays the
-- source the API returns; this table mirrors it and is rewritten with it in
-- the same transaction.
CREATE TABLE managed_host_labels (
    host_id TEXT NOT NULL REFERENCES managed_hosts(id) ON DELETE CASCADE,
    workspace_id BIGINT NOT NULL,
    label_key TEXT NOT NULL,
    label_value TEXT NOT NULL,
    PRIMARY KEY (host_id, label_key)
);
CREATE INDEX idx_managed_host_labels_lookup ON managed_host_labels(workspace_id, label_key, label_value, host_id);
