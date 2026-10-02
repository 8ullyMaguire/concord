-- Embeddings for duplicate detection at filing time (spec revision 4 §6.1,
-- §4.4, §4.6).
--
-- "Duplicate detection at filing time uses semantic similarity before submit"
-- needs one thing this schema does not have: a way to ask "have we seen this
-- before?" that does not require the words to match. Existing text search is
-- FTS5 over projects and documents; a complaint about "cannot resume upload
-- after a network drop" shares almost no tokens with one about "large uploads
-- fail silently and restart from zero", and both are the same pain.
--
-- One table for every embeddable entity rather than a column per table. The
-- entity kinds are meant to grow (solutions and list entries arrive with §6.3
-- and §7), and a new kind is then one new value of `kind` instead of another
-- ALTER TABLE plus another copy of the search path.
--
-- Dimensions and model are recorded per row. An embedding is only comparable
-- with another produced by the same model at the same dimensionality: mixing a
-- 768-dim nomic vector with a 256-dim hashed vector and taking their cosine
-- similarity produces a number that looks valid and means nothing. Storing
-- model_id makes that detectable rather than silent, and Embed_Compatible below
-- refuses to compare across them.

CREATE TABLE embeddings (
    -- 'complaint' | 'feature' | 'request' | 'solution' | 'list_entry' |
    -- 'project'. Checked rather than free text because this is the discriminator
    -- every query filters on, and a typo in it silently hides rows from search.
    kind       TEXT NOT NULL CHECK (kind IN
                 ('complaint','feature','request','solution','list_entry','project')),

    -- Composite primary key: one embedding per entity, so a re-embed replaces
    -- rather than accumulates. An accumulating table would let a stale vector
    -- from an older model keep winning searches after a model change.
    entity_id  INTEGER NOT NULL,

    -- Which part of the entity this vector covers: 'title' or 'full'.
    --
    -- Both, because the two are embedded and queried by different things. A
    -- filer checking "does this already exist?" types a title and nothing else,
    -- while the row that exists was indexed as title+body. Comparing a title
    -- query against a full-text vector dilutes it with body words it cannot see:
    -- measured, an identical complaint scored 0.80 instead of 1.0. A title is
    -- also the strongest single signal for a complaint, so it deserves its own
    -- vector rather than a weighted share of one.
    --
    -- Similarity is the MAX over fields, not the average: a filing matches when
    -- either its title or its full text does, and averaging would let a strong
    -- title match be cancelled out by a weak body match.
    field      TEXT NOT NULL DEFAULT 'full' CHECK (field IN ('title','full')),

    -- project_id denormalised onto the row. Every similarity query is scoped to a
    -- project (or explicitly crosses projects for the "this was fixed in X" case
    -- of §6.1), and without it every query is a full scan plus a join.
    project_id INTEGER REFERENCES projects(id) ON DELETE CASCADE,

    -- Float32 little-endian, length = dims * 4. A BLOB rather than JSON text:
    -- 969 rows at 768 dims is ~3MB of JSON to parse on every query, and the
    -- storage layer has no reason to understand vector layout.
    vector     BLOB NOT NULL,

    dims       INTEGER NOT NULL CHECK (dims > 0),

    -- Identifies the model that produced vector. Compared before any similarity
    -- is computed; see the table comment.
    model_id   TEXT NOT NULL,

    -- The exact text that was embedded. Kept so a re-embed can tell "the text
    -- changed" from "the model changed", and so an operator debugging a bad
    -- match can see what was actually compared.
    source     TEXT NOT NULL,

    updated_at REAL NOT NULL,

    PRIMARY KEY (kind, entity_id, field)
);

-- The primary query is "similar complaints in this project", so the index leads
-- with the scope.
CREATE INDEX idx_embeddings_project_kind ON embeddings(project_id, kind, field);

-- Cross-project duplicate hunting ("was this fixed somewhere else?") filters by
-- kind alone.
CREATE INDEX idx_embeddings_kind ON embeddings(kind);

-- Backfill tracking. An index is only as good as its coverage, and a partially
-- populated one produces confident wrong answers: a query that skips rows with
-- no embedding reports "no duplicates found" rather than "not indexed yet".
CREATE TABLE embedding_backfill (
    kind       TEXT NOT NULL,
    model_id   TEXT NOT NULL,
    total      INTEGER NOT NULL DEFAULT 0,
    embedded   INTEGER NOT NULL DEFAULT 0,
    updated_at REAL NOT NULL,
    PRIMARY KEY (kind, model_id)
);
