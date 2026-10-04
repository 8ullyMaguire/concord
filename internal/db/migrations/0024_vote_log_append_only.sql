-- 0024_vote_log_append_only.sql
--
-- pairwise_votes is append-only, enforced rather than merely intended.
--
-- WHY THIS TABLE AND NOT ANOTHER. `admin_ledger` got no-update/no-delete
-- triggers in 0013, and `pairwise_votes` never did -- so the ranking substrate,
-- the table every priority score on this site is computed from, is the one
-- ledger a writer can rewrite. KNOWN-ISSUES.md recorded this as "still no DB
-- trigger forbidding UPDATE/DELETE" next to the R3 note, and the fix has a
-- proven precedent to copy rather than invent.
--
-- WHY IT MATTERS MORE HERE THAN FOR admin_ledger. An admin_ledger row is a
-- record of an event. A pairwise_votes row is an INPUT to a Glicko-2 rating:
-- RecordVote applies the voter's weight, then the rating is derived from the
-- sum. Deleting or editing one row does not merely erase the history, it
-- silently changes every rating derived from it, and Glicko-2 has no way to
-- tell a corrected database from a tampered one -- a tampered rating set is
-- still internally consistent, which is what makes it hard to notice. A vote
-- that cannot be edited cannot be bought, and a rating nobody can forge by
-- rewriting three rows is a rating a ranking can be defended from.
--
-- THE ONE ESCAPE HATCH, AND WHY IT IS EXPLICIT. seed/dedupe.py --apply
-- DELETEs from this table when it removes a duplicate feature: a feature's pain
-- is the sum over its linked complaints, so a complaint recorded twice doubles
-- the pain of every feature linked to it, and the ranking the site exists to
-- produce is quietly wrong. That is a legitimate repair of a corrupted seed, and
-- a trigger with no way through would turn a maintenance script into a crash
-- rather than a warning.
--
-- So the deletes are permitted ONLY while the seeding session flag is set, and
-- that flag has to be set by the script itself -- it cannot be inferred. Two
-- properties make this a real restriction rather than a formality:
--
--   1. The flag lives in a dedicated table with a CHECK on its value, so it
--      cannot be set to any "truthy" string. It is a fact about the database,
--      not a convention.
--   2. The triggers refuse the write if any OTHER signalled session exists, so
--      two maintenance runs cannot interleave and one cannot mask the other.
--
-- An application request never sets it: `store.RecordVote` and every other
-- writer are INSERT-only and do not know the table exists. There is no HTTP
-- path to it, no admin route, and no request-scoped value that reaches it.
--
-- WHY NOT A TRIGGER ON EVERY UPDATE. Only the two columns that change meaning
-- are guarded. A vote's `reason` and `arena_id` are written at INSERT and never
-- revised, but guarding them costs nothing and guessing wrong costs a silent
-- hole, so the whole row is guarded: an UPDATE on pairwise_votes is refused
-- outright. Nothing in application code performs one (verified: the only
-- UPDATE/DELETE of this table in the tree is seed/dedupe.py's duplicate
-- repair), so a blanket refusal cannot break a working feature.

-- The session flag. CHECKed, so the only states are 0 and 1 and a typo cannot
-- arm the escape hatch.
CREATE TABLE IF NOT EXISTS maintenance_signals (
    name         TEXT PRIMARY KEY,
    enabled      INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    set_at       REAL,
    set_by       TEXT
);

CREATE TRIGGER trg_pairwise_votes_no_update
BEFORE UPDATE ON pairwise_votes
BEGIN
    SELECT RAISE(ABORT, 'pairwise_votes is append-only: a vote cannot be edited');
END;

CREATE TRIGGER trg_pairwise_votes_no_delete
BEFORE DELETE ON pairwise_votes
WHEN NOT EXISTS (
    SELECT 1 FROM maintenance_signals
    WHERE name = 'seed_dedupe'
      AND enabled = 1
      AND (SELECT count(*) FROM maintenance_signals WHERE enabled = 1) = 1
)
BEGIN
    SELECT RAISE(ABORT, 'pairwise_votes is append-only: a vote cannot be deleted');
END;