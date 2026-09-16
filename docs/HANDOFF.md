# Concord — handoff

For the implementing agent taking over from here. Read this, then
`docs/PLAN.md`, then `docs/concord-spec.md` (§2, §4, §5, §15, §16, §17),
then `docs/ARCHITECTURE.md`.

## What exists right now (verified)

- **Full platform, building and tested.** `make verify` is green: gofmt,
  vet, all tests, CGO-free build of `bin/concord` (14.2MB). `make verify`
  must pass before every commit. NOTE: green verify means the *old* tests
  still pass — most new store code has no tests at all (see Known issues).
- **Schema** (`internal/db/migrations/0001_init.sql` + `0002_request_board.sql`):
  the full spec data model — identity, charters, complaints, features,
  votes, consensus, objections, board, merge layer, reputation, audit,
  discovery (project_tags/languages/metrics + `projects_fts` FTS5),
  lists (`lists`, `list_entries`, `list_entry_votes`), and the request
  board (`requests`, `request_answers`, `request_answer_votes`).
- **Pure engines, tested:** `internal/ranking` (Glicko-2 with the
  Illinois solver — validated against Glickman's published worked
  example r'=1464.06, RD'=151.52, σ'=0.05999; pain scores; priority;
  vote weight), `internal/governance` (role levels, charter defaults,
  quorum math, the five consensus outcomes, merge gate),
  `internal/discovery` (transparent health score).
- **Exemplar vertical:** project CRUD + first-class search
  (`/api/v1/search` with FTS, tag/language/health/model/license filters,
  facets, sorts). Copy this pattern for every new domain.
- **Store layer** (10 files): `board.go`, `complaints.go`, `consensus.go`,
  `features.go`, `lists.go`, `merge.go`, `requests.go`, `search.go`,
  `store.go`, `votes.go`. Each exposes CRUD + domain-error mapping
  following the `projects.go`/`search.go` exemplar pattern.
- **HTTP API** (5 files): `handlers.go` (shared helpers, web page handlers,
  healthz), `projects.go` (CRUD + tags + languages + metrics),
  `search.go` (FTS search), `server.go` (router + NewServer constructor),
  `httpapi_test.go` (test harness with `newTestServer` + `doJSON`).
- **Web UI** — server-rendered with `html/template`:
  - **Templates** (6 files): `templates/base.html` (layout with header,
    nav, footer, flash messages), `templates/index.html`,
    `templates/search.html`, `templates/projects.html`,
    `templates/project.html`, `templates/board.html`.
  - **CSS** (`web/assets/css/style.css`): responsive design with CSS
    variables, card/button/form/table/board styles, mobile breakpoints.
  - **JS** (`web/assets/js/{search,project,board}.js`): client-side
    hydration — search form, project detail loading, kanban board
    rendering.
- **Server rendering**: `NewServer(store, version)` loads templates
  gracefully (nil if missing), `s.render()` uses `html/template` with
  base layout, chi `NotFound` handler returns JSON `{"error": "not found"}`.
- **Docs:** spec (verbatim copy — the vault is the master), premise
  (shareable), architecture, this file, plan.

## Structure

See `docs/ARCHITECTURE.md` "Package map" — that is the single source of
truth for the layout. Additions since the skeleton: `internal/store/` now
has one file per domain, `internal/httpapi/handlers.go` holds the newer
API handlers plus web page handlers, and `templates/` + `web/assets/`
hold the server-rendered UI. Do not maintain a second map here; it drifted
once already.

## Environment facts

- Project root: `~/code/projects/concord` — this is an sshfs mount to
  thinkcentre's pool (`~/mnt/thinkcentre/personal/documents/code/...`).
  Builds are fine (Go caches live on local disk); don't be surprised by
  slower file ops.
- Go 1.27.1 installed via pacman. Forgejo reference clone:
  `~/code/upstream/forgejo` (shallow, 2026-09-16).
- Never put live SQLite on the NFS pool — the default DB path is local
  (`$XDG_DATA_HOME/concord/concord.db`); tests use `t.TempDir()`.
- Default listen address `127.0.0.1:8410`.

## Commands

```
make verify    # vet + test + build — REQUIRED green before every commit
make test      # go test -timeout 300s ./...
make run       # go run ./cmd/concord (respects CONCORD_DB / CONCORD_LISTEN)
make build     # bin/concord
```

Quick API tour:

