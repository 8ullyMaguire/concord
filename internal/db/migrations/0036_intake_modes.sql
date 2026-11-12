-- Intake modes (docs/specs/idea-intake-and-milestones-spec.md §3, R16).
--
-- The default is the behaviour that already exists, so this migration changes no
-- project's semantics: every project on the instance is complaint_first, and all
-- 332 features on project `concord` carry a validated complaint. The column exists
-- so a project can say otherwise, not so it can drift silently.
ALTER TABLE projects ADD COLUMN intake_mode TEXT NOT NULL DEFAULT 'complaint_first'
    CHECK (intake_mode IN ('complaint_first','idea_first','both'));
