# Contribution feed — implementation plan

**Spec:** `docs/specs/contribution-feed-spec.md`. **Plan written:** 2026-10-30.
**Milestones:** R9–R12 in `docs/PLAN-r4.md`.

Four tickets, in this order, because each one is the input to the next:

| ticket | delivers | blocked by |
|---|---|---|
| CF-1 | repository identity + health ingest | the 74-row `seed/forge_repos.json` |
| CF-2 | work-item contract (labels, time budget, claim) | nothing |
| CF-3 | the feed surface (Finder variant) | CF-1 + CF-2 |
| CF-4 | newcomer responsiveness from forge events | CF-1 |

Rules that apply to every ticket: `make verify` green before commit; a new store method
with no HTTP caller is not shipped (§27's export inventory gate fails it); every mutation
of the 3-file contract below is accompanied by a test that fails without it — proved by
disabling the line, not by reading it.

---

## CF-1 — Repository identity and health ingest

### CF-1.1 Migration `internal/db/migrations/0030_repo_identity.sql`

```sql
-- Concord §4.5.6.1: the catalog never knew where a project lives, which is why no
-- surface could offer to contribute to one. repo_url is stated, never derived: a slug
-- guessed into a URL sends a stranger to somebody else's repository.

ALTER TABLE projects ADD COLUMN repo_url  TEXT;
ALTER TABLE projects ADD COLUMN repo_host TEXT
    CHECK (repo_host IS NULL OR repo_host IN ('forgejo','github','codeberg'));

ALTER TABLE project_metrics ADD COLUMN newcomer_response_hours REAL;
ALTER TABLE project_metrics ADD COLUMN newcomer_sample         INTEGER NOT NULL DEFAULT 0;

CREATE TABLE forge_events (
    id          INTEGER PRIMARY KEY,
    repo_full   TEXT NOT NULL,
    kind        TEXT NOT NULL
                CHECK (kind IN ('push','pr_opened','pr_closed','pr_review','pr_comment')),
    number      INTEGER,
    actor       TEXT NOT NULL DEFAULT '',
    at          REAL NOT NULL,
    received_at REAL NOT NULL,
    UNIQUE (repo_full, kind, number, actor, at)
);
CREATE INDEX forge_events_repo ON forge_events (repo_full, at);
```

`ALTER TABLE … ADD COLUMN` is the established shape here (`0009_project_visibility.sql`);
the `concord:preserve-columns-split` marker in `0008_project_delete_cascade.sql` means a
replay of that rebuild keeps columns a later migration added.

**Verify:** `go test ./internal/db -run TestMigrate -count=1` → `ok`; and
`sqlite3 ":memory:" ".read internal/db/migrations/0030_repo_identity.sql"` errors only on
the missing tables it references (none — this file has no FK), so do the real check on a
copy of the live DB:
`sqlite3 /tmp/c.db ".read internal/db/migrations/0030_repo_identity.sql" && sqlite3 /tmp/c.db "pragma table_info(projects)" | grep repo_url` → one row containing `repo_url`.

### CF-1.2 `internal/store/projects.go` (new file): `SetProjectRepo`, `ListProjectsWithRepo`

```go
// SetProjectRepo records where a project lives. repoHost is one of the three known
// forges; an unknown host is refused rather than stored, because the ingest switches on
// it and a typo would silently exclude the project from every fetch.
func (d *DB) SetProjectRepo(ctx context.Context, slug, repoURL, repoHost string) (Project, error) {
	fields, operr := parseRepoURL(repoURL)          // internal/store/repo_url.go, pure, table-tested
	if operr != nil {
		return Project{}, fmt.Errorf("%w: %s", ErrInvalid, operr)
	}
	if repoHost != fields.Host {
		return Project{}, fmt.Errorf("%w: host %q disagrees with URL %q", ErrInvalid, repoHost, repoURL)
	}
	id, err := d.projectIDBySlug(ctx, slug)
	if err != nil {
		return Project{}, err
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE projects SET repo_url=?, repo_host=?, updated_at=? WHERE id=?`,
		fields.URL, fields.Host, float64(time.Now().Unix()), id); err != nil {
		return Project{}, fmt.Errorf("set repo: %w", err)
	}
	return d.GetProject(ctx, slug)
}