```
curl -s localhost:8410/api/v1/healthz
curl -s -X POST localhost:8410/api/v1/projects -H 'Content-Type: application/json' \
  -d '{"slug":"demo","name":"Demo"}'
curl -s -X PUT localhost:8410/api/v1/projects/demo/tags -H 'Content-Type: application/json' \
  -d '{"tags":["governance"],"applied_by":"<username>"}'
curl -s 'localhost:8410/api/v1/search?tag=governance&min_health=0.5&sort=health'
```

Quick web tour:

```
# Visit http://localhost:8410/           — homepage with CTA and feature overview
# Visit http://localhost:8410/search     — search form with query support
# Visit http://localhost:8410/projects   — project listing page
# Visit http://localhost:8410/projects/demo/board — kanban board
```

## Things that will bite you (learned the hard way here)

1. **MaxOpenConns(1) + open cursor = deadlock.** `SearchProjects` closes
   its rows explicitly before faceting for a reason. Never query while a
   cursor is open; keep the 300s test timeout so hangs fail instead of
   hanging CI.
2. **FTS5 rows are rebuilt on write** by `ReindexProject` (tags, languages,
   name, description). If you add a searchable field, update the reindex
   and its test. Triggers come in M12.
3. **FK columns with "optional" ids:** pass `NULL`, not `0`
   (`project_tags.applied_by = 0` fails the FK — handled in
   `ApplyProjectTag`; keep the pattern).
4. **Glicko-2 chord:** the Illinois iteration is
   `C = A − fA·(A−B)/(fA−fB)`. The `fA−fB` sign matters; anchoring the
   chord at `a`, or flipping the denominator, diverges (both mistakes were
   made and caught by `TestRateGlickmanExample`). Don't touch the solver
   without rerunning that test.
5. **vet parses `%` in test failure strings** — write "50 pct", not "50%".
6. **Auth is interim:** mutating endpoints trust a body `actor` username
   (trusted self-host model). M10 replaces it with sessions/OAuth; design
   new endpoints with that in mind. Interim does NOT mean permissionless:
   role checks (M1 ACs, M5 gates) are still required and currently missing.

## How the reviewer (Hermes) will check your work

- Runs `make verify` and reads diffs. Green tests are the floor, not the
  goal: tests must assert the *spec's* behavior (quorum math, gates,
  decay), not just "the handler returns 200".
- Checks `docs/PLAN.md` status honesty against the actual tree.
- Reads `docs/QUESTIONS.md` first and answers your ASSUMED items.
- Will reject: governance simplification (no-quorum shortcuts), silent
  permission widening, search regressions (facets/filters broken),
  un-logged privileged actions, new heavy dependencies.
- Keeps `docs/concord-spec.md` byte-identical to the vault master; propose
  spec changes via `docs/QUESTIONS.md` instead.

## Milestone status

Honest states after the 2026-09-16 review (reviewer-verified against the
tree). "Complete" requires `make verify` green AND the milestone's AC
tests from PLAN.md — none of M1–M3 met that bar; labels corrected.

| Milestone | Status | Notes |
|-----------|--------|-------|
| M1 — Complaint lifecycle | code present, untested | store + handlers exist; zero tests, no permission checks, PainScore wired with age=0 and hardcoded halflife 90 — charter decay ignored |
| M2 — Feature lifecycle | code present, untested | CRUD + strategic weight; no tests, no role gate despite the function comment claiming "Maintainer+ only" |
| M3 — Pairwise voting | code present, PARTIALLY WIRED | RecordVote now calls ApplyPairwiseVote and ratings move; GetNextPair fallback fixed; scan errors propagated; VoteWeight/PriorityScore still unused, no priority endpoint, no M1–M3 AC tests |
| M4 — Consensus | partial | create/position/objection/close exist; EvaluateConsensus now uses project's governance_model (was hardcoded DefaultCharter(Collective)); objection lifecycle + role auth still missing; untested |
| M5 — Board + charter | partial | board CRUD exists; charter endpoints, WIP gates, move authorization pending; untested |
| M6 — Merge + webhooks | partial | MR CRUD/approve/execute/reject exist; CheckMergeGate NOT wired into execute; no webhook receiver, no bot-merge client; untested |
| M7 — Threads | not started | comments + comment_votes tables already exist in 0001; labels table missing |
| M8 — Lists | not started | schema in 0001; generalized decision targets are the prerequisite |
| M9 — Request board | code present, untested | store + API exist; fit ranking not wired (same gap as M3); affiliation labels, duplicate-answer handling, quorum removal missing |
| M10 — Identity & auth | not started | body-actor interim; role gates missing project-wide |
| M11 — Web UI | partial | 6 templates + CSS/JS exist and render; built before M1–M9 gates (ordering violation); no golden-page tests |
| M12 — Sync + hardening | not started | |

