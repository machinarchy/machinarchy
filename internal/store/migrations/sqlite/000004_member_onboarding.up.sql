-- Member onboarding tokens (ADR-060). Only the SHA-256 hash of a token is
-- stored. kind 'invite' is an emailed invitation; 'add' is reserved for the
-- owner-added user flow (step 2.4).
CREATE TABLE member_onboarding (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('invite', 'add')),
    token_hash TEXT NOT NULL UNIQUE,
    email TEXT COLLATE NOCASE,
    username TEXT,
    role TEXT NOT NULL CHECK (role IN ('operator', 'reader')),
    created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

CREATE UNIQUE INDEX idx_member_onboarding_invite_email
    ON member_onboarding(workspace_id, email) WHERE kind = 'invite';
