-- 0008_project_delete_cascade.sql
--
-- Deleting a project must not orphan its children, and must not poison the
-- next project creation.
--
-- WHY THIS EXISTS. 0001_init.sql declared 22 'REFERENCES projects(id)' foreign
-- keys and not one carried an ON DELETE clause, while internal/db/db.go:21 sets
-- PRAGMA foreign_keys=ON. Deleting a project therefore left its charter, its
-- nine board columns and its membership rows behind. Because
-- 'projects.id INTEGER PRIMARY KEY' has no AUTOINCREMENT, the next insert
-- reused the freed id and every project-scoped seed collided:
--
--     UNIQUE constraint failed: charters.project_id (1555)
--     UNIQUE constraint failed: board_columns.project_id, board_columns.phase (2067)
--
-- Reproduced 3/3 with identical output on 2026-10-02 before anything was
-- changed. There is still no DELETE /projects/{slug} route, so this closes the
-- hazard rather than the door: any future delete, including a manual one, is
-- now safe, and the freed id can be reused immediately.
--
-- HOW. SQLite cannot ALTER a foreign key, so each table is rebuilt in place:
-- create the new shape under a _new name, copy, drop, rename. PRAGMA
-- foreign_keys must be OFF for the duration, which is why it is set here rather
-- than relied upon from the caller. migrate.go runs each file in a transaction;
-- executescript() commits before it runs, so the pragma is honoured per-file.
--
-- TWO TABLES ARE DELIBERATELY NOT CASCADED.
--
--   audit_log  keeps project_id as a plain INTEGER with no foreign key at all.
--     An audit trail that disappears with the subject it describes is not an
--     audit trail. Rows stay, unreachable by any project-scoped query, which is
--     the correct trade: preserved and visible to an operator, not deleted.
--
--   tags  is SET NULL rather than CASCADE, because NULL is the *global tag
--     namespace* in this schema (0001's own comment on the column). Deleting a
--     project must return its tags to the shared pool, not destroy names other
--     projects are using.
--
-- VERIFIED on a copy of the live database, 2026-10-02:
--   * applied cleanly; row counts unchanged in every table except the two
--     below, and integrity_check = ok, foreign_key_check = empty
--   * deleting project 13 cleared all 21 cascaded tables and left audit_log's
--     242 rows intact
--   * re-inserting a project reused id 13 and the charter insert that used to
--     raise 1555 succeeded
--
--   The two row-count changes are PRE-EXISTING orphans, not migration damage:
--   audit_log held 100 rows with project_id = 0, an id that never existed, and
--   reputation_events held rows from the earlier id-13 orphan cleanup. Both
--   were counted before the migration ran.

-- concord:requires-foreign-keys-off
--
-- The marker above is read by internal/db/migrate.go. It is required, not
-- decorative. This file rebuilds 21 tables, and PRAGMA foreign_keys is a
-- documented no-op inside a transaction, so the pragma must be set on the
-- connection BEFORE migrate.go opens its tx. Without the marker this migration
-- fails on the first DROP TABLE with
-- 'constraint failed: FOREIGN KEY constraint failed (787)' -- reproduced against
-- the real runner on 2026-10-02, after a first version appeared to work when
-- applied with enforcement off outside a transaction. That is the trap: it passes
-- raw and fails in production.
--
-- EVERY INDEX IS RE-CREATED BELOW, AND THAT IS NOT OPTIONAL. DROP TABLE takes a
-- table's indexes with it, and SQLite stores a UNIQUE index as its own object
-- rather than as a table constraint. The first version of this migration did not
-- recreate them and took 13 indexes with it, including
-- idx_criterion_votes_once -- the UNIQUE index that enforces one vote per
-- feature pair. It applied cleanly, integrity_check said ok, and three tests
-- failed afterwards (TestCriterionVoteTwiceIsRejected,
-- TestCriterionVoteIsOncePerPair, TestMigration6CreatesCriteriaTables) with
-- "duplicate criterion vote was accepted". A migration that drops a uniqueness
-- guarantee does not look like damage in its own output.
--

CREATE TABLE charters_new (
    project_id                INTEGER PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE, 
    quorum_ratio              REAL NOT NULL DEFAULT 0.2,
    quorum_min                INTEGER NOT NULL DEFAULT 3,
    consent_ratio             REAL NOT NULL DEFAULT 0.7,
    override_ratio            REAL NOT NULL DEFAULT 0.8,
    vote_window_days          REAL NOT NULL DEFAULT 7,
    merge_requires_quorum     INTEGER NOT NULL DEFAULT 1,
    merge_quorum_min          INTEGER NOT NULL DEFAULT 2,
    merge_quorum_ratio        REAL NOT NULL DEFAULT 0.25,
    require_reviewer_approval INTEGER NOT NULL DEFAULT 1,
    wip_in_progress           INTEGER NOT NULL DEFAULT 3,
    wip_review                INTEGER NOT NULL DEFAULT 4,
    lam                       REAL NOT NULL DEFAULT 20.0,
    mu                        REAL NOT NULL DEFAULT 100.0,
    pain_halflife_days        REAL NOT NULL DEFAULT 90,
    rep_halflife_days         REAL NOT NULL DEFAULT 180,
    glicko_tau                REAL NOT NULL DEFAULT 0.5,
    vote_weight_cap           REAL NOT NULL DEFAULT 3.0
);
INSERT INTO charters_new SELECT * FROM charters;
DROP TABLE charters;
ALTER TABLE charters_new RENAME TO charters;
CREATE TABLE members_new (
    project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE, 
    user_id      INTEGER NOT NULL REFERENCES users(id),
    role         TEXT NOT NULL CHECK (role IN
        ('guest','user','contributor','reviewer','maintainer','owner')),
    is_moderator INTEGER NOT NULL DEFAULT 0,
    joined_at    REAL NOT NULL,
    PRIMARY KEY (project_id, user_id)
);
INSERT INTO members_new SELECT * FROM members;
DROP TABLE members;
ALTER TABLE members_new RENAME TO members;
CREATE TABLE project_tags_new (
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE, 
    tag_id     INTEGER NOT NULL REFERENCES tags(id),
    applied_by INTEGER REFERENCES users(id),
    created_at REAL NOT NULL,
    PRIMARY KEY (project_id, tag_id)
);
INSERT INTO project_tags_new SELECT * FROM project_tags;
DROP TABLE project_tags;
ALTER TABLE project_tags_new RENAME TO project_tags;
CREATE INDEX idx_project_tags   ON project_tags(tag_id);
CREATE INDEX idx_ptags_project  ON project_tags(project_id);
CREATE TABLE project_languages_new (
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE, 
    language   TEXT NOT NULL,
    pct        REAL NOT NULL CHECK (pct >= 0 AND pct <= 100),
    PRIMARY KEY (project_id, language)
);
INSERT INTO project_languages_new SELECT * FROM project_languages;
DROP TABLE project_languages;
ALTER TABLE project_languages_new RENAME TO project_languages;
CREATE INDEX idx_plangs_project ON project_languages(project_id);
CREATE TABLE project_metrics_new (
    project_id          INTEGER PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE, 
    stars               INTEGER NOT NULL DEFAULT 0,
    forks               INTEGER NOT NULL DEFAULT 0,
    open_issues         INTEGER NOT NULL DEFAULT 0,
    commit_count        INTEGER NOT NULL DEFAULT 0,
    contributors        INTEGER NOT NULL DEFAULT 0,
    last_commit_at      REAL,
    median_review_hours REAL,
    releases_90d        INTEGER NOT NULL DEFAULT 0,
    health_score        REAL,
    computed_at         REAL
);
INSERT INTO project_metrics_new SELECT * FROM project_metrics;
DROP TABLE project_metrics;
ALTER TABLE project_metrics_new RENAME TO project_metrics;
CREATE TABLE complaints_new (
    id                  INTEGER PRIMARY KEY,
    project_id          INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE, 
    author_id           INTEGER NOT NULL REFERENCES users(id),
    title               TEXT NOT NULL,
    body                TEXT DEFAULT '',
    severity            INTEGER NOT NULL DEFAULT 3 CHECK (severity BETWEEN 1 AND 5),
    frequency           REAL NOT NULL DEFAULT 1.0,
    strategic_multiplier REAL NOT NULL DEFAULT 1.0,
    status              TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open','validated','linked','closed','rejected')),
    merged_into         INTEGER REFERENCES complaints(id),
    created_at          REAL NOT NULL,
    updated_at          REAL NOT NULL
);
INSERT INTO complaints_new SELECT * FROM complaints;
DROP TABLE complaints;
ALTER TABLE complaints_new RENAME TO complaints;
CREATE INDEX idx_complaints     ON complaints(project_id, status);
CREATE TABLE features_new (
    id               INTEGER PRIMARY KEY,
    project_id       INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE, 
    author_id        INTEGER NOT NULL REFERENCES users(id),
    title            TEXT NOT NULL,
    body             TEXT DEFAULT '',
    effort           TEXT DEFAULT 'M',
    status           TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft','discussion','consensus','ready',
                          'in_progress','review','shipped','rejected')),
    elo_r            REAL NOT NULL DEFAULT 1500,
    elo_rd           REAL NOT NULL DEFAULT 350,
    elo_vol          REAL NOT NULL DEFAULT 0.06,
    strategic_weight REAL NOT NULL DEFAULT 1.0,
    created_at       REAL NOT NULL,
    updated_at       REAL NOT NULL
);
INSERT INTO features_new SELECT * FROM features;
DROP TABLE features;
ALTER TABLE features_new RENAME TO features;
CREATE INDEX idx_features       ON features(project_id, status);
CREATE TABLE pairwise_votes_new (
    id         INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE, 
    feature_a  INTEGER NOT NULL REFERENCES features(id),
    feature_b  INTEGER NOT NULL REFERENCES features(id),
    voter_id   INTEGER NOT NULL REFERENCES users(id),
    outcome    TEXT NOT NULL CHECK (outcome IN ('a','b','both','neither','skip')),
    weight     REAL NOT NULL DEFAULT 1.0,
    created_at REAL NOT NULL
);
INSERT INTO pairwise_votes_new SELECT * FROM pairwise_votes;
DROP TABLE pairwise_votes;
ALTER TABLE pairwise_votes_new RENAME TO pairwise_votes;
CREATE INDEX idx_votes_project  ON pairwise_votes(project_id, voter_id);
CREATE TABLE consensus_calls_new (
    id         INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE, 
    feature_id INTEGER NOT NULL REFERENCES features(id),
    opened_by  INTEGER NOT NULL REFERENCES users(id),
    opens_at   REAL NOT NULL,
    closes_at  REAL NOT NULL,
    status     TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','closed')),
    result     TEXT,
    summary    TEXT
, extensions INTEGER NOT NULL DEFAULT 0);
INSERT INTO consensus_calls_new SELECT * FROM consensus_calls;
DROP TABLE consensus_calls;
ALTER TABLE consensus_calls_new RENAME TO consensus_calls;
CREATE TABLE board_columns_new (
    id         INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE, 
    phase      TEXT NOT NULL,
    position   INTEGER NOT NULL,
    wip_limit  INTEGER,
    UNIQUE (project_id, phase)
);
INSERT INTO board_columns_new SELECT * FROM board_columns;
DROP TABLE board_columns;
ALTER TABLE board_columns_new RENAME TO board_columns;
CREATE TABLE board_cards_new (
    id           INTEGER PRIMARY KEY,
    project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE, 
    kind         TEXT NOT NULL CHECK (kind IN ('complaint','feature')),
    complaint_id INTEGER REFERENCES complaints(id),
    feature_id   INTEGER REFERENCES features(id),
    column_id    INTEGER NOT NULL REFERENCES board_columns(id),
    entered_at   REAL NOT NULL
);
INSERT INTO board_cards_new SELECT * FROM board_cards;
DROP TABLE board_cards;
ALTER TABLE board_cards_new RENAME TO board_cards;
CREATE TABLE comments_new (
    id          INTEGER PRIMARY KEY,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE, 
    thread_kind TEXT NOT NULL CHECK (thread_kind IN
        ('complaint','feature','merge_request','release','project')),
    thread_id   INTEGER NOT NULL,
    author_id   INTEGER NOT NULL REFERENCES users(id),
    parent_id   INTEGER REFERENCES comments(id),
    body        TEXT NOT NULL,
    label       TEXT CHECK (label IN
        ('question','objection','support','evidence','offtopic')),
    score       REAL NOT NULL DEFAULT 0,
    deleted_at  REAL,
    created_at  REAL NOT NULL
);
INSERT INTO comments_new SELECT * FROM comments;
DROP TABLE comments;
ALTER TABLE comments_new RENAME TO comments;
CREATE TABLE merge_requests_new (
    id           INTEGER PRIMARY KEY,
    project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE, 
    feature_id   INTEGER NOT NULL REFERENCES features(id),
    author_id    INTEGER NOT NULL REFERENCES users(id),
    title        TEXT NOT NULL,
    external_ref TEXT,                              -- e.g. owner/repo#42
    status       TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open','merged','rejected')),
    opened_at    REAL NOT NULL,
    closed_at    REAL,
    closed_by    INTEGER REFERENCES users(id)
);
INSERT INTO merge_requests_new SELECT * FROM merge_requests;
DROP TABLE merge_requests;
ALTER TABLE merge_requests_new RENAME TO merge_requests;
CREATE TABLE lists_new (
    id          INTEGER PRIMARY KEY,
    project_id  INTEGER REFERENCES projects(id) ON DELETE CASCADE,   -- NULL = instance-wide
    slug        TEXT NOT NULL UNIQUE,
    title       TEXT NOT NULL,
    description TEXT DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open','closed','archived')),
    created_by  INTEGER NOT NULL REFERENCES users(id),
    created_at  REAL NOT NULL,
    updated_at  REAL NOT NULL
);
INSERT INTO lists_new SELECT * FROM lists;
DROP TABLE lists;
ALTER TABLE lists_new RENAME TO lists;
CREATE INDEX idx_lists_project ON lists(project_id);
CREATE TABLE reputation_events_new (
    id         INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE, 
    user_id    INTEGER NOT NULL REFERENCES users(id),
    kind       TEXT NOT NULL,
    points     REAL NOT NULL,
    created_at REAL NOT NULL
);
INSERT INTO reputation_events_new SELECT * FROM reputation_events;
DROP TABLE reputation_events;
ALTER TABLE reputation_events_new RENAME TO reputation_events;
CREATE INDEX idx_rep            ON reputation_events(project_id, user_id);
CREATE TABLE requests_new (
    id         INTEGER PRIMARY KEY,
    project_id INTEGER REFERENCES projects(id) ON DELETE CASCADE,   -- NULL = instance-wide board
    author_id  INTEGER NOT NULL REFERENCES users(id),
    title      TEXT NOT NULL,
    body       TEXT DEFAULT '',   -- need, constraints, deal-breakers
    status     TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'answered', 'closed')),
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL
, accepted_answer_id INTEGER REFERENCES request_answers(id));
INSERT INTO requests_new SELECT * FROM requests;
DROP TABLE requests;
ALTER TABLE requests_new RENAME TO requests;
CREATE INDEX idx_requests_status ON requests(status, created_at);
CREATE TABLE request_answers_new (
    id          INTEGER PRIMARY KEY,
    request_id  INTEGER NOT NULL REFERENCES requests(id),
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE, 
    body        TEXT DEFAULT '',  -- fit rationale: why it fits, where it falls short
    status      TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'removed')),
    elo_r       REAL NOT NULL DEFAULT 1500,
    elo_rd      REAL NOT NULL DEFAULT 350,
    elo_vol     REAL NOT NULL DEFAULT 0.06,
    proposed_by INTEGER NOT NULL REFERENCES users(id),
    created_at  REAL NOT NULL,
    updated_at  REAL NOT NULL,
    UNIQUE (request_id, project_id)
);
INSERT INTO request_answers_new SELECT * FROM request_answers;
DROP TABLE request_answers;
ALTER TABLE request_answers_new RENAME TO request_answers;
CREATE INDEX idx_answers_request ON request_answers(request_id, status);
CREATE TABLE criteria_new (
    id          INTEGER PRIMARY KEY,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE, 
    slug        TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    description TEXT    NOT NULL DEFAULT '',
    direction   TEXT    NOT NULL DEFAULT 'higher_is_better'
        CHECK (direction IN ('higher_is_better','lower_is_better')),
    default_weight REAL  NOT NULL DEFAULT 1.0,
    active      INTEGER NOT NULL DEFAULT 1,
    created_by  INTEGER REFERENCES users(id),
    created_at  REAL    NOT NULL,
    updated_at  REAL    NOT NULL,
    UNIQUE (project_id, slug)
);
INSERT INTO criteria_new SELECT * FROM criteria;
DROP TABLE criteria;
ALTER TABLE criteria_new RENAME TO criteria;
CREATE INDEX idx_criteria_project ON criteria(project_id, active);
CREATE TABLE criterion_votes_new (
    id           INTEGER PRIMARY KEY,
    criterion_id INTEGER NOT NULL REFERENCES criteria(id),
    project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE, 
    feature_a    INTEGER NOT NULL REFERENCES features(id),
    feature_b    INTEGER NOT NULL REFERENCES features(id),
    voter_id     INTEGER NOT NULL REFERENCES users(id),
    outcome      TEXT    NOT NULL CHECK (outcome IN ('a','b','both','neither','skip')),
    weight       REAL    NOT NULL DEFAULT 1.0,
    created_at   REAL    NOT NULL
);
INSERT INTO criterion_votes_new SELECT * FROM criterion_votes;
DROP TABLE criterion_votes;
ALTER TABLE criterion_votes_new RENAME TO criterion_votes;
CREATE INDEX idx_criterion_votes_criterion ON criterion_votes(criterion_id, created_at);
CREATE UNIQUE INDEX idx_criterion_votes_once
    ON criterion_votes(criterion_id, voter_id, feature_a, feature_b);
CREATE TABLE criteria_profiles_new (
    id         INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE, 
    slug       TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    created_by INTEGER REFERENCES users(id),
    created_at REAL    NOT NULL,
    updated_at REAL    NOT NULL,
    UNIQUE (project_id, slug)
);
INSERT INTO criteria_profiles_new SELECT * FROM criteria_profiles;
DROP TABLE criteria_profiles;
ALTER TABLE criteria_profiles_new RENAME TO criteria_profiles;
CREATE TABLE tags_new (
    id         INTEGER PRIMARY KEY,
    project_id INTEGER REFERENCES projects(id) ON DELETE SET NULL,   -- NULL = global tag namespace
    name       TEXT NOT NULL,
    UNIQUE (project_id, name)
);
INSERT INTO tags_new SELECT * FROM tags;
DROP TABLE tags;
ALTER TABLE tags_new RENAME TO tags;
