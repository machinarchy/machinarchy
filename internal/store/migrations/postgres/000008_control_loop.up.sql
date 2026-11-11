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
