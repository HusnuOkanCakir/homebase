-- Recovery codes: the way back in when the password is gone.
--
-- ADR-0015. One active code per account, stored the same way a password is —
-- argon2id, never in a form Homebase can display again. A code that could be
-- shown twice would be a code an attacker who reads this file can use.

CREATE TABLE recovery_codes (
    user_id   TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    code_hash TEXT NOT NULL,
    issued_at TEXT NOT NULL,

    -- When this account was last recovered using a code. Carried forward when a
    -- used code is replaced, because the interesting fact is that a reset
    -- happened at all — the dashboard shows it, and somebody who did not do it
    -- needs to see it.
    last_used_at TEXT
);

-- Whether a code exists and when it was issued is shown on the security screen,
-- so somebody can find out they never wrote one down before they need it.
