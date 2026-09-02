-- Joining a household, which is not the same thing as recovering an account.
--
-- Both were the recovery-code mechanism. That worked and said the wrong things:
-- somebody joining the server produced an event reading "The password for
-- father was reset using the recovery code" at error severity, with the message
-- that everything signed in as them had been signed out. Nothing had been. It
-- was their first sign-in.
--
-- Beyond the wording, the two want different properties. A recovery code is
-- deliberately permanent — it is written on paper and kept for the day it is
-- needed, and an expiring one would be worthless exactly then. An invitation is
-- handed over in a message or across a kitchen table and used within the hour,
-- so it should stop working; a joining code that is still live in six months is
-- a way into the server sitting in somebody's chat history.
--
-- One per account, replaced when reissued, and hashed the way a password is.
CREATE TABLE invitations (
    user_id    TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    code_hash  TEXT NOT NULL,

    -- Who invited them. An account is a way into this server that outlives
    -- whoever granted it, so the grant is worth being able to read back even
    -- after the person who made it has gone.
    issued_by  TEXT,

    issued_at  TEXT NOT NULL,
    expires_at TEXT NOT NULL,

    -- Set when the invitation is used. The row is kept rather than deleted:
    -- "this account was joined on the fourth" is a fact worth having, and a
    -- deleted row cannot say it.
    accepted_at TEXT
);
