-- 0018_solutions.sql
-- Solutions are first-class and ranked (spec revision 4 §6.3-6.5).
--
-- A feature states WHAT outcome and why. A solution states HOW. A feature can
-- have many competing solutions, and they compete in the feature's own solution
-- arena under the question "Which approach should we build?".
--
-- This migration adds the solutions table and the coverage claims that §6.4
-- scores. It does NOT add consensus-call opening or decision records; those
-- follow once solutions can be ranked.

-- ---------------------------------------------------------------------------
-- solutions
-- ---------------------------------------------------------------------------
CREATE TABLE solutions (
    id          INTEGER PRIMARY KEY,
    feature_id  INTEGER NOT NULL REFERENCES features(id) ON DELETE CASCADE,
    author_id   INTEGER NOT NULL REFERENCES users(id),

    -- §6.3: "Summary and design: technical and UX."
    title       TEXT NOT NULL,
    body        TEXT NOT NULL DEFAULT '',

    -- §6.3: "Type: build-new, extend-existing, integrate-external (a catalog
    -- link), config-or-docs-only, workaround, or do-nothing."
    --
    -- 'do-nothing' is the baseline type from §6.3's "permanent baseline: do
    -- nothing / document the workaround". It is a type rather than a flag so
    -- that "keep documenting the workaround" is a proposal somebody can argue
    -- for on its merits instead of a permanent fixture nobody voted on.
    type        TEXT NOT NULL CHECK (type IN
                ('build-new','extend-existing','integrate-external',
                 'config-or-docs-only','workaround','do-nothing')),

    -- §6.3: "integrate-external (a catalog link)". NULL for every other type.
    --
    -- CHECKed rather than left to the store: an external integration with no
    -- catalog link is unreviewable, and the review is the point of the type.
    external_ref TEXT,

    -- §6.3: "Affiliation: if the author benefits (e.g. their own library), it is
    -- labeled." An author of their own library ranking it is the single most
    -- damaging bias in a ranked system, and §5.2 already refuses the vote --
    -- but the label has to survive into every display, so it is stored rather
    -- than derived at render time from an authorship check.
    affiliation TEXT,

    -- §6.3: "Relationship: exclusive (default, competes with the others) or
    -- complementary (stackable, phased, not compared to its complements)."
    relationship TEXT NOT NULL DEFAULT 'exclusive'
                 CHECK (relationship IN ('exclusive','complementary')),

    -- §6.3: "Forking a solution ('same, but with X') creates a derived
    -- solution. It inherits the parent's rating with inflated RD."
    --
    -- Plain column, not a foreign key, for the same reason arena_entries'
    -- parent_entry_id is not: a fork may point at a solution in another arena
    -- only by mistake, and the store checks. A self-referencing FK would not
    -- catch the fork loop that matters (A forks B forks A) anyway.
    parent_solution_id INTEGER REFERENCES solutions(id) ON DELETE SET NULL,

    status      TEXT NOT NULL DEFAULT 'draft'
                CHECK (status IN
                       ('draft','discussion','consensus','ready',
                        'in_progress','review','shipped','rejected')),

    -- §6.4: "Expertise-weighted. Votes on solutions weight expertise tags more
    -- heavily (a Rust-heavy design is weighed by Rust-reputed voters)."
    -- Comma-separated tags, the same shape features use, so the expertise
    -- weighting has one representation rather than two.
    expertise_tags TEXT NOT NULL DEFAULT '',

    created_at  REAL NOT NULL,
    updated_at  REAL NOT NULL
);

CREATE INDEX idx_solutions_feature ON solutions(feature_id, status);

-- A feature's solutions are the competitors in its arena. One live solution per
-- title per feature: two entries differing only in case or spacing would be
-- filed as rivals and then compared against each other, which is noise.
CREATE UNIQUE INDEX idx_solutions_title ON solutions(feature_id, title);

-- ---------------------------------------------------------------------------
-- solution_coverage -- §6.4: "Complaint coverage: which linked complaints it
-- resolves, and which it explicitly leaves unresolved."
--
-- This is what coverage is computed from, so it is a table rather than a
-- free-text field: coverage = (pain of complaints this solution verifiably
-- resolves) / (pain of all linked complaints), and a claim that cannot be
-- enumerated cannot be summed.
--
-- 'resolves' | 'leaves-unresolved'. The second is not a lesser claim: §6.3 asks
-- for what a solution explicitly does NOT fix, and a row is the only honest
-- record of that.
CREATE TABLE solution_coverage (
    solution_id INTEGER NOT NULL REFERENCES solutions(id) ON DELETE CASCADE,
    complaint_id INTEGER NOT NULL REFERENCES complaints(id) ON DELETE CASCADE,
    claim       TEXT NOT NULL CHECK (claim IN ('resolves','leaves-unresolved')),

    -- §6.4: "Coverage claims are challengeable. A reviewer can contest a claim,
    -- and the claim must stand to count."
    --
    -- A contested claim does not count toward coverage. Without this column a
    -- challenge is a comment and coverage silently includes claims nobody
    -- believes, which makes the κ·coverage term decorative.
    contested     INTEGER NOT NULL DEFAULT 0,
    contested_by  INTEGER REFERENCES users(id),
    contested_at  REAL,
    contest_reason TEXT,

    created_at REAL NOT NULL,

    PRIMARY KEY (solution_id, complaint_id)
);

