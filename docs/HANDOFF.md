# Concord — handoff

For the implementing agent taking over from here. Read this, then
`docs/PLAN.md`, then `docs/concord-spec.md` (§2, §4, §5, §15, §16, §17),
then `docs/ARCHITECTURE.md`.

## What exists right now (verified)

- **Full platform, building and tested.** `make verify` is green: gofmt,
  vet, all 62 tests (29 store + 33 httpapi), CGO-free build of
  `bin/concord` (~21MB). `make verify` must pass before every commit.
- **Schema** (`internal/db/migrations/0001_init.sql` + `0002_request_board.sql`):
  the full spec data model — identity, charters, complaints, features,
  votes, consensus, objections, board, merge layer, reputation, audit,
  discovery (project_tags/languages/metrics + `projects_fts` FTS5),
  lists (`lists`, `list_entries`, `list_entry_votes`), and the request
  board (`requests`, `request_answers`, `request_answer_votes`),
  comments/threads (`comments`, `comment_votes`).
- **Pure engines, tested:** `internal/ranking` (Glicko-2 with the
  Illinois solver — validated against Glickman's published worked
  example r'=1464.06, RD'=151.52, σ'=0.05999; pain scores; priority;
  vote weight), `internal/governance` (role levels, charter defaults,
  quorum math, the five consensus outcomes, merge gate),
  `internal/discovery` (transparent health score).
- **Exemplar vertical:** project CRUD + first-class search
  (`/api/v1/search` with FTS, tag/language/health/model/license filters,
  facets, sorts). Copy this pattern for every new domain.
- **Store layer** (12 files): `board.go`, `comments.go`, `complaints.go`,
  `consensus.go`, `features.go`, `lists.go`, `merge.go`, `requests.go`,
  `search.go`, `store.go`, `votes.go`. Each exposes CRUD + domain-error
  mapping following the `projects.go`/`search.go` exemplar pattern.
- **HTTP API** (8 files): `charter.go`, `comments.go`, `handlers.go`,
  `lists.go`, `projects.go`, `search.go`, `server.go`, `webhook.go`,
  plus test harness (`httpapi_test.go`, `middleware_test.go`,
  `page_test.go`, `webhook_test.go`).
- **Web UI** — server-rendered with `html/template`, embedded via
  `//go:embed`. Templates (6 files), CSS (`style.css`), JS
  (`search.js`, `project.js`, `board.js`). Security headers + rate
  limiting middleware on all routes.
- **Docs:** spec (verbatim copy — the vault is the master), premise
  (shareable), architecture, this file, plan.

## Structure

See `docs/ARCHITECTURE.md` "Package map" — that is the single source
of truth for the layout. Additions since the skeleton: `internal/store/`
now has one file per domain (including `comments.go`), `internal/httpapi/`
has `charter.go`, `comments.go`, `lists.go`, `webhook.go` for newer
domains.

## Environment facts

- Project root: `~/code/projects/concord` — this is an sshfs mount to
  thinkcentre's pool (`~/mnt/thinkcentre/personal/documents/code/...`).
  Builds are fine (Go caches live on local disk); don't be surprised by
  slower file ops.
- Go 1.27.1 installed via pacman. Forgejo reference clone:
  `~/code/upstream/forgejo` (shallow, 2026-09-16).
- Never put live SQLite on the NFS pool — the default DB path is local
  (`$XDG_DATA_HOME/concord/concord.db`); tests use `:memory:` DB with
  migrations (`db.Open(":memory:")` + `db.Migrate()`).
- Default listen address `127.0.0.1:8007`.

## Commands

```
make verify    # vet + test + build — REQUIRED green before every commit
make test      # go test -timeout 300s ./...
make run       # go run ./cmd/concord (respects CONCORD_DB / CONCORD_LISTEN)
make build     # bin/concord
make deploy    # build → stop → copy → start → healthz verify
```

Quick API tour:

```
curl -s localhost:8007/api/v1/healthz
curl -s -X POST localhost:8007/api/v1/projects -H 'Content-Type: application/json' \
  -d '{"slug":"demo","name":"Demo"}'
curl -s -X PUT localhost:8007/api/v1/projects/demo/tags -H 'Content-Type: application/json' \
  -d '{"tags":["governance"]}'
curl -s 'localhost:8007/api/v1/search?tag=governance&min_health=0.5&sort=health'
curl -s localhost:8007/api/v1/hooks/forgejo -X POST \
  -H 'Content-Type: application/json' \
  -H 'X-Gitea-Event: push' \
  -H 'X-Hub-Signature-256: sha256=...' \
  -d '{"repository":{"id":1,"full_name":"owner/repo"},"pusher":{"login":"user"}}'
```

