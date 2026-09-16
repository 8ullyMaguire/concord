# Concord

A complaint-driven, consensus-based forge: development is driven by real
complaints, ranked by pairwise Glicko-2 comparisons, decided through quorum
consensus, executed on a phase-gated kanban board — and **discovered** through
a first-class search surface (tags, quality, maintenance health, languages).

Governance is **collective by default**: maintainers exist, but direction is
decided by collaborators. See `docs/concord-spec.md`.

## Stack contract (shares Forgejo's stack)

- **Go** (same language as Forgejo; this repo targets the current stable).
- **chi v5** router — the same router Forgejo uses.
- **SQLite** via `modernc.org/sqlite` (pure Go, `CGO_ENABLED=0` builds).
  Deliberate divergence from Forgejo (XORM + mattn/go-sqlite3): Concord uses
  `database/sql` + plain SQL with file-based migrations while small. See
  `docs/ARCHITECTURE.md`.
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
`CONCORD_FORGEJO_SECRET` (webhook HMAC secret, optional).

## Layout

```
cmd/concord/         server entrypoint
internal/config/     env/flag configuration
internal/db/         open + file-based migrations (migrations/*.sql, embedded)
internal/ranking/    Glicko-2, pain scores, priority, vote weight (pure logic)
internal/governance/ roles, charters, consensus evaluation, merge gate (pure logic)
internal/discovery/  maintenance-health score (pure logic)
internal/store/      SQL data layer; project CRUD is the exemplar vertical
internal/httpapi/    chi router + JSON API v1
docs/                spec, architecture, PLAN.md (build roadmap), HANDOFF.md
```

## Development

```
make verify   # vet + test + build — must pass before every commit
make run
```

The build roadmap for contributors and agents lives in `docs/PLAN.md`;
start with `docs/HANDOFF.md`.