// ListProjectsWithRepo returns every project with a repository, for the ingest. It is not
// visibility-filtered: an unlisted project still gets its metrics synced, and filtering
// here would leave it permanently unmeasured.
func (d *DB) ListProjectsWithRepo(ctx context.Context) ([]ProjectForge, error)
```

`ProjectForge` is `{ID, Slug, RepoURL, RepoHost}`. Add `GetProjectRepo` if a reader needs
one; do not add a store method with no caller (§27).

**Tests** — `internal/store/projects_repo_test.go`:
- `TestSetProjectRepoRefusesMismatchedHost` — `github.com` URL with `repoHost=forgejo` → `ErrInvalid`.
- `TestSetProjectRepoRefusesNonRepoURL` — `https://example.com/x` → `ErrInvalid`; the message names the parsed host.
- `TestListProjectsWithRepoSkipsNull` — two projects, one with a repo → one row.

### CF-1.3 `seed/forge_repos.json` + `cmd/forgeseed`

```json
[
  {"slug": "ao3downloader", "repo_url": "https://github.com/Armored-Dragon/AO3Downloader", "repo_host": "github"}
]
```

`cmd/forgeseed` refuses the file unless every slug resolves (exit 1, naming the misses)
and every project with a `null` repo is listed in an explicit `"skip": true` entry — so a
new project added later shows up as a refusal rather than silently missing from the ingest.
It prints the count it applied; it does not print URLs.

**Verify:** `go run ./cmd/forgeseed -file seed/forge_repos.json -dry-run` → a line per
project; then without `-dry-run` → `applied N of N`; re-run → `applied 0 of N (already set)`
and exit 0 (idempotent by construction: `SetProjectRepo` with the same values writes the
same row).

### CF-1.4 `cmd/healthsync/main.go`

One pass over `ListProjectsWithRepo`, per host a `fetchMetrics(ctx, repo) (discovery.Metrics, forgeFacts, error)`:

- **github**: `GET /repos/{owner}/{repo}` (stars, forks, open issues, license, pushed_at),
  `GET /repos/{owner}/{repo}/contributors?per_page=100&anon=false`,
  `GET /repos/{owner}/{repo}/releases?per_page=100` (count where `published_at` within 90d),
  `GET /repos/{owner}/{repo}/pulls?state=all&per_page=50&sort=updated` for review latency.
  Unauthenticated is 60 req/h — one pass over 74 repos is under that; a token
  (`$CONCORD_FORGE_TOKEN`) lifts it and is required for Codeberg/Forgejo.
- **forgejo/codeberg**: the same shapes under `/api/v1/repos/{owner}/{repo}`.

Then:

```go
m := discovery.Metrics{
	LastCommitAgeDays: now.Sub(pushedAt).Hours() / 24,
	MedianReviewHours: medianReviewHours(prs, timeOfFirstReview),
	Contributors:      distinctAuthors365d(contributors),
	Releases90d:       releases90d,
}
if _, err := store.UpdateProjectMetrics(ctx, slug, m, stars, forks, openIssues, commits, license); err != nil {
```

`UpdateProjectMetrics` is used **as is** — the health weights, the `last_commit_at`
derivation and the `computed_at` stamp live in one place and this ticket must not add a
second. It already leaves `license` alone when the argument is empty.

Flags: `-dry-run` (print the `discovery.Metrics` and the URL it came from, write nothing),
`-only slug[,slug]`, `-rate 1s`. Exit non-zero if any project failed, printing
`FAILED <slug>: <error>` per line; a failure must not write zeros (§4.5.6.1's rule and the
reason `UpdateProjectMetrics` is only called on a complete fetch).

`deploy/concord-healthsync.service` + `.timer` (hourly, `Persistent=true`), installed
beside `deploy/concord.service`. Not a goroutine: the server restarts on every deploy.

