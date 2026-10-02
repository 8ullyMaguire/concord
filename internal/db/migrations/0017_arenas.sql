-- Arenas: the single ranking engine (spec revision 4 §5).
--
-- §5 is the load-bearing structural change in revision 4, and this migration is
-- its price. Before it, ranking was hardcoded to features: `pairwise_votes`
-- carried `feature_a`/`feature_b`, `criterion_ratings` was keyed on
-- `feature_id`, and the rating itself lived in `features.elo_r/elo_rd/elo_vol`.
-- Three consequences the spec names explicitly:
--
--   - "Everything competes pairwise" (§2.2) was true of features only. The
--     solution arena (§6.4), the alternatives arena (§5 table), the use-case
--     arena and the Scout verdict arena all reuse the identical engine, and
--     none of them can be expressed as a feature_id.
--   - "Every ranking is recomputable from the public vote log" (§2.5) could not
--     hold for any entity whose rating lived in its own table, because the vote
--     log named features and nothing else.
--   - The spec's §10.2 data model lists `arenas(id, type, question, ...)` and
--     `arena_entries(arena, entity_type, entity_id, r, rd, sigma)`. Revision 3
--     had neither.
--
-- The design keeps the existing tables rather than replacing them. The reason is
-- regression risk: 532 complaints, 437 features and a live instance depend on
-- `features.elo_r`, and a rewrite that moved every rating to `arena_entries`
-- would need the vote log backfilled with per-entity rows it never stored. So
-- features keep their columns and gain a per-project arena; the new tables carry
-- the same (r, rd, sigma) triple for every other entity type. `PairwiseVotes`
-- gains `arena_id`, and existing rows are backfilled into a `feature-priority`
-- arena so one query shape answers every arena.

