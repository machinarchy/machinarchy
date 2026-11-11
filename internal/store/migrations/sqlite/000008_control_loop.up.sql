CREATE TABLE outbox_events (
    event_id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version > 0),
    occurred_at TEXT NOT NULL,
    producer TEXT NOT NULL,
    workspace_id INTEGER NOT NULL REFERENCES workspaces(id),
    subject_kind TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    subject_generation INTEGER NOT NULL CHECK (subject_generation > 0),
    subject_revision INTEGER NOT NULL CHECK (subject_revision > 0),
    correlation_id TEXT NOT NULL DEFAULT '',
    causation_id TEXT NOT NULL DEFAULT '',
    actor_user_id INTEGER,
    actor_token_id INTEGER,
    payload TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_outbox_subject ON outbox_events(workspace_id, subject_kind, subject_id, subject_revision);
CREATE INDEX idx_outbox_created ON outbox_events(created_at, event_id);

CREATE TABLE outbox_deliveries (
    event_id TEXT NOT NULL REFERENCES outbox_events(event_id) ON DELETE CASCADE,
    consumer_group TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at_ns INTEGER NOT NULL,
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_expires_at_ns INTEGER NOT NULL DEFAULT 0,
    delivered_at_ns INTEGER NOT NULL DEFAULT 0,
    quarantined_at_ns INTEGER NOT NULL DEFAULT 0,
    last_error_class TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (event_id, consumer_group)
);
CREATE INDEX idx_outbox_delivery_poll ON outbox_deliveries(consumer_group, delivered_at_ns, quarantined_at_ns, available_at_ns, lease_expires_at_ns);

CREATE TABLE resource_leases (
    resource_kind TEXT NOT NULL,
    workspace_id INTEGER NOT NULL REFERENCES workspaces(id),
    resource_id TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    fencing_token INTEGER NOT NULL CHECK (fencing_token > 0),
    expires_at_ns INTEGER NOT NULL,
    PRIMARY KEY (resource_kind, workspace_id, resource_id)
);
