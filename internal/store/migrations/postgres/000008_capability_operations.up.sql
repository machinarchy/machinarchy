-- Agent capability Operations (ADR-057, AUDIT/10 Phase 17).
ALTER TABLE operations DROP CONSTRAINT IF EXISTS operations_kind_check;
ALTER TABLE operations ADD CONSTRAINT operations_kind_check CHECK (kind IN ('Run', 'Capability'));

-- A per-host enrolment token binds the agent that uses it to that host.
ALTER TABLE agent_enrollment_tokens ADD COLUMN host_id TEXT NOT NULL DEFAULT '';

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
