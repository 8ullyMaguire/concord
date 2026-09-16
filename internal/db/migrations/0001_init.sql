-- Concord initial schema (0001).
-- Ported from the spec's data model (docs/concord-spec.md §9, §15).
-- Timestamps are REAL epoch seconds so decay math stays in SQL/Go.

CREATE TABLE users (
    id           INTEGER PRIMARY KEY,
    username     TEXT NOT NULL UNIQUE,
    display_name TEXT,
    created_at   REAL NOT NULL
);

CREATE TABLE api_tokens (
    token_hash TEXT PRIMARY KEY,             -- sha256 hex; raw token never stored
    user_id    INTEGER NOT NULL REFERENCES users(id),
    created_at REAL NOT NULL
);

CREATE TABLE projects (
    id               INTEGER PRIMARY KEY,
    slug             TEXT NOT NULL UNIQUE,
    name             TEXT NOT NULL,
    description      TEXT DEFAULT '',
    governance_model TEXT NOT NULL DEFAULT 'collective'
        CHECK (governance_model IN ('collective', 'maintainer_led')),
    license          TEXT,
    created_at       REAL NOT NULL,
    updated_at       REAL NOT NULL
);

CREATE TABLE charters (
    project_id                INTEGER PRIMARY KEY REFERENCES projects(id),
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

CREATE TABLE members (
    project_id   INTEGER NOT NULL REFERENCES projects(id),
    user_id      INTEGER NOT NULL REFERENCES users(id),
    role         TEXT NOT NULL CHECK (role IN
        ('guest','user','contributor','reviewer','maintainer','owner')),
    is_moderator INTEGER NOT NULL DEFAULT 0,
    joined_at    REAL NOT NULL,
    PRIMARY KEY (project_id, user_id)
);

CREATE TABLE tags (
    id         INTEGER PRIMARY KEY,
    project_id INTEGER REFERENCES projects(id),  -- NULL = global tag namespace
    name       TEXT NOT NULL,
    UNIQUE (project_id, name)
);

CREATE TABLE project_tags (
    project_id INTEGER NOT NULL REFERENCES projects(id),
    tag_id     INTEGER NOT NULL REFERENCES tags(id),
    applied_by INTEGER REFERENCES users(id),
    created_at REAL NOT NULL,
    PRIMARY KEY (project_id, tag_id)
);

-- Language percentages per project (forge-synced via go-enry later,
-- collaboratively correctable per spec §15).
CREATE TABLE project_languages (
    project_id INTEGER NOT NULL REFERENCES projects(id),
    language   TEXT NOT NULL,
    pct        REAL NOT NULL CHECK (pct >= 0 AND pct <= 100),
    PRIMARY KEY (project_id, language)
);

-- Maintenance-health and activity metrics (forge-synced; health score in
-- internal/discovery, per spec §15 "transparent, not vibes").
CREATE TABLE project_metrics (
    project_id          INTEGER PRIMARY KEY REFERENCES projects(id),
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

CREATE TABLE complaints (
    id                  INTEGER PRIMARY KEY,
    project_id          INTEGER NOT NULL REFERENCES projects(id),
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

CREATE TABLE complaint_impacts (
    complaint_id INTEGER NOT NULL REFERENCES complaints(id),
    user_id      INTEGER NOT NULL REFERENCES users(id),
    severity     INTEGER,
    created_at   REAL NOT NULL,
    PRIMARY KEY (complaint_id, user_id)
);

CREATE TABLE complaint_tags (
    complaint_id INTEGER NOT NULL REFERENCES complaints(id),
    tag_id       INTEGER NOT NULL REFERENCES tags(id),
    PRIMARY KEY (complaint_id, tag_id)
);

CREATE TABLE features (
    id               INTEGER PRIMARY KEY,
    project_id       INTEGER NOT NULL REFERENCES projects(id),
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

CREATE TABLE feature_complaints (
    feature_id   INTEGER NOT NULL REFERENCES features(id),
    complaint_id INTEGER NOT NULL REFERENCES complaints(id),
    PRIMARY KEY (feature_id, complaint_id)
);

CREATE TABLE feature_tags (
    feature_id INTEGER NOT NULL REFERENCES features(id),
    tag_id     INTEGER NOT NULL REFERENCES tags(id),
    PRIMARY KEY (feature_id, tag_id)
);

CREATE TABLE pairwise_votes (
    id         INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects(id),
    feature_a  INTEGER NOT NULL REFERENCES features(id),
    feature_b  INTEGER NOT NULL REFERENCES features(id),
    voter_id   INTEGER NOT NULL REFERENCES users(id),
    outcome    TEXT NOT NULL CHECK (outcome IN ('a','b','both','neither','skip')),
    weight     REAL NOT NULL DEFAULT 1.0,
    created_at REAL NOT NULL
);

CREATE TABLE consensus_calls (
    id         INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects(id),
    feature_id INTEGER NOT NULL REFERENCES features(id),
    opened_by  INTEGER NOT NULL REFERENCES users(id),
    opens_at   REAL NOT NULL,
    closes_at  REAL NOT NULL,
    status     TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','closed')),
    result     TEXT,
    summary    TEXT
);

CREATE TABLE positions (
    call_id    INTEGER NOT NULL REFERENCES consensus_calls(id),
    user_id    INTEGER NOT NULL REFERENCES users(id),
    position   TEXT NOT NULL CHECK (position IN
        ('consent','abstain','stand_aside','block')),
    reason     TEXT DEFAULT '',
    updated_at REAL NOT NULL,
    PRIMARY KEY (call_id, user_id)
);

CREATE TABLE objections (
    id         INTEGER PRIMARY KEY,
    call_id    INTEGER NOT NULL REFERENCES consensus_calls(id),
    user_id    INTEGER NOT NULL REFERENCES users(id),
    principle  TEXT NOT NULL,
    violation  TEXT NOT NULL,
    remedy     TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open','withdrawn','resolved','overridden','vetoed')),
    resolution TEXT,
    created_at REAL NOT NULL
);

CREATE TABLE board_columns (
    id         INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects(id),
    phase      TEXT NOT NULL,
    position   INTEGER NOT NULL,
    wip_limit  INTEGER,
    UNIQUE (project_id, phase)
);

CREATE TABLE board_cards (
    id           INTEGER PRIMARY KEY,
    project_id   INTEGER NOT NULL REFERENCES projects(id),
    kind         TEXT NOT NULL CHECK (kind IN ('complaint','feature')),
    complaint_id INTEGER REFERENCES complaints(id),
    feature_id   INTEGER REFERENCES features(id),
    column_id    INTEGER NOT NULL REFERENCES board_columns(id),
    entered_at   REAL NOT NULL
);

CREATE TABLE comments (
    id          INTEGER PRIMARY KEY,
    project_id  INTEGER NOT NULL REFERENCES projects(id),
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

CREATE TABLE comment_votes (
    comment_id INTEGER NOT NULL REFERENCES comments(id),
    user_id    INTEGER NOT NULL REFERENCES users(id),
    value      INTEGER NOT NULL CHECK (value IN (-1, 1)),
    PRIMARY KEY (comment_id, user_id)
);

CREATE TABLE merge_requests (
    id           INTEGER PRIMARY KEY,
    project_id   INTEGER NOT NULL REFERENCES projects(id),
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

CREATE TABLE merge_approvals (
    mr_id      INTEGER NOT NULL REFERENCES merge_requests(id),
    user_id    INTEGER NOT NULL REFERENCES users(id),
    weight     REAL NOT NULL DEFAULT 1.0,
    created_at REAL NOT NULL,
    PRIMARY KEY (mr_id, user_id)
);

-- Collaborative lists (spec §16): first-class awesome-lists. Entries are
-- proposed and admitted by quorum like moderation actions, and ranked by
-- the same Glicko-2 engine as features. The lists milestone (PLAN.md)
-- generalizes consensus_calls to arbitrary targets and wires the
-- quorum decision linkage onto list_entries.status.
CREATE TABLE lists (
    id          INTEGER PRIMARY KEY,
    project_id  INTEGER REFERENCES projects(id),  -- NULL = instance-wide
    slug        TEXT NOT NULL UNIQUE,
    title       TEXT NOT NULL,
    description TEXT DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open','closed','archived')),
    created_by  INTEGER NOT NULL REFERENCES users(id),
    created_at  REAL NOT NULL,
    updated_at  REAL NOT NULL
);

CREATE TABLE list_entries (
    id          INTEGER PRIMARY KEY,
    list_id     INTEGER NOT NULL REFERENCES lists(id),
    url         TEXT NOT NULL,
    title       TEXT NOT NULL,
    description TEXT DEFAULT '',
    category    TEXT,
    status      TEXT NOT NULL DEFAULT 'proposed'
        CHECK (status IN ('proposed','accepted','rejected','removed')),
    elo_r       REAL NOT NULL DEFAULT 1500,
    elo_rd      REAL NOT NULL DEFAULT 350,
    elo_vol     REAL NOT NULL DEFAULT 0.06,
    proposed_by INTEGER NOT NULL REFERENCES users(id),
    created_at  REAL NOT NULL,
    updated_at  REAL NOT NULL,
    UNIQUE (list_id, url)
);

CREATE TABLE list_tags (
    list_id INTEGER NOT NULL REFERENCES lists(id),
    tag_id  INTEGER NOT NULL REFERENCES tags(id),
    PRIMARY KEY (list_id, tag_id)
);

CREATE TABLE list_entry_votes (
    id         INTEGER PRIMARY KEY,
    list_id    INTEGER NOT NULL REFERENCES lists(id),
    entry_a    INTEGER NOT NULL REFERENCES list_entries(id),
    entry_b    INTEGER NOT NULL REFERENCES list_entries(id),
    voter_id   INTEGER NOT NULL REFERENCES users(id),
    outcome    TEXT NOT NULL CHECK (outcome IN ('a','b','both','neither','skip')),
    weight     REAL NOT NULL DEFAULT 1.0,
    created_at REAL NOT NULL,
    UNIQUE (voter_id, entry_a, entry_b)
);

CREATE INDEX idx_lists_project ON lists(project_id);
CREATE INDEX idx_entries_list  ON list_entries(list_id, status);
CREATE INDEX idx_list_votes    ON list_entry_votes(list_id, voter_id);

CREATE TABLE reputation_events (
    id         INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects(id),
    user_id    INTEGER NOT NULL REFERENCES users(id),
    kind       TEXT NOT NULL,
    points     REAL NOT NULL,
    created_at REAL NOT NULL
);

CREATE TABLE audit_log (
    id         INTEGER PRIMARY KEY,
    project_id INTEGER,
    actor_id   INTEGER,
    action     TEXT NOT NULL,
    entity     TEXT,
    entity_id  INTEGER,
    detail     TEXT,                                 -- JSON
    created_at REAL NOT NULL
);

CREATE INDEX idx_votes_project  ON pairwise_votes(project_id, voter_id);
CREATE INDEX idx_rep            ON reputation_events(project_id, user_id);
CREATE INDEX idx_audit          ON audit_log(project_id, created_at);
CREATE INDEX idx_complaints     ON complaints(project_id, status);
CREATE INDEX idx_features       ON features(project_id, status);
CREATE INDEX idx_project_tags   ON project_tags(tag_id);
CREATE INDEX idx_ptags_project  ON project_tags(project_id);
CREATE INDEX idx_plangs_project ON project_languages(project_id);

-- First-class discovery (spec §15): denormalized FTS index over projects.
-- The store reindexes a project row (DELETE + INSERT) whenever its name,
-- description, tags, or languages change; external-content triggers are a
-- later hardening task (docs/PLAN.md).
CREATE VIRTUAL TABLE projects_fts USING fts5(
    slug        UNINDEXED,
    name,
    description,
    tags,
    languages
);