CREATE TABLE arenas (
    id         INTEGER PRIMARY KEY,
    -- 'feature-priority' | 'solution' | 'list' | 'request' | 'alternatives' |
    -- 'use-case'. Checked because this is the discriminator every lookup
    -- filters on and a typo would hide an arena rather than fail.
    type       TEXT NOT NULL CHECK (type IN
                ('feature-priority','solution','list','request','alternatives','use-case')),

    -- The owning entity. A feature-priority arena belongs to a project; a
    -- solution arena belongs to a feature; an alternatives arena belongs to a
    -- project and is scoped by a use case. Stored as two nullable columns
    -- rather than a polymorphic (owner_type, owner_id) pair because the project
    -- is the one that matters for scoping and permission, and a bare integer
    -- would need a join to find out whether it is a project or a feature.
    project_id INTEGER REFERENCES projects(id) ON DELETE CASCADE,
    feature_id INTEGER REFERENCES features(id) ON DELETE CASCADE,

    -- The question put to voters, §5's table: "Which should we prioritize
    -- first?", "Which approach should we build?". Stored rather than derived
    -- from `type` because §5.1 lets a pair carry enough context to judge, and a
    -- generic string is what that context is.
    question   TEXT NOT NULL DEFAULT '',

    -- Use case for the use-case and alternatives arenas (§5: "Best for
    -- self-hosted kanban, small team"). NULL elsewhere. Part of the arena's
    -- identity rather than of the question, because the same two projects rank
    -- differently for different use cases and those are different arenas.
    use_case   TEXT,

    -- §6.3: "Every solution arena has a permanent baseline: do nothing /
    -- document the workaround." NULL for arenas with no baseline.
    baseline_entry_id INTEGER,

    created_at REAL NOT NULL
);

-- One arena per (type, owner). Enforced in the schema rather than in code so two
-- concurrent requests cannot each create "the" solution arena for a feature and
-- leave the second one orphaned.
CREATE UNIQUE INDEX idx_arenas_feature_priority
    ON arenas(project_id) WHERE type = 'feature-priority';
CREATE UNIQUE INDEX idx_arenas_solution
    ON arenas(feature_id) WHERE type = 'solution';
-- The use case is part of the key only for the two arena types that have one;
-- NULL use_case must not collide across unrelated arenas, and SQLite treats
-- NULLs as distinct in unique indexes anyway, so this is a plain index that
-- only constrains non-NULL use cases.
CREATE UNIQUE INDEX idx_arenas_use_case
    ON arenas(project_id, use_case) WHERE type = 'use-case' AND use_case IS NOT NULL;
CREATE UNIQUE INDEX idx_arenas_alternatives
    ON arenas(project_id, use_case) WHERE type = 'alternatives' AND use_case IS NOT NULL;

-- Existing feature votes predate arenas, so each project gets a feature-priority
-- arena and its votes are attached to it. Without this backfill every historical
-- vote would be invisible to the arena queries, and a recompute from the log
-- would silently drop the entire ranking history.
INSERT INTO arenas (id, type, project_id, feature_id, question, use_case, baseline_entry_id, created_at)
SELECT p.id,
       'feature-priority',
       p.id,
       NULL,
       'Which should we prioritize first?',
       NULL,
       NULL,
       COALESCE(p.created_at, 0)
FROM projects p;

-- Every project now has exactly one feature-priority arena whose id equals the
-- project id, because the insert above took p.id. That is a coincidence of this
-- migration, not a contract: the store always looks the arena up by (type,
-- project), never by assuming the ids match.

CREATE TABLE arena_entries (
    arena_id  INTEGER NOT NULL REFERENCES arenas(id) ON DELETE CASCADE,

    -- 'feature' | 'solution' | 'project' | 'list_entry'. The spec's §5 table
    -- needs all four: features, solutions, list entries, answers-as-projects and
    -- projects-as-competitors.
    entity_type TEXT NOT NULL CHECK (entity_type IN
                 ('feature','solution','project','list_entry')),

    -- Deliberately NOT a foreign key. The four entity types live in four tables
    -- and SQLite cannot express "referenced by whichever table entity_type
    -- names". The store hydrates and drops rows whose entity is gone (see
    -- FindSimilar's stale-vector handling for the same pattern), so the
    -- alternative is a cascade trigger per type, which is more machinery for a
    -- guarantee SQLite cannot give anyway.
    entity_id INTEGER NOT NULL,

    r     REAL NOT NULL DEFAULT 1500,
    rd    REAL NOT NULL DEFAULT 350,
    sigma REAL NOT NULL DEFAULT 0.06,
    games INTEGER NOT NULL DEFAULT 0,

    -- For the solution arena (§6.4): the permanent "do nothing" baseline, and a
    -- derived solution forked from a parent (§6.3). NULL for most arenas.
    is_baseline INTEGER NOT NULL DEFAULT 0,
    -- A derived solution's parent, as a plain column rather than a foreign key.
    --
    -- SQLite requires an FK to reference the full primary key of its target, and
    -- arena_entries is keyed on (arena_id, entity_type, entity_id) -- three
    -- columns, of which only the entry identity is wanted here. A FK to all
    -- three would mean storing entity_type as well, for a value the store
    -- already knows. Forking (§6.3) is a small enough surface that
    -- ForkSolution checking the parent is in the same arena is the right trade.
    parent_entry_id INTEGER,

    updated_at REAL NOT NULL,

    PRIMARY KEY (arena_id, entity_type, entity_id)
);

CREATE INDEX idx_arena_entries_entity ON arena_entries(entity_type, entity_id);

-- Append-only vote log, generalized. §2.5 and §5.1: "ratings are recomputed
-- deterministically from the append-only vote log. Anyone can verify a
-- ranking."
--
-- The existing table names feature_a/feature_b and requires project_id NOT NULL,
-- which cannot express a solution vote. Adding a parallel generic pair of
-- columns rather than renaming keeps every existing reader working: the old
-- feature vote endpoints read feature_a/feature_b, and the arena ones read
-- a/b. The CHECK below enforces that exactly one shape is present, so a row can
-- never be half of each and silently unscored.
--
-- The table is rebuilt rather than altered because SQLite cannot add a CHECK
-- constraint to an existing table, and a NOT NULL column cannot be made nullable.
ALTER TABLE pairwise_votes RENAME TO pairwise_votes_legacy;

CREATE TABLE pairwise_votes (
    id         INTEGER PRIMARY KEY,

    -- The arena the vote belongs to. NOT NULL here even though the legacy table
    -- had no such column, because the backfill below fills it for every existing
    -- row and an arena-scoped vote log is the point of the change.
    arena_id   INTEGER NOT NULL REFERENCES arenas(id) ON DELETE CASCADE,

    -- Legacy feature-vote columns, kept so the existing project vote endpoints
    -- and the priority query keep working untouched. NULL for a non-feature vote.
    project_id INTEGER REFERENCES projects(id) ON DELETE CASCADE,
    feature_a  INTEGER REFERENCES features(id),
    feature_b  INTEGER REFERENCES features(id),

    -- Generic columns: any arena's entities. NULL for a legacy feature vote,
    -- which is backfilled into its project's feature-priority arena below and
    -- read through the legacy columns.
    entity_type TEXT CHECK (entity_type IS NULL OR entity_type IN
                 ('feature','solution','project','list_entry')),
    a           INTEGER,
    b           INTEGER,

    voter_id   INTEGER NOT NULL REFERENCES users(id),
    outcome    TEXT NOT NULL CHECK (outcome IN ('a','b','both','neither','skip')),
    weight     REAL NOT NULL DEFAULT 1.0,
    -- §5.1: "a vote can include a short reason", and the skip reason flags an
    -- unclear write-up. Nullable because requiring one would be a tax on every
    -- vote for the sake of the rare case.
    reason     TEXT,
    created_at REAL NOT NULL,

    -- Shape invariants. Two separate rules rather than "exactly one shape",
    -- because a backfilled row legitimately carries BOTH: it is a legacy feature
    -- vote that also has to be visible to the arena recompute, or the recompute
    -- rebuilds every feature rating at the prior and the ranking history is gone.
    --
    -- An earlier version of this constraint demanded exactly one shape, which
    -- rejected the backfill's own row shape. SQLite reports a CHECK violation as
    -- a statement error and the migration runner continued, so the table was
    -- created and 2 votes vanished with no error in any log -- a data-loss bug
    -- that looked like a clean migration.
    --
    -- What actually needs enforcing:
    --   - a generic pair is all-or-nothing (a without b is uncomparable), and
    --   - a generic row names its entity type.
    -- The legacy shape needs no constraint: the old endpoints already wrote it.
    CHECK (a IS NULL OR (a IS NOT NULL AND b IS NOT NULL AND entity_type IS NOT NULL))
);

CREATE INDEX idx_votes_arena      ON pairwise_votes(arena_id, voter_id, outcome);
CREATE INDEX idx_votes_arena_time ON pairwise_votes(arena_id, created_at, id);

-- Every legacy feature vote moves into its project's feature-priority arena,
-- filling BOTH shapes: the legacy columns so the old endpoints still see it, and
-- the generic columns so the arena queries and recompute do too.
--
-- Without this a recompute from the log would rebuild every feature rating at
-- the prior and erase the entire ranking history -- the exact failure §2.5
-- exists to prevent, arriving through the backfill path.
INSERT INTO pairwise_votes
    (id, arena_id, project_id, feature_a, feature_b,
     entity_type, a, b, voter_id, outcome, weight, reason, created_at)
SELECT v.id,
       a.id,
       v.project_id,
       v.feature_a,
       v.feature_b,
       'feature',
       v.feature_a,
       v.feature_b,
       v.voter_id,
       v.outcome,
       v.weight,
       NULL,
       v.created_at
FROM pairwise_votes_legacy v
JOIN arenas a
  ON a.type = 'feature-priority' AND a.project_id = v.project_id;

-- RENAME TO carried the old indexes onto the legacy table, so they are dropped
-- with it. The replacements above were created before this point and would
-- otherwise collide.
DROP INDEX IF EXISTS idx_votes_project;
DROP TABLE pairwise_votes_legacy;

-- Recreated after the drop, because RENAME TO carried the original onto the
-- legacy table and a same-named index cannot be created twice.
CREATE INDEX idx_votes_project ON pairwise_votes(project_id, voter_id);
