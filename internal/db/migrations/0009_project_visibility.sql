-- 0009_project_visibility.sql
--
-- Projects were all-public: there was no way to say that one should not appear
-- in the project list, and nothing to enforce if you did. This adds four
-- visibility levels, defaulting every existing row to 'public' so nothing
-- changes for projects already on the site.
--
-- THE FOUR LEVELS, and the distinction that matters:
--
--   public     listed in /api/v1/projects and in discovery/search, readable by
--              anyone, including anonymous callers.
--
--   unlisted   NOT listed anywhere, but readable by direct link with no session
--              at all. This is obscurity: the URL is the capability. Useful for
--              something you want to share with people who have no account, and
--              honest about being obscurity rather than access control.
--
--   protected  NOT listed, and readable only by a signed-in user who has been
--              granted access (a members row, or a redeemed invite).
--
--   private    NOT listed, and readable only by a member. This is the strict
--              level: an authenticated non-member gets 404, not 403, because a
--              403 would confirm the slug exists and make the instance
--              enumerable. Reachable by direct link with a session, and by
--              nothing else.
--
-- WHY 404 AND NOT 403 FOR A NON-MEMBER. auth.go already set this principle in
-- requireWriteActor: "the response code must not depend on whether a project
-- exists", or an anonymous caller can enumerate every slug on the site.
-- Returning 403 to a signed-in non-member reintroduces that leak one level up.
-- So a visibility refusal is indistinguishable from "no such project" in the
-- response body, and only the audit row records which it was.
--
-- WHY ADD COLUMN AND NOT A TABLE REBUILD. SQLite can add a column with a NOT
-- NULL DEFAULT and a CHECK in one statement, which preserves every index,
-- every trigger and the rowid contract. Rebuilding the table to add one column
-- would mean re-declaring projects_fts -- which is maintained by the
-- application (store.go deletes and reinserts by slug on write; it has no
-- triggers) and carries tags and languages columns that have nothing to do with
-- this change. An earlier draft of this file did exactly that and was wrong.
--
-- THE DEFAULT IS NOT NULL WITH A CHECK, NOT A BARE COLUMN. A nullable
-- visibility would let a row exist with NULL, and then every read path needs its
-- own COALESCE to decide whether to hide it. Making NULL impossible means
-- "unset" is a state the database refuses rather than one each handler
-- reinterprets. Verified: the CHECK does bite on UPDATE.

ALTER TABLE projects
    ADD COLUMN visibility TEXT NOT NULL DEFAULT 'public'
        CHECK (visibility IN ('private', 'unlisted', 'protected', 'public'));

-- Listing must filter on visibility cheaply: this is the query every anonymous
-- project list and every discovery search runs.
CREATE INDEX idx_projects_visibility ON projects(visibility);

-- An unguessable token, not a counter: invite links get handed to people.
CREATE TABLE project_invites (
    id           INTEGER PRIMARY KEY,
    project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    token        TEXT NOT NULL UNIQUE,
    created_by   INTEGER NOT NULL REFERENCES users(id),
    created_at   REAL NOT NULL,
    expires_at   REAL,            -- NULL = no expiry
    max_uses     INTEGER,         -- NULL = unlimited
    uses         INTEGER NOT NULL DEFAULT 0,
    revoked_at   REAL,            -- non-NULL kills the link without erasing history
    CHECK (max_uses IS NULL OR max_uses > 0),
    CHECK (uses >= 0),
    -- A cap below the current use count is meaningless and would only confuse
    -- the redeem path.
    CHECK (max_uses IS NULL OR uses <= max_uses)
);

CREATE INDEX idx_project_invites_project ON project_invites(project_id);
CREATE INDEX idx_project_invites_token ON project_invites(token);

-- Redeeming the same invite twice must not create two memberships.
CREATE TABLE invite_redemptions (
    invite_id   INTEGER NOT NULL REFERENCES project_invites(id) ON DELETE CASCADE,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    redeemed_at REAL NOT NULL,
    PRIMARY KEY (invite_id, user_id)
);

CREATE INDEX idx_invite_redemptions_user ON invite_redemptions(user_id);