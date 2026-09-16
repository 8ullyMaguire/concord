# Concord — build plan

This is the working roadmap for the implementing agent (any agent or
human). Work top to bottom; do not skip ahead. `docs/concord-spec.md` is
the contract — read §2, §4, §5, §15, §16 before writing code.

## Ground rules

1. **`make verify` must pass before every commit** (gofmt-clean, vet,
   tests, build). Commit small: one milestone task per commit, message
   style `domain: summary` (e.g. `complaints: validate transition + audit`).
2. **Governance invariants are not negotiable** (docs/ARCHITECTURE.md
   "Governance invariants"). If a test seems easier without the quorum
   gate, the test is wrong.
3. **The spec is a copy, not the master.** Never edit
   `docs/concord-spec.md`; propose spec changes in `docs/QUESTIONS.md`.
4. **New dependencies require a written justification** in
   `docs/QUESTIONS.md` plus an ARCHITECTURE.md note. stdlib-first. The
   stack contract is chi + database/sql + modernc sqlite; do not add an
   ORM, a web framework, or a CSS framework.
5. **Follow the exemplar verticals.** Project CRUD + search
   (`internal/store/store.go`, `internal/store/search.go`,
   `internal/httpapi/projects.go`, `internal/httpapi/search.go`) show the
   handler → store → SQL pattern, domain-error mapping, test setup via
   `newTestServer`, and the FTS reindex rule. Copy them.
6. **Concurrency rule:** the pool is `MaxOpenConns(1)`. Never issue
   queries while a `*sql.Rows` cursor is still open in the same flow
   (see the explicit `rows.Close()` in `SearchProjects`). Prefer
   collecting rows, closing, then querying again.
7. **Unknowns and decisions you cannot make go to `docs/QUESTIONS.md`**
   (create it on first use), each with your chosen assumption marked
   `ASSUMED`. The reviewer reads it first.
8. Update the **Status** table at the bottom of this file as you go.
   Lying in the status table is the one unpardonable sin.

## Search & discovery priority

Discovery is the product owner's stated #1 priority (spec §15). When
trading polish against search quality, search wins. Every milestone that
touches data must keep the search surface coherent (reindex on write,
facets still correct, filters still composable).

## Milestones

### M1 — Complaints domain
- Store: `file_complaint` (auto card in `inbox`, audit row),
  `add_impact` ("me too", reputation-weighted affected count),
  `validate/reject/close` transitions (contributor+; card moves; rep event
  on validate; spam rep penalty on reject), `merge_complaints`
  (maintainer+; impacts and feature links move to target).
- API: `POST /projects/{slug}/complaints`, `POST /complaints/{id}/impact|validate|reject|close|merge`,
  `GET` list/detail with computed pain.
- Wire `ranking.PainScore` (severity × frequency × ln(1+affected) ×
  strategic_mult, decayed by charter halflife).
- AC: API test covers the whole lifecycle incl. permission denials
  (user cannot validate, guest cannot file) and duplicate-merge; pain
  matches a hand-computed decayed value.

### M2 — Features domain
- `propose_feature`: contributor+, ≥1 linked complaint, all linked
  complaints must be validated/linked (then flip to `linked`); card in
  `solution_draft`. `link_complaint`, tags, `set_strategic_weight`
  (maintainer+, audited with old→new).
- AC: proposing without a validated complaint → 400; strategic weight
  change appears in audit with old and new.

### M3 — Pairwise ranking + priority (search priority)
- `GET /api/v1/projects/{slug}/vote/next`: active-learning pair selection
  (`ranking` pair scoring: close ratings, high RD; exclude pairs this
  voter already voted on; fall back to closest pair).
- `POST .../vote`: outcome a/b/both/neither/skip; weight from
  `ranking.VoteWeight` (rep decay + role mult + charter cap); apply
  `ranking.ApplyPairwiseVote` (weighted rating delta, standard RD/vol);
  audit with weight.
