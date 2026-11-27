-- Member onboarding tokens (ADR-060). Only the SHA-256 hash of a token is
-- stored. kind 'invite' is an emailed invitation; 'add' is reserved for the
-- owner-added user flow (step 2.4).
CREATE TABLE member_onboarding (
    id BIGSERIAL PRIMARY KEY,
    workspace_id BIGINT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('invite', 'add')),
    token_hash TEXT NOT NULL UNIQUE,
    email VARCHAR(320),
    username VARCHAR(255),
    role TEXT NOT NULL CHECK (role IN ('operator', 'reader')),
    created_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

CREATE UNIQUE INDEX idx_member_onboarding_invite_email
    ON member_onboarding(workspace_id, lower(email)) WHERE kind = 'invite';
