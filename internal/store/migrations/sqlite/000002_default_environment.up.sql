-- Every host belongs to an environment. Each workspace gets a "default"
-- environment and hosts that have none join it. The column stays nullable;
-- the service always sets it.
INSERT INTO environments (workspace_id, name, description, created_at)
SELECT w.id, 'default', 'Default environment', '2026-01-01T00:00:00Z' FROM workspaces w
WHERE NOT EXISTS (SELECT 1 FROM environments e WHERE e.workspace_id = w.id AND lower(e.name) = 'default');

UPDATE managed_hosts SET environment_id = (
    SELECT e.id FROM environments e WHERE e.workspace_id = managed_hosts.workspace_id AND lower(e.name) = 'default')
WHERE environment_id IS NULL;
