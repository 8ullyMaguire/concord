-- Guests, consent, and the MAU read that closes the door.
--
-- A guest is a real users row. Everything a guest does already points at a
-- users.id -- 47 columns in this schema carry `REFERENCES users(id)` -- so a
-- parallel guests table would mean a second owner column on all of them, or a
-- nullable one, which is two sources of truth for "who did this". This schema has
-- already paid for that mistake twice; the fold is a column list precisely
-- because the owner of a row is a users.id and nothing else.
--
-- password_hash stays '' for a guest. That is not a new convention: 0003 added the
-- column with the comment that a credential-less account "cannot log in", and
-- Login refuses an empty hash explicitly rather than treating it as a match.

ALTER TABLE users ADD COLUMN is_guest INTEGER NOT NULL DEFAULT 0;

-- When the visitor accepted the cookie notice. NULL = not accepted. Stamped
-- against the identity when there is one, so "who accepted, and when" survives
-- the browser that held the cookie.
ALTER TABLE users ADD COLUMN consented_at REAL;

-- The MAU read is COUNT(DISTINCT actor_id) over a 30-day window, and it runs on
-- the request path of every guest write. Without this index that is a full scan
-- of the audit log each time.
CREATE INDEX IF NOT EXISTS idx_audit_actor_created ON audit_log(actor_id, created_at);

-- The operator surface counts guests; the fold and the status read both filter on
-- this flag.
CREATE INDEX IF NOT EXISTS idx_users_is_guest ON users(is_guest);
