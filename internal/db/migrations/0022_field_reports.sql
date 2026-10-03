-- Field reports (spec revision 4 §4.7, milestone R5).
--
-- "A field report is a structured experience record, designed to replace the
-- rants and 'does anyone use X?' threads."
--
-- That sentence is the whole design constraint, and it is why `outcome` is a
-- four-value CHECK rather than prose. A free-text outcome field IS the rant the
-- section rules out: 'it works fine mostly, I think' parses and reads as
-- evidence. An enum forces the reporter to have decided what happened, which is
-- the only part of the report a ranker can use.
--
-- The other structural choice: a report is never hard-deleted. §4.7 says
-- abusive or fabricated reports are "removed by quorum with appeal". A DELETE
-- would destroy the thing the appeal is about and make the removal
-- unauditable, so removal sets a flag and keeps every other column.

CREATE TABLE field_reports (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    -- Free text on purpose. §4.7 wants a structured record, but 'which version
    -- did you run' has no closed set -- the catalog indexes 70 projects at
    -- untracked versions and a fixed enum would reject most real reports.
    version     TEXT NOT NULL DEFAULT '',
    -- §4.7 requires the use case be TAGGED, so use_case is a tag name and
    -- 'worked for 83% of reporters on arm64' is answerable by indexing it.
    use_case    TEXT NOT NULL DEFAULT '',
    environment TEXT NOT NULL DEFAULT '',
    scale       TEXT NOT NULL DEFAULT '',
    duration    TEXT NOT NULL DEFAULT '',
    outcome     TEXT NOT NULL CHECK (outcome IN
                  ('worked','worked-with-caveats','abandoned','migrated-away')),
    -- §4.7: "migrated away (to what?)". A migrated-away report that names no
    -- successor is half a report, and the successor is the most valuable part
    -- of it. NOT NULL would forbid the empty string, so the rule lives in Go
    -- where the error can name the field.
    migrated_to TEXT NOT NULL DEFAULT '',
    caveats     TEXT NOT NULL DEFAULT '',
    workaround  TEXT NOT NULL DEFAULT '',
    advice      TEXT NOT NULL DEFAULT '',
    removed     INTEGER NOT NULL DEFAULT 0,
    removed_by  INTEGER REFERENCES users(id),
    removal_reason TEXT NOT NULL DEFAULT '',
    created_at  REAL NOT NULL
);

-- Owner responses: §4.7, "Owners can respond but cannot delete reports."
CREATE TABLE field_report_responses (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    report_id  INTEGER NOT NULL REFERENCES field_reports(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id),
    body       TEXT NOT NULL,
    -- Recorded at write time from the membership table, never read from the
    -- request body. A client that sends is_owner:true gets an owner badge it
    -- did not earn, which is the whole point of the field.
    is_owner   INTEGER NOT NULL DEFAULT 0,
    created_at REAL NOT NULL
);

-- Project pages read reports newest-first and outcome rates per environment
-- ("83% of reporters on arm64"), which is a project_id scan ordered by date.
CREATE INDEX idx_field_reports_project ON field_reports(project_id, created_at DESC);

-- The outcome rate aggregates over non-removed reports for one project.
-- Partial because removed reports still occupy the table and are excluded from
-- every aggregate; without the predicate SQLite reads the whole project's
-- history and filters in the query.
CREATE INDEX idx_field_reports_live ON field_reports(project_id) WHERE removed = 0;

CREATE INDEX idx_field_report_responses_report ON field_report_responses(report_id);