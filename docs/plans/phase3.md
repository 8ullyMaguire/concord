# Concord — plan for the remaining work (Phase 3 close-out)

Implements `docs/specs/phase3-spec.md`. Written 2026-10-03 against commit
`40c97e3`. Read that spec first; this file is the order and the verification.

Every symbol below was read out of the tree on the day this was written. A
symbol marked **(new)** does not exist and is introduced by the step naming it.

## Ground rules

1. `make verify` green before every commit. Commit small: one step per commit.
2. `make gates` must stay green. It rewrites source files per mutant, so never
   run `go test`, `make verify` or a build while it is running, and never stage
   a file while it runs — see `mutation-gate-discipline`.
3. `make e2e` must stay green for anything user-visible. It is four pytest
   processes and takes ~110s; it is not optional for S1 and S2. A NEW browser
   suite gets its own process, its own `pytest.ini` entry and a named entry in
   `test_every_suite_is_collected_by_a_directory_run` — all three, or it is
   silently skipped.
4. New domain ⇒ new test file. Every `internal/store/*.go` domain has been
   paired with a test since arenas landed; keep it that way.
5. MaxOpenConns(1) (PLAN.md rule 6): never query while a `*sql.Rows` cursor is
   open. Collect, close, then query.
6. 100 req/min per-visitor rate limit is global in the e2e suites; each browser
   context gets its own `X-Forwarded-For`. This is not optional and the plan's
   claim that it was "already" true was only true of `finder_e2e.py`: a new
   browser suite that omits it fails mid-run with `{"error":"rate limit
   exceeded"}` rendered as the page body, which reads as a broken panel.
7. If reality differs from this plan, fix the plan in the same commit as the
   code and say in the message what the plan got wrong.

## Baseline, measured 2026-10-03

```
make verify            # green: vet + 579 tests + build
make gates             # 4 gates, 59/59 mutants killed, 0 survived
make e2e               # 8 harness + 35 site + 38 finder = 81 passed
schema version 22      # 78 tables on the live instance
```

Measured again after S1.1 and S1.2 (commits `fd2ceae`, `633cdcc`):

```
make verify            # green
make gates             # field_reports.go gate now 16/16, 0 survived
make e2e               # 8 harness + 35 site + 38 finder + 12 panels = 93 passed
```

Verify before starting:

```bash
cd ~/code/projects/concord
make verify && echo VERIFY_OK
```

---

# S1 — Project page surfaces

Three read-only panels on `/projects/{slug}`. The spec's §4.

**Status 2026-10-03: S1.1 and S1.2 are DONE** (`fd2ceae`, `633cdcc`). S1.3 (the
Solutions panel) is not started. Notes on what the plan got wrong are in each
commit message and repeated at the step, per ground rule 7.

## Step S1.1 — Read the three data paths and fix what is missing  [DONE]

Three API surfaces must already answer "what does this panel show?" or S1 is
building a view over a question nobody can ask. Check each, and close the gap
if there is one:

```bash
# What already answers each panel's question, and what does not.
grep -rn 'features/{id}/solutions' internal/httpapi/server.go        # solutions: registered
grep -rn 'capabilities' internal/httpapi/server.go                    # nothing
grep -rn 'field-reports\|fieldreports\|field_reports' internal/httpapi/server.go  # nothing
```

Known state: **solutions exist** — `GET
/api/v1/projects/{project_id}/features/{id}/solutions` is registered and
`ListSolutions` returns the ranked payload. **Capabilities and field reports
have no HTTP surface at all**: `internal/store/capabilities.go` and
`internal/store/field_reports.go` exist with full AC test coverage, and no
handler reads them. The Finder reaches capabilities through its own server-side
store call, not an API.

So S1.1 is: add two read-only handlers plus routes.

**What S1.1 actually found, beyond the two handlers.** The plan said "add two
read-only handlers plus routes" and that was true but incomplete:

1. `requireProjectID` answered `{"error": "not found: project \"slug\""}` for an
   absent project and bare `{"error": "not found"}` for an unreadable one. Same
   status code, different body — an existence oracle in exactly the way the 403
   it replaced would have been, and invisible to a status-code assertion. 14
   files route through that helper, so all of them leaked. Fixed in the helper,
   not in `mapError`, whose per-sentinel detail is worth keeping elsewhere.