CREATE INDEX idx_coverage_complaint ON solution_coverage(complaint_id);

-- ---------------------------------------------------------------------------
-- §6.4: solution_score = (r - 2*RD) + kappa * coverage
--
-- "kappa defaults to 200 and is charter-configurable."
--
-- 200 is chosen so that full coverage is worth about one standard Glicko
-- deviation at the prior (2*350 = 700, so 200 is 29% of a maximally uncertain
-- entry's spread) and a fully-attested, fully-covering solution beats a
-- partially-covering one only on coverage once both are settled. Making it
-- charter-configurable rather than a constant is the point of the spec line: a
-- project whose complaints are broad and overlapping needs less weight on
-- coverage than one with a single dominant complaint.
ALTER TABLE charters ADD COLUMN solution_kappa REAL;
UPDATE charters SET solution_kappa = 200.0 WHERE solution_kappa IS NULL;

-- ---------------------------------------------------------------------------
-- arenas.baseline_entry_id is dropped.
--
-- It is the second representation of a fact that arena_entries.is_baseline
-- already carries, and it was the one nothing populated -- which is how
-- "neither" silently scored as a 0.5/0.5 draw for every solution arena instead
-- of counting as a loss to doing nothing (§6.3, §5.1). See
-- docs/KNOWN-ISSUES.md.
--
-- A baseline is now identified by solutions.type = 'do-nothing' together with
-- the arena's is_baseline flag, and solutions.arena_id is a foreign key, so the
-- flag is the only place the fact lives and the solution row is reachable from
-- it in one step. Rebuilding arenas is required because SQLite dropped a column
-- before 3.35 and this is 3.45, but it is done explicitly rather than relied
-- upon: the table is small, the rebuild is the documented shape, and a silent
-- DROP COLUMN on a table the ranking engine depends on is not something to take
-- on trust.
--
-- The FK is added in the same rebuild so a baseline cannot be deleted out from
-- under an arena. It could not be before: arena_entries is keyed on three
-- columns, and SQLite requires an FK to reference a full primary key.
--
-- Every index on arenas is recreated below. This is not optional: DROP TABLE
-- takes a table's indexes with it, and the four partial UNIQUE indexes are the
-- only thing stopping two concurrent requests from each creating "the" solution
-- arena for a feature. A migration that drops a uniqueness guarantee does not
-- look like damage in its own output -- that is exactly what 0008 did to
-- idx_criterion_votes_once.
CREATE TABLE arenas_new (
    id         INTEGER PRIMARY KEY,
    type       TEXT NOT NULL CHECK (type IN
                ('feature-priority','solution','list','request','alternatives','use-case')),
    project_id INTEGER REFERENCES projects(id) ON DELETE CASCADE,
    feature_id INTEGER REFERENCES features(id) ON DELETE CASCADE,
    question   TEXT NOT NULL DEFAULT '',
    use_case   TEXT,
    created_at REAL NOT NULL
);

INSERT INTO arenas_new (id, type, project_id, feature_id, question, use_case, created_at)
    SELECT id, type, project_id, feature_id, question, use_case, created_at FROM arenas;

DROP TABLE arenas;
ALTER TABLE arenas_new RENAME TO arenas;

CREATE UNIQUE INDEX idx_arenas_feature_priority
    ON arenas(project_id) WHERE type = 'feature-priority';
CREATE UNIQUE INDEX idx_arenas_solution
    ON arenas(feature_id) WHERE type = 'solution';
CREATE UNIQUE INDEX idx_arenas_use_case
    ON arenas(project_id, use_case) WHERE type = 'use-case' AND use_case IS NOT NULL;
CREATE UNIQUE INDEX idx_arenas_alternatives
    ON arenas(project_id, use_case) WHERE type = 'alternatives' AND use_case IS NOT NULL;

-- pairwise_votes is not touched by this migration, so its three indexes from
-- 0017 are untouched too -- verified after the rebuild, not assumed, because
-- 0008 dropped thirteen indexes this way and applied cleanly while doing it.
