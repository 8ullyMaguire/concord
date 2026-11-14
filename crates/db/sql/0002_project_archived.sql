-- The `Archived` stage, as an ACT rather than an inference (review decision 8).
--
-- `concord_ranking::lifecycle` refuses to infer this one on purpose: a stage the code assigns to somebody's project is a stage that is
-- wrong about it. What a maintainer can do is declare it, and the declaration needs a date (when), a reason (why, in their words) and an
-- actor (who). The reason is a sentence and not an enum, because "declared dead" without a reason is a label a reader cannot act on —
-- and the note's own requirement is that abandonment carries suggested alternatives.
--
-- Nullable everywhere: absent means "nobody has declared this", which is not the same as "alive" and must not be stored as a false.
alter table projects add column if not exists archived_at timestamptz;
alter table projects add column if not exists archived_reason text;
alter table projects add column if not exists archived_by bigint references users(id) on delete set null;

-- Partial: the only query that matters is "which projects are archived", and an index over the nulls would be an index over the catalogue.
create index if not exists projects_archived_at on projects (archived_at) where archived_at is not null;