2. Registering the routes inside the existing `/{slug}` route block is WRONG. chi
   binds that parameter as `slug`, `requireProjectID` reads `project_id`, gets
   `""`, and every call 404s with `project ""` — an error that points at the data
   rather than at the route. They must be top-level `/api/v1/projects/
   {project_id}/...`, like `criteria`. This cost about an hour and is why
   `TestANonSlugProjectPathIsANotFoundRatherThanAnEmptyProject` exists.

**Files:** `internal/httpapi/field_reports.go` **(new)**,
`internal/httpapi/capabilities.go` **(new)**,
`internal/httpapi/server.go` (edit).

Routes (numeric `project_id`, per the rule that slug routes fail
`strconv.ParseInt`):

```
GET /api/v1/projects/{project_id}/capabilities
GET /api/v1/projects/{project_id}/field-reports
```

Handler shape. Both use the existing `requireProjectID(w, r)`, which resolves
`{project_id}` **by slug** to a numeric id and enforces visibility — it is what
21 handlers already funnel through, and it returns `ErrNotFound` (never 403) on
a visibility refusal, which is the anti-enumeration rule Finder also uses. Do
not `strconv.ParseInt` this parameter: `auth.go:87` records twenty-one handlers
that did, parsed a slug to 0, and then queried project 0, and the tests missed
it because "no rows for project 0" is still a 200.

```go
// (new) handleListProjectCapabilities
// GET /api/v1/projects/{project_id}/capabilities
// 200 {"project_id":N,"capabilities":[...],"unknown_count":M}
// 404 when the project is missing OR not readable -- same body both ways.
```

`unknown_count` is the panel's headline: how many of the catalog's capabilities
nobody has said anything about *for this project*. It is the contribution queue,
and it is why the panel exists.

Field reports:

```go
// (new) handleListProjectFieldReports
// 200 {"project_id":N,"outcome_rate":0.83,"sample_size":11,
//      "by_environment":{"arm64":0.5},"reports":[...],"omitted":M,"shown":10}
```

`outcome_rate` is `nil` when `sample_size == 0`. Never `0`. A rate with no
reports is not 0%, it is unmeasured — the same honesty as
`evidence_coverage` in the Finder, and the reason
`FieldReportOutcomeRate` returns a triple instead of a float.

**Verification:**

```bash
make verify && echo OK
# then, against a throwaway DB:
CONCORD_DB=/tmp/s1.db ./bin/concord -migrate-only
sqlite3 /tmp/s1.db "insert into users (username, display_name, created_at) values ('s1','S1',0);"
# seed a project + assertions + one field report, then:
./bin/concord -db /tmp/s1.db -listen 127.0.0.1:8477 &
curl -s 127.0.0.1:8477/api/v1/projects/1/capabilities | head -c 300
curl -s 127.0.0.1:8477/api/v1/projects/1/field-reports | head -c 300
# expect real JSON with the seeded values, NOT {"error":"not found"}
```

The last two curls are the check `make deploy`'s healthz cannot make: healthz is
one route and these are two others, and a deploy that reports success while the
new route answers `not found` has proved nothing.

## Step S1.2 — Three page tests, and the private-project test  [DONE]

**Files:** `internal/httpapi/project_surfaces_test.go` **(new)**,
`internal/httpapi/project_panels_script_test.go` **(new)**

Use `newTestServerWithStore(t)` (`httpapi_test.go:114`) and seed through the
store, not through HTTP, so a test does not depend on the handler it is
testing.

