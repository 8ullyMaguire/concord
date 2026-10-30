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

### STAT-1.1 `internal/store/stats.go` (new)

```go
// Census is the composition of the instance: what exists, not what happened. Every slice is
// a still frame, which is the only kind of aggregate a seeded instance can publish without
// lying: a batch import changes a total, and it changes a "per week" series.
type Census struct {
	Projects      []FacetCount `json:"projects_by_language"`
	Tags          []FacetCount `json:"projects_by_tag"`
	Licenses      []FacetCount `json:"projects_by_license"`
	Governance    []FacetCount `json:"projects_by_governance"`
	FeatureStatus []StatusCount `json:"features_by_status"`
	Complaints    []StatusCount `json:"complaints_by_status"`
	Members       []FacetCount `json:"members_by_role"`
	Documents     []FacetCount `json:"documents_by_kind"`
}

// FacetCount carries the split on the same struct, not as a second query: a caller that
// has to remember to fetch the split is a caller that will print a total without one.
type FacetCount struct {
	Value string `json:"value"`
	Total int    `json:"total"`
	Seed  int    `json:"seed"`   // origin IN ('seed','import')
	Other int    `json:"other"`  // everything else; Total = Seed + Other always
}

func (d *DB) Census(ctx context.Context) (Census, error)
```

`Census` reuses the facet SQL that already exists in `internal/store/search.go`
(tag/language/license/governance top-25) and extends each with
`SUM(CASE WHEN origin IN ('seed','import') THEN 1 ELSE 0 END)`. Top-25 stays 25: a facet list
that grows unbounded is a second bug, and the existing queries already cap.

**The invariant is arithmetic:** `Total = Seed + Other` for every row, asserted in a store
test over a fixture with one organic and one seeded row of each kind. A row where the two
disagree is a `SUM` over a `LEFT JOIN` that multiplied — the classic facet-count defect, and
it shows as a total larger than the table.

### STAT-1.2 `internal/httpapi/stats.go` + route

```
GET /api/v1/stats/summary        # Census, public, no auth (reads are exempt from consent)
```

Registered inside the existing `/api/v1` group. No page yet — STAT-2 renders it, and a route
without a reader fails the reader-coverage gate, so **STAT-1 and STAT-2 must land in the same
commit or STAT-1 ships behind a page that already exists**. Recommended: land both together.

**Verify:**
```sh
curl -s 127.0.0.1:8006/api/v1/stats/summary | python3 -c \
 'import sys,json; c=json.load(sys.stdin); print(c["features_by_status"][:2])'
# [{"value":"draft","total":981,"seed":981,"other":0}, ...]  <-- split present, sums to total
```

---

## STAT-2 — Decision latency and exposure

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

### STAT-3.5 Responsiveness leaderboard (§3.5)

`GET /api/v1/stats/responsiveness` — projects ordered by `newcomer_response_hours` ascending,
filtered to `newcomer_sample >= 5`, each row carrying `n`. A project under five samples is
absent, not last.

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
