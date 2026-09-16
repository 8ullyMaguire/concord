# Concord — handoff

For the implementing agent taking over from here. Read this, then
`docs/PLAN.md`, then `docs/concord-spec.md` (§2, §4, §5, §15, §16, §17),
then `docs/ARCHITECTURE.md`.

## What exists right now (verified)

- **Full platform, building and tested.** `make verify` is green: gofmt,
  vet, all tests (including 21 AC tests in `internal/store/store_test.go`),
  CGO-free build of `bin/concord` (~21MB). `make verify`
  must pass before every commit.
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

Honest states after the fourth-pass review (2026-09-16, reviewer-verified
against the tree AND the live service). "Done" requires the PLAN-specified
AC tests — the new store tests are real but CRUD-level; several ACs are
still untested, so "done" rows were re-graded.

| Milestone | Status | Notes |
|-----------|--------|-------|
| M1 — Complaint lifecycle | store-tested | CRUD + charter-wired pain tested; AC permission-denial tests (user cannot validate, guest cannot file) still missing |
| M2 — Feature lifecycle | store-tested | CRUD tested; strategic weight has 401 but no maintainer-threshold check |
| M3 — Pairwise voting | WIRED, live | contract coherent (body pair, no re-selection), voter reputation via GetReputation, charter tau, exhaustion errors; AC tests for rating-movement and weight-scaling still missing; priorities endpoint exists but live probe returns "feature 0" error — route shape suspect |
| M4 — Consensus | partial | model-aware close; five-outcome table-driven test and objection lifecycle missing |
| M5 — Board + charter | partial | board CRUD tested; gate matrix, WIP overflow, charter endpoints missing |
| M6 — Merge + webhooks | partial | MR CRUD tested; HMAC webhook receiver, CheckMergeGate wiring, bot client missing |
| M7 — Threads | not started | comments tables exist in 0001; labels missing |
| M8 — Lists | store-tested | CRUD tested; quorum admission flow and entry ranking missing |
| M9 — Request board | store-tested | CRUD tested; fit ranking, affiliation labels, quorum removal missing |
| M10 — Identity & auth | partial | 401s live on 13 handlers; contributor+ check only on vote; sessions/OAuth and token round-trip untested |
| M11 — Web UI | LIVE | embedded templates+assets render on thinkcentre:8007 (verified); golden-page tests missing |
| M12 — Sync + hardening | in progress | deployed on 8007 (127.0.0.1, systemd user unit, active); CF origin re-point pending (owner) |

## Known issues (fourth pass, reviewer-verified)

### Fixed and verified

1. **Vote contract.** Body `feature_a + feature_b + outcome`; both
   features validated against the project; no server-side re-selection.
2. **Single weight, real reputation.** Handler computes
   `VoteWeight(GetReputation(project, actor), 1.0, cap)`; `RecordVote`
   applies exactly that weight with `charter.GlickoTau`.
3. **Complaints charter wiring.** `GetComplaintPain` uses
   `charter.PainHalflifeDays` with real age.
4. **Exhaustion.** `GetNextPair` errors when no unvoted pairs remain.
5. **Deployment is real.** systemd user unit active on thinkcentre,
   127.0.0.1:8007 listening, healthz + homepage + embedded CSS/JS
   verified by the reviewer via ssh.
6. **Remotes in sync.** github and forgejo both at HEAD (51485be).
7. **21 store tests** exist and pass; several real schema/scan bugs were
   fixed while writing them (GetUser columns, CreateFeature tx order,
   NULL handling, etc.).

### Still pending (critical first)

1. **Tests are CRUD-grade, not AC-grade.** `TestRecordVote` asserts
   "at least one vote" — not that ratings MOVED. No weight-scaling test,
   no permission-denial tests, no five-outcome consensus test, no gate
   matrix. PLAN's "done" definition is the AC tests; the milestone table
   was re-graded accordingly.
2. **Two dead/broken tests.** `TestGetNextPairExhausted` discards the
   error (`_ = err`) and asserts nothing; `TestGetNextPair`'s condition
   is logically wrong and passes trivially. Fix both to assert real
   behavior.