- `GET /api/v1/projects/{slug}/priority`: ranked features with
  `priority`, `lower_bound`, `pain_sum`.
- AC: voting moves ratings in the right direction; a weight-2 voter moves
  a rating twice as far as weight-1; `neither`/`skip` change nothing but
  are recorded; priority endpoint orders by the score formula.

### M4 — Consensus engine
- Open call (contributor+ in collective, maintainer+ in maintainer_led;
  feature must be draft/discussion with ≥1 linked complaint; no second
  open call; snapshot eligible count).
- Positions: upsert (latest wins); `block` requires principle + violation
  + remedy → objections row; rep event on first position.
- Objection lifecycle: withdraw (blocker or maintainer on behalf),
  resolve, veto (maintainer+, written justification, rep penalty).
- Close: uses `governance.EvaluateConsensus`; unmet quorum → extend
  window (audit `consensus.extended`); accepted → feature `ready` + card
  move + rep; accepted_overridden → mark objections overridden + rep
  penalty; rejected → feature `rejected`; blocked → back to `discussion`.
  Early close (before deadline) is maintainer+ only.
- AC: table-driven API test producing all five results; a non-maintainer
  cannot close early; extended call keeps feature in `consensus`.

### M5 — Board + charter
- `GET /projects/{slug}/board` (columns, cards, WIP, entity summaries).
- `POST .../board/move`: contributor+; gates — into `consensus` forbidden
  (open a call instead), into `ready` forbidden in collective (maintainer+
  in maintainer_led), into `done` maintainer+ with `board.force_done`
  audit; WIP limit enforced; feature status synced from phase.
- Charter endpoints: get + update (maintainer+; `governance_model` owner+;
  validated fields; audited diff).
- AC: gate matrix test (each transition × role × model); WIP overflow → 400.

### M6 — Merge layer + Forgejo webhooks
- MR open (feature must be ready/in_progress/review; card → review),
  approve (contributor+, weight snapshot, `review_given` rep once),
  merge (reviewer+ executes; `governance.CheckMergeGate`; on success:
  feature shipped, card done, linked complaints closed, rep events),
  reject (back to in_progress).
- Webhook receiver `POST /api/v1/hooks/forgejo` (HMAC
  `CONCORD_FORGEJO_SECRET`, constant-time compare): `pr_opened` opens an
  MR, `pr_merged` merges through the gate (409 with missing requirements
  when the gate fails), `pr_closed` rejects.
- Bot-merge client (Forgejo API token via env; behind a flag, off by
  default): after a gated merge, call Forgejo's merge endpoint for the
  mapped PR.
- AC: full gate test (quorum miss → 403, reviewer miss → 403, pass →
  shipped); webhook test with signed and unsigned payloads.

### M7 — Threads
- Nested comments on complaint/feature/merge_request/project threads,
  labels (question/objection/support/evidence/offtopic), ±1 votes with
  score maintenance, soft delete (moderator flag or maintainer+).
- AC: nesting + moderation permission test.

### M8 — Collaborative lists (spec §16)
- Generalize `consensus_calls` to decision targets: migration adding
  `target_kind` + `target_id` (backfill `feature`), keeping existing
  behavior for features.
- Lists + entries: create list (user+), propose entry (any contributor;
  low-reputation proposers need a higher confirmation count — charter
  field), entry admission/removal/categorization as quorum decisions on
  the generalized calls; entry ranking via `list_entry_votes` reusing
  `ranking.ApplyPairwiseVote`; `next_entry_pair` mirroring M3 selection.
- Discovery: `lists_fts` mirroring the `projects_fts` pattern (rebuild on
  write); extend search with `type=lists|entries` and cross-list entry
  search ("every Rust debugging tool, ranked, across all lists").
- AC: propose → quorum → accepted flow test; rejected entry stays
  `rejected`; ranking moves entry order; list search returns facets.

### M9 — Request board (spec §17)
- Generalized decisions from M8 are a prerequisite (spam-answer removal is
  a quorum decision; affiliation checks reuse member/project ownership).