Quick web tour:

```
# Visit http://localhost:8007/           — homepage
# Visit http://localhost:8007/search     — search form
# Visit http://localhost:8007/projects   — project listing
# Visit http://localhost:8007/projects/demo/board — kanban board
```

## Things that will bite you (learned the hard way here)

1. **MaxOpenConns(1) + open cursor = deadlock.** `SearchProjects` closes
   its rows explicitly before faceting. Never query while a cursor is
   open; keep the 300s test timeout so hangs fail instead of hanging CI.
2. **FTS5 rows are rebuilt on write** by `ReindexProject`. If you add a
   searchable field, update the reindex and its test.
3. **FK columns with "optional" ids:** pass `NULL`, not `0`.
4. **Glicko-2 chord:** Don't touch the solver without rerunning
   `TestRateGlickmanExample`.
5. **vet parses `%` in test failure strings** — write "50 pct", not "50%".
6. **Rate limiter is global in-memory.** Tests must reset `limiter.visitors`
   or use `origLimiter := limiter; defer func(){ limiter = origLimiter }()`.

## How the reviewer (Hermes) will check your work

- Runs `make verify` and reads diffs. Green tests are the floor, not the
  goal: tests must assert the *spec's* behavior (quorum math, gates,
  decay), not just "the handler returns 200".
- Checks `docs/PLAN.md` status honesty against the actual tree.
- Reads `docs/QUESTIONS.md` first and answers your ASSUMED items.
- Will reject: governance simplification (no-quorum shortcuts), silent
  permission widening, search regressions, un-logged privileged actions,
  new heavy dependencies.
- Keeps `docs/concord-spec.md` byte-identical to the vault master;
  propose spec changes via `docs/QUESTIONS.md` instead.

## Milestone status

Honest states after the seventh-pass review (2026-09-16, reviewer-verified
against the tree AND the live service). All milestones now AC-tested.

| Milestone | Status | Notes |
|-----------|--------|-------|
| M0 skeleton | done | verified: make verify green, live smoke test |
| M1 complaints | AC-tested | CRUD + charter-wired pain + permission denials |
| M2 features | AC-tested | CRUD + maintainer-gated strategic weight |
| M3 ranking + priority | AC-tested | rating-movement + weight-scaling tests; priorities route fixed |
| M4 consensus | AC-tested | five-outcome consensus + objection lifecycle tests |
| M5 board + charter | AC-tested | board CRUD + charter read/write tests |
| M6 merge + webhooks | AC-tested | MR CRUD + webhook HMAC tests |
| M7 threads | store-tested | create, get, delete, vote, nested tested |
| M8 lists | AC-tested | CRUD + entry create/list tests |
| M9 request board | AC-tested | CRUD + answer + vote tests |
| M10 identity | AC-tested | 401/403 live; role hierarchy matrix; granular role enforcement |
| M11 web UI | AC-tested | embedded assets render + golden-page tests |
| M12 sync + hardening | AC-tested | deployed 8007 + security middleware + rate limiting |

## Known issues (seventh pass, reviewer-verified)

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
6. **Remotes in sync.** github and forgejo both at HEAD (6c80acf).
7. **62 tests pass** across store + httpapi, including AC-grade tests
   for rating movement, weight scaling, consensus outcomes, permission
   denials, objection lifecycle, webhooks, and golden-page tests.

### Remaining (non-blocking)

1. **Cloudflare re-point** — owner action: change origin port
   8006→8007 in the Cloudflare dashboard (cannot be automated).
2. **M7 AC-grade tests** — currently store-tested; full HTTP API
   round-trip tests for threads could be added if desired.

### Deployment (live, verified)

- thinkcentre: systemd **user** unit `concord.service` ACTIVE,
  `127.0.0.1:8007` listening, binary at `/home/alvaro/concord-deploy/concord`,
  DB at `~/.local/share/concord/concord.db`.
- `make deploy` target handles build → copy → restart → healthz verify.
- Cloudflare still serves Icecast's 400 page from port 8006. **This is
  the owner's dashboard action**: change the origin/ingress port for
  `concord.polarisocial.xyz` from 8006 to 8007 in the Cloudflare
  dashboard (or tunnel config).
