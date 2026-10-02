-- Namespaced hierarchical tags, aliases, and quorum-ratified taxonomy changes
-- (spec revision 4 §4.3).
--
-- Two defects in the existing tag handling that this migration is built around.
--
-- First, tags were flat and anonymous. `tags` is (id, project_id, name) with
-- UNIQUE(project_id, name), so `kanban` and `topic:kanban` are simply two rows
-- with no relationship. §4.3 requires namespaced tags (topic:, domain:,
-- platform:, lang:, role:, deploy:, audience:) forming a hierarchy (is-a,
-- part-of), because the taxonomy is the primary discovery facet and a flat list
-- cannot express "this is a kind of that".
--
-- Second, `ensureTag` silently INSERTed a brand-new global tag whenever one was
-- not found. §4.3 says the opposite in two separate ways: the tagging UI must
-- suggest existing tags before allowing new ones, and global taxonomy changes are
-- proposals decided by quorum. Auto-create-on-use made the global taxonomy a
-- side effect of a per-project edit, which is exactly the "maintainers hold
-- unilateral taxonomy power" that revision 4 removes.

-- namespace: the prefix before the colon in `topic:kanban`.
--
-- Nullable with a backfill to 'topic' rather than a NOT NULL default, because
-- existing tags were namespaced by whoever wrote them or not at all. Guessing a
-- namespace for each would put a confident-looking but invented value on real
-- data; NULL means "not yet classified", which is honest and is what the
-- taxonomy-completeness tooling should surface.
ALTER TABLE tags ADD COLUMN namespace TEXT;

ALTER TABLE tags ADD COLUMN parent_id INTEGER REFERENCES tags(id);

-- How child relates to parent: is-a (a kind of) or part-of (a component of).
-- A CHECK because these are the only two relations §4.3 defines, and a third
-- would be a relation nobody has agreed on.
ALTER TABLE tags ADD COLUMN relation TEXT
    CHECK (relation IS NULL OR relation IN ('is-a','part-of'));

-- suggested tags come from README/manifest/embedding inference and need a human
-- confirmation before they count as taxonomy. §4.3 requires the label, so it is
-- stored rather than implied by which code path wrote the row.
ALTER TABLE tags ADD COLUMN suggested INTEGER NOT NULL DEFAULT 0;

-- Taxonomy changes are proposals decided by quorum among eligible taggers
-- (§4.3). Merges, renames, aliases, re-parents and deletes all route through
-- one table so there is one ratification path rather than five.
CREATE TABLE taxonomy_proposals (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    -- NULL for an instance-wide proposal. §4.3 makes GLOBAL taxonomy changes
    -- quorum-decided, and a project-scoped tag is ordinary housekeeping, so
    -- both live here and the scope is explicit rather than assumed.
    project_id   INTEGER REFERENCES projects(id) ON DELETE CASCADE,
    proposed_by  INTEGER NOT NULL REFERENCES users(id),
    action       TEXT NOT NULL CHECK (action IN ('create','rename','alias','merge','reparent','delete')),
    target_tag   TEXT NOT NULL,
    -- For create: the new name. For rename: the new name. For alias: the alias
    -- that resolves to target_tag. For merge: the tag being absorbed. For
    -- reparent: the new parent. Kept as a single column because each action has
    -- exactly one operand, and a separate nullable column per action would be
    -- five ways to store NULL.
    value        TEXT NOT NULL DEFAULT '',
    rationale    TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending','ratified','rejected','withdrawn','stale')),
    created_at   REAL NOT NULL,
    decided_at   REAL
);

-- One pending proposal per (scope, action, target, value): two people proposing
-- the same merge cannot each be ratified by accident.
CREATE UNIQUE INDEX idx_taxonomy_one_pending
    ON taxonomy_proposals (IFNULL(project_id, 0), action, target_tag, value)
    WHERE status = 'pending';

CREATE INDEX idx_taxonomy_project_status
    ON taxonomy_proposals (project_id, status);

-- Consents, mirroring strategic_weight_consents. A dedicated table rather than
-- counting audit rows because the question is always "how many DISTINCT
-- eligible taggers", which the JSON audit detail cannot answer without parsing.
CREATE TABLE taxonomy_consents (
    proposal_id  INTEGER NOT NULL REFERENCES taxonomy_proposals(id) ON DELETE CASCADE,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    consented_at REAL NOT NULL,
    PRIMARY KEY (proposal_id, user_id)
);

-- Aliases collapse duplicates: js ≡ javascript (§4.3). Kept with merge history
-- rather than by rewriting every reference, so the rename is auditable.
--
-- ON DELETE CASCADE from tags: if the alias's target tag is deleted the alias is
-- meaningless, and a dangling alias would resolve to nothing.
CREATE TABLE tag_aliases (
    alias_id    INTEGER PRIMARY KEY AUTOINCREMENT,
    tag_id      INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    alias       TEXT NOT NULL,
    created_by  INTEGER NOT NULL REFERENCES users(id),
    created_at  REAL NOT NULL,
    UNIQUE (tag_id, alias)
);

-- The reverse lookup is the whole point: given "js", find the tag.
CREATE INDEX idx_tag_alias_alias ON tag_aliases(alias);

-- An alias must not shadow a real tag. If `javascript` is both a tag and an
-- alias for something else, resolution is ambiguous and search results would
-- depend on which table was consulted. Enforced rather than documented because
-- an ambiguous taxonomy is unrecoverable once data exists.
CREATE TRIGGER trg_tag_alias_no_shadow
BEFORE INSERT ON tag_aliases
FOR EACH ROW WHEN EXISTS (
    SELECT 1 FROM tags WHERE lower(name) = lower(NEW.alias)
)
BEGIN
    SELECT RAISE(ABORT, 'alias shadows an existing tag');
END;

-- A tag cannot be its own parent: a cycle makes the hierarchy traversal in
-- §4.3's tag-hierarchy-overlap loop forever. Two-line cycles are still possible
-- here, so this is the cheap guard rather than a complete one -- the reparent
-- path additionally refuses a descendant, which is the real check.
CREATE TRIGGER trg_tag_no_self_parent
BEFORE UPDATE OF parent_id ON tags
FOR EACH ROW WHEN NEW.parent_id IS NOT NULL AND NEW.parent_id = NEW.id
BEGIN
    SELECT RAISE(ABORT, 'a tag cannot be its own parent');
END;