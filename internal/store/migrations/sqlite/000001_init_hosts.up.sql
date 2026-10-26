CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS hosts (
    id TEXT PRIMARY KEY,               -- Text-based primary identifier matching domain.Host.ID
    environment_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    hostname TEXT NOT NULL,
    ip_address TEXT NOT NULL,
    connection TEXT NOT NULL,          -- Maps to ConnectionMethod (SSH, AGENT_PULL, etc.)
    status TEXT NOT NULL,              -- Maps to HostStatus (PENDING, ACTIVE, etc.)
    
    -- Native Map Support via Binary JSON (Requires SQLite 3.45+)
    labels BLOB NOT NULL DEFAULT (jsonb('{}')), -- Maps cleanly to Go's map[string]string
    
    -- Flattened SSH Access Sub-configuration Layer
    port INTEGER NOT NULL DEFAULT 22,
    username TEXT NOT NULL,
    password TEXT NOT NULL,            -- Encrypted string payload
    private_key TEXT NOT NULL,         -- Encrypted string payload
    passphrase TEXT NOT NULL,          -- Encrypted string payload
    expected_fingerprint TEXT NOT NULL,
    
    -- Housekeeping ISO8601 UTC Timestamps
    created_at TEXT NOT NULL,          -- Managed via time.RFC3339 strings in Go
    updated_at TEXT NOT NULL
);

-- Production Performance Optimization Indexes
CREATE INDEX IF NOT EXISTS idx_hosts_status ON hosts(status);
CREATE INDEX IF NOT EXISTS idx_hosts_environment ON hosts(environment_id);
