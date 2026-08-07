-- Initial schema: users, sessions, jobs, events.
--
-- Timestamps are RFC 3339 in UTC, stored as TEXT. SQLite has no date type, and
-- text sorts correctly in that format — which matters because the audit and
-- event tables are queried by time far more than anything else.

CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    -- argon2id, encoded with its parameters so the cost can be raised later
    -- without invalidating existing hashes.
    password_hash TEXT NOT NULL,
    -- JSON array. Denormalised deliberately: permissions are read on every
    -- request and written approximately never.
    permissions   TEXT NOT NULL DEFAULT '[]',
    created_at    TEXT NOT NULL,
    last_login_at TEXT
);

CREATE TABLE sessions (
    -- The token itself is never stored. This is a SHA-256 of it, so a stolen
    -- database does not hand over live sessions.
    token_hash TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    -- Recorded for the "where you are signed in" view a user should be able to
    -- see. Never used to make an authorisation decision: it is client-supplied.
    user_agent TEXT
);

CREATE INDEX sessions_user ON sessions(user_id);
CREATE INDEX sessions_expiry ON sessions(expires_at);

CREATE TABLE jobs (
    id        TEXT PRIMARY KEY,
    operation TEXT NOT NULL,
    state     TEXT NOT NULL,
    stage     TEXT,
    progress  INTEGER,
    -- Written for the person reading it, not for the developer who wrote it.
    message   TEXT,

    -- The error envelope, as JSON, matching schemas/error.schema.json.
    error TEXT,

    cancellable INTEGER NOT NULL DEFAULT 0,

    -- A repeated key returns the original job rather than starting a second
    -- one. A user on poor Wi-Fi presses the button twice; an automated client
    -- that loses a connection retries.
    idempotency_key TEXT UNIQUE,

    -- The kernel's boot id when the job started.
    --
    -- This is how a reboot job resolves itself. The connection dies with the
    -- machine, so nothing can observe the operation completing. On the next
    -- start, a job left in `running` whose recorded boot id differs from the
    -- current one is evidence the machine really did restart — the same test
    -- the VM tests use, for the same reason. Without it the alternative is
    -- assuming success, and a job system that assumes is one nobody can trust.
    boot_id TEXT,

    -- Whether losing the machine mid-operation is the expected outcome rather
    -- than a failure. True for reboot; false for everything else so far.
    interrupts_host INTEGER NOT NULL DEFAULT 0,

    created_by  TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at  TEXT NOT NULL,
    started_at  TEXT,
    finished_at TEXT
);

CREATE INDEX jobs_state ON jobs(state);
CREATE INDEX jobs_created ON jobs(created_at DESC);

CREATE TABLE events (
    id          TEXT PRIMARY KEY,
    -- Machine-readable, domain.event. Consumers must never have to parse a
    -- human-readable string to learn what happened.
    type        TEXT NOT NULL,
    severity    TEXT NOT NULL,
    subject     TEXT,
    reason      TEXT,
    recoverable INTEGER,
    -- A human rendering. Never the only source of information.
    message     TEXT,
    occurred_at TEXT NOT NULL
);

CREATE INDEX events_occurred ON events(occurred_at DESC);
CREATE INDEX events_severity ON events(severity, occurred_at DESC);
