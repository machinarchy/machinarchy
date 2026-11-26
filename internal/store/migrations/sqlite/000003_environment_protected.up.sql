-- Protected environments (C-02): the server refuses guarded calls on their hosts
-- unless the caller confirms the environment name.
ALTER TABLE environments ADD COLUMN protected INTEGER NOT NULL DEFAULT 0;
