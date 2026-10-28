-- Enforce host identity in the database so concurrent registrations cannot
-- create duplicates. Within one workspace a host name must be unique
-- (case-insensitively) and so must an address:port pair.
--
-- The migrator runs a pre-flight check (internal/store/preflight.go) before this
-- file and refuses to continue, listing the conflicts, if existing rows would
-- violate these indexes. No rows are ever renamed or deleted automatically.
--
-- NOCASE folds ASCII letters only, which matches SQLite's lower() used by the
-- pre-flight check; host names and addresses are expected to be ASCII.
CREATE UNIQUE INDEX IF NOT EXISTS idx_hosts_workspace_name
    ON hosts(workspace_id, name COLLATE NOCASE);
CREATE UNIQUE INDEX IF NOT EXISTS idx_hosts_workspace_address
    ON hosts(workspace_id, ip_address COLLATE NOCASE, port);
