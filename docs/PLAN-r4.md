# Concord — r4 implementation plan

Derived from `docs/concord-spec-r4.md` (recovered 2026-10-02 from the session
transcript; it had never been written to disk). M0–M12 in `docs/PLAN.md` are
complete. This plan covers what revision 4 adds.

## Where r4 stands against the code

> The rows below were surveyed before any milestone was built. Cells have been
> corrected against the running instance where the state has since changed; the
> survey's *reasoning* (that several r4 names are renames of existing tables) still
> holds and is worth keeping.

Measured by reading the live schema and the ranking package, not by inferring
from the spec's table list — several r4 names are renames of tables that already
exist under other names (`threads`→`comments`, `roles`→`members`,
`boards`/`columns`/`cards`→`board_*`, `reputations`→`reputation_events`). The
survey below was taken when the schema had 69 tables; it is now 73, and the table
count in the Status section is the current one.

| r4 requirement | state | note |
|---|---|---|
| Glicko-2 engine | **exists** | `internal/ranking/glicko2.go`, but feature-hardcoded |
| Multi-criteria ranking | **exists** | `criterion_ratings` keyed on `feature_id` |
| Vote log append-only | **partial** | `pairwise_votes` now has `arena_id` + `reason` (0017); still no DB trigger forbidding UPDATE/DELETE |
| **Arenas (§5)** | **shipped** | `arenas`, `arena_entries`; 70 live. Was the spec's central abstraction |
| **Solutions (§6.3–6.5)** | **shipped** | all six types, forks, claims |
| Solution arena + baseline | **shipped** | permanent per feature; see KNOWN-ISSUES on `is_baseline` |
| Coverage + challengeable claims | **shipped** | κ=200; contested claims stop counting until upheld |
| Derived/forked solutions | **shipped** | `parent_id`, rating inherited with inflated RD |
| Ranking->consensus gating | **shipped** | gate + agenda live; the ADR on close writes a `decision record` |
| Decision records (ADR) | **shipped** | §6.5. `writeDecisionRecord` stores one as a `project_documents` row of kind `adr`; **16 live** |
| Complaints | exists | + duplicate detection (this session) |
| Features | exists | r4 wants `outcome`/`criteria` fields |
| Consensus | exists | + hold, tiers, stand-aside fixed |
| Lists / Requests | exists | separate vote tables, not arenas |
| Field reports | **shipped** (2026-10-03) | §4.7. Store + 16-mutant gate + `GET /api/v1/projects/{id}/field-reports` + project-page panel. §4.7's per-environment split is rendered |
| Capabilities matrix | **shipped** (2026-10-03) | §4.1. Store + `GET /api/v1/projects/{id}/capabilities` + project-page panel. Three claim states kept distinct on the page |
| Project-page solutions panel | **shipped** | §4.10. `solutionsPanel()` in project.js, with a script-level test |
| Scout | **shipped** | §4.5. A scout report is a `project_documents` row of kind `scout`, not a table — so looking for a `scout_reports` table finds nothing and wrongly reads as missing |
| Alternatives arena | **shipped** | §5. `alternativesPanel()` in project.js |

## Milestone order

Chosen so each ships something usable and each is independently verifiable.
Arenas come first because solutions need one, and generalizing the ranking engine
is a prerequisite for four of the five r4 pillars.

### R1 — Arenas: the generic ranking substrate
Migration `0017`: `arenas`, `arena_entries`, and `pairwise_votes` gains
`arena_id` + `reason` (existing feature votes backfilled into a
`feature-priority` arena, unchanged semantics).
- `internal/store/arenas.go`: create, get, list, `EnsureArena`,
  `UpsertEntry`, `RemoveEntry`, baseline handling.
- Generalize `ranking` to `(arena, entityType, entityID)`; keep
  `criterion_ratings` working for both features and solutions.
- Pair selection and vote application re-point at arenas; the existing
  `/vote/next` and `/vote` routes keep working through a per-project feature
  arena.