**Verify:**
```sh
go run ./cmd/healthsync -dry-run -only ao3downloader
# ao3downloader github ... last_commit_age=0.4d contributors=7 releases90d=1 review_h=0
sqlite3 ~/.local/share/concord/concord.db \
  "select p.slug, m.health_score, m.last_commit_at, m.contributors from project_metrics m join projects p on p.id=m.project_id where p.slug='ao3downloader'"
# ao3downloader|0.71|<epoch>|7      <-- non-null, and != 0.0 for a live repo
```
And the two broken surfaces, which is the point:
```sh
curl -s '127.0.0.1:8006/api/v1/search?q=&sort=health&limit=3' | python3 -c 'import sys,json;print([h["slug"] for h in json.load(sys.stdin)])'
# must differ from the same query with sort=updated once >1 project has health
curl -s '127.0.0.1:8006/api/v1/search?q=&min_health=0.5' | python3 -c 'import sys,json;print(len(json.load(sys.stdin)))'
# non-zero. Today this is 0 for every value > 0 — that is the bug being fixed.
```

### CF-1.5 Gate: `internal/httpapi/health_sort_test.go`

Pins invariant 2 of the spec: seed two projects, one measured and one not, and assert
`min_health=0.5` returns exactly the measured one — **and assert that it fails against
`COALESCE(m.health_score,0) >= ?`** by running the same query with the old predicate in the
test file (a comment-documented control, the way `arena_verification` proved its own gap).
A test that passes on unfixed code is not evidence.

---

## CF-2 — The work-item contract

### CF-2.1 Migration `0031_work_items.sql`

```sql
ALTER TABLE features ADD COLUMN difficulty          TEXT
    CHECK (difficulty IS NULL OR difficulty IN ('S','M','L'));
ALTER TABLE features ADD COLUMN time_budget_minutes INTEGER
    CHECK (time_budget_minutes IS NULL OR time_budget_minutes > 0);
ALTER TABLE features ADD COLUMN needs_mentor        INTEGER NOT NULL DEFAULT 0
    CHECK (needs_mentor IN (0,1));
ALTER TABLE features ADD COLUMN origin              TEXT NOT NULL DEFAULT 'api'
    CHECK (origin IN ('api','seed','import','ui'));
ALTER TABLE complaints ADD COLUMN origin            TEXT NOT NULL DEFAULT 'api'
    CHECK (origin IN ('api','seed','import','ui'));

-- One live claim per feature, enforced here rather than in Go: two implementers on one
-- feature is the harm a contribution feed creates, and a handler check loses the race.
CREATE TABLE work_item_claims (
    feature_id  INTEGER NOT NULL REFERENCES features(id) ON DELETE CASCADE,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    claimed_at  REAL NOT NULL,
    released_at REAL,
    PRIMARY KEY (feature_id, user_id)
);
CREATE UNIQUE INDEX work_item_claims_active
    ON work_item_claims (feature_id) WHERE released_at IS NULL;
```

Backfill: `UPDATE features SET origin='seed' WHERE project_id IN (...)` is **not** written
here — the mapping is `seed/`'s business, and guessing which 1,306 rows were seeded from
their `created_at` batch would encode an inference as a fact. CF-2.4 sets origin in the
seed scripts instead, and `cmd/originbackfill` exists for rows already in the live DB,
taking an explicit file of `slug<TAB>created_before` — refusing to run when the file's
project list does not cover the live catalog.

### CF-2.2 `internal/store/workitems.go`

```go
type WorkItemLabel struct {
	Difficulty        *string `json:"difficulty"`
	TimeBudgetMinutes *int    `json:"time_budget_minutes"`
	NeedsMentor       bool    `json:"needs_mentor"`
}

// SetWorkItemLabel records what a task costs a newcomer. Maintainer-only at the HTTP
// layer: labelling something "good first" is a promise about review capacity.
func (d *DB) SetWorkItemLabel(ctx context.Context, projectID, featureID int64, l WorkItemLabel) error

// ClaimFeature takes the single active claim. It returns ErrDuplicate when another
// claim is live, so the HTTP layer maps it to 409 without a second query — the unique
// partial index is the authority, not a SELECT-then-INSERT race.
func (d *DB) ClaimFeature(ctx context.Context, projectID, featureID, userID int64) error

func (d *DB) ReleaseClaim(ctx context.Context, projectID, featureID, userID int64) error

// ListWorkItems returns a project's open work: complaints in 'open', features in
// 'ready'/'in_progress' with no active claim, and solutions awaiting an implementer.
func (d *DB) ListWorkItems(ctx context.Context, projectID int64) ([]WorkItem, error)
```

