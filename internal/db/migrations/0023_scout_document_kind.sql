-- Scout reports (spec revision 4 §4.5.1, phase3-spec §6, scout-spec.md §8).
--
-- A scout report is a document a project keeps: what idea was scouted, what
-- capabilities it decomposed into, and what the catalog says could be adopted,
-- based on, extended, learned from or avoided. It belongs in project_documents
-- because that is already the per-project document store with a read path, a
-- slug-uniqueness rule and full-text search.
--
-- The kind is `scout`, and it does not exist yet. SQLite cannot ALTER a CHECK
-- constraint, so this rebuilds the table rather than smuggling a scout report
-- into an existing kind: writing one as kind `plan` would make ListDocuments
-- report a decision record as a plan, which is a lie in the one place a reader
-- would go looking for the truth. phase3-spec §6.1 states this rather than
-- hiding it.
--
-- The FTS table and its triggers are rebuilt too. SQLite has no
-- ALTER TABLE ... RENAME CONSTRAINT, and a trigger left pointing at the old
-- table name would silently stop maintaining the index — a full-text search that
-- finds old documents and never new ones is worse than one that is obviously
-- broken.

PRAGMA foreign_keys = OFF;

CREATE TABLE project_documents_new (
    id          INTEGER PRIMARY KEY,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- readme | spec | plan | wiki | adr | changelog | scout
    kind        TEXT    NOT NULL
        CHECK (kind IN ('readme','spec','plan','wiki','adr','changelog','scout')),
    slug        TEXT    NOT NULL,
    title       TEXT    NOT NULL DEFAULT '',
    body        TEXT    NOT NULL DEFAULT '',
    revision    INTEGER NOT NULL DEFAULT 1,
    author_id   INTEGER NOT NULL REFERENCES users(id),
    created_at  REAL    NOT NULL,
    updated_at  REAL    NOT NULL,
    UNIQUE (project_id, kind, slug)
);

INSERT INTO project_documents_new
    (id, project_id, kind, slug, title, body, revision, author_id,
     created_at, updated_at)
SELECT id, project_id, kind, slug, title, body, revision, author_id,
       created_at, updated_at
FROM project_documents;

DROP TABLE project_documents;
ALTER TABLE project_documents_new RENAME TO project_documents;

-- Everything the base table owned goes with it, and this was verified rather than
-- assumed.
--
-- The index and the three FTS triggers are children of `project_documents`, and
-- `DROP TABLE` drops a table's indexes and triggers with it. A first draft of
-- this migration carried a comment claiming the triggers "resolve their table at
-- run time against the schema, not against the name it had when written", which
-- would have left a rebuilt table with NO triggers and a full-text search that
-- silently stopped indexing every new document.
--
-- Measured on the same SQLite this project runs, before and after the
-- rebuild-and-rename dance:
--
--     before: ['idx_project_documents_project',
--              'project_documents_fts_ai', 'project_documents_fts_ad',
--              'project_documents_fts_au']
--     after:  []
--
-- So all four are recreated below. The trigger bodies are copied from migration
-- 0007 verbatim, including the `'delete'` command form an external-content FTS5
-- table requires -- a `DELETE FROM project_documents_fts WHERE rowid = ...` is
-- not valid against one, which is why the first draft's version of these did not
-- survive being written down.
CREATE INDEX idx_project_documents_project
    ON project_documents (project_id, kind, updated_at DESC);

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

-- The index itself is NOT rebuilt. It is an external-content table
-- (`content = 'project_documents'`) over the base table, and its rows were never
-- dropped -- only the triggers that maintain it were. Rebuilding it anyway would
-- be wrong: for an external-content FTS5 table the command is 'rebuild', and a
-- plain re-INSERT is a duplicate insert into the index rather than a rebuild.
--
-- `TestTheFtsMirrorStillIndexesDocumentsAfterTheScoutKindMigration` inserts a
-- document through the rebuilt triggers and searches for it, so a mirror left
-- stale by this migration fails a test rather than quietly answering nothing.

PRAGMA foreign_keys = ON;