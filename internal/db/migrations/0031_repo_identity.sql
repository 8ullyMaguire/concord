-- R9 / spec §4.5.7.1–.2. Where a project lives, and what its repository says about it.
--
-- `repo_url` is STATED, never derived. A slug guessed into a URL sends a stranger to somebody
-- else's repository, and a health number computed from the wrong repository is worse than no
-- number at all -- so the column stays NULL until a person says what it is, and every surface
-- that needs it treats NULL as "not stated" rather than "no repository".
--
-- `repo_host` is the ingest's switch, and it is constrained rather than free text: a typo
-- ('gihub') would exclude the project from every fetch with no error anywhere, which is the
-- silent-miss shape this repository has fixed twice before.
ALTER TABLE projects ADD COLUMN repo_url  TEXT;
ALTER TABLE projects ADD COLUMN repo_host TEXT
    CHECK (repo_host IS NULL OR repo_host IN ('forgejo','github','codeberg'));

-- §4.5.7.2's newcomer responsiveness, published ONLY with its sample size: a median over three
-- PRs is a number nobody should act on, so `newcomer_sample` is what the reader sees and the
-- hours column stays NULL until the sample is large enough (cmd/healthsync decides, not the
-- schema, because the threshold is a policy and the table stores facts).
ALTER TABLE project_metrics ADD COLUMN newcomer_response_hours REAL;
ALTER TABLE project_metrics ADD COLUMN newcomer_sample         INTEGER NOT NULL DEFAULT 0;

-- The webhook already receives these events and throws them away; this is where they land.
-- `UNIQUE (repo_full, kind, number, actor, at)` makes a webhook REDELIVERY free: the same
-- delivery arriving twice is one row, and no code has to keep a list of seen delivery ids.
CREATE TABLE forge_events (
    id          INTEGER PRIMARY KEY,
    repo_full   TEXT NOT NULL,
    kind        TEXT NOT NULL
                CHECK (kind IN ('push','pr_opened','pr_closed','pr_review','pr_comment')),
    number      INTEGER,
    actor       TEXT NOT NULL DEFAULT '',
    at          REAL NOT NULL,
    received_at REAL NOT NULL,
    UNIQUE (repo_full, kind, number, actor, at)
);
CREATE INDEX forge_events_repo ON forge_events (repo_full, at);
