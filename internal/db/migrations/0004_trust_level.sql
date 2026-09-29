-- Concord migration 0004: trust level for status transitions.
--
-- The store has had UpdateFeatureStatus since M2, but nothing could call it:
-- a feature was created as `draft` and only ever reached `shipped` by
-- approving a merge request, which is the right path for code and the wrong
-- one for importing a portfolio that already exists. There was no way to
-- express "this is a plan" or "this is an idea" at all.
--
-- Roles already exist (members.role, six levels) and requireRole already
-- enforces them, so this adds no new concept — it adds a per-user numeric
-- trust level that a configurable minimum is compared against. Keeping it
-- separate from the role matters: role is per-project and answers "what may
-- this person do here", while trust level is per-user and answers "how much
-- do we believe them on the whole instance".
ALTER TABLE users ADD COLUMN trust_level INTEGER NOT NULL DEFAULT 0;

-- The seeding path sets an absolute level, so the ceiling is recorded rather
-- than implied. A level above the ceiling cannot be reached by assignment
-- either, which makes the ceiling a real bound rather than documentation.
CREATE TABLE trust_config (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    max_level     INTEGER NOT NULL DEFAULT 1,
    updated_at    REAL NOT NULL
);

INSERT OR IGNORE INTO trust_config (id, max_level, updated_at)
VALUES (1, 1, strftime('%s','now'));

-- Audit trail for a privilege change. Assigning trust is the one write in
-- this schema that can make an account able to approve things, so it is
-- recorded with the actor that performed it rather than being a bare UPDATE.
CREATE TABLE trust_grants (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    level       INTEGER NOT NULL,
    granted_by  INTEGER NOT NULL REFERENCES users(id),
    reason      TEXT NOT NULL DEFAULT '',
    created_at  REAL NOT NULL
);

CREATE INDEX idx_trust_grants_user ON trust_grants(user_id);
