-- §6.2's themes need a lifetime, and a proposal table that can hold a proposal.
--
-- 0012 shipped this pillar's data model and stopped there, and the shape of what is
-- missing is the finding: `strategic_themes` has `expires_at REAL NOT NULL` with a comment
-- explaining why it cannot be NULL, and NO WRITER and NO READER anywhere in the tree;
-- `strategic_theme_proposals` has no `proposed_by` at all, so the consent rule every other
-- decision in this repository enforces (§5.1: nobody votes on their own item) could not be
-- expressed against it. A requirement whose data model landed first is the most convincing
-- kind of unimplemented requirement there is -- it reads as done in every schema dump.
--
-- This migration supplies the missing half: where the lifetime comes from, what identifies a
-- live theme, and who proposed a change.
ALTER TABLE charters ADD COLUMN theme_lifetime_days REAL NOT NULL DEFAULT 90;

-- Reshaped rather than ALTERed. `proposed_by` is NOT NULL and a referenced user id, and
-- ALTER TABLE ADD COLUMN NOT NULL needs a default for existing rows -- which would have to
-- be a fake user id on a column that REFERENCES users(id). Recreating is safe HERE and only
-- here because the table has no writer in the entire tree and is therefore empty by
-- construction: measured 0 rows on the live instance, and no code path in any revision has
-- ever inserted one. A migration that dropped a table someone could have written to would be
-- a different thing entirely, and this comment is the reason a reader can tell them apart.
DROP TABLE strategic_theme_proposals;
CREATE TABLE strategic_theme_proposals (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    theme_tag       TEXT NOT NULL,
    theme_namespace TEXT NOT NULL DEFAULT 'topic',
    -- §5.1: the proposer cannot consent to their own proposal, which needs the proposer
    -- recorded. This is the column 0012 omitted, and the rule it makes expressible is the
    -- one that stops a one-collaborator project ratifying on a single consent.
    proposed_by     INTEGER NOT NULL REFERENCES users(id),
    -- The live weight the proposal expects to replace, or NULL when the tag has no live
    -- theme. NULL rather than 0 because 0 is a legal theme weight (the range is -2..3), and
    -- a proposal that cannot tell "no theme" from "a theme worth nothing" cannot detect that
    -- its target moved -- the staleness check the weight proposals needed weight_before for.
    weight_before   REAL,
    weight_after    REAL NOT NULL,
    rationale       TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','ratified','rejected','withdrawn','stale')),
    created_at      REAL NOT NULL,
    decided_at      REAL
);

-- One PENDING change per tag and namespace, as 0012 intended; a ratified one is not unique,
-- because the history of a tag's weights is the point.
CREATE UNIQUE INDEX idx_theme_one_pending
    ON strategic_theme_proposals(project_id, theme_tag, theme_namespace)
    WHERE status = 'pending';

-- The live side needs the one-row-per-tag identity the pending side just got. Without it a
-- RENEWAL -- ratifying a second change to a tag already in force -- inserts a second row and
-- ThemeWeightTotal sums the tag twice, so renewing a theme would silently DOUBLE its weight.
--
-- Measured before writing it: strategic_themes holds 0 rows on the live instance, so the
-- index cannot fail there. On an instance that somehow held duplicates this fails loudly
-- instead of summing twice in silence, which is the right way round.
CREATE UNIQUE INDEX idx_theme_identity ON strategic_themes(project_id, tag, namespace);

-- Consents to a theme change, for the reason strategic_weight_consents (0012) gives:
-- counting DISTINCT collaborators by proposal id out of audit_log's JSON detail is a LIKE
-- over encoded text with no index, and the question is always the count. The audit row is
-- still written on every consent -- this table answers the query, the log remains the record.
--
-- ON CONFLICT DO NOTHING at the call site rather than an update, so a collaborator
-- re-consenting keeps their original timestamp, which is part of the record.
CREATE TABLE strategic_theme_consents (
    proposal_id  INTEGER NOT NULL REFERENCES strategic_theme_proposals(id) ON DELETE CASCADE,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    consented_at REAL NOT NULL,
    PRIMARY KEY (proposal_id, user_id)
);
