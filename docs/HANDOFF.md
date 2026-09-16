# Concord — handoff

For the implementing agent taking over from here. Read this, then
`docs/PLAN.md`, then `docs/concord-spec.md` (§2, §4, §5, §15, §16), then
`docs/ARCHITECTURE.md`.

## What exists right now (verified)

- **Skeleton, building and tested.** `make verify` is green: vet, all
  tests, CGO-free build of `bin/concord`. A live smoke test served
  `healthz`, project creation, tag application, metrics update, and a
  combined search (`?q=quorum&tag=governance&min_health=0.5`) returning
  the right hit with facets.
- **Schema** (`internal/db/migrations/0001_init.sql`): the full spec data
  model — identity, charters, complaints, features, votes, consensus,
  objections, board, merge layer, reputation, audit, discovery
  (project_tags/languages/metrics + `projects_fts` FTS5), and lists
  (`lists`, `list_entries`, `list_entry_votes`) — all in one migration.
- **Pure engines, tested:** `internal/ranking` (Glicko-2 with the
  Illinois solver — validated against Glickman's published worked
  example r'=1464.06, RD'=151.52, σ'=0.05999; pain scores; priority;
  vote weight), `internal/governance` (role levels, charter defaults,
  quorum math, the five consensus outcomes, merge gate),
  `internal/discovery` (transparent health score).
- **Exemplar vertical:** project CRUD + first-class search
  (`/api/v1/search` with FTS, tag/language/health/model/license filters,
  facets, sorts). Copy this pattern for every new domain.
- **Docs:** spec (verbatim copy — the vault is the master), premise
  (shareable), architecture, this file, plan.

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

## Current decision log (do not re-litigate silently)

- Ground-up service, Forgejo as unmodified substrate (OAuth + webhooks +
  bot-mediated merges). Soft-forking Forgejo was evaluated and rejected
  (merge-tax on a security-sensitive codebase; the decision engine shares
  nothing with git plumbing).
- `database/sql` + plain SQL over XORM; SQL-file migrations over Go
  migrations; SQLite now, Postgres later. All in ARCHITECTURE.md.
- Governance: collective by default; merge = reviewer approval + quorum;
  admin-light; lists and moderation run through the same quorum machinery.