- Store + API: `POST /requests` (user+, title, body with constraints,
  optional project scope, tags), `GET /requests` (filter status/tag, sort
  newest/active), `GET /requests/{id}` with answers ranked by fit
  (Glicko columns; `affiliated: true` when the answerer is a member —
  owner/maintainer — of the answered project).
- Answers: `POST /requests/{id}/answers` (project_id + fit rationale;
  UNIQUE request×project → duplicate answer returns the existing one for
  editing instead of a 409), status transitions: `removed` only via a
  quorum decision (M8 machinery), author `accept` marker stored on
  `requests.accepted_answer_id` (display-only, never feeds the ranking).
- Fit ranking: `GET /requests/{id}/vote/next` (pair selection among the
  request's active answers, close ratings + high RD, exclude the voter's
  past pairs), `POST .../vote` reusing `ranking.ApplyPairwiseVote` with
  reputation-weighted votes.
- AC: ranking reorders answers after fit votes; second answer for the
  same project does not create a row; non-affiliated vs affiliated label
  correctness; spam removal requires the quorum decision, a plain
  maintainer delete is rejected; accepted marker is visible but does not
  change order.

### M10 — Identity & auth
- Sessions (cookie), Forgejo OAuth2 login (Concord as client against the
  instance at `git.polarisocial.xyz`; test with a fake provider via
  httptest), Forgejo-user → Concord-user mapping, per-project role table.
- Replace body-`actor` with the session identity for all mutating
  endpoints; keep API tokens for CLI/machine use (sha256 at rest).
- AC: unauthorized mutation → 401; role gating per project; token auth
  round-trip.

### M11 — Web UI (server-rendered, search-first)
- `html/template` pages: search (query builder + facets + saved searches —
  build this first), board, project home, complaint/feature pages, vote
  widget, consensus pages, list pages with entry ranking, and the request
  board (ask form, answer form, fit-vote widget, affiliation badges).
- No SPA framework. Concord's own CSS, small and dark-theme friendly.
- AC: golden-page smoke tests via httptest; every page reachable from the
  project home.

### M12 — Forge sync & hardening
- go-enry language percentages; metrics sync (webhooks + periodic
  recompute of health); rate limits; FTS external-content tables +
  triggers replacing rebuild-on-write; audit viewer; GDPR-style data
  export; OpenAPI spec for API v1; Postgres portability audit.
- AC: language mix endpoint populated from a fixture repo; reindex no
  longer needed on tag write (trigger test).

## Non-goals for now

ActivityPub/ForgeFed federation, email notifications, CI runners, LFS,
SSH git transport, mobile apps. Revisit after M11.

## Status

| Milestone | Status | Notes |
|-----------|--------|-------|
| M0 skeleton (schema, ranking, governance, discovery, projects+search API, docs) | done | verified: make verify green, live smoke test |
| M1 complaints | code present, untested | reviewed 2026-09-16: no tests, no role checks, decay not charter-wired |
| M2 features | code present, untested | CRUD + strategic weight; no role gate |
| M3 ranking + priority | partially wired | charter tau + server-side weight landed, but vote contract incoherent (pair re-selected server-side, outcome semantics flip) and RecordVote misuses feature strategic weight as voter weight; no priority endpoint; no tests |
| M4 consensus | partial | close is model-aware (DefaultCharter(gm)); charter-row values unwired until M5; objection lifecycle + auth missing |
| M5 board + charter | partial | board CRUD only; gates/charter endpoints pending |
| M6 merge + webhooks | partial | MR CRUD exists; CheckMergeGate not wired; no webhook receiver |
| M7 threads | not started | comments tables exist in 0001; labels missing |
| M8 lists | not started | schema already in 0001 |
| M9 request board | code present, untested | fit ranking not wired; quorum removal missing |
| M10 identity | not started | |
| M11 web UI | partial | templates/CSS/JS render; built out of order; no golden-page tests |
| M12 sync + hardening | not started | |
