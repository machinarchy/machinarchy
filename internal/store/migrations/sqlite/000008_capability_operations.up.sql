-- Agent capability Operations (ADR-057, AUDIT/10 Phase 17).
--
-- 1. operations.kind gains 'Capability'. SQLite cannot change a CHECK, so
--    the table is rebuilt. Migrations run with foreign keys on, and dropping
--    the old operations table cascade-deletes the rows that reference it, so
--    operation_targets is rebuilt as well: both are copied before anything
--    is dropped, and renaming the new parent updates the new child's
--    reference. (Rebuilding only operations would delete every target.)
CREATE TABLE operations_v8 (
    id TEXT PRIMARY KEY,
    workspace_id INTEGER NOT NULL REFERENCES workspaces(id),
    kind TEXT NOT NULL CHECK (kind IN ('Run', 'Capability')),
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
INSERT INTO operations_v8 SELECT id, workspace_id, kind, state, revision, actor_user_id, actor_token_id,
    spec_encrypted, cancel_asked, conditions, created_at, started_at, finished_at FROM operations;
CREATE TABLE operation_targets_v8 (
    operation_id TEXT NOT NULL REFERENCES operations_v8(id) ON DELETE CASCADE,
    host_id TEXT NOT NULL,
    host_generation INTEGER NOT NULL,
    display_name TEXT NOT NULL,
    access_profile_id TEXT NOT NULL DEFAULT '',
    access_profile_revision INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL CHECK (state IN ('Pending', 'Running', 'Succeeded', 'Failed', 'TimedOut', 'Cancelled', 'Interrupted', 'Lost')),
    exit_code INTEGER,
    output TEXT NOT NULL DEFAULT '',
    error_class TEXT NOT NULL DEFAULT '',
    launch TEXT NOT NULL DEFAULT '',
    live TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (operation_id, host_id)
);
INSERT INTO operation_targets_v8 SELECT operation_id, host_id, host_generation, display_name, access_profile_id,
    access_profile_revision, state, exit_code, output, error_class, launch, live FROM operation_targets;
DROP TABLE operation_targets;
DROP TABLE operations;
ALTER TABLE operations_v8 RENAME TO operations;
ALTER TABLE operation_targets_v8 RENAME TO operation_targets;
CREATE INDEX idx_operations_workspace_created ON operations(workspace_id, created_at DESC, id DESC);
CREATE INDEX idx_operations_state_created ON operations(state, created_at, id);
CREATE INDEX idx_operation_targets_host ON operation_targets(host_id, operation_id);
CREATE INDEX idx_operation_targets_detached ON operation_targets(state, operation_id) WHERE launch <> '';

-- 2. A per-host enrolment token binds the agent that uses it to that host.
ALTER TABLE agent_enrollment_tokens ADD COLUMN host_id TEXT NOT NULL DEFAULT '';

-- 3. Which agent serves which managed host (one each way).
CREATE TABLE agent_host_bindings (
    host_id TEXT PRIMARY KEY REFERENCES managed_hosts(id) ON DELETE CASCADE,
    workspace_id INTEGER NOT NULL REFERENCES workspaces(id),
    agent_id TEXT NOT NULL UNIQUE REFERENCES agents(id),
    token_id TEXT NOT NULL,
    bound_at_ns INTEGER NOT NULL
);

-- 4. Commands to agents, written with the Operation's targets and published
--    by the relay (PubAck-gated, Nats-Msg-Id = command_id). Times are Unix
--    nanoseconds.
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
    next_attempt_at_ns INTEGER NOT NULL,
    created_at_ns INTEGER NOT NULL,
    expires_at_ns INTEGER NOT NULL,
    -- When a target with no terminal result becomes Lost (request rows only).
    result_due_at_ns INTEGER NOT NULL DEFAULT 0,
    published_at_ns INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_operation_agent_commands_due ON operation_agent_commands(state, next_attempt_at_ns);
CREATE INDEX idx_operation_agent_commands_target ON operation_agent_commands(operation_id, host_id);
CREATE INDEX idx_operation_agent_commands_agent_op ON operation_agent_commands(agent_operation_id);