3. **Non-hermetic test DB.** Shared `/tmp/concord_test.db` with
   `isUniqueViolation` tolerance — order-dependent state. Use
   `t.TempDir()` per test (or a per-package reset).
4. **Granular roles.** 401-for-unknown exists on 13 handlers, but
   maintainer/reviewer thresholds (strategic weight, board done-move,
   merge execute, consensus early close) are unenforced.
5. **Priorities endpoint shape.** Live probe
   `/api/v1/projects/1/features/priorities` returns
   `{"error":"not found: feature 0"}` — a project-scoped listing should
   not reference feature 0. Check the route registration and handler.
6. **Reputation writers.** `GetReputation` reads `reputation_events`,
   but no flow writes events yet (validate/merge/position/rep penalties).
7. **M4 objection lifecycle, M6 webhooks, audit coverage** of privileged
   actions — unstarted.

### Deployment (live, verified via ssh 2026-09-16)

- thinkcentre: systemd **user** unit `concord.service` ACTIVE,
  `127.0.0.1:8007` listening, binary at `/home/alvaro/concord-deploy/concord`,
  DB at `~/.local/share/concord/concord.db` (local disk — policy OK).
- Verified by reviewer: healthz `{"status":"ok"}`, homepage renders
  embedded HTML, `/assets/css/style.css` 200, `/assets/js/search.js`
  serves. Live projects list is `[]` — the earlier claim "4 projects
  returned" does not match the live DB.
- Cloudflare still serves Icecast's 400 page from the origin on port
  8006. **This is the owner's dashboard action** (it was not done):
  change the origin/ingress port for `concord.polarisocial.xyz` from
  8006 to 8007 in the Cloudflare dashboard (or the tunnel config). Do
  NOT "update the DNS A record to 127.0.0.1" (nonsense — that was in the
  previous handoff revision) and do NOT stop icecast2 (different port,
  unrelated).
- Deploy hygiene: the binary copy to `~/concord-deploy/` is manual.
  Add a `make deploy` target (build → scp → restart unit → healthz curl)
  so the deployed binary always matches HEAD, and re-deploy after every
  merged milestone.

## Next steps (owner-set priority, 2026-09-16, fourth pass)

1. **Upgrade the store tests from CRUD-grade to AC-grade** — this is the
   single highest-value task:
   - `TestRecordVote`: assert elo_r actually moved (and moved the right
     direction for each outcome).
   - Add `TestVoteWeightScaling`: two voters with different reputation →
     proportional rating deltas.
   - Add `TestConsensusOutcomes`: table-driven, all five results through
     the store.
   - Add `TestPermissionDenials`: guest/user vs contributor/maintainer
     actions on validate, strategic weight, close, move, merge.
   - Fix `TestGetNextPairExhausted` (assert the error; currently `_ = err`)
     and `TestGetNextPair` (broken condition, passes trivially).
   - Make `setup()` hermetic: `t.TempDir()` per test instead of the shared
     `/tmp/concord_test.db`.
2. **httpapi tests**: 401 for unknown actor and role denials on all
   mutating handlers; a full vote round-trip through the HTTP API
   (fetch pair → vote → assert ratings moved).
3. **Granular roles**: a `requireRole(project, actor, minRole)` helper;
   apply the PLAN matrix (strategic weight = maintainer+, board done =
   maintainer+, merge execute = reviewer+, consensus early close =
   maintainer+).
4. **Priorities endpoint**: fix the route/handler so a project-scoped
   listing doesn't error with "feature 0" (live probe).
5. **Reputation writers**: emit reputation_events from validate, merge,
   consensus positions, moderation, spam penalties — GetReputation is
   reading a table nothing writes.
6. **M4 objection lifecycle, M6 webhooks (HMAC, constant-time), audit
   coverage** for every privileged action.
7. **Deploy hygiene**: add `make deploy` (build → copy to
   `~/concord-deploy/` → restart unit → healthz curl from thinkcentre);
   re-deploy the current HEAD; do NOT claim deployment state without that
   curl.
8. **Cloudflare re-point is the OWNER's dashboard action** (origin port
   8006 → 8007). Ask; don't document guesses about DNS.
9. Then M7 threads and M8/M9 depth per PLAN. UI polish stays frozen until
   the gates and AC tests exist.

