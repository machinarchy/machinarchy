CREATE TABLE access_profiles (
    id TEXT PRIMARY KEY,
    workspace_id BIGINT NOT NULL REFERENCES workspaces(id),
    name VARCHAR(128) NOT NULL,
    description VARCHAR(512) NOT NULL DEFAULT '',
    current_revision BIGINT NOT NULL CHECK(current_revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by BIGINT NOT NULL,
    deleted_at TEXT,
    UNIQUE(workspace_id, name),
    UNIQUE(workspace_id, id)
);
CREATE TABLE access_profile_revisions (
    profile_id TEXT NOT NULL REFERENCES access_profiles(id),
    workspace_id BIGINT NOT NULL,
    revision BIGINT NOT NULL CHECK(revision > 0),
    document TEXT NOT NULL,
    checksum VARCHAR(64) NOT NULL,
    created_at TEXT NOT NULL,
    created_by BIGINT NOT NULL,
    PRIMARY KEY(profile_id, revision),
    FOREIGN KEY(workspace_id, profile_id) REFERENCES access_profiles(workspace_id, id)
);
CREATE INDEX idx_managed_hosts_access_profile ON managed_hosts(workspace_id, access_profile_id, id);
