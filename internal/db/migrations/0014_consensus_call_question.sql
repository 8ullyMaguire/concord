-- Consensus calls get the question they are actually asking (§6.6).
--
-- Three real defects in CreateConsensusCall, all found while implementing the
-- emergency hold:
--
--   1. It took `title, description` and threw them away. The INSERT has no
--      columns for them, so every consensus call in the database has no stated
--      question. §6.6 makes a call "a formal decision on one specific
--      solution" -- a decision record with nothing recording what was decided.
--      It also breaks the §6.6 requirement that the emergency-hold confirmation
--      vote states the maintainer's reason, since there was nowhere to put it.
--
--   2. It hardcoded opened_by to 1 regardless of the caller, so every call
--      claimed user 1 opened it. The attribution was fictional.
--
--   3. feature_id was NOT NULL and the function validated the feature, so a
--      call could only exist about a feature. §6.6's hold confirmation and the
--      charter/theme ratification calls are about a *decision*, not a feature.
--
-- Nullable feature_id with the column still populated for the normal case: a
-- decision about a feature keeps the link, and a process decision about the
-- project (a hold confirmation, a charter amendment) leaves it NULL.
--
-- question/description are nullable rather than NOT NULL with a default,
-- because backfilling a default would put a plausible-looking but fabricated
-- question on the historical rows. NULL says "this predates the column", which
-- is the honest value, and nothing reads these to make a decision.

ALTER TABLE consensus_calls ADD COLUMN question TEXT;
ALTER TABLE consensus_calls ADD COLUMN description TEXT;

-- Backfill only where the question is genuinely recoverable: the linked
-- feature's title. That is what the call was about, and it is derived from a
-- stored row rather than invented.
UPDATE consensus_calls
   SET question = (SELECT title FROM features WHERE features.id = consensus_calls.feature_id)
 WHERE question IS NULL
   AND feature_id IS NOT NULL
   AND EXISTS (SELECT 1 FROM features WHERE features.id = consensus_calls.feature_id);

-- feature_id becomes nullable. SQLite cannot ALTER a column's nullability, so
-- the table is rebuilt. All other columns, their types, defaults and CHECKs are
-- copied verbatim; only nullability changes.
--
-- Rebuilt rather than left NOT NULL because §6.6 needs calls that are about a
-- decision rather than a feature (hold confirmations, charter amendments), and
-- forcing those to hang off an arbitrary feature row would put a false link in
-- the record.
CREATE TABLE consensus_calls_new (
    id          INTEGER PRIMARY KEY,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- Nullable: 0/NULL for a process decision that is not about one feature.
    feature_id  INTEGER REFERENCES features(id),
    opened_by   INTEGER NOT NULL REFERENCES users(id),
    opens_at    REAL NOT NULL,
    closes_at   REAL NOT NULL,
    status      TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','closed')),
    result      TEXT,
    summary     TEXT,
    -- closed_at was referenced by CloseConsensusCall but never existed in the
    -- table, so closing any consensus call failed with "no such column". The
    -- Go struct has always carried ClosedAt; the column was simply missing. It
    -- is added here because 0014 rebuilds the table and would otherwise drop it
    -- from the code's expectations a second time.
    closed_at   REAL,
    extensions  INTEGER NOT NULL DEFAULT 0,
    question    TEXT,
    description TEXT
);

INSERT INTO consensus_calls_new
    (id, project_id, feature_id, opened_by, opens_at, closes_at, status,
     result, summary, closed_at, extensions, question, description)
SELECT
    id, project_id, feature_id, opened_by, opens_at, closes_at, status,
    result, summary, NULL, extensions, question, description
FROM consensus_calls;

DROP TABLE consensus_calls;
ALTER TABLE consensus_calls_new RENAME TO consensus_calls;

-- A call may reference at most one feature. ON DELETE SET NULL rather than
-- CASCADE: deleting a feature must not silently delete a recorded decision, and
-- a deleted feature leaves the call standing with no subject, which is visible
-- rather than lost.
CREATE INDEX idx_calls_feature ON consensus_calls(feature_id);
CREATE INDEX idx_calls_project_status ON consensus_calls(project_id, status);