- AC: a feature vote still moves a rating; the same vote log now also scores a
  solution pair; `vote/next` never returns a pair the voter already judged.

### R2 — Solutions
Migration `0018`: `solutions`, `solution_complaint_claims`.
- Types `build-new`, `extend-existing`, `integrate-external`,
  `config-or-docs-only`, `workaround`, `do-nothing`; relation
  `exclusive`/`complementary`; `parent_id` for forks; `external_project_id`.
- Store: propose, get, list, fork (inherit `r` with inflated RD), challenge/
  uphold a coverage claim.
- API: `POST/GET /features/{id}/solutions`, fork, claim challenge.
- AC: forking a rated solution starts near its parent's rating, not at 1500;
  a `do-nothing` solution exists permanently in every feature's arena.

### R3 — Solution ranking with coverage
`κ = 200`, charter-configurable. `coverage = pain(resolved)/pain(all linked)`.
- Challenger cannot contest their own claim; a contested claim stops counting
  until upheld.
- Leaderboard: rank, score, confidence, coverage, effort, risk, type, pro/con
  digest.
- AC: a solution claiming coverage of an unlinked complaint is rejected; a
  challenged claim contributes 0 coverage.

### R4 — Ranking → consensus and ADRs
- Auto-open eligibility: leads runner-up **and** baseline at ≥80% win
  probability, enough distinct voters, stable N hours.
- Outcomes: accepted, accepted-with-amendments (spawns a derived solution),
  fall back to #2, rejected (baseline wins), sent back.
- `decision_records` written on every close, with positions, objections,
  remedies, and the fallback chain that was pre-agreed.
- AC: a call cannot auto-open against a solution that does not beat the
  baseline; a `fall back to #2` outcome records the next solution in the ADR.

### R5 — Discovery intelligence
- `field_reports(project, user, version, use_case, env, outcome, body)`.
- `capabilities` + `capability_confirmations`.
- Alternatives: an arena per (project × use-case) comparing projects.
- AC: a field report on an unclaimed version is listed as unconfirmed; two
  independent confirmations promote a capability claim.

### R6 — Scout
`scout_reports(input, capabilities, matches, verdicts)`: "I need X" → ranked
candidates with adopt/extend/build verdicts.
- AC: a Scout run for a capability the catalog has returns the ranking with
  coverage evidence, and a verdict per candidate.

### R7 — Documentation sync
- DONE: replaced with the r4 text (it had been a copy of the master;
  the master was rewritten, so the copy is now two revisions stale).
- `docs/PLAN.md`: mark r4 milestones, correct the Current State block.
- `docs/ARCHITECTURE.md`: the arena abstraction and why.
- Import the spec into Concord itself as a document, as was done for
  `frontend-spec.md`.

### R9 — Repository identity and health ingest (spec §4.5.7.1–.2) — CODE HALF SHIPPED

The prerequisite for every "healthy projects" surface, and the repair for two that
already exist and already lie. Full detail: `docs/plans/contribution-feed.md` CF-1.

- Migration `0031_repo_identity.sql`: `projects.repo_url`, `projects.repo_host`,
  `project_metrics.newcomer_response_hours`, `project_metrics.newcomer_sample`,
  `forge_events`.
- `internal/store/projects.go` (new): `SetProjectRepo` (host must agree with the URL —
  a typo silently excludes a project from every fetch), `ListProjectsWithRepo`.
- `seed/forge_repos.json` + `cmd/forgeseed`: the 74-row mapping, refused unless every
  slug resolves. **Never derived from a slug** — a guessed URL sends a stranger to
  somebody else's repository.
- `cmd/healthsync` + a systemd timer: fetch per host, then call the EXISTING
  `store.UpdateProjectMetrics`. No second scorer, no second set of weights.
- A failed fetch leaves the previous row: unknown health is not zero health, or a
  transient 502 ranks the whole catalog `avoid`.
- AC: `sort=health` differs from `sort=updated` once more than one project is measured;
  `min_health=0.5` returns rows (today it returns none, for any value > 0).