## Known issues from review (fix before new features)

### Fixed (verified, commit f01baa9)

1. **Votes don't rank (was CRITICAL, spec §6).** `store.RecordVote`
   now computes weight via `ranking.VoteWeight` (reputation decay
   + role mult + charter cap), applies `ranking.ApplyPairwiseVote`,
   persists new ratings + vol. Glicko-2 ratings actually move now.
2. **Charter ignored at decision time.** `consensus.go` now queries
   the project's `governance_model` from the DB and uses
   `governance.DefaultCharter(gm)` instead of hardcoded
   `DefaultCharter(Collective)`. `complaints.go` still hardcodes
   halflife 90 / age 0 — needs charter wiring.
3. **GetNextPair fallback bug.** The "fallback: closest pair
   overall" block now checks `votedPairs` so it doesn't re-offer
   already-voted pairs. Scan errors are now propagated instead of
   silently swallowed.
4. **Web UI broken.** `render()` was calling `ExecuteTemplate(w,
   "index.html", ...)` but `index.html` only defines `"content"`,
   not `"index.html"`. Fixed to use `"base.html"` as layout.
   `handleIndex` had a broken path check that caused root `/` to
   return JSON `{"error": "not found"}`. Both fixed.

### Still pending

5. **No role enforcement anywhere.** Any named actor can validate
   complaints, set strategic weight, close consensus, move cards,
   and execute merges. Add the role checks per the M1/M5 ACs.
6. **Complaints charter wiring.** `complaints.go` still hardcodes
   halflife 90 / age 0 for PainScore — must read the project's
   charter (already loaded in `store.go`).
7. **Test debt.** ~1,500 lines of store code with zero test files
   and no new httpapi tests. PLAN's ground rule 1 and the ACs make
   tests the definition of done, not decoration.

### Deployment

- **Service:** `systemctl --user enable --start concord.service`
  (runs `bin/concord` on `0.0.0.0:8006`, SQLite at
  `~/.local/share/concord/concord.db`)
- **Cloudflare route on port 8006** — if `https://concord.polarisocial.xyz/`
  shows Icecast2 Status, Cloudflare cache needs purging or the
  origin config needs checking (icecast is not installed on the
  system; Concord is the only service on port 8006)

## Next steps

1. **Write M1–M3 AC tests** (PLAN ground rule 1) — permission
   denials, weight-scaled rating movement, decayed pain vs a
   hand-computed value, priority ordering. Tests are the
   definition of done, not decoration.
2. **Complaints charter wiring** — `complaints.go` hardcodes
   halflife 90 / age 0 for PainScore; must read the project's
   charter from `store.go`.
3. **Role enforcement** — add role checks per the M1/M5 ACs.
   Any named actor can currently validate complaints, set
   strategic weight, close consensus, move cards, and execute
   merges.
4. **M4 consensus completion** — objection lifecycle
   (`WithdrawObjection`, `ResolveObjection`, `VetoObjection`),
   project charter in evaluation (done), quorum-gated close,
   role-gated early close.
5. **M5 board gates** — role-based authorization in
   `handleMoveCard`, charter endpoints, WIP limit enforcement.
6. **M6 Forgejo webhooks** — `POST /api/v1/hooks/forgejo`
   with HMAC verification (constant-time), webhook receiver
   with signature check.
7. **M10 auth** — sessions (cookie), Forgejo OAuth2 login
   with email-based role resolution.
8. **Test debt** — zero test files for ~1,500 lines of store
   code. Start with store tests for complaints/features/votes,
   then httpapi tests for permission denials.
9. Keep new UI work behind the working API — no more UI before the gates
   and tests exist.

## Current decision log (do not re-litigate silently)

- Ground-up service, Forgejo as unmodified substrate (OAuth + webhooks +
  bot-mediated merges). Soft-forking Forgejo was evaluated and rejected
  (merge-tax on a security-sensitive codebase; the decision engine shares
  nothing with git plumbing).
- `database/sql` + plain SQL over XORM; SQL-file migrations over Go
  migrations; SQLite now, Postgres later. All in ARCHITECTURE.md.
- Governance: collective by default; merge = reviewer approval + quorum;
  admin-light; lists and moderation run through the same quorum machinery.
- Request board accepted (spec §17, PLAN M9): answers are project
  references ranked by pairwise fit votes; self-promotion is labeled, not
  banned; spam removal goes through quorum; the author's accepted-answer
  marker is display-only and never feeds the ranking. Semantic
  auto-suggestion of candidate answers (local embeddings) is a possible
  post-M12 addition — not committed.
