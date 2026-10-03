# Concord — spec for the remaining work (Phase 3 close-out)

Version 1. Written 2026-10-03, against commit `40c97e3`, verified against the
running instance rather than inferred from a status table. Every "missing" below
was confirmed by reading the code or by a query, not by a term grep — a
zero-grep is not proof of absence, so each claim names the file that was read.

This document covers what is left between the current tree and r4 Phase 3. It
does not restate what is already built; `docs/concord-spec-r4.md` is the
contract, `docs/PLAN-r4.md` is the milestone list, and this file is the gap
between the two and reality.

## 1. How this gap list was produced

Two documents were wrong before this was written, so the method is recorded
first. `docs/PLAN-r4.md` had said for weeks that R4 was "partial" and that
`decision_records` "does not exist" — the one remaining gap in the consensus
pillar. It does exist, and it was implemented on purpose: migration
`0019_solution_consensus_calls.sql` states the decision in its own header
comment ("The decision record (ADR) is NOT a table here… A second ADR table
would be the second representation of one fact"), and
`writeDecisionRecord` in `internal/store/consensus_solutions.go` writes it
through `PutDocument` as `kind='adr'`, slug `call-<id>`.
`TestTheDecisionRecordCarriesPositionsAndObjections` asserts the positions,
objections and remedies are in the body.

So the gap list below is the set of things this document verified are absent,
and it names the file that proves each one. A milestone is not on this list
because a status table said "pending"; it is on this list because a
capability could not be found in the tree.

## 2. What is genuinely missing

### 2.1 Scout (r4 §4.5.1, Phase 3)

The flagship new-project workflow. Nothing in the tree implements it:
`grep -rn 'scout_reports' internal/db/migrations/` returns 0 across 22
migrations, and there is no `internal/scout/` package, no `scout` route in
`internal/httpapi/server.go`, and no handler. The spec lists nine behaviours
(decompose, match, classify adopt/base-on/extend/inspire/avoid, coverage
heatmap, surface known pain, prior art including dead projects, suggest joining
instead of duplicating, hard-constraint checks, produce a shareable report).

The parts it would need are **mostly built and currently unconnected**:
capability assertions (`internal/store/capabilities.go`), field reports and
their outcome rate (`internal/store/field_reports.go`), unresolved complaints,
and the fit scorer the Finder already computes (`internal/finder/score.go`).
Scout is therefore mostly assembly, with one genuinely new judgement: the
five-way classification, which has no analogue anywhere in the tree.

### 2.2 Alternatives arenas (r4 §7.3, §5)

`ArenaAlternatives` and `ArenaUseCase` exist as constants, in
`DefaultArenaQuestion`, and in `FindArena`'s key shape — and **nothing ever
creates one**. The only three `EnsureArena` callers in non-test code are
solutions (`ArenaSolution`), feature votes (`ArenaFeaturePriority`) and the
solution board (`ArenaSolution`). Every one of the 70 live arenas is
`feature-priority`.

This is the same class of defect as the field-report and capability data: the
machinery is correct, the *inputs* were never populated. §7.3 wants "a ranked
better for / worse for list" per use case, and the engine that would produce it
(`CastArenaVote`, `NextArenaPair`, `ArenaLeaderboard`, all already
project-entity-aware via `EntityProject`) has no arena to act on.

### 2.3 The project page has no solutions, capabilities or field reports (r4 §4.10)

<!-- SHIPPED 2026-10-03, in three commits. Capabilities and field reports
     (fd2ceae, 633cdcc), then solution standings (ef0977f). The three panels now
     render on the project page and each is covered at the API, script and
     browser layers. This section is kept as the record of what was missing, not
     as a live gap -- see §4 for what each panel now asserts. -->


§4.10 spells out the page anatomy: "Capabilities → Health breakdown → **Field
reports** → Known pain → Roadmap (native: ranked features, **solution
standings**, board snapshot)". Read the template list in
`internal/httpapi/server.go` — `project.html` is the only project-scoped
template. `grep -rn 'solutions\|field-report\|capabilit'
internal/httpapi/templates/*.html` matches **only** `finder.html`, in a link
to `/projects`. So a reader of the API can see all of it and a reader of the
site sees none of it. The `/rank` and `/ranking` pages exist and are
reachable, but neither solutions nor field reports have a page at all.

### 2.4 Lists and requests still bypass arenas (r4 §5)

`internal/store/lists.go` and `internal/store/requests.go` keep their own vote
tables (`VoteAnswer` with a `direction int`, `list_entry_votes`). §5 declares
arenas "the single ranking engine" and names `list` and `request` as two of its
six kinds. Ranks fine today — it is not broken — but those two surfaces cannot
appear in one leaderboard with features and solutions, and a `neither` outcome
in them means something different than in an arena. Recorded as a known
inconsistency, not a defect to rush.

### 2.5 CLI (r4 §4.11)

§4.11 lists `concord search`, `concord scout "idea"`, `concord similar`,
`concord alt`, `concord why-not`, `concord report`. `cmd/concord/main.go` is a
single-flag server (`-db`, `-listen`, `-version`, `-migrate-only`); there is no
subcommand dispatch at all. `concord scout` is blocked on 2.1 and `concord alt`
on 2.2, but `search`, `similar` and `report` need only the API that already
exists.

### 2.6 Opportunity Radar, For You, dependency alerts (r4 §4.8, §4.5.4–5)

`grep -rn 'radar\|Radar' internal/ --include=*.go` → 0. For You, dependency
alerts and the Radar are all Phase 3 items with no table, no route and no
handler. §4.9 (dependency intelligence) additionally needs a dependency graph
that does not exist; `internal/store/` has no dependency table, which is why
M12's "Forge sync" was marked done on its non-graph half only.

## 3. What is being built now, and why this order

Three milestones, ordered so each ships something usable and each is
independently verifiable, and the order is forced twice over.

**S1 — Project page surfaces.** Before Scout. Scout is a build-vs-adopt report
whose inputs are a candidate's capabilities, field-report outcome and open
complaints; three of those are already in the API and none is visible on the
site. Building Scout first means building its report before its inputs have a
human-readable surface, and the same three components get written twice.
Page first is also strictly cheaper to verify: httptest plus Playwright against
a known fixture, no new judgement calls.

**S2 — Alternatives arenas.** After S1, before Scout. It populates the one
input Scout's "classify as adopt / base on / extend / inspire / avoid" needs
that does not already exist — a community-ranked statement of *what is better
for what*, which is what makes "adopt" more than a fit score. Populating an
arena needs no new schema (`arenas` and `arena_entries` already accept
`EntityProject`) and no new UI, so it is the cheapest remaining milestone that
adds a fact rather than a view.

**S3 — Scout.** Last, because it is the only one of the three that needs a new
concept (the classification) and it consumes the other two.

The force is not only dependency. Scout's honest failure mode on today's data
is the same one that capped the Finder: the live catalog holds 4.68 bits of
entropy across every hard-constraint dimension and 3 of 12 dimensions clear the
ask threshold. A build-vs-adopt report built on that data would be
arithmetically correct and would say nothing. S1 and S2 add data, not views
over the same data, so they are the ones that make Scout worth having.

## 4. S1 — Project page surfaces

### 4.1 Scope

Three read-only panels on `/projects/{slug}`, in §4.10's order:

1. **Capabilities** — the project's assertions with their state
   (`asserted` / `confirmed` / `disputed`) and the value, plus the count of
   capabilities nothing is known about. Disputed claims are shown *as disputed*,
   never rewritten to `no`.
2. **Field reports** — the reports, the outcome rate with its **sample size**
   beside it, the caveat text, and owner responses. A rate is never shown
   without its denominator: "83% of reporters" from one report is a different
   claim from 83% from eleven.
3. **Solutions** — the feature's ranked solution standings, with coverage and
   confidence, using the existing `ListSolutions` payload.

Plus, if it is cheap and honest: the alternatives panel, which reads whatever
arena S2 populates and renders an explicit empty state before then.

### 4.2 Rules

- **Visibility first.** All three are behind `projectReadable`. A panel that
  leaks a private project's capability set is worse than no panel: the Finder
  already made this decision for the same data (`internal/httpapi/finder.go`
  returns 404, not 403, precisely so a slug cannot be probed).
- **No new page.** One route, one template, one JS file. `project.html` gains
  three sections; a new template would be a second place for the same project's
  facts.
- **Disputed is a value, not an absence.** A disputed capability claim renders
  as disputed. The Finder engine counts a stored `unknown` as an unknown bucket
  (`docs/specs/finder-spec.md` §2.4); the page must not flatten it to `no`.
- **Rates carry their denominator**, always, from the `FieldReportOutcomeRate`
  triple that already returns `(rate, sampleSize, error)`.
- **Bounded.** Cap each panel (capabilities: all; field reports: newest 10 with a
  count; solutions: top 10 with a count) so a project with 400 assertions does
  not produce a 40,000-node page. Say how many were not shown.

### 4.3 Definition of done

- Three httptest page tests asserting the panel's data comes from the API and
  not from a hardcoded literal (the failure mode that already bit
  `test_project_detail_features`, which asserted 3 cards for two commits).
- One httptest asserting a private project's page does not render any of the
  three.
- A Playwright test driving `/projects/{slug}` against a seeded fixture with
  known values in all three panels, asserting the values appear and that a
  disputed claim reads as disputed.
- `make verify` green.

### 4.4 Shipped — Capabilities and Field reports (2026-10-03, `fd2ceae` + `633cdcc`)

Both panels are live on `/projects/{slug}` and covered at three layers: the API
tests assert the JSON, `project_panels_script_test.go` asserts the script, and
`project_panels_e2e.py` asserts the rendered DOM in real Chromium. Twelve browser
tests; three named mutants killed.

What the live build changed, which is the reason this section exists:

- **`requireProjectID` was an existence oracle.** It answered
  `{"error": "not found: project \"slug\""}` for an absent project and a bare
  `{"error": "not found"}` for an unreadable one — same status code, different
  body, which is exactly what §4.2's visibility rule exists to prevent. Fourteen
  files route through it, so all of them leaked. Fixed in the helper, not in
  `mapError`, whose per-sentinel detail is worth keeping on routes where the
  caller is entitled to ask.
- **The route must be top-level `/api/v1/projects/{project_id}/…`.** Registered
  inside the existing `/{slug}` block, chi binds the parameter as `slug`, the
  helper reads `project_id`, gets `""`, and every call 404s with `project ""` — an
  error naming the data rather than the route. It cost about an hour.
- **Two shapes of "don't know" stay apart**, and the panel is where that is
  decided: a capability nobody has asserted, versus an assertion valued
  `unknown`. The panel says `no claim yet` for the first, and deliberately NOT
  `not reported` — on that row the other reading is "somebody reported that it is
  unknown", which is the second state, while field reports sit one screen below
  using the word correctly.
- **`outcome_rate` is absent, never 0, with no reports.** The
  `outcome_rate != null` guard first sat after an early return on empty reports,
  so its false branch was unreachable: `if (true)` was indistinguishable from the
  real guard and a mutation run reported it SURVIVED. The behaviour was right; the
  guard was decorative. It now governs both paths.
- **A failed fetch and an empty panel are different states** and render
  differently. Both being an empty state is how a broken endpoint passes for a
  project with no data.
- **The evidence behind a claim was being dropped.** The API returned it and the
  table discarded it, which reduced every claim to an unfalsifiable bare value —
  "yes" from a project that enforces WIP limits and "yes" from one that heard of
  them rendered identically.

Not done in §4: the **Solutions panel**, the third of the three. §4.7's
per-environment split IS shown, from the same handler.

The §4.3 definition of done says "three httptest page tests". Nine API tests and
ten script tests were written instead, plus twelve browser tests. Two reasons,
both structural rather than thoroughness: the API and the script are different
questions and merging them produced three wrong assertions in a row, and a test
that pins a badge's exact wording is a change-detector that fails on a
legitimate edit.

## 5. S2 — Alternatives arenas

### 5.1 Model

An alternatives arena is `(project, use_case)` with `EntityProject` entries —
already expressible: `FindArena` handles `ArenaAlternatives` on exactly
`(type, project_id, use_case)`. What is missing is who creates one, who joins
it, and how it is read.

- `POST /api/v1/projects/{project_id}/alternatives` — `{use_case, question}`
  creates the arena (contributor+).
- `POST /api/v1/projects/{project_id}/alternatives/{arena_id}/entries`
  `{project_id}` adds a competing project. Refuse the arena's own project
  (`refuseOwnVote`'s sibling rule) and refuse a project the caller cannot see.
- The existing `POST .../vote` on the arena is reused unchanged: `CastArenaVote`
  already takes `(arenaID, voter, aType, aID, bType, bID, ...)`, so ranking
  projects needs no new vote code at all. This is the point of R1.
- `GET /api/v1/projects/{project_id}/alternatives` — the ranked list with
  `better_for` / `worse_for` read from the arena's `use_case`.

### 5.2 Rules

- **No baseline.** The do-nothing competitor (§6.3) is a *solution* concept —
  for an alternatives arena "do nothing" is "keep using X", which is the arena's
  own subject and not a competitor to it. `setBaseline` must never be called for
  an alternatives arena, and `ArenaLeaderboard` must not expect one.
- **Visibility on both sides.** A candidate project you cannot see cannot be
  added and must not be ranked into a response you can see. Same anti-enumeration
  reasoning as Finder.
- **A use case is required.** `FindArena` keys on `use_case = ''` for the other
  two shapes, so an empty use case would collide with them under a different
  type. Reject empty.
- **Migration note on the edge is a document**, not a column — §7.3 wants
  migration notes "community-written, ranked" attached to each edge. Deferred
  and recorded; the edge itself is `pairwise_votes`, which is append-only.

### 5.3 Definition of done

- `TestAnAlternativesArenaRanksProjectsNotFeatures` — two projects voted, the
  leaderboard order changes and both entries are `entity_type='project'`. This
  is the test that fails if S2 quietly reuses the feature arena.
- `TestAVoteOnAProjectPairMovesBothProjectsRatings` — and the same vote does
  NOT touch any feature's rating (the mirror image of
  `TestAVoteCannotCrossIntoAnotherFeatureArena`).
- `TestTheArenaCannotContainTheProjectItIsAbout`.
- `TestAnAlternativesArenaHasNoDoNothingEntry`.
- `TestAnAlternativesArenaRefusesACandidateTheCallerCannotSee`.
- `TestAnEmptyUseCaseIsRefused` — and that the refusal names the use case.
- Playwright: an alternatives panel on the project page showing a real ranking.
- A mutation gate over the new code, with the "own vote" and "no baseline"
  rules as mutants.

## 6. S3 — Scout

### 6.1 Scope

Phase 1 of §4.5.1, and deliberately not all nine steps:

- Free-text input (no stack-file parsing, no repo link, not yet).
- **Decompose** into capabilities — from the text, using the seeded capability
  keys. Editable by the user, which is what the spec asks for and what makes the
  decomposition reviewable rather than authoritative.
- **Match** each capability to catalog projects with the Finder's fit scorer
  (`internal/finder/score.go` already renormalises over available evidence and
  reports `evidence_coverage`).
- **Classify** each match: `adopt` / `base-on` / `extend` / `inspire` / `avoid`,
  with the reason. Rules, stated so they are testable:
  - `avoid` when the project is abandoned (health below a stated floor) or its
    license is incompatible with a stated constraint.
  - `base-on` when the project is the top match on the *most* capabilities and
    has a baseline-shaped relationship (a healthy, licensed, actively-maintained
    project beats a fork request).
  - `adopt` when it satisfies a *subset* — a library to depend on rather than a
    base to fork.
  - `extend` when the gap is a narrow, linked, open complaint.
  - `inspire` when it is in the catalog, matches, and is dead or archived.
  Every classification carries the signals it used. An unexplained
  classification is a bug, because the whole surface exists to answer "why did
  it say that".
- **Hard-constraint checks**: license, language, platform, self-hosting — from
  the capability matrix and the project columns that already exist.
- **Coverage heatmap**: capabilities covered / partly covered / open, from the
  assertions' states.
- **Surface known pain**: unresolved complaints and low field-report outcomes
  for the matched projects.
- **A Scout Report**: a `project_documents` row of kind `adr`… no — of a kind
  that does not exist yet. `DocumentKinds` is a CHECK-constrained list
  (`readme|spec|plan|wiki|adr|changelog`), so a scout report needs a migration to
  add `'scout'`. Stated rather than smuggled into an existing kind.

### 6.2 Out of scope, with reasons

| deferred | why |
|---|---|
| stack-file parsing (`Cargo.toml`, `package.json`) | a parser per ecosystem; the free-text path exercises the same downstream |
| repo links | needs the catalog ingest surface, which is Phase 0 |
| live collaborator calls | no such API exists |
| "one click becomes a project" | creates projects from a report; that is a governance surface (charter seeding), not a discovery one |
| export to Markdown/JSON | `project_documents` already serves Markdown; JSON is a `GET` on the API |
| dependency intelligence | §4.9 needs a dependency graph that does not exist |

### 6.3 Definition of done

- `TestScoutDecomposesAnIdeaIntoSeededCapabilities` — and
  `TestScoutNeverInventsACapabilityOutsideTheCatalog` (a decomposition naming a
  capability key no row exists for must be reported as a *new* capability
  proposal, never scored against nothing).
- `TestScoutReturnsAReasonForEveryClassification` — a report whose any
  candidate lacks `signals` fails.
- `TestAnAbandonedProjectIsClassifiedAvoidWithAReason` — the honest telemetry
  test, the same shape as `TestArenaStandingIsComputedNotConstant`: with today's
  data `health_score` is 0 for all 70 projects, so a health floor keyed on it
  would classify everything `avoid` and pass a test asserting "at least one
  avoid". Two fixtures with different health scores must classify differently.
- `TestASubsetMatchIsAdoptAndAMaximalMatchIsBaseOn` — the two verdicts are
  distinguishable.
- `TestScoutReportsEvidenceCoverageBesideFit` — the same honesty the Finder
  fix established: a fit percentage without its coverage is misleading.
- A Scout page and a Playwright test driving it end to end.
- Live: `GET /api/v1/scout?idea=...` on the running instance returns a report,
  not `{"error":"not found"}` — the deploy check that caught an unreachable
  route last time.

## 7. Things recorded but not planned

- **`docs/concord-spec.md` is two revisions stale** (KNOWN-ISSUES §"docs/
  concord-spec.md"). It remains a verbatim copy of a master that r4 replaced. Not
  fixed here because it is a copy discipline problem, and rewriting it now would
  produce a third stale copy.
- **`arenas.baseline_entry_id` is still unpopulated** while
  `arena_entries.is_baseline` is the authority. Two representations of one fact,
  one unwritten. This is a known issue from before S2 and S2 makes it worse,
  because an alternatives arena is the first arena type where the question
  "what is the baseline here?" has a real answer ("keep using X") and no place to
  put it. Fix it before S2, or state in S2's docs that alternatives arenas
  deliberately have none.
- **No GraphQL.** Excluded in `PLAN-r4.md` and in the Finder spec. Unchanged.