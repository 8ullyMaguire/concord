# Instance statistics and decision velocity — spec

**Status:** specified 2026-10-30, nothing built.
**Implements:** spec **§4.13** (new in r4.2) and the exposure half of §4.11 (embeds, badges).
**Plan:** `docs/plans/instance-statistics.md` (STAT-1 … STAT-3, milestones R13–R15).
**Depends on:** §4.5.7's `origin` column (R10) for the provenance split, and R12 for the
newcomer-responsiveness leaderboard.

## 1. Why this needs a spec at all

"Show totals and velocity" is one sentence and three separate hazards. Measured on the live
instance on 2026-10-30:

| what a naive page would show | what it would actually be |
|---|---|
| "1,874 features" | 949 have a `create_feature` audit row; the other **925 were written by a seed script**. The total is a fact about the database, not about people |
| "features opened per week" | a chart of an import: features exist on **12 distinct days**, in batches — 601 on 2026-09-29, 302 on 10-25, 171 on 09-30, 146 on 10-30, then 138/116/100/100/100 |
| "open vs closed backlog" | from `audit_log` this shows **closings without openings**: a seeded row has no create event, but the `set_feature_status` it receives today is audited (513 rows). The backlog would appear to shrink out of nothing |
| "74 projects, 71 measured" | 71 `project_metrics` **rows**, of which 0 carry a `health_score`. A count of rows is not a count of measurements |

So the deliverable is not a page of numbers. It is a rule for which numbers may be published,
and a data store that makes the honest ones cheap.

## 2. Definitions

**Aggregate.** A number computed over more than one row and shown to a reader.

**Provenance split.** Every aggregate over rows that can be created by an import is published
as two numbers: the organic count and the seeded count. `origin ∈ {api, seed, import, ui}`
(R10). `ui` and `api` are organic; `seed` and `import` are not. An aggregate that cannot
produce the split is not published — not published with a footnote, not published with an
asterisk.

**Latency median.** A median over per-entity durations, computed from the entity's own
timestamps, with its sample size. Not a difference of two counts (that is throughput), and
not a mean (one row that sat for 400 days destroys a mean and is exactly the row a reader
wants to know about).

**Snapshot.** One row per day holding the aggregates as they were that day, so history does
not have to be re-derived from a log that was never complete.

## 3. What is published

### 3.1 Census (composition, not growth) — STAT-1

- projects by language, tag, license, governance model, visibility;
- features by status and by effort; complaints by status;
- members by role; arenas by type; documents by kind;
- for each of the above, the organic/seeded split where the table carries `origin`.

Still frames. A census cannot be mistaken for a trend, which is why it ships first and alone.

### 3.2 Decision latency — the headline — STAT-2

Three medians, each with `n`:

| latency | from | to | source |
|---|---|---|---|
| triage | complaint `created_at` | first `validate_complaint` audit row | audit + row |
| delivery | feature entering `ready` | feature entering `shipped` | audit `set_feature_status` pairs |
| decision | consensus call `created_at` | call closed | `consensus_calls` + its close audit row |

Edge rules, each of which is a published number going wrong if it is missed:

- **A row that has not finished contributes nothing**, and the count of unfinished rows is
  printed beside the median ("delivery: 9 days median, n=41, 216 still in progress"). The
  unfinished rows are the interesting population, not an exclusion.
- **A row whose start is a seed has no start.** Seeded features were inserted with a status;
  they never passed through `ready`, so they contribute to `n` only when an audit row records
  the transition. This is a property of the data, and the number must say so rather than
  silently count them as zero days.
- **Reopened entities are measured on their current episode** (this is what the audit rows
  give); a total-time-across-episodes number is not published because nobody has decided
  which one a reader means.

### 3.3 Snapshot history — STAT-3

`instance_stats` — one row per day: census totals, the three latencies with their `n`,
the count of projects with a measured `health_score`, and the count of rows with no `origin`
(NULL, from before R10). Written by `cmd/statsnapshot`, run by a daily timer.

