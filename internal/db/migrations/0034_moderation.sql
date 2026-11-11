-- An act that reverses another. Nullable, because most acts reverse nothing, and the
-- self-reference keeps "undone by" in the log instead of in a second table that could
-- disagree with it.
ALTER TABLE audit_log ADD COLUMN undoes INTEGER REFERENCES audit_log(id);

-- Keyset paging. The existing index is (project_id, created_at); a cursor compares
-- (created_at, id), and without the id in the index two acts in the same millisecond can
-- repeat or skip a row across pages.
CREATE INDEX idx_audit_keyset ON audit_log(project_id, created_at DESC, id DESC);
