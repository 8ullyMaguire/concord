-- Capability matrix (spec revision 4 §4.2, milestone R5).
--
-- §4.2 defines a capability as a structured claim -- `wip-limits: yes | partial
-- | no | unknown` -- with evidence links, organized per category, asserted by
-- any contributor and confirmed by the same quorum machinery as everything else.
--
-- This exists before Finder rather than alongside it because Finder's brief says
-- it uses the capability matrix and needs "no new data model". Measured on the
-- live instance (2026-10-03) that was false: no such table existed, so the
-- questions Finder's own mockup names -- wip-limits, offline, arm64 -- had no
-- data behind them and carried 0 bits of information gain.
--
-- The decision that makes the rest of the design work is that `unknown` is a
-- VALUE, not an absence of a row.
--
-- A missing row means "nobody has said". A row whose value is 'unknown' means
-- "somebody recorded that nobody knows". Finder treats both as unknown when it
-- scores, but only the second is something a contributor can fill in, and the
-- distinction is the whole contribution loop: an absent row is invisible, an
-- 'unknown' row is a question the instance is asking the community. Collapsing
-- the two -- either by dropping 'unknown' from the enum, or by storing NULL --
-- makes the catalog's ignorance invisible, which is the failure mode the whole
-- Finder engine is built to avoid.

-- A capability is a keyed question about a project, grouped so the matrix can
-- be browsed per category. `key` is the stable identifier the Finder engine
-- namespaces as `cap:<key>` and the query language uses as `cap:wip-limits=yes`,
-- so it is normalised rather than display-cased.
CREATE TABLE capabilities (
    key        TEXT PRIMARY KEY,
    label      TEXT NOT NULL,
    category   TEXT NOT NULL,
    -- boolean: yes/partial/no/unknown. enum: a named set, held in `values`.
    -- Recorded rather than inferred from `values`, because a three-value
    -- boolean and a three-value enum need different question shapes and the
    -- engine must not re-derive the distinction by counting.
    kind       TEXT NOT NULL CHECK (kind IN ('boolean','enum')),
    -- JSON array of the values this capability admits. For a boolean it is the
    -- four states above; for an enum it is whatever the category defines.
    -- NOT NULL and non-empty by way of the Go guard rather than CHECK(json_valid)
    -- so a bad row is refused with a message that names the capability.
    --
    -- Quoted as "values" because VALUES is a reserved word in SQLite: the
    -- unquoted column name is a syntax error at CREATE TABLE time, not a
    -- runtime surprise later.
    "values"   TEXT NOT NULL,
    created_at REAL NOT NULL
);

-- One row per (capability, project): what somebody asserts the project has.
--
-- UNIQUE(capability, project_id) makes a re-assertion an update rather than a
-- second competing claim. Two people disagreeing about a capability is a
-- dispute, recorded in capability_confirmations -- not two rows, because two
-- rows would mean the matrix has no single current value and every reader would
-- have to invent a tiebreak.
CREATE TABLE capability_assertions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    capability  TEXT NOT NULL REFERENCES capabilities(key),
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    value       TEXT NOT NULL CHECK (value IN ('yes','partial','no','unknown')),
    evidence    TEXT NOT NULL DEFAULT '',
    asserted_by INTEGER NOT NULL REFERENCES users(id),
    asserted_at REAL NOT NULL,
    UNIQUE(capability, project_id)
);

-- Confirmations and disputes, in one table.
--
-- `confirmed` is 0 or 1 rather than two tables because a dispute IS a negative
-- confirmation: it is the same act, by the same people, with the same weight.
-- Two tables would make "3 confirmations and 2 disputes" a question of which
-- table to read first, and the answer would differ per reader.
--
-- One vote per user per assertion: a person cannot confirm a claim to raise its
-- score and then dispute it to lower someone else's. That is the vote-ring
-- shape §5.2's anti-gaming rules exist to prevent, and a UNIQUE key is the
-- cheapest place to forbid it.
CREATE TABLE capability_confirmations (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    assertion_id INTEGER NOT NULL REFERENCES capability_assertions(id) ON DELETE CASCADE,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    confirmed    INTEGER NOT NULL CHECK (confirmed IN (0,1)),
    at           REAL NOT NULL,
    UNIQUE(assertion_id, user_id)
);

-- Finder enumerates its candidate set with a range predicate over project_id,
-- which is the second column of the assertion index. Without this index that
-- query is a full scan of every assertion on the instance, once per candidate
-- set, once per question.
CREATE INDEX idx_capability_assertions_project ON capability_assertions(project_id, capability);

-- Confirmation counting is per assertion, and the assertion id is already the
-- primary key of the assertions table, so the UNIQUE constraint above produces
-- the covering index for it and this one would be redundant.