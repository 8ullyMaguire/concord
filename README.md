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
`CONCORD_FORGEJO_SECRET` (webhook HMAC secret, optional),
`CONCORD_TRUST_LEVEL_MIN` (minimum trust level required to set a feature's
status, default `1`).

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
