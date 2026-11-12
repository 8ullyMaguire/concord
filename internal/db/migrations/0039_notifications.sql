-- Notifications (docs/specs/notification-spec.md §4.1).
--
-- The author of a removed feature, or of a rejected complaint, gets a row here in the SAME
-- transaction that records the decision. Until now the only record was audit_log, which is a
-- public modlog: a person had to read a log to be told about their own contribution.
CREATE TABLE notifications (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN ('feature_removed','complaint_rejected')),
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- The feature id or the complaint id, per kind. Deliberately NOT a foreign key: the
    -- notification is a record of a decision about a thing that may be deleted afterwards,
    -- and a cascade would erase the record of the decision at the moment it matters most.
    subject_id  INTEGER NOT NULL,
    -- Copied at write time, not joined at read time, for the same reason: a join to
    -- features.title renders "(null)" the moment the feature row goes, which is exactly the
    -- case this table exists for. This is the text the author needs, not a second truth.
    title       TEXT NOT NULL,
    reason      TEXT NOT NULL DEFAULT '',
    -- Who decided. Nullable because the reader should still be told the decision after the
    -- moderator's account is deleted; the page renders "a moderator" in that case.
    actor_id    INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at  REAL NOT NULL,
    read_at     REAL
);
CREATE INDEX notifications_inbox  ON notifications(user_id, created_at DESC);
CREATE INDEX notifications_unread ON notifications(user_id, read_at);
