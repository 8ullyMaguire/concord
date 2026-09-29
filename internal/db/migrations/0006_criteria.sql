-- Criteria-aware ranking (2026-09-29).
--
-- Criteria are first-class, per-project, and proposable. Each criterion carries
-- its own Glicko-2 pool, so "best designed" and "best lightweight" are two
-- different questions with two different answers rather than one blended score.
--
-- Design notes that the schema itself records:
--
--   * criteria are scoped to a project, not global. A Glicko pool is only
--     meaningful within a comparison set: "efficiency" means one thing for LLMs
--     and another for hiking trails.
--   * criterion_ratings is a new table rather than extra columns on features.
--     features already holds a single r/rd/sigma triple with 601 live rows; the
--     new dimension is purely additive and the existing priority path is
--     untouched.
--   * criterion_votes is a new table rather than a nullable criterion_id on
--     pairwise_votes. A nullable column would make every existing query carry
--     "WHERE criterion_id IS NULL" to mean the default ranking, and the two vote
--     kinds would drift into inconsistent semantics.
--
-- `neither` and `both` outcomes are deliberately NOT accepted here. Concord's
-- existing Outcome.ScorePair already refuses to move a rating for them
-- (AppliesRatingChange), and a criteria vote that silently did nothing would be
-- indistinguishable from a lost vote.

CREATE TABLE criteria (
    id          INTEGER PRIMARY KEY,
    project_id  INTEGER NOT NULL REFERENCES projects(id),
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

CREATE INDEX idx_criteria_project ON criteria(project_id, active);

-- One row per (feature, criterion) that has been rated at least once. Absence
-- means "never compared on this dimension", which is different from a rating of
-- 1500: a missing row must not be imputed as average, or an unvoted feature
-- would be ranked as though a panel had judged it mediocre.
CREATE TABLE criterion_ratings (
    criterion_id INTEGER NOT NULL REFERENCES criteria(id),
    feature_id   INTEGER NOT NULL REFERENCES features(id),
    r            REAL    NOT NULL DEFAULT 1500,
    rd           REAL    NOT NULL DEFAULT 350,
    sigma        REAL    NOT NULL DEFAULT 0.06,
    games        INTEGER NOT NULL DEFAULT 0,
    updated_at   REAL    NOT NULL,
    PRIMARY KEY (criterion_id, feature_id)
);

CREATE INDEX idx_criterion_ratings_feature ON criterion_ratings(feature_id);

-- A comparison made *about* one criterion. Votes without a criterion_id stay in
-- the original pairwise_votes and continue to drive feature priority unchanged.
CREATE TABLE criterion_votes (
    id           INTEGER PRIMARY KEY,
    criterion_id INTEGER NOT NULL REFERENCES criteria(id),
    project_id   INTEGER NOT NULL REFERENCES projects(id),
    feature_a    INTEGER NOT NULL REFERENCES features(id),
    feature_b    INTEGER NOT NULL REFERENCES features(id),
    voter_id     INTEGER NOT NULL REFERENCES users(id),
    outcome      TEXT    NOT NULL CHECK (outcome IN ('a','b','both','neither','skip')),
    weight       REAL    NOT NULL DEFAULT 1.0,
    created_at   REAL    NOT NULL
);

CREATE INDEX idx_criterion_votes_criterion ON criterion_votes(criterion_id, created_at);
CREATE UNIQUE INDEX idx_criterion_votes_once
    ON criterion_votes(criterion_id, voter_id, feature_a, feature_b);

-- Saved weightings, so "edge deployment" is a nameable thing rather than a set of
-- numbers retyped every time.
CREATE TABLE criteria_profiles (
    id         INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects(id),
    slug       TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    created_by INTEGER REFERENCES users(id),
    created_at REAL    NOT NULL,
    updated_at REAL    NOT NULL,
    UNIQUE (project_id, slug)
);

CREATE TABLE criteria_profile_weights (
    profile_id  INTEGER NOT NULL REFERENCES criteria_profiles(id) ON DELETE CASCADE,
    criterion_id INTEGER NOT NULL REFERENCES criteria(id) ON DELETE CASCADE,
    weight      REAL    NOT NULL DEFAULT 1.0,
    PRIMARY KEY (profile_id, criterion_id)
);
