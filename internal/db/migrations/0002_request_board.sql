-- Request board (spec §17): users post natural-language requests, the
-- community answers with project references, and answers are ranked by
-- pairwise fit votes using the same Glicko-2 engine as features.
-- Migration discipline: schema additions go in new files, never edits
-- to applied migrations.

CREATE TABLE requests (
    id         INTEGER PRIMARY KEY,
    project_id INTEGER REFERENCES projects(id),  -- NULL = instance-wide board
    author_id  INTEGER NOT NULL REFERENCES users(id),
    title      TEXT NOT NULL,
    body       TEXT DEFAULT '',   -- need, constraints, deal-breakers
    status     TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'answered', 'closed')),
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL
);

CREATE TABLE request_tags (
    request_id INTEGER NOT NULL REFERENCES requests(id),
    tag_id     INTEGER NOT NULL REFERENCES tags(id),
    PRIMARY KEY (request_id, tag_id)
);

CREATE TABLE request_answers (
    id          INTEGER PRIMARY KEY,
    request_id  INTEGER NOT NULL REFERENCES requests(id),
    project_id  INTEGER NOT NULL REFERENCES projects(id),
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

-- Pairwise fit votes: "which fits this request better?" — same outcome
-- set and weight model as feature and list-entry ranking.
CREATE TABLE request_answer_votes (
    id         INTEGER PRIMARY KEY,
    request_id INTEGER NOT NULL REFERENCES requests(id),
    answer_a   INTEGER NOT NULL REFERENCES request_answers(id),
    answer_b   INTEGER NOT NULL REFERENCES request_answers(id),
    voter_id   INTEGER NOT NULL REFERENCES users(id),
    outcome    TEXT NOT NULL CHECK (outcome IN ('a', 'b', 'both', 'neither', 'skip')),
    weight     REAL NOT NULL DEFAULT 1.0,
    created_at REAL NOT NULL,
    UNIQUE (voter_id, answer_a, answer_b)
);

CREATE INDEX idx_requests_status ON requests(status, created_at);
CREATE INDEX idx_answers_request ON request_answers(request_id, status);
CREATE INDEX idx_request_votes   ON request_answer_votes(request_id, voter_id);

-- The request author's accepted-answer marker. Displayed on the UI; it
-- does NOT affect the Glicko ranking (spec §17). Added via ALTER so the
-- FK target (request_answers) exists first.
ALTER TABLE requests ADD COLUMN accepted_answer_id INTEGER REFERENCES request_answers(id);
