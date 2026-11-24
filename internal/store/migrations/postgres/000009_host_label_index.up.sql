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

INSERT INTO managed_host_labels (host_id, workspace_id, label_key, label_value)
SELECT h.id, h.workspace_id, j.key, j.value
FROM managed_hosts h, jsonb_each_text(h.labels::jsonb) j;
