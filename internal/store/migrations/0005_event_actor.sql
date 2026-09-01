-- Who did it.
--
-- The audit log could not answer the first question anybody asks of one. Until
-- this server had more than one account that was not a real gap: there was one
-- person, and "who" had one answer. There are now roles, invitations and
-- removals, all of which are things one person does to another's access.
--
-- Nullable, and null is not "unknown". A disk being unplugged, a scheduled
-- backup, an update arriving — nothing did those on somebody's behalf, and
-- recording a name there would be inventing one. Existing rows are null for the
-- same reason: nobody knows who they were, and a migration that guessed would
-- put a wrong name in an audit log, which is worse than an empty one.
ALTER TABLE events ADD COLUMN actor TEXT;

-- Reading back what one person did is the query this exists for.
CREATE INDEX IF NOT EXISTS idx_events_actor ON events(actor, occurred_at DESC);