`ListWorkItems` must scope by `project_id` in SQL, not by filtering in Go, and must not
return a feature whose project the caller cannot read — visibility is resolved by the
handler through `s.projectReadable`, the same rule as every other page route.

**Tests** — `internal/store/workitems_test.go`:
- `TestWorkItemClaimIsExclusive` — second claim → `ErrDuplicate`; after release, a claim succeeds and two rows exist with one `released_at IS NULL`.
- `TestClaimRefusesNonReadyFeature` — `draft` → `ErrInvalid`.
- `TestListWorkItemsExcludesClaimedAndDraft`.

### CF-2.3 HTTP: `internal/httpapi/workitems.go`, routes in `server.go`

```
GET    /projects/{project_id}/work-items
PUT    /features/{feature_id}/work-item        # requireRole(projectID,"maintainer")
POST   /features/{feature_id}/claim            # requireRole(projectID,"contributor")
DELETE /features/{feature_id}/claim
```

`requireWriteActor` + consent gate apply as everywhere else. `ErrDuplicate → 409` is
already in `mapError`. Every handler resolves the project from the feature id
(`requireFeatureInProject`, the same shape as `requireCallInProject`) — a route that loads
by id alone leaks a private project's rows, which is the single most common find in this
codebase.

### CF-2.4 Seed scripts set `origin`

`seed/build_new_projects.py`, `seed/concord_import_100_ideas.py`,
`seed/concord_import_portfolio.py` write `origin='seed'` explicitly, and every
`POST /features` / `POST /complaints` handler writes `origin='api'`. `origin='ui'` is for
rows created through the site's own forms (a browser session), so a later "was this a
person?" question has one column to read.

**Verify:** `python3 seed/concord_import_100_ideas.py --dry-run` prints `origin=seed` per
row; then `sqlite3 … "select origin, count(*) from features group by 1"` shows at least two
non-zero buckets on a re-seeded copy.

---

## CF-3 — The feed

### CF-3.1 `internal/finder/finder.go`: two `classify` cases

```go
	case "minutes":                 // the smallest time budget labelled anywhere in the project
		return FamilyMaturity, "How much time do you have?"
	case "mentor":
		return FamilyGovernance, "Is a mentor offered?"
```

`default` files unknown keys under `FamilyCapability`, so without this the question catalog
labels a time budget as a capability — a label bug no test would catch unless it names the
label. **Test:** `TestClassifyTimeBudgetIsMaturity` in `finder_test.go` asserting both the
family and the label; it must fail when the case is removed.

### CF-3.2 `internal/httpapi/contribute.go`

```go
// contributeCandidates is finderCandidates plus the two contribution attrs. It does not
// build a second candidate set: the Finder's set already carries capabilities, language,
// governance, license, field-report rate and arena standing, and a second loader is how
// the two surfaces start disagreeing about which projects exist.
func (s *Server) contributeCandidates(r *http.Request) ([]finder.Candidate, error)
```

`minutes` = the minimum non-null `time_budget_minutes` over the project's open work items,
rendered as `"15"|"60"|"480"|"2400"` (nearest bucket ≥ the value; a 90-minute task is
offered to a 60-minute ceiling only as `60`+label, never silently). `mentor` = `"yes"` if
any open work item has `needs_mentor`.

Then the unchanged engine: `finder.NextQuestion(state)`, `finder.Apply`,
`finder.Score`, `finder.Gaps`. Answers come from the same `finder.Answer` shape the
existing `/finder/sessions/{id}/answers` route already parses, so no new session store.

