-- R10 / spec §4.5.7. What a task costs a newcomer, where a row came from, and one live claim.
--
-- `difficulty` is deliberately separate from `effort`: effort (S/M/L/XL) is how BIG a change is,
-- difficulty is how HOSTILE the ground is for someone who has never seen the tree (S = one file,
-- a test exists, a documented command reproduces it). A feature can be `effort=S` and
-- `difficulty=L` -- a one-line change in a file with no tests and no reproduction is the worst
-- possible first contribution, and the schema could not say so.
--
-- `origin` ships defaulting to 'api' and then EVERY EXISTING ROW is relabelled 'unknown', because
-- the other reading is a lie the whole statistics strand is built on: 925 of the 1,874 features on
-- the live instance were written by seed scripts, not by a person, and nothing in the row says
-- which. 'unknown' is the honest answer; `cmd/originbackfill` (an explicit file, refused unless it
-- covers the catalog) is what turns it into 'seed' or 'api'. Rows created from now on take the
-- default and are genuinely 'api'.
ALTER TABLE features ADD COLUMN difficulty TEXT
    CHECK (difficulty IS NULL OR difficulty IN ('S','M','L'));
ALTER TABLE features ADD COLUMN time_budget_minutes INTEGER
    CHECK (time_budget_minutes IS NULL OR time_budget_minutes > 0);
ALTER TABLE features ADD COLUMN needs_mentor INTEGER NOT NULL DEFAULT 0
    CHECK (needs_mentor IN (0,1));
ALTER TABLE features ADD COLUMN origin TEXT NOT NULL DEFAULT 'api'
    CHECK (origin IN ('api','seed','import','ui','unknown'));
UPDATE features SET origin = 'unknown';

ALTER TABLE complaints ADD COLUMN origin TEXT NOT NULL DEFAULT 'api'
    CHECK (origin IN ('api','seed','import','ui','unknown'));
UPDATE complaints SET origin = 'unknown';

-- One live claim per work item, enforced HERE rather than in Go: two people implementing the same
-- `ready` feature is the specific harm a contribution feed creates, and a SELECT-then-INSERT check
-- loses the race between two requests on a busy instance. `released_at` rather than a DELETE, so
-- "picked up and dropped twice" stays answerable -- the same shape as a withdrawal elsewhere.
CREATE TABLE work_item_claims (
    feature_id  INTEGER NOT NULL REFERENCES features(id) ON DELETE CASCADE,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    claimed_at  REAL NOT NULL,
    released_at REAL,
    PRIMARY KEY (feature_id, user_id)
);
CREATE UNIQUE INDEX work_item_claims_active
    ON work_item_claims (feature_id) WHERE released_at IS NULL;