### R10 — Work-item contract (spec §4.5.7, CF-2) — SHIPPED

- Migration `0030_work_items.sql` (R10 was built first, so it took the number R9 was
  planned to have; R9's identity migration is `0031`): `features.difficulty`,
  `features.time_budget_minutes`,
  `features.needs_mentor`, `features.origin`, `complaints.origin`, `work_item_claims`.
- `origin` ships defaulting to `api` and then every EXISTING row is relabelled
  `unknown`, because 925 of the 1,874 features on the live instance were written by
  seed scripts and nothing in the row says which. `unknown` is the honest answer; the
  backfill is `cmd/originbackfill` (still unbuilt, see the AC below).
- The active-claim partial unique index is the rule; releasing writes `released_at` and
  never deletes. A claim never changes a feature's status — the board stays the only
  mechanism that moves work.
- Routes: `GET /projects/{slug}/work-items`, `PUT /features/{id}/work-item`
  (maintainer), `POST|DELETE /features/{id}/claim` (contributor, `409` on a live claim).
- AC: two concurrent claims → one row with `released_at IS NULL` and a `409`; a `draft`
  feature refuses a claim; seeded rows carry `origin='seed'`.
- MET WITH ONE RECORDED GAP: the two-claims and draft ACs are proven both in
  `internal/store/workitems_test.go` (against the database, counting live rows) and in
  `internal/httpapi/workitems_test.go` (201 then 409; 400 for a draft; 403 for a
  non-member; the maintainer-only label write), and the role check was mutation-tested
  (removing `requireRole` fails `TestClaimRequiresMembership`). The third clause is
  NOT met: nothing in the tree knows which rows the seed wrote, so existing rows carry
  `origin='unknown'` and `cmd/originbackfill` does not exist yet. `unknown` is the
  honest state — the whole point of the statistics strand is that a figure which cannot
  state its origin is not published — but "seeded rows carry seed" is a claim about a
  backfill that has not been built, so it stays unmet rather than ticked.
- Live at `GET /api/v1/projects/{slug}/work-items`; the panel is on the project page,
  below the features, ordered complaint-first then cheapest.

### R11 — Contribution feed (spec §4.5.7, CF-3) — SHIPPED (with three deviations from this ticket)

- `finder.classify` gains `minutes`/`difficulty` → **`FamilyEffort`** and `mentor` →
  **`FamilySupport`**, two families this milestone adds. The ticket said maturity/governance;
  neither is what a time budget is, and filing it there would make the finder's question picker
  describe a task's cost as a maintenance stage. Spec §4.5.7 and `finder-spec` §3 list families,
  so adding two is a spec change this ticket did not declare.
- `GET /api/v1/contribute/next` builds candidates through the existing
  `finderCandidatesWithIDs` plus the item's own label, runs the unchanged engine, and emits
  ranked work items with reasons. `internal/finder/contribute.go` holds the vocabulary
  (`MinutesBucket`, `DifficultyWord`, `WorkItemCandidate`, `OrderWorkItems`) so no second ranker
  exists.
- **No finder session machinery.** `/contribute` is a server-rendered page: the same
  `contributeFeed` function backs both the JSON route and the HTML page, a plain `GET` form is
  the whole client, and every answer is a linkable URL. There is therefore **no new
  `assets/js` reader**, which is why `reader_coverage_test.go` has nothing to demand here — the
  ticket's third bullet assumed a JavaScript page.
- **`difficulty` is stored as S/M/L**, so `DifficultyWord` translates it to easy/medium/hard for
  the answer set. Without it every difficulty filter returns nothing (found by the fixture, not
  by inspection).
- AC: an item with no stated budget is kept (§2.7: unknown is not over-budget) and its `Why`
  says the fit is unknown; two identical requests return an identical order (`OrderWorkItems`
  is `SliceStable` with a total tiebreak); a private project's work items never appear, asserted
  as a stranger.

### R12 — Newcomer responsiveness (spec §4.5.7, CF-4)

- The webhook already receives `pull_request`; it needs `pull_request_review` and
  `pull_request_comment`, and it must record all of them in `forge_events` (idempotent on
  redelivery) instead of only writing an audit row.
- `internal/discovery/responsiveness.go`: pure `NewcomerResponse(events)` → median hours
  for PRs by an author with no earlier PR in that repo, first response by someone else,
  `ok=false` under five samples. A self-comment is not a response.
- Displayed on the project page with its `n`, hidden below five samples, and NOT folded
  into `health_score`.
- AC: `newcomer_response_hours` is NULL for a repo with four samples and non-NULL for one
  with five; a replayed payload inserts nothing twice.

### R13 — Census with a provenance split (spec §4.13, STAT-1)

Composition, not growth. The whole reason this is a milestone and not a template is stored in
one number: 925 of 1,874 features have no `create_feature` audit row, so every "opened per
week" series describes a seed script.

- `internal/store/stats.go`: `Census` with `FacetCount{Value, Total, Seed, Other}` where
  `Total = Seed + Other` is an arithmetic invariant, not a convention. Reuses the facet SQL
  already in `search.go` and extends each with an `origin` sum.
- `GET /api/v1/stats/summary`.
- AC: a fixture with one organic and one seeded row per kind; every facet row sums; and the
  total is never larger than `count(*)` (the classic `LEFT JOIN` multiplication defect).

### R14 — Decision latency and exposure (STAT-2)

- `internal/discovery/latency.go`: `Latency{MedianHours, N, Unfinished, Unit}` and three
  functions — triage (complaint → first validate), delivery (feature `ready` → `shipped`),
  decision (call opened → closed).
- Rules that are the feature: a row with no recorded start counts as `Unfinished`, never as
  zero hours (a seeded feature would otherwise publish as a zero-day delivery); an even sample
  uses the lower-middle, never an interpolation; a median is always printed with its `n`.
- `GET /api/v1/stats` (one computation), `/stats` page rendering **from** it, `/badge/{slug}.svg`
  and `/badge/instance.svg` (`—` for unmeasured, never `0.0`), and a single `<item>` per day in
  the existing project feed.
- AC: the page's rendered number equals the JSON's, asserted as text; a NULL `health_score`
  renders a dash.

### R15 — Snapshot history and the derived surfaces (STAT-3)

- Migration `0032_instance_stats.sql`: one row per day, `day TEXT PRIMARY KEY`.
- `cmd/statsnapshot` + daily timer (`Persistent=true`), idempotent on the day
  (`ON CONFLICT(day) DO UPDATE`) so a second run corrects rather than duplicates.
- `GET /api/v1/stats/history?days=90` — the only series route.
- Then, and only then: **rising** (§4.5.5.7, slope between two snapshots ≥ 14 days apart,
  organic rows only, unranked with one point) and the **responsiveness leaderboard**
  (R12's metric, `newcomer_sample >= 5`, absent rather than last below five).
- AC: two runs in one day leave one row; a missing day is a gap, not a zero; rising does not
  rank a project with a single snapshot.
- **Delivered**: STAT-3.1–.3 on `0032`; STAT-3.5 (leaderboard) on R12's metric; STAT-3.4
  (rising) on `0033_project_stats.sql`, which the sentence above did not anticipate — a
  project slope needs a per-project row, and `instance_stats` has no project column.

## Non-goals

Phase 4 of the r4 roadmap (federation, mobile, code search) — the spec's own
roadmap puts these at months 12–24 and the current work is Phase 1–3.
GraphQL (§10.3) — the API surface is still moving; a generated schema now would
be rewritten. Federation and MCP are listed in the plan for later.

## Status

Measured against the running instance (schema 35, 86 tables, 1242 tests; the build it is
running is whatever `GET /api/v1/version` answers, which is deliberately not written down here --
a version string in a status paragraph is stale the moment the next commit lands), not against the
plan's own history. "Shipped"
means there is code, a migration and a test -- not that the surface is complete.

**These numbers are gated.** `scripts/check_doc_numbers.py` fails when a document
states a schema version, migration count, table count or test count that
disagrees with the tree, so this header cannot go quietly stale the way the last
one did.

| Milestone | Status | Notes |
|---|---|---|
| r4 spec recovered to `docs/concord-spec-r4.md` | done | 55,987 chars, was only ever in the transcript |
| r4 gap survey | done | the table above |
| R1 arenas | **shipped** | `0017_arenas.sql`; 70 arenas live. Generalized ranking is the substrate; `pairwise_votes` gained `arena_id` + `reason` |
| R2 solutions | **shipped** | `0018_solutions.sql`; all six types, `exclusive`/`complementary`, forks with inflated RD, claim challenge/uphold |
| R3 solution ranking + coverage | **shipped** | kappa=200; leaderboard carries rank/score/confidence/coverage/effort/risk. Known issue: `is_baseline` has no DB-level guarantee |
| R4 ranking->consensus gating | **shipped** | `0019_solution_consensus_calls.sql`; agenda, eligibility gate, and the ADR on close (a `kind='adr'` document). The ADR now carries **the ranking the decision was made against** (`docs/REMAINING.md` §21.2) — §6.5's "the ranking is the fallback chain", so the runner-up is marked as the pre-agreed fallback, and "why this and not the runner-up" is answerable from the record |
| R5 field reports + capabilities | **shipped** | `0021_capabilities.sql`, `0022_field_reports.sql`; panels on the project page |
| R6 Scout | **shipped** | `0023_scout_document_kind.sql`; a report is a `kind='scout'` document, so the absent `scout_reports` table is the design, not the gap |
| R7 docs sync | **in progress** | this file, README, KNOWN-ISSUES and HANDOFF updated 2026-10-02; `docs/REMAINING.md` §13 and `docs/QUESTIONS.md` added 2026-10-15. `docs/concord-spec.md` is still two revisions stale -- it remains a verbatim copy of the master that the r4 rewrite replaced, and `scripts/check_spec_drift.py` keeps every citation off it |
| R8 administration surface | **shipped 2026-10-15** | the settings page owns visibility, tags, languages and invites; `GET /languages` added as the read half. Six endpoints had handlers, guards and tests and no reader anywhere on the site. See `docs/REMAINING.md` §13.2 |
| **R9 repository identity + health ingest** | **shipped 2026-10-30 (code half)** | spec §4.5.7.1–.2, plan CF-1, migration `0031_repo_identity.sql`. `ParseRepoURL`/`SetProjectRepo`/`ListProjectsWithRepo`/`ProjectSlugs`, `cmd/forgeseed`, `cmd/healthsync` (GitHub + Forgejo/Codeberg), the timer units and `make deploy-admin`. **The data half is Q5**: `seed/forge_repos.json` states 5 of 74 and skips 69 with a reason (gravity's Q5 remote answers 404 as of 2026-10-30), so `sort=health` is still mostly-unknown. §32.2 records a finding that changes the ticket: the two "broken" search surfaces were a DATA gap, not the predicate bug the plan prescribed a fix for — the predicates are equivalent, proved in `health_sort_test.go` |
| **R10 work-item contract** | **shipped 2026-10-30** | CF-2, migration `0030_work_items.sql`. `GET /projects/{slug}/work-items` (public), `PUT /features/{id}/work-item` (maintainer), `POST\|DELETE /features/{id}/claim` (contributor, `409` on a live claim, released rows kept). The panel sits under the features on the project page. `origin` exists and every pre-existing row reads `unknown` — the seed backfill (`cmd/originbackfill`) is the one recorded gap |
| **R11 contribution feed** | **shipped 2026-10-30** | CF-3, spec §4.5.7. `internal/finder/contribute.go` (MinutesBucket, DifficultyWord, WorkItemCandidate, OrderWorkItems) + `internal/httpapi/contribute.go` + a server-rendered `/contribute` page — no second ranker, no second candidate loader, no JavaScript. Deviations recorded in the R11 section: two new question families, no finder-session reuse, and S/M/L → easy/medium/hard translation (without which every difficulty filter matched nothing) |
| **R12 newcomer responsiveness** | **shipped 2026-11-06** | CF-4. `forge_events` written from four webhook arms (push, `pull_request`, `pull_request_review`, `pull_request_comment`; the last reads both the Forgejo `issue_comment` and GitHub `pull_request_comment` shapes), idempotent on redelivery by `UNIQUE (repo_full, kind, number, actor, at)` + `ON CONFLICT DO NOTHING`. `internal/discovery/responsiveness.go` exports `NewcomerResponse` (first-PR-only, self-comment is not a response, lower-middle median, `ok=false` under five). `cmd/healthsync` reads the events per repository and writes `newcomer_response_hours` / `newcomer_sample` through `UpdateProjectNewcomer` — a SEPARATE write from `UpdateProjectMetrics`, because that upsert is also reached by `PATCH /projects/{slug}/metrics` and folding the columns in would let an operator repairing a star count blank the median. Never folded into `health_score`. Displayed on the project page with its `n`, **hidden entirely when NULL**. Two mutants killed: a median written as 0, and a `time.Now()` fallback in `forgeEventAt` (the second needed a GARBAGE-timestamp case, since an absent stamp exits through a different guard) |
| **R13 census with provenance** | **STAT-1 shipped 2026-11-06** | spec §4.13, plan `docs/plans/instance-statistics.md` STAT-1. `internal/store/stats.go` (`Census`, `ProvenanceCount`), `GET /api/v1/stats/summary`, and a `/stats` page shell that renders from it. **Four plan statements were wrong and are corrected in the plan**: `FacetCount` already existed in `search.go`; only `features`/`complaints` carry `origin`; the split is three-way because `0030` backfilled pre-existing rows to `'unknown'`; and the table is `project_documents`, not `documents`. STAT-2 shipped the same day; STAT-3 (snapshot history) is the remainder |
| **R14 decision latency + exposure** | **STAT-2 shipped 2026-11-06** | STAT-2. `internal/discovery/latency.go` (`Measure`, lower-middle median, `Unmeasured` as a fourth count) + `internal/store/latency.go` (the three episode builders). `GET /api/v1/stats`, three latency cards on `/stats`, `/badge/{slug}.svg` + `/badge/instance.svg` (an unmeasured project renders `—`, never `0.0`), and the `?kind=stats` feed item. **Six more plan statements were wrong** — the `health_score` column is 0 for all 70 live projects, the delivery target status is inside the audit `detail` string, and an open consensus call is inserted with `closes_at = opens_at` so an unfiltered query measures every open call as 0.0h. Verified live and in a browser: the rendered cards match the API field-for-field |
| **R15 snapshot history + derived surfaces** | **STAT-3.1–.5 shipped 2026-11-06** | Migration `0032_instance_stats.sql` (one row per day, `PRIMARY KEY(day)` so a second run **corrects** rather than appends), `internal/store/statsnapshot.go` (`ComputeSnapshot`/`WriteSnapshot`/`SnapshotHistory`), `cmd/statsnapshot`, `GET /api/v1/stats/history?days=90` (a bare array, capped at 365), and `deploy/concord-statsnapshot.{service,timer}` (daily 03:10 UTC, `Persistent=true`). Two consecutive runs leave exactly one row, verified live. **STAT-3.5 (responsiveness leaderboard) shipped 2026-11-06**: `ResponsivenessLeaderboard` + `GET /api/v1/stats/responsiveness` (a bare array, fastest first, `newcomer_sample >= 5`, a slug tiebreak so the order is stable) and a card on `/stats`. It publishes NAMED projects, so it also filters `visibility='public'` — unlike every other statistics route, which publishes aggregates a private project contributes to invisibly. **STAT-3.4 (rising) shipped 2026-11-06**: the plan sentence was unimplementable as written — `instance_stats` records one instance-wide row per day and has no project column, so a project slope was not derivable from it at all. Migration `0033_project_stats.sql` adds `project_stats(day, project_id, organic_features_shipped, organic_complaints_filed)`, `PRIMARY KEY(day, project_id)` so a second run corrects rather than appends, counts CUMULATIVE and ORGANIC (a stored delta is wrong the moment a run is missed; seed/import rows would make the slope a chart of an import script). `cmd/statsnapshot` writes that day's rows in the same pass as the instance row. `GET /api/v1/stats/rising` ranks by organic features per week across **each project's own** first-and-last span, requires `julianday(last) - julianday(first) >= 14` (`RisingMinDays`) or the project is ABSENT, carries the span in every row, keeps signed deltas rather than clamping them, filters `visibility='public'` (it names projects), and shows health as a LEVEL beside the slope from `ProjectHealth` — never the `project_metrics.health_score` column, which is 0 for every project. Card on `/stats` with a stated empty state. Three plan statements were wrong; the sharpest is that `sql.NullFloat64` has no `MarshalJSON`, so the API published `{"Float64":0,"Valid":false}` where the spec wants `null` |

### What is deliberately left

Section 8.2's eligible collaborator has two clauses with no representation, and
both are stated in `internal/store/eligibility.go` rather than approximated:
"seated by charter consensus" (no charter amendment or seat table exists to
count) and "agents are excluded" (`users` has no agent flag).

### Where the next milestone should go

**Updated 2026-11-11.** R12–R15 landed on the statistics strand (`REMAINING` §34) and are
**not deployed**: the live instance answers `pre-merge-2026-11-05-4-gde880a5`, which
predates migrations `0032`/`0033` and the `statsnapshot` timer. So the first task is a
deploy (`make deploy`, then `make deploy-admin` for the timer), not a feature. After it,
the two gaps a measurement pass on 2026-11-11 put in front of everything else:

1. **Q5's data half, which is 31 slugs from being mechanical.** `seed/forge_repos.json`
   holds 74 entries — 5 with a `repo_url`, 69 skipped as `not stated (Q5)` — so
   `concord-healthsync` logs `synced 5, skipped 0, failed 1 of 6` and exits 1 every hour,
   `sort=health` stays a constant ordering and `min_health>0` stays empty for 68 of 73
   projects. A bounded walk of `~/code` and `~/code-local` (depth 3, origin remotes only)
   found a remote for **31 of those 69**: ascend, blackrep, suki, orbital, kindred,
   gravity, threadlight and 24 others. Two of the 31 embed a token in the URL — strip it
   before seeding. The one failed fetch is `ferrisfeed`, whose `repo_url`
   (`hirrolot19/ferrisfeed` on git.polarisocial.xyz) answers 404.
2. **The consensus phase contains no decisions.** `consensus_calls` is empty on the whole
   instance while 87 features carry `status='consensus'`. The page can list calls as of
   `REMAINING` §35 and the list is empty on every project. Either open calls from the
   arenas the ranking already chose — `GET …/features/{id}/consensus` is the readiness
   gate and the only path that opens one — or the statuses are wrong. A board claiming 87
   decisions in progress while the record holds none is the same class of lie as an
   unmeasured `health_score`, and it is the cheaper one to see.

Unchanged from the last pass: 2,325 complaints are `validated` and **none** is `open`, so
§4.5.7's "complaints needing validation" pool is still zero rows wherever it is offered.

**Updated 2026-10-30.** The next milestone is **R9**, and it is not a new surface: it is the
column the catalog never had. `projects` carries no repository reference at all (2 of 74
descriptions hold a URL, and nothing parses them), and `project_metrics` holds a row per
project with `health_score` on none of them — so `sort=health` is a constant ordering and
`min_health>0` returns an empty set on every instance. R9 exists to make those two
honest before anything is built on top of them. It is also the smallest of the four: the
scorer, the weights and the writer already exist; only the identity and the fetch are
missing. R10–R12 (`docs/plans/contribution-feed.md`) build §4.5.7 on top of it.

There is a **second strand, independent of the first**: R13–R15 (`docs/plans/instance-statistics.md`,
spec §4.13) — census with a provenance split, decision latency as the headline number, and a
daily snapshot so history is recorded rather than re-derived. R13 needs only R10's `origin`
column; R14 needs nothing else; R15's leaderboard needs R12. Both strands came from the same
measurement pass, and R9/R13 are the two that must land before any page claims a number.

**Updated 2026-10-16 (ninth pass).** Both candidates named here on 2026-10-15 are now
done, and each was done somewhere other than where this section expected:

1. ~~R4's `decision_records`~~ — **done**, `docs/REMAINING.md` §21. The ADR existed (a
   `kind='adr'` document) and the part this section asked for — "why we did this instead
   of the runner-up" — was missing for a worse reason than a missing table: the route that
   writes the record refused any call that had been CLOSED, which is the order the page
   produces, so the record was writable only by not closing the call. The record now
   carries the ranking it was decided against, and the close no longer forecloses it.
2. ~~`docs/QUESTIONS.md` Q1 — the browser session~~ — **answered and implemented
   2026-10-15** (§14, `concord_nav`): the question file still carries the reasoning and the
   rejected options on purpose. This section had not been updated to say so.

**Where a next pass should look** — the pattern that has paid four times now, rather than a
named feature: walk `chi.Walk`'s route table against `readerInventory` and look for rows
whose `Routes` count is larger than the surface it names. Every one of `/close`, §6.6's
window, `/outcome`, `/fallback` and `/hold` was found that way, and all five were routes
with handlers, store tests and **no reader** — hidden by a coarse row that any page
reading a call satisfies. The inventory now counts routes per row precisely so the next
one shows up as a changed number rather than a silent absence.

The 2026-10-05 correction below -- "check the claim against the code, not the tracker" --
is the reason both of those were found by reading handlers and route tables rather than this
file: §13's unguarded routes and the never-working merge were invisible in every tracker and
visible in `chi.Walk`.


---

## Correction (2026-10-05)

Four of the five `**missing**` / `**partial**` rows above were WRONG, and every one
was wrong in the same direction: the table claimed a gap that the code had
already closed. Checked against the code and the live instance rather than
against this file:

| claimed | actually |
|---|---|
| no `decision_records` | `writeDecisionRecord` (consensus_solutions.go:552) stores one as a `project_documents` row of kind `adr` — **16 live** |
| solutions panel missing | `solutionsPanel()` in project.js, covered by project_panels_script_test.go |
| Scout missing | a scout report IS a document of kind `scout`; there is no `scout_reports` table **by design** (0023) |
| alternatives arena missing | `alternativesPanel()` in project.js |

**A tracker row is a claim, not a measurement.** The `decision_records` row was
the most misleading of the four: the feature exists, is named after a table the
design deliberately does not use, and reading the table sent me looking for
something absent when the thing I wanted was already in production. The
generalisation worth keeping: **when a plan says something is missing, confirm it
is missing before building it**, because "the plan says so" is exactly as
unreliable as a test that never met its mutant.

**Also note the live schema is 78 tables with 1,806 audit rows** — and the audit
log could be written by twenty call sites and read by none until this pass.
Nothing on this page mentioned that.

**Also queued, on the owner's request (2026-11-11): moderation.** Spec and plan are written
and nothing is built — `docs/specs/moderation-spec.md`, `docs/plans/moderation.md`. It answers
the arena-removal request as one mechanism: an act with a reason, a reversal that is a second
act, and the audit log grown into a filterable modlog with a keyset cursor. It deliberately
lands *after* the deploy above, because it changes the schema (migration `0034`) and the deploy
is what proves the previous three migrations on the live host. One thing it settles in passing:
`RemoveArenaEntry` has existed since the arena work with no caller, so the removal primitive is
adoption, not construction — the missing half was always the route, the permission and the log.

