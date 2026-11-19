-- Failed logins per account and per client network (AUDIT S-09), shared by
-- every replica. scope is 'account' or 'network'. Times are Unix nanoseconds.
CREATE TABLE login_failures (
    scope TEXT NOT NULL CHECK (scope IN ('account', 'network')),
    subject TEXT NOT NULL,
    failures INTEGER NOT NULL,
    window_started_at_ns BIGINT NOT NULL,
    blocked_until_ns BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (scope, subject)
);
