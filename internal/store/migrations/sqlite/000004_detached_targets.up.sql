-- Detached execution (AUDIT/10b §1): the Lost target state, each target's
-- launch handle (plain JSON: mode, unit, pid, state root, endpoint, times)
-- and its live progress while running (encrypted like target output).
-- SQLite cannot change a CHECK constraint, so the table is rebuilt.
CREATE TABLE operation_targets_v4 (
    operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
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
INSERT INTO operation_targets_v4 (operation_id, host_id, host_generation, display_name, access_profile_id,
    access_profile_revision, state, exit_code, output, error_class)
SELECT operation_id, host_id, host_generation, display_name, access_profile_id,
    access_profile_revision, state, exit_code, output, error_class FROM operation_targets;
DROP TABLE operation_targets;
ALTER TABLE operation_targets_v4 RENAME TO operation_targets;
CREATE INDEX idx_operation_targets_host ON operation_targets(host_id, operation_id);
CREATE INDEX idx_operation_targets_detached ON operation_targets(state, operation_id) WHERE launch <> '';
