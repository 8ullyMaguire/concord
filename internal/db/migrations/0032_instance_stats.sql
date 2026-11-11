-- R15 / spec §4.13 STAT-3. The instance's statistics, recorded once a day.
--
-- WHY A TABLE AND NOT A QUERY. History has to be RECORDED. Re-deriving yesterday from
-- audit_log gives a different answer than re-deriving it today, because the log was never
-- complete (925 of 1,874 seeded features have no create row), and a migration that rebuilds a
-- table rewrites any series derived from the tables themselves. A snapshot is a still frame
-- taken at a moment; only a stored row can still be that frame next month.
--
-- ONE ROW PER DAY, ENFORCED BY THE PRIMARY KEY. A second run on the same day must CORRECT the
-- row rather than add one: a duplicate day would put two points at the same x on a chart, and
-- the slope between them would be infinite. `ON CONFLICT(day) DO UPDATE` is the write, and this
-- constraint is what makes it a correction rather than an append.
CREATE TABLE instance_stats (
    day               TEXT PRIMARY KEY,  -- 'YYYY-MM-DD', UTC
    projects          INTEGER NOT NULL,
    projects_measured INTEGER NOT NULL,  -- with_health: a count of MEASUREMENTS, not projects
    features_total    INTEGER NOT NULL,
    features_seed     INTEGER NOT NULL,  -- origin IN ('seed','import')
    complaints_total  INTEGER NOT NULL,
    complaints_seed   INTEGER NOT NULL,
    triage_median_h   REAL,             -- NULL when n = 0; never 0.0
    triage_n          INTEGER NOT NULL DEFAULT 0,
    delivery_median_h REAL,
    delivery_n        INTEGER NOT NULL DEFAULT 0,
    decision_median_h REAL,
    decision_n        INTEGER NOT NULL DEFAULT 0,
    -- Rows that predate R10's backfill, across BOTH tables 0030 touched. Counted together
    -- because they are one population: the rows whose provenance nobody can state. Two
    -- columns would invite a reader to compare them, and the split is not a fact about the
    -- rows -- it is a fact about which table they are in.
    no_origin         INTEGER NOT NULL DEFAULT 0,
    computed_at       REAL NOT NULL
);
