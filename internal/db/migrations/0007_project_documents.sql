-- Project documents (2026-09-30).
--
-- A project in Concord was a name, a description and a list of features. The
-- documents that actually explain it -- README, specification, plan, wiki --
-- lived in a repository somewhere else and were invisible to anyone reading
-- Concord. The first symptom was trying to add Tessera's spec to its entry and
-- finding there was no way to do it.
--
-- Why a table rather than a wiki bolted onto features:
--
--   * A 3,982-line specification in a feature body is unreadable, loses its
--     section structure, and cannot be searched as prose. Pasting documents into
--     features was tried first and rejected.
--   * Documents are versioned and revised; features are proposed and voted on.
--     Mixing them means every doc edit needs a rating and every vote needs a
--     revision history.
--   * Several kinds with different lifecycles share one store: a README changes
--     when the project changes, a spec changes when the design is revised, a
--     plan changes weekly. One table with a `kind` discriminates; four tables
--     would not.
--
-- Design notes the schema records:
--
--   * (project_id, kind, slug) is UNIQUE, so a project has at most one README
--     and at most one spec, but many wiki pages. Enforcing that in the
--     application would mean a race between two concurrent creates.
--   * `slug` is the human-facing name within the kind ('architecture' for a
--     spec) and `title` is the display name. A wiki wants both; a README does
--     not, which is why title defaults rather than being derived.
--   * `body` is markdown and is stored as given. Rendering is the reader's job;
--     storing rendered HTML would make every spec edit a migration.
--   * `revision` increments on every update and is returned by the API. Without
--     it a reader cannot tell whether they are looking at the current document
--     or a cached one, which is exactly the confusion this table exists to
--     remove.
--   * No soft delete. Documents have no tombstones because nothing federates
--     them yet; adding deleted_at now would be a column that is always NULL.

CREATE TABLE project_documents (
    id          INTEGER PRIMARY KEY,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- readme | spec | plan | wiki | adr | changelog
    kind        TEXT    NOT NULL
        CHECK (kind IN ('readme','spec','plan','wiki','adr','changelog')),
    slug        TEXT    NOT NULL,
    title       TEXT    NOT NULL DEFAULT '',
    body        TEXT    NOT NULL DEFAULT '',
    revision    INTEGER NOT NULL DEFAULT 1,
    author_id   INTEGER NOT NULL REFERENCES users(id),
    created_at  REAL    NOT NULL,
    updated_at  REAL    NOT NULL,
    UNIQUE (project_id, kind, slug)
);

-- The read path is "every document of this project, newest first", which is the
-- project page. The kind is in the index so a kind filter stays an index seek
-- rather than a filter over the whole set.
CREATE INDEX idx_project_documents_project
    ON project_documents (project_id, kind, updated_at DESC);

-- Full-text search across document bodies. A separate FTS table rather than a
-- column on project_documents because the tokenizer choice is a schema decision
-- and SQLite's FTS5 cannot be altered after creation -- the same reason the
-- project search uses its own FTS tables.
CREATE VIRTUAL TABLE project_documents_fts USING fts5(
    title,
    body,
    content = 'project_documents',
    content_rowid = 'id',
    tokenize = 'unicode61 remove_diacritics 2'
);

-- Keep the FTS index in step with the table. project_documents has no soft
-- delete and no tombstone, so INSERT and DELETE are the only triggers needed;
-- UPDATE is covered by delete-then-insert.
CREATE TRIGGER project_documents_fts_ai AFTER INSERT ON project_documents BEGIN
    INSERT INTO project_documents_fts (rowid, title, body)
    VALUES (new.id, new.title, new.body);
END;

CREATE TRIGGER project_documents_fts_ad AFTER DELETE ON project_documents BEGIN
    INSERT INTO project_documents_fts (project_documents_fts, rowid, title, body)
    VALUES ('delete', old.id, old.title, old.body);
END;

CREATE TRIGGER project_documents_fts_au AFTER UPDATE ON project_documents BEGIN
    INSERT INTO project_documents_fts (project_documents_fts, rowid, title, body)
    VALUES ('delete', old.id, old.title, old.body);
    INSERT INTO project_documents_fts (rowid, title, body)
    VALUES (new.id, new.title, new.body);
END;
