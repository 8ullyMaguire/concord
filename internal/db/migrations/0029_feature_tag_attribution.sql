-- §6.2's themes are tag-scoped weights, so a feature's tags are the input they steer by --
-- and `feature_tags` has had NO WRITER since 0001. Every INSERT in the tree is in a test;
-- the live instance holds 537 features and 0 rows here. The table is a join of
-- (feature_id, tag_id) with no record of who applied the tag or when.
--
-- That matters more for this table than for the others: taxonomy.go's merge already moves
-- feature_tags rows when two tags are merged, so the repository treats feature tags as a
-- real surface, and a tag on a feature is a steering input -- something a reader should be
-- able to attribute, the way project_tags.applied_by and list_entries.proposed_by are.
--
-- Both columns are added with defaults, and applied_by stays NULLABLE, because rows written
-- before this migration have no actor to name and inventing one is worse than an empty
-- attribution. The fold carries applied_by: an account merged into another keeps its tags.
ALTER TABLE feature_tags ADD COLUMN applied_by INTEGER REFERENCES users(id);
ALTER TABLE feature_tags ADD COLUMN created_at REAL NOT NULL DEFAULT 0;
