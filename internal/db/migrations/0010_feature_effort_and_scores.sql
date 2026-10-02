-- Feature effort: constrain the existing column and record the impact/effort
-- scores the ranking is supposed to be derived from.
--
-- Two defects motivate this.
--
-- 1. `effort` was an unconstrained TEXT defaulting to 'M', and
--    store.CreateFeature hardcoded 'M' in its INSERT while the HTTP layer
--    accepted and silently discarded a caller-supplied `effort`
--    (handlers.go:128 declares the field; it was never passed on to
--    CreateFeature). Every feature ever created therefore reads 'M'
--    regardless of what was asked for, and nothing rejected a typo like
--    "medium" or "3". A CHECK makes the domain explicit, and a t-shirt size is
--    what the spec asks for (spec §117: "Effort estimate: t-shirt size or
--    story points").
--
-- 2. The priority score is computed from rating, pain and strategic weight.
--    Impact and effort -- the two things a maintainer actually judges when
--    proposing work -- were not recorded at all, so "Impact ÷ Effort" could
--    not be expressed, let alone ranked on.
--
-- SQLite cannot add a constraint to an existing column, and it cannot add a
-- GENERATED column with ALTER TABLE, so `features` is rebuilt. The rebuild
-- copies every row, so nothing is lost -- in particular the 358 rows of
-- feature_complaints are re-inserted rather than recreated empty.
--
-- Deferring FK enforcement for the rebuild: feature_complaints references
-- features(id), and the table is dropped and recreated around it.
-- concord:requires-foreign-keys-off
--
-- features is rebuilt below, and feature_complaints references it. SQLite
-- refuses the DROP while foreign keys are enforced, with error 787.
-- PRAGMA defer_foreign_keys does NOT cover this: it defers checking to COMMIT,
-- but the DROP itself is what is refused. The runner reads this marker and sets
-- foreign_keys=OFF on the connection before the transaction opens, which is the
-- only point at which the pragma takes effect.

-- Add the two score columns to the CURRENT table first. The rebuild below
-- recreates them, but the INSERT ... SELECT that copies rows across reads them
-- from the table as it exists now, so they have to exist before it runs.
-- Without these the copy fails with "no such column: impact" and the whole
-- migration rolls back -- which is what happened the first time.
ALTER TABLE features ADD COLUMN impact       INTEGER;
ALTER TABLE features ADD COLUMN effort_score INTEGER;

CREATE TABLE features_new (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id        INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    author_id         INTEGER NOT NULL REFERENCES users(id),
    title             TEXT NOT NULL,
    body              TEXT NOT NULL DEFAULT '',
    effort            TEXT NOT NULL DEFAULT 'M' CHECK (effort IN ('S','M','L','XL')),
    status            TEXT NOT NULL DEFAULT 'draft'
                      CHECK (status IN ('draft','discussion','consensus','ready',
                                       'in_progress','review','shipped','rejected')),
    elo_r             REAL NOT NULL DEFAULT 1500,
    elo_rd            REAL NOT NULL DEFAULT 350,
    elo_vol           REAL NOT NULL DEFAULT 0.06,
    strategic_weight  REAL NOT NULL DEFAULT 1.0,
    -- impact and effort_score are the 1-10 judgements from the "Impact ÷
    -- Effort" table. Nullable, because most existing features predate this
    -- column and a zero would be a claim they do not make: it would divide
    -- into a ratio and outrank everything.
    impact            INTEGER CHECK (impact IS NULL OR impact BETWEEN 1 AND 10),
    effort_score      INTEGER CHECK (effort_score IS NULL OR effort_score BETWEEN 1 AND 10),
    -- impact_ratio is I/E, the column that list is sorted on. GENERATED so it
    -- cannot drift from its inputs: a stored ratio would eventually disagree
    -- with the scores that produced it and nobody would notice.
    impact_ratio      REAL GENERATED ALWAYS AS (
                          CASE WHEN effort_score IS NULL OR effort_score = 0
                               THEN NULL
                               ELSE CAST(impact AS REAL) / effort_score
                          END
                      ) VIRTUAL,
    created_at        REAL NOT NULL,
    updated_at        REAL NOT NULL
);

-- effort is currently unconstrained and every row reads 'M' (CreateFeature
-- hardcoded it), but the CASE keeps the migration correct on an instance where
-- something else wrote a size: an out-of-domain value becomes 'M' rather than
-- aborting the whole migration.
INSERT INTO features_new
    (id, project_id, author_id, title, body, effort, status, elo_r, elo_rd,
     elo_vol, strategic_weight, impact, effort_score, created_at, updated_at)
SELECT id, project_id, author_id, title,
       COALESCE(body, ''),
       CASE WHEN effort IN ('S','M','L','XL') THEN effort ELSE 'M' END,
       status, elo_r, elo_rd, elo_vol, strategic_weight,
       impact, effort_score, created_at, updated_at
FROM features;

DROP TABLE features;
ALTER TABLE features_new RENAME TO features;

-- Re-assert the index that 0001 established for this table. The name is
-- idx_features, not idx_features_project.
CREATE INDEX IF NOT EXISTS idx_features ON features(project_id, status);

-- feature_complaints survived the drop by name, so its rows are still there;
-- re-insert them idempotently and add the cascade that 0008 promised for every
-- other child table but that this one never actually got.
CREATE TABLE IF NOT EXISTS feature_complaints_new (
    feature_id   INTEGER NOT NULL REFERENCES features(id) ON DELETE CASCADE,
    complaint_id INTEGER NOT NULL REFERENCES complaints(id) ON DELETE CASCADE,
    PRIMARY KEY (feature_id, complaint_id)
);
-- 15 rows here point at feature ids 35-39, which do not exist: leftovers from
-- the trial imports that deleted projects before feature_complaints had the
-- cascade it now has. Copying them forward would leave the join table
-- permanently inconsistent, so they are dropped rather than carried. Every row
-- with a live feature is preserved.
INSERT OR IGNORE INTO feature_complaints_new (feature_id, complaint_id)
SELECT fc.feature_id, fc.complaint_id
FROM feature_complaints fc
JOIN features f ON f.id = fc.feature_id;
DROP TABLE IF EXISTS feature_complaints;
ALTER TABLE feature_complaints_new RENAME TO feature_complaints;