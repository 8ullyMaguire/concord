# Concord

A complaint-driven, consensus-based forge: development is driven by real
complaints, ranked by pairwise Glicko-2 comparisons, decided through quorum
consensus, executed on a phase-gated kanban board — and **discovered** through
a first-class search surface (tags, quality, maintenance health, languages).

Complaints are not a queue of work items; each one is the problem statement, and
**solutions** are proposed against it and ranked against each other in a shared
arena. Every feature permanently carries a `do-nothing` baseline solution, so
"do the obvious thing" is something you have to argue for rather than something
you get by doing nothing.

Governance is **collective by default**: maintainers exist, but direction is
decided by collaborators.

The current spec is `docs/concord-spec-r4.md`; `docs/concord-spec.md` is two
revisions stale. Milestone state is in `docs/PLAN-r4.md`.

## Stack contract (shares Forgejo's stack)

- **Go** (same language as Forgejo; this repo targets the current stable).
- **chi v5** router — the same router Forgejo uses.
- **SQLite** via `modernc.org/sqlite` (pure Go, `CGO_ENABLED=0` builds).
  Deliberate divergence from Forgejo (XORM + mattn/go-sqlite3): Concord uses
  `database/sql` + plain SQL with file-based migrations while small. See
  `docs/ARCHITECTURE.md`.
- **Transformer embeddings** via ollama (`nomic-embed-text`), behind an
  interface with a dependency-free hashed fallback for tests. Used for
  duplicate detection and similarity search. Optional: unset
  `CONCORD_EMBED_*` and those features report unavailable rather than blocking.
- Forgejo reference checkout: `~/code/upstream/forgejo` (shallow clone).

## Quickstart

```
make build
./bin/concord --listen 127.0.0.1:8410
curl -s localhost:8410/api/v1/healthz
curl -s 'localhost:8410/api/v1/search?q=consensus&tag=governance&sort=health'
```

Environment: `CONCORD_DB` (default `$XDG_DATA_HOME/concord/concord.db`),
`CONCORD_LISTEN` (default `127.0.0.1:8410`),
`CONCORD_FORGEJO_SECRET` (webhook HMAC secret, optional),
`CONCORD_TRUST_LEVEL_MIN` (minimum trust level required to set a feature's
status, default `1`),
`CONCORD_EMBED_URL` / `CONCORD_EMBED_MODEL` / `CONCORD_EMBED_TIMEOUT_MS`
(embedding backend for duplicate detection and similarity search; unset disables
semantic features and duplicate detection falls back to reporting unavailability
rather than silently blocking filings).

### Embeddings are not automatic for existing data

Duplicate detection and `/similar` need a vector per entity. Rows created *after*
the embedder is configured are embedded on write; rows that predate it are not,
and nothing warns you. `cmd/embedbackfill` is idempotent and must be run by hand:

```
go run ./cmd/embedbackfill -status   # per-kind coverage; 0.0% means an empty index
go run ./cmd/embedbackfill           # backfill (~1 entity/sec per HTTP call)
```

An index that has never been built makes every semantic feature silently inert.
Check it with `-status` before believing a similarity result.

### Trust levels and feature status

A feature is created as `draft`. Changing its status is normally a side effect
of the governance flow: a consensus call can set `consensus`, and an approved
merge request sets `shipped`. `PUT /api/v1/projects/{slug}/features/{id}/status`
is the direct route, for the cases the flow cannot express — importing a
portfolio that already has a state, or a maintainer recording a decision out of
band.

That route is gated on a per-user `trust_level` (migration `0004`), compared
against `CONCORD_TRUST_LEVEL_MIN`. The threshold defaults to the highest level
`trust_config.max_level` allows, so **an unconfigured instance is locked**: a
freshly registered account cannot set a status. An unparseable or negative value
falls back to the default rather than opening the gate.

Trust level is deliberately not the same thing as a project role. A role is
granted per project and answers "what may this person do here"; a trust level is
per user and answers "how much do we believe them on this instance". Coupling
them would change an account's power when it joins a project.

To grant a level, use the store's `SetTrustLevel` (it enforces the ceiling and
writes a `trust_grants` audit row) rather than a bare `UPDATE`:

```sql
-- one-off bootstrap, on an account that already exists
UPDATE users SET trust_level = 1 WHERE username = 'someone';
INSERT INTO trust_grants (user_id, level, granted_by, reason, created_at)
SELECT id, 1, id, 'bootstrap', strftime('%s','now')
  FROM users WHERE username = 'someone';
```

To raise the instance ceiling, edit `trust_config.max_level` (default `1`) —
a level above it cannot be assigned by any path.

## Layout

```
cmd/concord/         server entrypoint
cmd/embedbackfill/   build or extend the embedding index (-status to inspect)
cmd/granttrust/      one-off trust-level bootstrap
internal/config/     env/flag configuration
internal/db/         open + file-based migrations (migrations/*.sql, embedded)
internal/ranking/    Glicko-2, pain scores, priority, vote weight (pure logic)
internal/governance/ roles, charters, consensus evaluation, merge gate (pure logic)
internal/discovery/  maintenance-health score (pure logic)
internal/embed/      embedding backends: hashed feature-hashing and ollama
internal/store/      SQL data layer; project CRUD is the exemplar vertical
internal/httpapi/    chi router + JSON API v1
docs/                spec, architecture, PLAN.md (build roadmap), HANDOFF.md
```

`internal/store` is organised by domain now — `arenas.go` and `embeddings.go` are
the substrate the rest builds on. `internal/embed` holds two interchangeable
backends behind one interface; the hashed one needs no service and is what tests
use, and the per-model similarity thresholds live in `config.go`.

## Development

```
make verify   # vet + test + build — must pass before every commit
make run
```

The build roadmap for contributors and agents lives in `docs/PLAN.md`;
start with `docs/HANDOFF.md`.
