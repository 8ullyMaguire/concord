-- Feature dependencies (docs/specs/idea-intake-and-milestones-spec.md §6, R18).
--
-- A milestone's contents can be listed but not ordered while nothing records that
-- one idea must land before another. Both columns are FKs to features, both with
-- ON DELETE CASCADE: deleting a feature cannot leave a dangling edge that blocks
-- something forever.
CREATE TABLE feature_deps (
    blocker_id  INTEGER NOT NULL REFERENCES features(id) ON DELETE CASCADE,
    blocked_id  INTEGER NOT NULL REFERENCES features(id) ON DELETE CASCADE,
    created_at  REAL NOT NULL,
    PRIMARY KEY (blocker_id, blocked_id),
    -- A feature cannot block itself. The cycle check in the store refuses longer
    -- cycles; this refuses the one-row one, which no walk can see.
    CHECK (blocker_id <> blocked_id)
);
CREATE INDEX feature_deps_blocked ON feature_deps(blocked_id);