| test | asserts |
|---|---|
| `TestTheCapabilityPanelShowsAConfirmedClaimAsConfirmed` | a `confirmed` assertion renders `confirmed`, and the value is the asserted one |
| `TestADisputedClaimIsShownAsDisputedNotAsNo` | the disputed value is rendered as `disputed`; the string `no` for that capability is **not** what the panel claims. The finding being guarded: the store deliberately never rewrites a dispute to `no` (`ConfirmCapability` returns `state='disputed'` with the original value), so a panel that maps `state != 'confirmed'` onto a boolean loses exactly the distinction the matrix was built to keep. |
| `TestAFieldReportRateIsNeverShownWithoutItsSampleSize` | the rate and `sample_size` appear together; a project with zero reports renders no rate at all |
| `TestAnAlternativesPanelWithNoArenaRendersAnEmptyState` | before S2 there is no arena; the panel must say so, not render nothing |
| `TestAPrivateProjectDoesNotRenderAnyOfTheThreePanels` | set visibility private, fetch the page as another user, assert none of the seeded values appear in the body |

That last one is the anti-enumeration test and the reason S1's rules put
visibility first.

**What S1.2 actually built, and where the plan's table was wrong.**

The plan's fifth row names an alternatives panel, which belongs to S2 and could
not be written in S1. It is dropped rather than stubbed: a test named
`TestAnAlternativesPanelWithNoArenaRendersAnEmptyState` that asserts the panel is
absent would pass forever and would be a lie about what it covers.

Nine API tests were written instead, and every one is proven by a named mutant
rather than trusted — `make gates` only covers the store layer, so the API tests
kill their own:

| mutant | killed by |
|---|---|
| restore the slug in the not-found body | `...TheRefusalIsNotDistinguishable` |
| 403 instead of 404 | the same test, both assertions |
| unasserted capability reported as `unknown` | `...IsListedAsUnassertedAndCountedAsUnknown` |

A first mutant attempt rewrote the guard as `if false`, which orphaned the
`errors` import and failed to BUILD. A build failure is not a RED and proves
nothing; the balanced version is what killed the test.

**A second test file the plan did not anticipate.** The plan puts all of S1.2 in
`project_surfaces_test.go`, reading it as page tests. The API tests and the
script tests are different questions — the first is "does the endpoint answer
with the right JSON", the second is "does the script fetch it and render the
distinction" — and merging them produced three bad assertions in a row (pinning
badge labels, grepping the whole file for a phrase that a comment explains,
counting `return` statements that belong to `.map()` callbacks). Split, they are
each obvious. `project_panels_script_test.go` asserts on `project.js` rather than
`project.html` because the template is a nine-line mount point, which is the
reasoning `TestProjectPageHasTheForms` already gives.

**Verification:**

```bash
go test ./internal/httpapi/ -run 'Capability|FieldReport|Alternative|Private' -v 2>&1 | tail -40
```

## Step S1.3 — The panel markup and the script  [DONE for Capabilities and Field reports]

Solutions is NOT done; see the step.

**Files:** `internal/httpapi/templates/project.html` (edit — append sections),
`internal/httpapi/assets/js/project.js` (edit), `internal/httpapi/assets/css/style.css` (edit — append only).

No new template, no new route: one project page, three sections, in §4.10's
order (Capabilities → Field reports → Solutions/Standings). The existing
`project.js` already fetches and renders the complaint list, so it already has
the fetch/error/empty-state idiom to copy.

Rules that are not negotiable:

