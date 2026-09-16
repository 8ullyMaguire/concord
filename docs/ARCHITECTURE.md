# Concord — architecture

Concord is a Go service that shares Forgejo's stack conventions while
remaining an independent decision layer. The Forgejo reference checkout
lives at `~/code/upstream/forgejo` (shallow clone; `git fetch --unshallow`
if history is needed).

## Stack contract

| Concern    | Concord                                   | Forgejo (reference)                  |
|------------|-------------------------------------------|--------------------------------------|
| Language   | Go 1.27+                                  | Go 1.26 (`module forgejo.org`)       |
| Router     | `go-chi/chi/v5`                           | `go-chi/chi/v5` (same)               |
| DB         | SQLite via `database/sql` + `modernc.org/sqlite` (pure Go, `CGO_ENABLED=0`) | XORM + mattn/go-sqlite3 (cgo), MySQL/Postgres also supported |
| Migrations | SQL files, `internal/db/migrations/NNNN_*.sql`, embedded, tracked in `schema_migrations` | Go-struct migrations in `models/gitea_migrations/`, `models/forgejo_migrations/` |
| Templates  | `html/template`, server-rendered          | `html/template` / templ              |
| Sessions   | cookies + Forgejo OAuth2 (M9)             | `go-chi/session` + OAuth2 provider   |
| CI target  | `make verify` (vet + test + build), CGO_ENABLED=0 | Makefile + Dockerfile         |

Deliberate divergences (do not "fix" without discussion): `database/sql`
with plain SQL instead of XORM — the governance logic is query-shaped and
XORM's reflection buys nothing here; SQL-file migrations instead of Go
structs — cheap for agents and humans to review. Postgres compatibility is
a hardening milestone; stick to SQLite-portable SQL until then.

## Package map

```
cmd/concord/          entrypoint: flags → config → db open → migrate → serve
internal/config/      env + flags (CONCORD_DB, CONCORD_LISTEN, CONCORD_FORGEJO_SECRET)
internal/db/          open (WAL, FK, busy_timeout, MaxOpenConns=1) + migrations
internal/ranking/     pure math: Glicko-2 (Illinois solver), pain, priority, vote weight
internal/governance/  pure rules: roles, charters, quorum, consensus evaluation, merge gate
internal/discovery/   pure rules: transparent maintenance-health score
internal/store/       SQL data layer; internal/store/projects.go + search.go are the exemplar
internal/httpapi/     chi router + JSON API v1; handler → store → JSON, mapError for domain errors
docs/                 spec (verbatim copy), premise, this file, PLAN.md, HANDOFF.md
```

Rule of layering: HTTP handlers never contain governance or ranking math;
pure packages never import SQL; the store translates between them.

## Schema overview (migration 0001)

- **Identity:** `users`, `api_tokens` (sha256-hashed), `members` (role +
  `is_moderator` flag; moderation is orthogonal to technical roles).
- **Governance:** `projects` (governance_model: collective default),
  `charters` (thresholds per project), `consensus_calls`, `positions`,
  `objections` (principle/violation/remedy), `audit_log` (append-only).
- **Work:** `complaints` (+impacts, tags), `features` (+links, tags, Glicko
  columns), `pairwise_votes`, `board_columns` (fixed phases, WIP limits),
  `board_cards`, `merge_requests`, `merge_approvals`, `reputation_events`.
- **Discovery (first-class, spec §15):** `project_tags`, `project_languages`
  (collaboratively correctable percentages), `project_metrics` (forge-synced
  facts + computed `health_score`), `projects_fts` (FTS5, reindexed on
  write by the store).
- **Lists (spec §16):** `lists`, `list_entries` (proposed → accepted by
  quorum; Glicko columns for ranking), `list_tags`, `list_entry_votes`.
- **Request board (spec §17):** `requests` (natural-language need + tags),
  `request_answers` (project references + fit rationale, Glicko columns,
  UNIQUE per request×project), `request_answer_votes` (pairwise fit
  ranking), `requests.accepted_answer_id` (display-only author marker).

## Governance invariants (never simplify these)

1. Governance model `collective` is the default; maintainers cannot
   unilaterally accept features, move cards into Ready, or merge.
2. Consensus: quorum counts cast positions; consent ratio excludes
   abstains; unmet quorum extends the window (silence never decides);
   blocks need principle/violation/remedy; override needs the
   supermajority ratio.
3. Merges need technical approval (reviewer+) AND the merge quorum, unless
   the charter is `maintainer_led` (logged bypass).
4. Priority is computed (pain + Elo lower bound + strategic weight) — no
   hand-pinned rankings.
5. Instance admins hold no content authority (spec §2.10, §8); every
   privileged action lands in `audit_log`.

## Search design (spec §15)

`GET /api/v1/search` combines FTS5 full text (AND-combined quoted terms;
user input can never inject FTS operators) with structured filters (tags
AND, languages AND with min percentage, min health, model, license) and
always returns facets (tags, languages, licenses, models) computed over
the matched set. Sorts: relevance (bm25 rank), updated, health, newest.
The planned query language (`tag:`, `language:`, `NOT`, grouping, saved
searches) compiles down to `SearchFilters` — see PLAN.md M3.

FTS rows are denormalized (`slug, name, description, tags, languages`) and
rebuilt by `store.ReindexProject` whenever project data changes. External-
content tables + triggers are a hardening task; keep the rebuild-on-write
invariant until then.

## Forgejo integration seams

- **Login (M9):** Forgejo as OAuth2 provider; Concord maps the Forgejo
  account to a local user and a per-project role.
- **Events:** Forgejo webhooks → `POST /api/v1/hooks/*` (HMAC via
  `CONCORD_FORGEJO_SECRET`): PR opened/merged/closed map to merge-request
  open/merge/reject.
- **Merge gate with teeth:** branch protection makes a Concord bot the only
  pusher to protected branches; Concord calls Forgejo's merge API only
  after `governance.CheckMergeGate` passes. Token via env, feature-flagged.
- **Language detection:** `github.com/go-enry/go-enry/v2` (the same lib
  Forgejo uses) for language percentages at sync time (M11).

Useful Forgejo reference paths: `routers/api/v1/api.go` (API organization),
`routers/web/` (page handlers), `models/db/` (db helpers), `models/gitea_migrations/`
(migration style), `services/webhook/` (webhook processing),
`modules/web/middleware/` (session/auth glue).