### CF-3.3 Ranking of work items inside a surviving project

```go
// rankWorkItems keeps first-time contributors away from contested ground: an unclaimed
// item outranks a claimed one, then the cheapest fits the offered budget, then impact.
```

Order: claim-free, `time_budget_minutes` ≤ offered, ascending; then `impact_ratio` desc;
then `feature_id`. Deterministic tie-break by id so two identical requests return an
identical order — a feed that reshuffles on refresh cannot be checked by a test or trusted
by a reader.

### CF-3.4 The page

`internal/httpapi/assets/js/contribute.js` + a `/contribute` template, reusing the
`/finder` session machinery (`finder.js` already implements session start, back, lift and
the results panel). The results panel names the **work item** and its project; the project
appears as context, not as the answer.

**Verify (live, not by body size):**
```sh
curl -s '127.0.0.1:8006/api/v1/contribute/next?minutes=60&language=python' | python3 -m json.tool | head
# {"items":[{"feature_id":…,"title":…,"time_budget_minutes":60,"why":["labelled 60m","project health 0.71 "]…}],"gaps":[…]}
```
and in a browser: `/contribute` → answer the language question → the panel shows at least
one work item with a time budget, and the "why" lines are the ones the API returned. A
blank page here is the SPA-shell failure mode; assert on `document.body.innerText`.

`reader_coverage_test.go` requires every new route to be named in the reader inventory with
a reader in `assets/js` — CF-3.4 is what satisfies it for CF-2.3's routes too.

---

## CF-4 — Newcomer responsiveness

### CF-4.1 Record the events the webhook already receives

`internal/httpapi/webhook.go` gains `pull_request_review` and `pull_request_comment` arms
and, in all four arms, an insert into `forge_events` (through
`store.RecordForgeEvent(ctx, ev)`, `INSERT … ON CONFLICT DO NOTHING` so a redelivery is
free). The audit row stays: the audit log answers "what did the instance do", the event
table answers "what happened in the repository", and collapsing them loses the ability to
recompute after a rule change.

### CF-4.2 Derive the median

```go
// newcomerResponse accepts only PRs whose author has no earlier PR in the same repo, and
// only pairs where the response came from someone else. Self-comment is not a response;
// counting it would publish a responsiveness number that no contributor experienced.
func newcomerResponse(events []store.ForgeEvent) (hours float64, n int, ok bool)
```

Pure function, in `internal/discovery/responsiveness.go` next to `health.go` — same package
because it is the same kind of transparent, fact-only score, and tested with the same
table style. `ok == false` under 5 samples; `cmd/healthsync` writes `newcomer_sample` and
leaves `newcomer_response_hours` NULL when `!ok`.

**Tests** — `internal/discovery/responsiveness_test.go`: first-time author only; a
self-comment is ignored; a second PR by the same author is excluded; median of an even
sample uses the lower-middle (documented, not interpolated — a median that interpolates
invents a response time nobody had); `< 5` samples → `ok=false`.

### CF-4.3 Display

Project page Health panel: *"first response to a newcomer: 6h median (n=41)"*, with `n`
always visible and the whole line hidden when `newcomer_response_hours IS NULL`. Not folded
into `health_score`.

---

## Order of work, and the check at each step

```sh
make verify          # vet, fmt-check, test, build, docs-check, e2e-sync-check — must exit 0
```

CF-1 → `make verify` + the two metadata curls above + one real project with a non-null
health score.
CF-2 → `make verify` + claim exclusivity proved by two concurrent curls (`409` on the
second).
CF-3 → `make verify` + `make e2e` (the contribute suite has its own port constant) + a
browser read of the panel text.
CF-4 → `make verify` + a replayed webhook payload (`curl -X POST` with a valid HMAC against
`/api/v1/hooks/forgejo`) landing a row in `forge_events`, then a recompute showing a
non-null `newcomer_response_hours` on a repo with ≥ 5 first-time PRs.

**What must not happen at any step:** a second health formula, a second candidate loader, a
store method with no caller, a route without a reader, or a statistic that counts seeded
rows as if a person filed them.
