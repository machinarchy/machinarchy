-- Explicitly track schema migrations
CREATE TABLE IF NOT EXISTS schema_migrations (
    version BIGINT PRIMARY KEY,
    applied_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS hosts (
    id TEXT PRIMARY KEY,               -- Keeps string type compatibility with your Go struct
    environment_id INT NOT NULL,
    name VARCHAR(255) NOT NULL,
    hostname VARCHAR(255) NOT NULL,
    ip_address VARCHAR(45) NOT NULL,   -- Accommodates up to IPv6 length strings
    connection VARCHAR(50) NOT NULL,   -- Maps to ConnectionMethod enum string
    status VARCHAR(50) NOT NULL,       -- Maps to HostStatus enum string
    labels JSONB NOT NULL DEFAULT '{}',-- Natively maps to Go's map[string]string
    
    -- SSH Specific Configuration Layer
    port INT NOT NULL DEFAULT 22,
    username VARCHAR(255) NOT NULL,
    password TEXT NOT NULL,            -- Encrypted payload
    private_key TEXT NOT NULL,         -- Encrypted payload
    passphrase TEXT NOT NULL,          -- Encrypted payload
    expected_fingerprint TEXT NOT NULL,
    
    -- High Precision Timestamps
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

-- Production Performance Optimization Indexes
CREATE INDEX IF NOT EXISTS idx_hosts_status ON hosts(status);
CREATE INDEX IF NOT EXISTS idx_hosts_environment ON hosts(environment_id);
CREATE INDEX IF NOT EXISTS idx_hosts_labels ON hosts USING gin(labels); -- Optimizes querying by specific labels
