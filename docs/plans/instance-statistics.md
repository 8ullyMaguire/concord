# Instance statistics — implementation plan

**Spec:** `docs/specs/instance-statistics-spec.md` (spec §4.13). **Plan written:** 2026-10-30.
**Milestones:** R13 (STAT-1), R14 (STAT-2), R15 (STAT-3) in `docs/PLAN-r4.md`.

Depends on `origin` (R10, migration `0031`) and, for the leaderboard only, on R12's newcomer
metric. STAT-1 may ship the moment `origin` exists; it does not need health ingest.

Standing rules: `make verify` green before every commit; no store method without a caller
(§27's export inventory gate); every route needs a reader in `assets/js`
(`reader_coverage_test.go`); no number in a document that the doc-number gate cannot verify.

---

## STAT-1 — Census with a provenance split

### Measured corrections to this plan, 2026-11-06

This section was written 2026-10-30 and **four of its statements do not survive contact with the
code**. Each was found by reading the tree, and each changes what gets built:

| # | the plan said | the code says | consequence |
|---|---|---|---|
| 1 | define `FacetCount{Value,Total,Seed,Other}` in `stats.go` | `FacetCount{Value,Count}` **already exists** in `internal/store/search.go:29` | a second type of that name will not compile; the new one is `ProvenanceCount` |
| 2 | the split applies to `projects`/`tags`/`licenses`/`governance`/`members`/`documents` | **only `features` and `complaints` carry `origin`** (`0030_work_items.sql:21,25`). `projects` has no `origin` column and no migration adds one | those facets cannot produce a split. Spec §3.1 says "where the table carries `origin`", so they publish a plain count |
| 3 | `Other` = "everything else" | `origin` has a fifth value, **`'unknown'`**, which `0030` wrote to every pre-existing row | folding it into `Other` reports the backfilled population as organic — the exact lie R13 exists to prevent. The split is **three-way** |
| 4 | `documents by kind` | the table is **`project_documents`** (`0007`, rebuilt by `0023`) | a query against `documents` is a runtime error the compiler cannot catch |

**Why the plain facets are still published** (the reading of spec §2 that this implements):
§2 withholds an aggregate that cannot produce the split only over *"rows that can be created by an
import"*. Projects are created by people — `CreateProject` requires a `user_id`
(`internal/store/store.go:338`) — and members, documents and arenas are human actions. So those
aggregates are not import charts and a plain count is honest. Features and complaints are the two
that are, and they are exactly the two that carry `origin`.

### STAT-1.1 `internal/store/stats.go` (new)

```go
// Census is the composition of the instance: what exists, not what happened.
type Census struct {
	// Tables that carry `origin` publish the split.
	FeatureStatus []ProvenanceCount `json:"features_by_status"`
	FeatureEffort []ProvenanceCount `json:"features_by_effort"`
	Complaints    []ProvenanceCount `json:"complaints_by_status"`

	// Tables that do not: a plain count, reused from search.go's FacetCount.
	Projects   []FacetCount `json:"projects_by_language"`
	Tags       []FacetCount `json:"projects_by_tag"`
	Licenses   []FacetCount `json:"projects_by_license"`
	Governance []FacetCount `json:"projects_by_governance"`
	Visibility []FacetCount `json:"projects_by_visibility"`
	Members    []FacetCount `json:"members_by_role"`
	Documents  []FacetCount `json:"documents_by_kind"`
	Arenas     []FacetCount `json:"arenas_by_type"`
}

// ProvenanceCount carries the split on the same struct, not as a second query: a caller that
// has to remember to fetch the split is a caller that will print a total without one.
//
// Three columns, because migration 0030 backfilled every pre-existing row to 'unknown':
// that population predates the backfill and is neither organic nor seeded.
type ProvenanceCount struct {
	Value   string `json:"value"`
	Total   int    `json:"total"`
	Organic int    `json:"organic"` // origin IN ('api','ui')
	Seeded  int    `json:"seeded"`  // origin IN ('seed','import')
	Unknown int    `json:"unknown"` // origin = 'unknown' (predates the 0030 backfill)
}

func (d *DB) Census(ctx context.Context) (Census, error)
```

`Census` issues nine grouped queries over six tables. **No query joins a fact table to a
dimension in a way that can multiply a facet** — the `project_tags`→`tags` join is 1:1 on the
primary key, and that is the defect the AC names (a total larger than `count(*)`), asserted in
`TestCensusTotalNeverExceedsTableCount`.

The facet SQL is *not* reused from `facetsFor` (`search.go:201`): that function is scoped to a
search result's `ids`, and a census is instance-wide. The SQL shape is similar; the scope is not.

**The invariant is arithmetic:** `Total = Organic + Seeded + Unknown` for every row, asserted in
`TestCensusProvenanceRowsSum` over a fixture holding all three populations.

### STAT-1.2 `internal/httpapi/stats.go` + route

```
GET /api/v1/stats/summary        # Census, public, no auth (reads are exempt from consent)
```

Registered inside the existing `/api/v1` group. GET is not mutating, so the consent gate does
not apply (`guest_handlers.go: isMutating`) — a visitor reads the instance's own numbers without
accepting anything first.

**Shipped 2026-11-06 with its own page shell** (`GET /stats`, `templates/stats.html`,
`assets/js/stats.js`), rather than waiting for STAT-2. The original advice was "STAT-1 and
STAT-2 must land in the same commit or STAT-1 ships behind a page that already exists" — the
reader-coverage gate forbids a route with no reader, and no `/stats` page existed, so the page
was built with STAT-1. It renders the census now and gains the latency cards when STAT-2 lands;
the API route is unchanged by that, which is the point of the page being a shell.

**The page computes nothing.** It fetches `/api/v1/stats/summary` and renders the response, so
there is one computation and two renderings (spec §3.4). The reader-inventory row is
`{Match: "/api/v1/stats/summary", Reader: "stats.js", Anchor: "/api/v1/stats/summary", Routes: 1}`.

**Verify:**
```sh
curl -s 127.0.0.1:8006/api/v1/stats/summary | python3 -c \
 'import sys,json; c=json.load(sys.stdin); print(c["features_by_status"][:2])'
# [{"value":"draft","total":981,"seed":981,"other":0}, ...]  <-- split present, sums to total
```

---

## STAT-2 — Decision latency and exposure

**SHIPPED 2026-11-06.** `GET /api/v1/stats`, the three latency cards on `/stats`, the
`/badge/{slug}.svg` + `/badge/instance.svg` badges, and the `?kind=stats` feed item.
`make verify` green at 1167 tests. Verified live against a throwaway instance **and in a
browser**, where the rendered cards were compared field-by-field against the API payload:
all three medians and all three count lines matched.

### Measured corrections to this plan, 2026-11-06

| # | the plan said | the code says |
|---|---|---|
| 5 | `Latency{MedianHours, N, Unfinished, Unit}` | a fourth count is needed. A row that is **finished but was never measured** (a seeded feature inserted directly as `shipped`, with no `ready` audit row) is neither `N` nor `Unfinished`; folding it into `Unfinished` reports a shipped feature as still in progress. Shipped as `Unmeasured`. |
| 6 | `health (— when NULL)`, read from the project | the stored `project_metrics.health_score` is **0 for all 70 live projects** — a placeholder, not a measurement. Reading the column renders "0.00" for every project, the exact lie §4.13 refuses. `DB.ProjectHealth` (scout.go:165) already computes through `discovery.HealthScore` and returns `known=false`; the badge uses it. |
| 7 | `Triage(complaints, validations)`, `Delivery(features, transitions)`, `Decision(calls, closes)` | the plan's signatures force each caller to re-derive which rows are in the sample. Shipped as one pure `discovery.Measure([]Episode)` plus three store-level episode builders, so the sample rules are testable without a database. |
| 8 | `Delivery`: "feature entering `ready` -> entering `shipped`" | the **target status is inside the audit `detail` string**, not a column: trust.go writes `"<from> -> <to>: <reason>"`. A `LIKE '%-> shipped%'` matches a reason that happens to contain that text, so it is parsed in Go. |
| 9 | `Decision`: "call created -> closed" | an **open** call is inserted with `closes_at = now()` — the same instant as `opens_at` — because the column is `NOT NULL` (consensus.go:141). Computing `closes_at - opens_at` over every call measures each open call as 0.0h: the zero-day lie. The filter is `status = 'closed'`. The schema allows only `('open','closed')`, so there is no `superseded` status. |
| 10 | a feature still in `draft` is unfinished | it is **not in the population at all**. `CreateFeature` writes no audit row and the metric measures the journey from `ready`; a draft feature has no journey. Counting one as unfinished reports every newly filed feature as a delivery in progress. |

### STAT-2.1 `internal/discovery/latency.go` (pure, table-tested)

```go
// Latency is a duration distribution, never a mean: one row that sat for 400 days is exactly
// the row a reader wants to know about, and it is the row a mean deletes.
type Latency struct {
	MedianHours float64 `json:"median_hours"`
	N           int     `json:"n"`
	Unfinished  int     `json:"unfinished"`
	Unit        string  `json:"unit"` // always "hours"; the page converts for display
}

// Triage: complaint created -> first validate_complaint audit row.
// Delivery: feature entering 'ready' -> entering 'shipped' (audit set_feature_status pairs).
// Decision: consensus call created -> closed.
func Triage(complaints []Completion, validations []Transition) Latency
func Delivery(features []Completion, transitions []Transition) Latency
func Decision(calls []Completion, closes []Transition) Latency
```

Median of an even sample is the **lower-middle** value, not an interpolation: an interpolated
median invents a duration nobody experienced, and this number will be quoted.

Rows with no recorded start contribute to `Unfinished`, never to `N`. That single rule is what
keeps a seeded feature from appearing as a zero-day delivery (spec §3.2).

**Tests:** `internal/discovery/latency_test.go` — `TestTriagedMedian`, `TestUnstartedIsUnfinished`,
`TestEvenSampleUsesLowerMiddle`, `TestReopenedUsesCurrentEpisode`.

### STAT-2.2 `GET /api/v1/stats` — one computation, two renderings

```json
{"census":{...},
 "latency":{"triage":{...},"delivery":{...},"decision":{...}},
 "measured":{"projects":74,"with_health":0,"with_origin":0},
 "generated_at":1234567890}
```

`/stats` page (template + `assets/js/stats.js`) renders **from this endpoint**, not from a
second server-side computation: the page is a client of its own API, or the two drift and the
page is the one nobody notices is wrong.

### STAT-2.3 `GET /badge/{slug}.svg` + `GET /badge/instance.svg`

Text-only SVG, two system font families, `Content-Type: image/svg+xml`,
`Cache-Control: max-age=3600`. Three badges: `health` (— when NULL), `latency`, `shipped30d`.
A badge for an unmeasured project renders `—`, never `0.0`: the zero is a lie that
propagates into somebody's README.

### STAT-2.4 Feeds

The existing `handleProjectsFeed` gains one entry per publication:
`<item><title>Instance statistics, 2026-10-30</title><description>…</description></item>` built
from the same `GET /stats` payload. One item per day at most — a feed is not a stream of
counters.

**Verify:**
```sh
curl -s 127.0.0.1:8006/api/v1/stats | python3 -c \
 'import sys,json; d=json.load(sys.stdin); print(d["latency"]["delivery"], d["measured"])'
# {'median_hours': 216.0, 'n': 41, 'unfinished': 216, 'unit': 'hours'} {'projects': 74, 'with_health': 0, ...}
curl -s 127.0.0.1:8006/badge/concord.svg | head -3
# <svg ...> with the number rendered, or a dash — NEVER "0.0" for NULL health
```
And the page-vs-API equality test (invariant 7): a Go test that fetches both and compares the
rendered `<span data-stat="delivery_median">` against the JSON, which fails if either path
recomputes.

---

## STAT-3 — Snapshot history, rising, and the leaderboard

**SHIPPED 2026-11-06 (STAT-3.1–.3 only).** Migration `0032_instance_stats.sql`,
`internal/store/statsnapshot.go` (`ComputeSnapshot`, `WriteSnapshot`, `SnapshotHistory`),
`cmd/statsnapshot`, `GET /api/v1/stats/history`, and `deploy/concord-statsnapshot.{service,timer}`.
`make verify` green at 1174 tests.

Verified live: `-dry-run` prints the frame and writes nothing; two consecutive runs leave
**exactly one row** for the day; the history route returns a bare array with `null` medians
for an unmeasured day.

### Measured corrections to this plan, 2026-11-06

| # | the plan said | the code says |
|---|---|---|
| 11 | `no_origin INTEGER NOT NULL DEFAULT 0, -- rows that predate R10's backfill` | ambiguous about which table. `0030` backfilled **both** `features` and `complaints`, and they are one population, so the column counts both — documented in the migration. Two columns would invite a reader to compare them, and the split is a fact about tables, not rows. |
| 12 | the medians are a `REAL` column | correct, but the Go side must not use `sql.NullFloat64` for the JSON: it has **no `MarshalJSON`**, so the API published `{"Float64":0,"Valid":false}` and a reader comparing against `null` got an object. `Snapshot` carries `*float64` for the JSON boundary and `sql.NullFloat64` for the database scan. Found by `TestStatsHistoryServesRecordedDays`. |
| 13 | STAT-3.4 (rising) and STAT-3.5 (leaderboard) are in this milestone | **STAT-3.5 shipped 2026-11-06**, once R12 gave it a metric to rank; **STAT-3.4 shipped 2026-11-06** on `0033_project_stats.sql`. This plan statement is why STAT-3.4 came last: "rank projects by the slope of … between two snapshots" cannot be derived from `instance_stats`, which records **one instance-wide row per day** and has no project column. Implementing it honestly needed exactly that table, which `0033` now provides: `project_stats(day, project_id, organic_features_shipped, organic_complaints_filed)`, cumulative and organic, written by `cmd/statsnapshot` in the same pass as the instance row. I bound the privacy question the plan never asked: a leaderboard names projects, so it also filters `visibility='public'` — every other statistics route publishes aggregates a private project contributes to invisibly. |

### STAT-3.1 Migration `0032_instance_stats.sql`

```sql
-- One row per day. History has to be RECORDED: re-deriving yesterday from audit_log gives a
-- different answer than re-deriving today, because the log was never complete (925 of 1,874
-- seeded features have no create row). A migration that rebuilds a table would rewrite any
-- series derived from the tables themselves.
CREATE TABLE instance_stats (
    day              TEXT PRIMARY KEY,          -- 'YYYY-MM-DD', UTC
    projects         INTEGER NOT NULL,
    projects_measured INTEGER NOT NULL,         -- with_health: a count of MEASUREMENTS
    features_total   INTEGER NOT NULL,
    features_seed    INTEGER NOT NULL,
    complaints_total INTEGER NOT NULL,
    complaints_seed  INTEGER NOT NULL,
    triage_median_h  REAL,
    triage_n         INTEGER NOT NULL DEFAULT 0,
    delivery_median_h REAL,
    delivery_n       INTEGER NOT NULL DEFAULT 0,
    decision_median_h REAL,
    decision_n       INTEGER NOT NULL DEFAULT 0,
    no_origin        INTEGER NOT NULL DEFAULT 0, -- rows that predate R10's backfill
    computed_at      REAL NOT NULL
);
```

### STAT-3.2 `cmd/statsnapshot` + daily timer

Same shape as `cmd/healthsync`: one pass, `-dry-run`, `-day YYYY-MM-DD` (default today UTC),
exit non-zero on failure. `INSERT … ON CONFLICT(day) DO UPDATE` so a second run on the same
day corrects the row instead of adding one.

`deploy/concord-statsnapshot.service` + `.timer`, daily at 03:10 UTC with `Persistent=true`.
A gap in the series is visible as a missing day; the alternative (a goroutine) hides it.

### STAT-3.3 `GET /api/v1/stats/history?days=90`

Bare JSON array of snapshot rows, capped at 365. This is the only series route; everything
else is a frame.

### STAT-3.4 Rising (§4.5.5.7) — needs history, so it is here

Rank projects by the slope of `projects_measured`… no: by the slope of **organic** features
shipped and complaints filed between two snapshots at least 14 days apart. A project with one
snapshot has no slope and is not ranked. Health is shown beside the slope, never substituted
for it: "rising" is a change, health is a level.

**Shipped 2026-11-06**, on a table this paragraph did not anticipate: `instance_stats` has no
project column, so the slope had to come from `0033_project_stats.sql` —
`(day, project_id, organic_features_shipped, organic_complaints_filed)`, cumulative, organic,
`PRIMARY KEY (day, project_id)` so a second run of the same day corrects rather than appending
a point at the same x. `cmd/statsnapshot` writes the day's project rows in the same pass as the
instance row.

`GET /api/v1/stats/rising`, five boundings the sentence above left open:

- **the span is the project's own** (its first and last row), not the instance's, or a project
  created last week is measured against a history it never had;
- `RisingMinDays = 14` **in the query**, and a project below it is ABSENT — with a daily timer,
  two rows one day apart turn one shipped feature into "seven per week";
- **deltas stay signed**, because a feature can be reopened and a clamped `0` is a measurement
  nobody took;
- **the span is printed in every row**, since a rate over 14 days and one over 90 are different
  claims;
- health comes from **`ProjectHealth`, not the `health_score` column**, which is `0` for every
  project on the live instance — the page renders a dash for unmeasured instead of a `0%`.

**Verify:**
```sh
go build -o bin/concord ./cmd/concord && CONCORD_DB=/tmp/p.db CONCORD_LISTEN=127.0.0.1:8415 ./bin/concord &
sqlite3 /tmp/p.db "insert into project_stats (day,project_id,organic_features_shipped,organic_complaints_filed) values ('2026-10-01',1,0,0),('2026-10-22',1,12,2)"
curl -s 127.0.0.1:8415/api/v1/stats/rising   # -> [{...,"days":21,"features_per_week":4,...}]
# a project with one row is absent, and so is a private one
```

### STAT-3.5 Responsiveness leaderboard (§3.5)

`GET /api/v1/stats/responsiveness` — projects ordered by `newcomer_response_hours` ascending,
filtered to `newcomer_sample >= 5`, each row carrying `n`. A project under five samples is
absent, not last.

**Shipped 2026-11-06.** `store.ResponsivenessLeaderboard` + `handleStatsResponsiveness`, and a
card on `/stats` that renders the API's order and never re-sorts. Three boundings the sentence
above left open:

- a **slug tiebreak** after the median, or two projects at the same number swap places between
  requests and the ranking cannot be compared with its own previous rendering;
- `visibility = 'public'`, because this is the one statistics surface that publishes NAMED
  projects rather than an aggregate;
- the floor is enforced in the QUERY as well as in `UpdateProjectNewcomer`, because a row can
  reach this table without the writer (`TestResponsivenessLeaderboardOmitsProjectsUnderTheFloor`
  seeds one that way — the mutant that drops the clause survives every other test).

**Verify:**
```sh
go run ./cmd/statsnapshot -dry-run
# day=2026-10-30 projects=74 measured=0 features=1874 seed=925 triage=n/a delivery=n/a decision=n/a
go run ./cmd/statsnapshot && go run ./cmd/statsnapshot
# both runs print the same day and leave ONE row:
sqlite3 ~/.local/share/concord/concord.db "select count(*) from instance_stats where day='2026-10-30'"
# 1
```

---

## Order, and the check at each step

1. **STAT-1 + STAT-2 in one commit** (the reader-coverage gate forbids a route without a page,
   and the latency numbers are what the page is for). `make verify`, then the two curls above,
   then a browser read of `/stats` asserting the rendered numbers equal the JSON.
2. **STAT-3.** `make verify`, the double-run snapshot check, and `GET /stats/history` returning
   exactly one row for today.
3. Rising and the leaderboard last, in the same commit as the history route: both are series
   reads over data that does not exist until the timer has run twice, so their tests seed
   snapshots directly rather than waiting two days.

**What must not happen:** a second place that computes the same aggregate; a total published
without its split; a median without `n`; a badge that renders `0.0` for unmeasured health; a
snapshot row that a second run duplicates; or a page that recomputes what the API already
returned.
