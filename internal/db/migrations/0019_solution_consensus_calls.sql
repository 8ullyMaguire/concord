-- 0019_solution_consensus_calls.sql
-- §6.5: from ranking to consensus -- the agenda and the fallback chain.
--
-- §6.5 says "Ranking does not decide. It sets the agenda." This migration gives
-- a consensus call a solution to be about, the charter knobs that decide when a
-- call may open on it, and the five outcomes §6.5 lists.
--
-- 0018 deliberately did NOT add any of this. A call whose target is a solution
-- cannot exist until solutions can be ranked, and a target column added before
-- then would be a foreign key to a table that could not be populated.
--
-- The decision record (ADR) is NOT a table here. §6.5 wants "the call's
-- result, positions, objections, and remedies auto-written into a decision
-- record attached to the feature" -- and project_documents already carries
-- kind='adr' with UNIQUE (project_id, kind, slug). A second ADR table would be
-- the second representation of one fact, which is exactly what the
-- arenas.baseline_entry_id duplication cost this schema.

-- ---------------------------------------------------------------------------
-- consensus_calls: the call is now about a solution, and can be opened early.
-- ---------------------------------------------------------------------------
ALTER TABLE consensus_calls ADD COLUMN solution_id INTEGER REFERENCES solutions(id);

-- §6.5: "A collaborator may open a call earlier with a stated reason. Early
-- calls are flagged as such."
--
-- Two columns rather than a derived state, because "was this call opened early"
-- is a historical fact about how the decision was reached. Re-deriving it from
-- the conditions that held at open time would mean re-running §6.5's arithmetic
-- later, against a board that has since moved, and the flag would drift.
-- Nullable then backfilled for the same reason as the charter columns above.
ALTER TABLE consensus_calls ADD COLUMN opened_early INTEGER;
UPDATE consensus_calls SET opened_early = 0 WHERE opened_early IS NULL;

-- Enforced in the store, which knows what an eligible collaborator is. NULL is
-- not allowed when opened_early is set, but SQLite cannot express that across
-- two columns without a trigger, and a trigger would be more machinery than the
-- guarantee is worth.
ALTER TABLE consensus_calls ADD COLUMN early_reason TEXT;

-- §6.5's five outcomes, distinct from consensus_calls.result.
--
-- result is governance.EvaluateConsensus's verdict -- accepted, blocked,
-- insufficient quorum. This is what the call decided *about the solution*,
-- which is a different question: a call can pass and still fall back to #2, and
-- §6.5's whole point is that "nobody restarts from scratch". Conflating the two
-- would make the fallback chain unrepresentable, which is the one thing §6.5
-- requires.
ALTER TABLE consensus_calls ADD COLUMN outcome TEXT
    CHECK (outcome IS NULL OR outcome IN
           ('accepted',
            'accepted-with-amendments',
            'fall-back',
            'rejected',
            'sent-back'));

-- "accepted with amendments" spawns a derived solution (§6.3's fork), and
-- "fall back to #2" names the solution that took over. Both are recorded rather
-- than inferred from the result, because the record is what a later reader
-- needs and inference requires the board to have stayed still.
ALTER TABLE consensus_calls ADD COLUMN fallback_to_solution_id INTEGER REFERENCES solutions(id);

CREATE INDEX idx_calls_solution ON consensus_calls(solution_id);
CREATE INDEX idx_calls_outcome  ON consensus_calls(outcome);

-- ---------------------------------------------------------------------------
-- Charters: §6.5's three conditions, all configurable.
-- ---------------------------------------------------------------------------
-- "It has enough distinct voters (project-configurable; default scaled to
-- project size)."
--
-- Scaled by the store against EligibleCollaboratorCount, not stored as an
-- absolute count: an absolute default of 3 is unreachable in a 2-person project
-- and trivial in a 200-person one. §6.5 says "default scaled to project size" and
-- this is the knob that makes the scaling explicit rather than implied.
-- Nullable then backfilled, NOT "ADD COLUMN ... NOT NULL DEFAULT 3".
--
-- The NOT NULL form does not write the default into rows that already exist.
-- Every read is then correct, but the C sqlite3 CLI reports
-- "NULL value in charters.solution_call_min_voters" once per row, forever, and
-- neither VACUUM nor wal_checkpoint clears it. The project's own driver
-- (modernc.org/sqlite) reports ok on the same file -- see
-- docs/KNOWN-ISSUES.md. Avoided rather than argued with: a shape that makes a
-- stock integrity_check scream is a shape an operator will eventually "fix" by
-- rebuilding the wrong table.
ALTER TABLE charters ADD COLUMN solution_call_min_voters INTEGER;
UPDATE charters SET solution_call_min_voters = 3 WHERE solution_call_min_voters IS NULL;

-- "It beats the runner-up and the baseline with >= 80% confidence (Glicko-2 win
-- probability)."
--
-- 0.80, matching the spec text exactly. Stored as a fraction so it reads the
-- same way §6.6's consent_ratio does.
ALTER TABLE charters ADD COLUMN solution_call_confidence REAL;
UPDATE charters SET solution_call_confidence = 0.80 WHERE solution_call_confidence IS NULL;

-- "Its position has been stable for N hours (default 72)."
ALTER TABLE charters ADD COLUMN solution_stable_hours REAL;
UPDATE charters SET solution_stable_hours = 72.0 WHERE solution_stable_hours IS NULL;

-- Charter CHECKs on the new columns, so a charter cannot be set to a condition
-- that can never be satisfied. A confidence above 1.0 or a stable window of zero
-- would otherwise make §6.5's first condition permanently false and silently
-- disable the agenda for the whole project.
--
-- CHECKs on ALTERed columns require a table rebuild in SQLite, and rebuilding
-- charters would mean re-declaring project_documents' neighbours in the wrong
-- order -- the mistake 0008 documented. So the bounds are enforced by
-- UpdateCharter instead, and this table records why there is no CHECK.