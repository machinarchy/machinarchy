-- Detached execution (AUDIT/10b §1): the Lost target state, each target's
-- launch handle (plain JSON: mode, unit, pid, state root, endpoint, times)
-- and its live progress while running (encrypted like target output).
ALTER TABLE operation_targets DROP CONSTRAINT IF EXISTS operation_targets_state_check;
ALTER TABLE operation_targets ADD CONSTRAINT operation_targets_state_check
    CHECK (state IN ('Pending', 'Running', 'Succeeded', 'Failed', 'TimedOut', 'Cancelled', 'Interrupted', 'Lost'));
ALTER TABLE operation_targets ADD COLUMN launch TEXT NOT NULL DEFAULT '';
ALTER TABLE operation_targets ADD COLUMN live TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_operation_targets_detached ON operation_targets(state, operation_id) WHERE launch <> '';
