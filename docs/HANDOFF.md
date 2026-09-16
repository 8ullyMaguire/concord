# Concord — handoff

For the implementing agent taking over from here. Read this, then
`docs/PLAN.md`, then `docs/concord-spec.md` (§2, §4, §5, §15, §16), then
`docs/ARCHITECTURE.md`.

## What exists right now (verified)

- **Full platform, building and tested.** `make verify` is green: gofmt,
  vet, all tests, CGO-free build of `bin/concord` (~21MB). `make verify`
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

```
cmd/concord/          entrypoint: flags → config → db open → migrate → serve
internal/config/      env + flags (CONCORD_DB, CONCORD_LISTEN, CONCORD_FORGEJO_SECRET)
internal/db/          sqlite open, migrate, schema
internal/discovery/   health score engine + tests
internal/governance/  role levels, charter defaults, quorum math, consensus outcomes, merge gate + tests
internal/httpapi/     JSON API v1 + server-rendered web pages + templates
internal/ranking/     Glicko-2 with Illinois solver + tests
internal/store/       10 files: all CRUD operations with domain-error mapping
templates/            html/template base + page templates
web/assets/           CSS + JS for the polished website
internal/db/migrations/ SQL migration files (0001_init.sql, 0002_request_board.sql)
```

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
   and its test. Triggers come in M11.
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
   (trusted self-host model). M9 replaces it with sessions/OAuth; design
   new endpoints with that in mind.

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

| Milestone | Status | Notes |
|-----------|--------|-------|
| M1 — Complaint lifecycle | Complete | CRUD + impact + validate + merge |
| M2 — Feature lifecycle | Complete | CRUD + strategic weight |
| M3 — Pairwise voting | Complete | Glicko-2 + pair selection |
| M4 — Consensus engine | Partial | Create + position + objection + close stubs exist; full objection lifecycle and quorum checks need M4 completion |
| M5 — Board + charter | Partial | Board CRUD exists; charter endpoints, WIP gates, board.move authorization pending |
| M6 — Merge layer + Forgejo webhooks | Partial | MR CRUD + approve + execute + reject exist; webhook receiver, bot-merge, CheckMergeGate pending |
| M7 — Threads | Not started | Nested comments, labels, votes, soft delete |
| M8 — Collaborative lists | Not started | Generalized consensus calls, list FTS, entry ranking |
| M9 — Request board | Partial | Store + API exist; fit ranking, duplicate answers, quorum removal pending |
| M10 — Identity & auth | Not started | Sessions, Forgejo OAuth2 |
| Web polish | Complete | html/template, CSS, JS, responsive design, proper 404 |

## Next steps

1. **M4 consensus objections** — implement `WithdrawObjection`,
   `ResolveObjection`, `VetoObjection` in store + API handlers.
2. **M5 board gates** — add role-based authorization to `handleMoveCard`,
   implement charter endpoints, add WIP limit enforcement.
3. **M6 Forgejo webhooks** — implement `POST /api/v1/hooks/forgejo`
   with HMAC verification, bot-merge client.
4. **M7 threads** — add `comments` and `labels` tables, nested comment CRUD.
5. **M8/M9 completion** — generalized decision targets, fit ranking,
   duplicate answer prevention, quorum-based spam removal.
6. **M10 auth** — cookie sessions + Forgejo OAuth2.

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
