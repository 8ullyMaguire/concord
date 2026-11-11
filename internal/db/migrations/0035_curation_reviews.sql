-- Reviews of a moderation act: endorse or contest.
--
-- M1's acts are unilateral and reversible, and neither of those makes them REVIEWABLE: a
-- reader who believes a removal was wrong can only undo it, which is a write, and there is
-- nowhere to record "I looked at this and disagreed" without doing one. This table is that
-- record.
--
-- It deliberately applies nothing. A contest raises the act in the queue and marks it
-- contested; it does not reverse it, because an automatic reversal at quorum would hand every
-- removal to whoever organises three people first. Reversal stays a deliberate act with a
-- named actor in the log.
CREATE TABLE act_reviews (
    audit_id  INTEGER NOT NULL REFERENCES audit_log(id) ON DELETE CASCADE,
    user_id   INTEGER NOT NULL REFERENCES users(id),
    vote      INTEGER NOT NULL CHECK (vote IN (1,-1)),
    rationale TEXT NOT NULL DEFAULT '',
    at        REAL NOT NULL,
    PRIMARY KEY (audit_id, user_id)
);

CREATE INDEX idx_act_reviews_audit ON act_reviews(audit_id);

-- "Who has already voted on what", over every vote-backed kind in the queue.
--
-- The queue asks two questions of every row -- how many said yes, and has this reader said
-- anything -- and without this view each question is written once per proposal table, with a
-- different id column name each time. A VIEW, not a table: the consent rows are the truth, and
-- a copy here would be a second place to fall out of step with a ratification.
CREATE VIEW curation_votes AS
    SELECT 'taxonomy' AS kind, proposal_id AS ref_id, user_id FROM taxonomy_consents
    UNION ALL
    SELECT 'strategy', proposal_id, user_id FROM strategic_weight_consents
    UNION ALL
    SELECT 'act', audit_id, user_id FROM act_reviews;
