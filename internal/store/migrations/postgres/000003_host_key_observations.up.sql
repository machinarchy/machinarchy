-- SSH host keys last seen for each managed Host (AUDIT/10b §2): kept so an
-- owner can approve them, or see what changed.
ALTER TABLE managed_hosts ADD COLUMN observed_host_keys TEXT NOT NULL DEFAULT '[]';
