-- Milestones (docs/specs/idea-intake-and-milestones-spec.md §4, R17).
--
-- Grouping, which the schema has never had: phases live in board_columns and
-- themes live in tags, so a release a maintainer is working towards had no
-- representation and the roadmap lived in Markdown.
CREATE TABLE milestones (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    body        TEXT NOT NULL DEFAULT '',
    -- Nullable on purpose: a milestone dated by wish is a deadline nobody agreed,
    -- and there is no honest value to store for "no date".
    target_date REAL,
    status      TEXT NOT NULL DEFAULT 'planned'
                CHECK (status IN ('planned','active','done','cancelled')),
    created_at  REAL NOT NULL,
    UNIQUE (project_id, title)
);
CREATE INDEX milestones_project ON milestones(project_id, status);

-- Nullable on purpose: the features no milestone claims ARE the backlog, and a
-- default milestone would make that set unrepresentable. ON DELETE SET NULL
-- because deleting a milestone must not delete the work it grouped.
ALTER TABLE features ADD COLUMN milestone_id INTEGER REFERENCES milestones(id) ON DELETE SET NULL;
-- Ordering INSIDE a milestone (§6 "order the features inside a milestone"). On the
-- feature rather than on a join table because a feature belongs to at most one
-- milestone, and a join table for a one-to-many is a second primary key to keep.
ALTER TABLE features ADD COLUMN milestone_position INTEGER NOT NULL DEFAULT 0;
CREATE INDEX features_milestone ON features(milestone_id, milestone_position);

-- Evidence that is not a complaint (§3.4). complaints keep their own table
-- (feature_complaints) so there is exactly one truth per kind: a row here is a
-- document revision or a field report standing as the reason a feature exists.
CREATE TABLE feature_evidence (
    feature_id  INTEGER NOT NULL REFERENCES features(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN ('document','field_report')),
    ref_id      INTEGER NOT NULL,
    created_at  REAL NOT NULL,
    PRIMARY KEY (feature_id, kind, ref_id)
);
