-- Maintainer emergency hold (§6.6), and the public admin-action ledger (§9.3).
--
-- The spec previously described the maintainer veto as an override path for
-- blocks in §5.5/§6.3 while the role definition limited it to security and
-- legal. That is two different powers described once, and the ambiguity is
-- dangerous: read the first way, a maintainer could cancel a community block.
--
-- Revision 4 resolves it. The hold is an *emergency hold*: it suspends a
-- proposal, and it cannot force a result, override a block, or close a call.
-- It requires a written reason, expires, and triggers an automatic confirmation
-- vote inside the expiry window. So the strongest thing a maintainer can do is
-- pause something, and the pause expires on its own.

CREATE TABLE emergency_holds (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    call_id       INTEGER NOT NULL REFERENCES consensus_calls(id) ON DELETE CASCADE,
    held_by       INTEGER NOT NULL REFERENCES users(id),
    -- Required, and the reason is stored rather than logged to a text field:
    -- a hold with no stated justification cannot be reviewed by the community,
    -- which is the entire mechanism that makes it accountable.
    reason        TEXT NOT NULL,
    -- security or legal, per the role definition. A CHECK rather than free text
    -- so the emergency power cannot be used for ordinary disagreement.
    grounds       TEXT NOT NULL CHECK (grounds IN ('security','legal')),
    created_at    REAL NOT NULL,
    expires_at    REAL NOT NULL,
    -- The confirmation vote the hold triggers (§6.6). NULL while no vote has
    -- been opened for it.
    confirmation_call_id INTEGER REFERENCES consensus_calls(id) ON DELETE SET NULL,
    released_at   REAL,
    release_reason TEXT
);

CREATE INDEX idx_holds_project ON emergency_holds(project_id, created_at);
CREATE INDEX idx_holds_call    ON emergency_holds(call_id);

-- A hold must state its expiry in the future, and the expiry is what bounds the
-- power: a hold that could be renewed indefinitely would just be a veto with
-- extra steps.
CREATE TRIGGER trg_holds_expiry_after_creation
BEFORE INSERT ON emergency_holds
FOR EACH ROW WHEN NEW.expires_at <= NEW.created_at
BEGIN
    SELECT RAISE(ABORT, 'emergency hold expiry must be after creation');
END;

-- §9.3: a public, always-visible ledger of admin and steward actions.
--
-- Separate from audit_log on purpose. audit_log is the operational record and
-- is filtered by project; §9.3 requires that a platform admin's actions on
-- content be visible instance-wide, because an admin with no content authority
-- is only a real constraint if everyone can watch what they did. Nothing in this
-- table is ever hidden or deleted.
CREATE TABLE admin_ledger (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    actor_id    INTEGER REFERENCES users(id),
    action      TEXT NOT NULL,
    subject     TEXT NOT NULL DEFAULT '',
    -- An admin override of a user decision requires written justification and
    -- triggers an automatic supermajority confirmation vote (§9.3). Nullable
    -- because most ledger entries are routine housekeeping.
    justification TEXT,
    project_id  INTEGER REFERENCES projects(id) ON DELETE SET NULL,
    created_at  REAL NOT NULL
);

CREATE INDEX idx_admin_ledger_created ON admin_ledger(created_at DESC);

-- Append-only, enforced rather than merely intended: an UPDATE or DELETE on the
-- ledger is refused by the database. §9.3 says "always-visible", and a table
-- any writer can rewrite is not that.
CREATE TRIGGER trg_admin_ledger_no_update
BEFORE UPDATE ON admin_ledger
BEGIN
    SELECT RAISE(ABORT, 'admin_ledger is append-only');
END;

CREATE TRIGGER trg_admin_ledger_no_delete
BEFORE DELETE ON admin_ledger
BEGIN
    SELECT RAISE(ABORT, 'admin_ledger is append-only');
END;