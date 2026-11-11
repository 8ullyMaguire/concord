-- R15 / spec §4.5.5.7 STAT-3.4. One row per (day, project), so a per-project slope
-- can be read from recorded history instead of re-derived from an audit log.
--
-- WHY A SECOND TABLE, AND WHY THE PLAN'S SENTENCE COULD NOT BE IMPLEMENTED.
-- `0032_instance_stats.sql` records ONE instance-wide row per day and has no project
-- column, so "rank projects by the slope of … between two snapshots" was not derivable
-- from it at all. The choice was between a single instance-wide slope (a number about
-- the instance, dressed as a ranking of projects) and this table. The ranking is the
-- deliverable, so the table is.
--
-- WHY THE COUNTS ARE CUMULATIVE AND ORGANIC-SUFFIXED.
--
--  * CUMULATIVE, NOT DELTAS. The row is "how many had happened by the end of this day".
--    A stored delta would be wrong the moment a snapshot run is missed: the day after a
--    gap would report one day's change for three. Cumulative counts make a gap a gap --
--    the slope is divided by the days actually between the two rows.
--  * ORGANIC ONLY, AND THE COLUMN NAME SAYS SO. `0030` backfilled every pre-existing
--    feature to origin='unknown' and the seed importer wrote 'seed'/'import'; a slope
--    computed over those is a chart of an import script, which is the exact defect
--    STAT-1 exists to prevent. `organic_` in the name is not decoration: the bare
--    `features_shipped` invites a reader to treat it as a total.
--
-- `PRIMARY KEY (day, project_id)` is the ON CONFLICT target, for the same reason as
-- `0032`: a second run on the same day must CORRECT the row rather than append a second
-- point at the same x, or the slope between them is infinite.
--
-- NO visibility FILTER HERE, deliberately -- the table stores facts, and the read side
-- decides what a stranger may see (the same split `ListProjectsWithRepo` and the health
-- ingest use). The leaderboard filters; the recorder does not.
CREATE TABLE project_stats (
    day                      TEXT NOT NULL,   -- 'YYYY-MM-DD', UTC
    project_id               INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    organic_features_shipped INTEGER NOT NULL DEFAULT 0,
    organic_complaints_filed INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, project_id)
);

-- The slope query reads one project's whole series, so the index leads with project_id.
CREATE INDEX project_stats_project ON project_stats (project_id, day);