A slope needs two points recorded the same way, and re-deriving yesterday from `audit_log`
produces a different number than re-deriving today, for reasons that have nothing to do with
the project — that is why the series is recorded rather than recomputed.

**Correction (2026-11-06): `instance_stats` cannot carry "rising".** This paragraph used to
imply it did. The table has no project column, so a per-project slope was not derivable from it
at any point; `0033_project_stats.sql` records the per-project half
(`day, project_id, organic_features_shipped, organic_complaints_filed`) in the same pass. The
instance row stays instance-wide, which is what its name says.

### 3.4 Exposure — STAT-2, STAT-3

- `GET /api/v1/stats` — the JSON the page shows. **One computation, two renderings**; a page
  that recomputes what the API returns is two answers waiting to disagree.
- `GET /badge/{slug}.svg` — health, decision latency, shipped-in-30d. Text-only, no font
  subsetting beyond two system families, `Cache-Control: max-age=3600`.
- The statistics block appended to the existing `feed.xml`/`atom.xml` (`kind=stats`), so a
  reader can subscribe to the number instead of the page.
- `GET /api/v1/stats/history?days=90` — the snapshot series, the only route that is a series
  rather than a frame.
- `GET /api/v1/stats/rising` — projects ranked by organic work per week across their own first
  and last snapshot, absent under a 14-day span, the span printed in every row, health beside
  the slope rather than instead of it.

### 3.5 Responsiveness leaderboard — STAT-3

Projects ranked by median first-response to a newcomer (§4.5.7 / R12). Ship only when
`newcomer_sample >= 5`; a project below that is absent from the ranking, never ranked last.
This is the only leaderboard this spec sanctions, and the reason is narrow: it ranks the
bottleneck (answering), not the abundance (producing).

## 4. What is refused, and why

- **A live activity ticker.** On 12 active days it is mostly an empty box, and it exists to
  hold attention rather than to inform (§4.5.4).
- **Volume as the headline** ("1,874 features!"). It is a seed count, and the first honest
  reader to check it destroys the trust the whole directory runs on.
- **Per-user public contribution counts.** Surveillance plus a volume incentive — both of the
  things complaint fatigue exists to prevent. Reputation stays tag-scoped and internal.
- **Stars as a health proxy.** The column exists and 0 of 74 projects have ever had it
  fetched; publishing it as a health signal publishes an empty column as a verdict (§§4.13, R9).
- **Mixed series.** One quantity may not be plotted with seeded and organic rows summed and a
  single label. Two lines, labelled, or no chart.
- **A median without `n`.** Not a smaller number — not a number.

## 5. Invariants to test

1. Every published aggregate over `features` or `complaints` carries a non-zero split
   whenever both kinds of row exist (a test that seeds one organic and one seeded row).
2. With one organic and one seeded feature, the "opened this week" series shows **two**
   labelled series and neither is the sum of both.
3. Latency: a feature still `in_progress` contributes to `unfinished`, not to `n`.
4. Latency: a seeded feature with no `ready` audit row does not appear as a zero-day delivery.
5. Snapshot: running `cmd/statsnapshot` twice on the same day updates today's row rather than
   inserting a second one (`UNIQUE(day)`), and the second run's numbers equal the first's for
   every column that does not depend on "now".
6. Badge: an unmeasured project (`health_score IS NULL`) renders "—", never "0.0".
7. `GET /api/v1/stats` and the `/stats` page return the same numbers — asserted by comparing
   the page's rendered text against the JSON, not by reading both bodies for equality.
8. A private project contributes to no public aggregate, but does contribute to a
   maintainer's own view (§6.6's visibility rule applied to counts, where a leak is one row
   of difference in a total and therefore the hardest kind to notice).

## 6. What this does not solve

Nothing here creates activity. The instance has 12 active days of rows and one person filing
them; these numbers will report that honestly, which is the point, and will look unimpressive,
which is also the point. A statistics page is the instrument, not the engine — and the first
thing it will measure is how little the engine has run.
