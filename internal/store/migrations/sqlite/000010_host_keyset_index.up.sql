-- The periodic resync pages through every Host in (workspace_id, id) order
-- (AUDIT Q-08). With this index each page is an index range read instead of
-- a sort of the whole table.
CREATE INDEX idx_managed_hosts_keyset ON managed_hosts(workspace_id, id);