- **`[hidden]` is now `!important` site-wide** (this session's commit), so a
  panel that hides itself by setting the attribute is safe. Do not introduce a
  `display:none` class instead.
- **Never write authored text into `innerHTML`.** There is a test
  (`TestDocumentsPageNeverWritesAuthoredTextIntoInnerHTMLWithoutTheRenderer`)
  because this has been a real bug on this codebase. Use `textContent`.
- **Bounded rendering**: capabilities all; field reports newest 10 plus a count;
  solutions top 10 plus a count. Say how many were omitted.

**Verification:**

```bash
make verify && echo OK
# the manual gate no unit test can do:
./bin/concord -db /tmp/s1.db -listen 127.0.0.1:8478 &
# open /projects/<seeded-slug> and confirm all three panels render, that the
# disputed claim reads as disputed, and that the omitted-count shows when a
# project has more than 10 reports.
```

## Step S1.4 — Playwright, then docs  [DONE for Capabilities and Field reports]

**Files:** `tests/e2e/project_panels_e2e.py` **(new)** — *not*
`tests/e2e/e2e_test.py`, as the plan below said. Recorded per ground rule 7.

**Why a separate suite.** Two reasons, and the first is the one that matters:

1. `project_panels_e2e.py` is a BROWSER suite: it opens a session-scoped
   `sync_playwright()` and drives Chromium. `e2e_test.py` is an HTTP suite with no
   browser. `harness_selftest_test.py` already asserts that the two browser suites
   cannot share one pytest process, so a third browser suite cannot live in the
   HTTP file either.
2. The panel tests need a `sync_playwright` fixture and a per-test
   `X-Forwarded-For` counter (the rate limiter is 100 req/min per client key and
   `clientKey()` honours that header from loopback, so an 8-test browser suite on
   one IP renders `{"error":"rate limit exceeded"}` as the page body). `e2e_test.py`
   has no such machinery and adding it would put browser fixtures in a file whose
   other 35 tests do not use a browser.

The suite is registered in `pytest.ini`'s `python_files` AND in the Makefile's
`e2e` target as its own process, and `test_every_suite_is_collected_by_a_directory_run`
now names it — otherwise it would be silently skipped, which is exactly how the
38 Finder tests went missing once before.

**The fixture seeds through sqlite, never through the API.** A browser test whose
data comes from the endpoints under test cannot fail for the reason it exists: a
bug in the write path produces an empty panel and "the panel is empty" passes.

Twelve tests rather than the four tabulated below, because the plan's four miss
the states that actually break:

| plan | shipped |
|---|---|
| disputed claim reads as disputed | plus: NOT shown as `no`, and NOT carrying the confirmed badge |
| rate with its sample size | plus: the rate is not 0 or 100 for a mixed fixture (a hand-computed literal would pass a wrong number) |
| — | asserted `unknown` is not rendered as "no claim yet" — the two states the matrix exists to separate |
| — | an unclaimed capability says so, is counted, and the panel lists ALL catalog capabilities |
| — | an instance with no catalog says so, distinctly from a project with no claims |
| — | per-environment split shown (§4.7's "83% of reporters on arm64") |
| — | a private project's page carries none of its data into the DOM |
| — | no console errors across every panel shape |

The plan's four, kept:

| test | asserts |
|---|---|
| `test_project_capabilities_panel_shows_a_disputed_claim` | the panel contains the capability label and the word `disputed`, and does not present that capability as `no` |
| `test_project_field_reports_show_a_rate_with_its_sample_size` | both numbers are present |
| `test_project_solutions_show_the_ranked_standings` | both solution titles present, and the panel does not reorder them into an arbitrary sequence |
| `test_the_panels_survive_a_project_with_no_data` | a project with zero assertions/reports/solutions renders the three empty states, not a broken layout |

```bash
python3 -m pytest tests/e2e/project_panels_e2e.py -q   # expect 12 passed
make e2e                                                # all four suites
```

**What shipped instead of the count above:** 12, and `make e2e` now reports
8 harness + 35 site + 38 finder + 12 panels = 93.

Three mutants were killed by name (disputed badge dropped, rate guard defeated,
unasserted treated as a value). The third SURVIVED the first attempt, and it was
right to: `outcome_rate != null` sat after an early return on empty reports, so
its false branch was unreachable — `if (true)` was indistinguishable from the
real guard. Behaviour was correct; the guard was decorative. Moved above the
empty check so it governs both paths, and the mutant now dies.

Docs: `docs/PLAN-r4.md` gets an **R8 Phase-3 close-out** row; `docs/HANDOFF.md`'s
"not started" list loses whatever S1 closed; `docs/specs/phase3-spec.md` §4 gets a
"shipped, and what the live run changed" section in the Finder spec's §9 shape.

---

# S2 — Alternatives arenas

## Step S2.0 — Fix `baseline_entry_id` before adding an arena type that wants one

**Why this is first and not last.** `arenas.baseline_entry_id` exists and is
populated by nothing, while `arena_entries.is_baseline` is the authority. For
solution and feature arenas that duplication is inert. For an **alternatives**
arena it is not: §7.3's honest "do nothing" is *keep using X*, which is the
arena's subject and not a competitor to it. Shipping S2 on top of a schema with
two baseline representations and one unwritten means the next person decides
which one is authoritative while under time pressure.

Two options; **take the second**, it is smaller and reversible:

1. Backfill `baseline_entry_id` everywhere it can be derived and add a trigger.
   Rejected: it writes a value for alternatives arenas that has no meaning.
2. **State it.** Add to `docs/ARCHITECTURE.md`, next to the arena abstraction:

```
`arenas.baseline_entry_id` is populated for solution and feature-priority
arenas only. Alternatives and use-case arenas deliberately have no baseline:
"keep using X" is the subject of the arena, not a competitor within it, and
writing the arena's own subject into its baseline column would let the
do-nothing candidate be voted against the project it is an alternative to.
Nothing reads this column; `arena_entries.is_baseline` is the authority
(KNOWN-ISSUES, "Arena is_baseline has no column-level guarantee").
```

**Verification:**

```bash
grep -rn 'baseline_entry_id' --include=*.go --include=*.sql . | grep -v _test
# expect: the column in 0017 and the claim in 0019's header comment, no writer
```

## Step S2.1 — Store methods

**Files:** `internal/store/alternatives.go` **(new)**,
`internal/store/alternatives_test.go` **(new)**,
`internal/store/alternatives_mutants.json` **(new)**

```go
// (new) AlternativesArena is the (project, use case) shape §7.3 describes.
type AlternativesArena struct {
    Arena
    Candidates []AlternativesCandidate `json:"candidates"`
}

// (new) AlternativesCandidate is one ranked competitor.
type AlternativesCandidate struct {
    ProjectID int64   `json:"project_id"`
    Slug      string  `json:"slug"`
    Name      string  `json:"name"`
    Rating    ArenaEntry
    Health    *float64 `json:"health_score,omitempty"`
}

func (d *DB) CreateAlternativesArena(ctx context.Context, projectID, by int64,
    useCase, question string) (Arena, error)
func (d *DB) AddAlternativesCandidate(ctx context.Context, arenaID, projectID,
    candidateID int64) error
func (d *DB) ListAlternatives(ctx context.Context, projectID int64) ([]AlternativesArena, error)
func (d *DB) ListAlternativesFor(ctx context.Context, projectID int64,
    arenaID int64) ([]AlternativesCandidate, error)
```

`CreateAlternativesArena` delegates to the existing `EnsureArena(ctx,
ArenaAlternatives, projectID, 0, useCase, question)` — the signature is already
correct, which is the payoff of R1. Its extra work is validation only:
trim `useCase`, refuse empty (§`FindArena` keys `use_case = ''` for the other two
shapes, so an empty one collides with them), and default the question through
the existing `DefaultArenaQuestion`.

`AddAlternativesCandidate` refuses the arena's own project, and refuses a
candidate the caller cannot see (visibility is checked in the handler, because
`projectReadable` lives in `httpapi` — pass the check in rather than
reimplementing it here).

**Verification:**

```bash
go test ./internal/store/ -run 'Alternatives' -v 2>&1 | tail -30
```

## Step S2.2 — The tests, each of which fails when its rule breaks

**Files:** `internal/store/alternatives_test.go`

| test | asserts |
|---|---|
| `TestAnAlternativesArenaRanksProjectsNotFeatures` | after a vote, both entries are `entity_type = 'project'` and the leaderboard order changed. Fails if the arena silently resolves to the feature-priority arena — which is the exact mistake a shared `EnsureArena` invites |
| `TestAVoteOnAProjectPairMovesBothProjectsRatings` | both `arena_entries` rows move, and **no** `features.elo_r` changes |
| `TestTheArenaCannotContainTheProjectItIsAbout` | `ErrPerm` / `ErrInvalid` on the arena's own project |
| `TestAnAlternativesArenaHasNoDoNothingEntry` | after creating the arena and adding two candidates, no row has `is_baseline = 1` |
| `TestAnEmptyUseCaseIsRefused` | and the message names the use case |
| `TestAnAlternativesArenaIsIdempotentPerUseCase` | creating the same `(project, use_case)` twice yields one arena (this is what `EnsureArena`'s SELECT-then-INSERT exists for) |
| `TestTwoUseCasesAreTwoArenas` | "for sync" and "for search" do not collide |

The first two are the honest-telemetry tests, in the shape of
`TestArenaStandingIsComputedNotConstant`: all 70 live arenas are
`feature-priority`, so a store method that accidentally created one of those
would behave perfectly and pass every CRUD assertion.

## Step S2.3 — Mutation gate

**Files:** `internal/store/alternatives_mutants.json` **(new)**,
`Makefile` (edit — add the gate line)

Mutants, each a one-line behaviourally-balanced replacement (the build must
still compile, or RED is a lie):

```json
[
  { "name": "the arena's own project becomes a candidate",
    "old": "if projectID == candidateID {", "new": "if false {" },
  { "name": "an empty use case is accepted",
    "old": "if useCase == \"\" {", "new": "if false {" },
  { "name": "a project pair is written as a feature pair",
    "old": "EntityProject, candidateID", "new": "EntityFeature, candidateID" },
  { "name": "the do-nothing entry is created anyway",
    "old": "// no baseline for alternatives", "new": "" }
]
```

The third mutant is the load-bearing one: it must be killed by
`TestAnAlternativesArenaRanksProjectsNotFeatures`.

```bash
# add to Makefile's `gates:` target:
#   python3 internal/mutate.py internal/store/alternatives.go ./internal/store/ \
#     internal/store/alternatives_test.go internal/store/alternatives_mutants.json
make gates
# expect: [alternatives.go] ---- N/N killed, 0 survived
```

## Step S2.4 — HTTP surface

**Files:** `internal/httpapi/alternatives.go` **(new)**,
`internal/httpapi/alternatives_api_test.go` **(new)**,
`internal/httpapi/server.go` (edit)

```
GET  /api/v1/projects/{project_id}/alternatives
POST /api/v1/projects/{project_id}/alternatives                      {use_case, question?}
POST /api/v1/projects/{project_id}/alternatives/{arena_id}/entries   {project_id}
```

Voting is the **existing** `POST /api/v1/projects/{project_id}/features/{id}/vote`?
No — that route is feature-specific. Add the generic arena vote route §5 needs:

```
POST /api/v1/arenas/{arena_id}/vote  {a:{type,id}, b:{type,id}, outcome, reason?}
```

wrapping `store.CastArenaVote`, which already takes
`(arena, voter, aType, aID, bType, bID, outcome, reason, weight)`. Add the
`weight` argument as optional (defaulting to 1.0 via `ranking.VoteWeight` the way
the feature vote route does) rather than trusting a client-supplied weight — a
client-supplied weight is the shape the webhook auth note in HANDOFF warns about.

Tests: the seven from S2.2 at the API level for the ones that are about
permissions and shapes (403 anonymous create, 404 not-readable, 400 empty use
case, the ranking moving after a vote), plus one asserting the vote route
**cannot** be used to vote a project into a feature arena
(`TestAVoteCannotCrossArenaKinds` — the mirror of the existing
`TestAVoteCannotCrossIntoAnotherFeatureArena`).

```bash
go test ./internal/httpapi/ -run 'Alternativ|Arena' -v 2>&1 | tail -30
make verify && echo OK
```

## Step S2.5 — Panel, Playwright, docs

Add the alternatives panel to `project.html` — the empty state already shipped
in S1 earns its keep here, since this is where it stops being hypothetical.

`tests/e2e/e2e_test.py`: seed an alternatives arena with two project entries and
a vote between them, then assert the panel shows the winner first and that the
arena's use case appears as its heading.

```bash
python3 -m pytest tests/e2e/e2e_test.py -q
make e2e && make gates && make verify
```

## Step S2.6 — Live deploy check

```bash
git add -A && git commit   # after S2.1–S2.5 are green
ssh thinkcentre 'cd /mnt/disk-important/personal/documents/code/projects/concord && make deploy'
ssh thinkcentre 'curl -s localhost:8006/api/v1/projects/1/alternatives | head -c 200'
# expect a JSON object with "arenas", NOT {"error":"not found"}
```

---

# S3 — Scout

The largest of the three, and the one whose honest output on today's catalog is
a short list with a coverage heatmap and a lot of open gaps. That is correct
behaviour, not a defect — the same finding that capped the Finder.

## Step S3.1 — Migration: a `scout` document kind

**Files:** `internal/db/migrations/0023_scout_reports.sql` **(new)**

`project_documents.kind` is CHECK-constrained to
`readme|spec|plan|wiki|adr|changelog`, so a Scout Report cannot be smuggled into
an existing kind. The CHECK in SQLite cannot be altered in place; rebuild the
table naming every column (the `0008_project_delete_cascade.sql` lesson: name
the columns, never `SELECT *`, or a later `ADD COLUMN` breaks the replay).

Per the plan's own rule, apply to a **copy** of the live DB first and compare
index counts before and after:

```bash
ssh thinkcentre 'sqlite3 ~/.local/share/concord/concord.db "select count(*) from sqlite_master where type=(char(105,110,100,101,120));"'
# record it, apply to /tmp/copy.db, record it again, expect equal
```

Also update `DocumentKinds` in `internal/store/documents.go` and
`internal/db/migrate_test.go`'s expected version list — the trap the Finder plan
recorded at step 1.

## Step S3.2 — The engine, as its own package

**Files:** `internal/scout/scout.go` **(new)**,
`internal/scout/classify.go` **(new)**,
`internal/scout/scout_test.go` **(new)**

No store pointer, like `internal/finder`: it takes plain structs and returns
plain structs, which is what makes every classification rule testable without a
database.

```go
// (new) package scout

// Request is what the user asked for.
type Request struct {
    Idea         string
    Capabilities []string   // after decomposition; user-editable
    Constraints  Constraints
}

// (new) Constraints are §4.5.1 step 8's hard checks.
type Constraints struct {
    Licenses   []string
    Languages  []string
    SelfHosted *bool
}

// (new) Candidate is one catalog project, pre-loaded with what Scout needs.
type Candidate struct {
    ProjectID  int64
    Slug, Name string
    License    string
    Language   string
    Health     *float64
    Caps       map[string]string   // capability -> yes|partial|no|unknown
    Fit        float64
    Coverage   float64             // evidence coverage, as the Finder reports it
    OpenPain   int                 // unresolved complaints
}

// (new) Verdict is one classified candidate.
type Verdict struct {
    ProjectID int64
    Slug      string
    Verdict   string   // adopt | base-on | extend | inspire | avoid
    Signals   []string // never empty: the spec's "every recommendation carries an explanation"
    Reason    string
}

// HealthFloor is §6's "abandoned" test. Named, because a bare 0.3 in a
// condition is a number nobody can later argue with.
const HealthFloor = 0.35

func Decompose(catalogKeys []string, idea string) []string
func Classify(r Request, cands []Candidate) []Verdict
func Coverage(caps []string, cands []Candidate) Heatmap
```

`Signals` is not optional. A verdict with no signals is a bug: the surface
exists to answer *why did it say that*, so a verdict that cannot is worse than
no verdict.

## Step S3.3 — The classification rules as tests

| test | asserts |
|---|---|
| `TestScoutNeverInventsACapabilityOutsideTheCatalog` | a decomposition naming a key with no row is reported as a **new** capability, never scored |
| `TestAnAbandonedProjectIsAvoidWithAReasonAndSignals` | health below `HealthFloor` ⇒ `avoid`, and `Signals` is non-empty |
| `TestTwoProjectsWithDifferentHealthClassifyDifferently` | **the honest-telemetry test.** `health_score` is 0 for all 70 live projects, so a health-keyed rule keyed on today's data classifies everything identically and a test asserting "at least one avoid" passes for the wrong reason. This one needs two fixtures whose health scores straddle the floor |
| `TestASubsetMatchIsAdoptAndAMaximalMatchIsBaseOn` | the two verdicts are distinguishable, or the classification is not doing anything |
| `TestEveryVerdictCarriesAtLeastOneSignal` | table over all five verdicts |
| `TestAnIncompatibleLicenseIsAvoidWithTheConstraintNamed` | the reason names the constraint, so the user can see which rule fired |
| `TestSelfHostedFalseRemovesRatherThanAvoids` | a hard constraint filters; it is not a judgement, and calling it `avoid` would put a good project in the same bucket as a dead one |
| `TestCoverageDistinguishesCoveredPartlyCoveredAndOpen` | heatmap cells come from assertion states, and a `disputed` claim is not `covered` |

`TestSelfHostedFalseRemovesRatherThanAvoids` is the boundary that separates §4.6
step 2 (hard filters) from step 3 (ranking). Conflating them means a user cannot
tell "you ruled this out" from "this is a bad idea".

## Step S3.4 — API and page

**Files:** `internal/httpapi/scout.go` **(new)**,
`internal/httpapi/scout_test.go` **(new)**,
`internal/httpapi/server.go` (edit),
`internal/httpapi/templates/scout.html` **(new)**,
`internal/httpapi/assets/js/scout.js` **(new)**.

```
POST /api/v1/scout              {idea, capabilities?, constraints?} → report
GET  /api/v1/scout/{report_id}
```

The report is a `project_documents` row of kind `scout`, written against a
project id of 0 — or better, **not yet**: a Scout Report is not attached to a
project, and `project_documents.project_id` is NOT NULL. So either the report
is a document on the project the user names, or S3 grows a
`scout_reports` table. Decide at the start of the step and record it; the spec
allows either. Recommendation: `scout_reports` as its own table, because §10.2
names it in the entity list and a report is genuinely not a project document.

Registering the template: add `"scout"` to the `pages` slice in
`loadTemplates` **and** a `r.Get("/scout", …)` route. An unregistered template
renders `<h1>scout</h1>` with HTTP 200 — that exact defect shipped `finder.html`
once already, and `render` now returns 500 for it, but only if the name is not
in the list.

Tests: `TestAScoutReportCarriesAVerdictAndSignalsForEveryCandidate`,
`TestAScoutReportForAnIdeaMatchingNothingIsHonest` (an empty heatmap and an
explanation, not a 500), `TestTheScoutPageRenders`,
`TestScoutIsReachableAfterDeploy` is **not** a unit test — it is the live curl in
S3.6.

## Step S3.5 — Playwright

`tests/e2e/finder_e2e.py`'s fixture already has a capability matrix that splits;
reuse that fixture for a Scout test rather than seeding a second catalog.

| test | asserts |
|---|---|
| `test_scout_renders_a_heatmap_from_the_seeded_catalog` | each capability named in the input appears as a cell |
| `test_every_verdict_shows_its_reason` | no verdict card lacks a reason |
| `test_scout_survives_an_idea_that_matches_nothing` | the honest empty report, with the coverage gap listed |

## Step S3.6 — Deploy and the live check

```bash
make verify && make gates && make e2e
git add -A && git commit
ssh thinkcentre 'cd /mnt/disk-important/.../concord && make deploy'
ssh thinkcentre 'curl -s -X POST localhost:8006/api/v1/scout -H "Content-Type: application/json" -d "{\"idea\":\"a self-hosted kanban with WIP limits\"}" | head -c 400'
ssh thinkcentre 'curl -s localhost:8006/api/v1/finder/questions | head -c 200'   # regression check
# expect a report with verdicts, NOT {"error":"not found"}
```

---

## Completion gate for all three milestones

```bash
make verify          # vet + tests + build
make gates           # every mutant killed, 0 survived
make e2e             # all three suites, no leaked servers on 8421/8422
ss -ltn | grep -E '8421|8422' || echo "no leaked e2e servers"
git status --short   # empty
```

Then, and only then, the parts automation cannot do:

1. Open `/projects/{slug}` on the running instance and confirm all three S1
   panels render real data.
2. Create an alternatives arena, vote, and confirm the ranking changes — on the
   live instance, not a fixture.
3. Run one Scout query and read the output critically. The question is whether
   its verdicts are *defensible*, not whether the endpoint answered 200.