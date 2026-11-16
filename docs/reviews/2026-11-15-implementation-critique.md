# Concord as it is, and as I would have built it

Written after reading the tree at `port-rust` (46,738 lines of our own Rust, 12,477 lines of our own
JavaScript) and measuring it rather than recalling it. Every number below has a command behind it.

## The measurements that frame everything

| What | Number | How |
|---|---|---|
| Our Rust | 46,738 lines | `find crates -name '*.rs' -exec cat {} + \| wc -l` |
| In the store crate | 22,924 (49%) | same, `crates/db` |
| In `crates/domain` | **1** (a doc comment) | same |
| `crates/app/src/lib.rs` | 427 KB, 217 handlers, 179 routes, one file | `wc -c`, `grep -c '^async fn'`, `grep -c '\.route('` |
| Handlers answering `Json<Value>` | 82, from 152 `json!` sites | `grep -c` |
| SQL queries | 330, **0** compile-checked (`query!`) | `grep -rho 'sqlx::query!'` → 0 |
| `bearer_user(...)` call sites | 122 | `grep -c` |
| Schema | 79 tables, 57 indexes, 161 FKs, one 1,145-line generated baseline | `crates/db/sql/0001_baseline.sql` |
| Our JS | 12,477 lines, 42 files, no build step | `find crates/web/assets/js` |
| `innerHTML` sinks vs manual escape calls | 137 vs 540 | `grep -rho` |
| Security headers in code | **0** (no CSP, no frame options) | `grep -rn 'Content-Security-Policy'` → none |
| Rate limiting | **none**; the vendored `polaris_ratelimit` is imported by 0 files | `grep -rn 429` → none |
| Audit rows written by hand | 16 `INSERT INTO audit` sites, 3 private per-module `audit()` helpers, 0 triggers | `grep -c` |
| Observability | 10 `tracing` statements, no `/metrics` | `grep -c` |
| Vendored estate crates | 4,052 lines; **4 of 7 imported by nothing** (1,383 lines dead) | `grep -rl` per crate |
| Tests | 198 Rust, 29 Python e2e suites, a 240-line gate, 47-line CI | `grep -c '#\[test\]'` |

## What is genuinely good, and I would keep

Say this first, because the critique has to be about the load-bearing walls and not the paint.

1. **The gate is one command and CI runs the same command** (`scripts/gate.sh`, three entry points, no
   second definition). Most projects at this stage have a README paragraph instead. This is the single
   best structural decision in the repo.
2. **The parity scripts are an instrument, not documentation.** `route_readers.py` counts 174 routes,
   166 with a proven reader, 8 declared with a file:line justification. `api_paths.py` catches a class
   of bug (trailing slash, GET where POST was meant) that no unit test would ever see. Instruments that
   can fail and name a class are rarer than tests.
3. **Errors are typed at the store boundary** (`MoveError::{Invalid,NoSuchColumn,NotFound,ColumnFull}`),
   not stringly-typed. `project_maintainer()` exists because somebody noticed four refusals were the
   same four refusals.
4. **The schema was adopted, not invented.** Generating the baseline from the live SQLite and reviewing
   the generator's decisions as a diff (`ddl.py`, rules R1–R6, an OVERRIDES table with a reason per
   line) is how a port should inherit a schema. It is also where the schema debt came from — see below.
5. **The UI-mutant suites** (`arena_ui_mutants`, `audit_ui_mutants`, `consensus_ui_mutants`,
   `feature_ui_mutants`) prove that a page *hides* what it must hide, which is the class of bug that
   passing tests usually certify as fine.

The rest of this note is what I would not have built this way.

## Nine criticisms, with the cost of each

### 1. The HTTP surface is one file, and authorization is a function call

427 KB, 217 handlers, 179 routes, and the proof that a request is authenticated is the line
`if bearer_user(&state, &headers).await?.is_none() { return Err(...) }` repeated **122 times**.

The cost is not readability. It is that **"unauthenticated by default" is the default**. A new route
added tomorrow with no check compiles, boots, and answers. Nothing fails. The only thing that catches it
is an e2e suite that happens to exercise the route anonymously — and those suites exist because somebody
remembered to write them. `route_readers.py` is the right idea applied to the wrong symptom: we audit
*whether a page calls a route*, when the more dangerous question is *whether a route has a policy*.

The fix is structural, not stylistic: a route table where every entry declares its policy in the type
system, and a startup assertion plus a test that every registered route has one.

### 2. The API has no type, so the contract is discovered by trial

82 handlers return `Json<Value>`; 152 `json!` literals; zero OpenAPI. The frontend therefore believes
things about shapes and finds out at runtime, which is why `api_paths.py` has to exist, and why the two
renderers per page (server HTML, then a client that rebuilds it) can disagree without anyone noticing
until a test asserts on the wrong one.

A typed response is not bureaucracy here: it is the only thing that makes a *rename* a compile failure
instead of a 500 that a `.catch` swallows. The repo has already paid for the alternative twice —
four trailing-slash 404s each swallowed by `getOrNull`, and a tags endpoint answering a bare array where
the client documented `{tags: […]}`.

### 3. There is no pagination convention, so every list endpoint is a latent 11.7 MB

`async fn list_projects(State(state): State<AppState>)` takes no query parameters and returns
everything. The documents list shipped every document *body* — 11.7 MB, still downloading at thirty
seconds — and the fix was made at that one endpoint (`?fields=meta`), not in the convention. With 97
projects nobody notices. With 10,000 a home page is a network incident, and the failure arrives on the
day the product succeeds.

The one thing I would make non-negotiable from day one: **every collection response is a page with a
stated size and a cursor**, and the default is 50, not "all".

### 4. The store crate is the domain, and the domain crate is a one-line comment

`crates/db` is 49% of the code, holds the rules (`move_card` decides what a column-full means), and the
crate named `domain` says "see docs/plans/port-to-profile.md for the slice that fills this crate". That
is honest, and it is a lie in the other direction: the crate graph *claims* a hexagonal layout that the
code does not have.

Two consequences. Rules that span two aggregates have nowhere to live except a function that owns a
transaction. And **330 runtime queries, 0 compile-checked** means a column rename is not a compile error
but a 500 at runtime; `--locked` protects the lockfile, nothing protects the SQL.

### 5. The audit trail is a convention with sixteen authors

16 hand-written `INSERT INTO audit` sites, three private `audit()` helpers (strategy, taxonomy, themes),
no triggers, no outbox. The product's central claim is "measured, not claimed", and the record of who
changed what is assembled by the same hand that does the changing. Every new write path is a new chance
to forget, and the failure is silent: an audit page that is merely incomplete looks exactly like one
that is complete.

This is the one criticism where I think the current design is not merely inconvenient but *wrong in
kind*: an audit trail derived from an event log cannot forget, because forgetting would mean the event
never happened, which means the state never changed.

### 6. Browser security is a 137-sink question and the answer is a bearer token in localStorage

No CSP, no frame options, no rate limiting anywhere (the estate's `polaris_ratelimit`, 592 lines,
imported by zero files), and the session token lives in `localStorage` — documented in `auth.js` — so
any successful XSS is a full account takeover with no re-auth. 137 `innerHTML` sites against 540 manual
`esc()` calls means escaping discipline is enforced by review, and the escape function is one `d'oh`
away from being skipped in the next file.

Login is a plain POST with argon2 verification and no throttle. On a public instance that is an
unlimited password-guessing oracle.

### 7. The schema was inherited whole, so the prototype's shape is now our migrations

79 tables in a generated baseline, 161 FK constraints, and one hand-written migration since. Inheriting
was right for a port — it made cutover a move rather than a redesign — but the price is that every
decision the Go prototype made (table granularity, denormalisation, absence of an outbox, absence of
soft-delete conventions) is now something we cannot change without a migration and cannot explain
without reading a generator.

### 8. Ops is a hardcoded pool and ten log lines

`PgPoolOptions::new().max_connections(16)` with no override, no `/metrics`, 10 `tracing` calls in
46,738 lines, no queue for anything that is slow (webhooks, forge ingest, embeds all run inside the
request that triggered them). The gateway budget problem on `/adopt` (16 deferred, 16 failed) is visible
only because a CLI printed a line to stderr; nothing emits it as a metric that could page someone.

### 9. Four of seven vendored crates are dead weight

`polaris_ledger` (466), `polaris_ratelimit` (592), `polaris_slug` (169), `polaris_healthz` (156) —
1,383 lines imported by nothing, vendored because they had no repository. ADR 0013 says the endgame is
to give them repositories; until then the repo carries a copy that will drift from any future fix, and a
reader cannot tell a vendored dependency from a vendored corpse.

## What I would have built from scratch

Not a different product. The product is right: a directory where what a project *has done* outranks
what it *says*, and consensus is the instrument. What follows is the same product with the walls in
different places.

### Shape

    concord-domain      pure: aggregates, invariants, events. No sqlx, no axum, enforced by CI.
    concord-store       the ports + the Postgres adapters, one module per aggregate, compile-checked SQL.
    concord-api         thin: extractors, policy, typed responses, OpenAPI emitted from the types.
    concord-work        outbox consumer: feeds, notifications, forge ingest, embeds, link health.
    concord-web         one renderer, escaping by construction.
    concord-cli         migrate, seed, ingest, probe — the operator surface, not an afterthought.

Eleven crates became six because eleven was a decomposition by *port slice*, not by responsibility.

### Five decisions I would take differently

**1. Domain first, and the compiler enforces it.** Rules live in `concord-domain` as data and functions
(`Stage::from_history(&History)`, `Consensus::tally(&ballots, &threshold)`); the store implements traits
the domain declares. Enforced by a CI grep (`cargo tree -p concord-domain` must not contain sqlx) rather
than by good intentions, because the current empty `domain` crate is what good intentions look like
after a port.

**2. Every write emits an event; state is a projection.** One `events` table, append-only, written in
the same transaction as the state change; projections for project state, feeds, notifications, audit
page, and webhooks. This makes the audit trail unforgeable, makes `/feeds` a read instead of a fan-out,
and makes "why is this project a sprout" answerable by reading its event stream rather than by
reconstructing a query. Rejected alternative: triggers on every table — fewer lines, no history of
*intent* (a trigger cannot say "archived because the owner said so").

**3. Authorization is a table, not a habit.** One route registry:

    ("POST", "/api/v1/projects/{slug}/board/cards/{card}/move", Policy::maintainer(Action::BoardWrite)),

with a startup assertion that the registered axum routes and the policy table are the same set, and a
test that walks every route anonymously expecting 401/403 unless the policy says `Public`. The current
`project_maintainer()` helper was the right instinct — this makes the helper the only way to reach a
handler instead of the polite way.

**4. Typed responses, generated client.** Handlers return `impl IntoResponse` over `#[derive(Serialize,
ToSchema)]` structs; CI emits `openapi.json` and fails on a breaking change without a version bump; the
browser client is generated. This deletes `api_paths.py` as a class of bug rather than patching the
symptoms it finds, and it makes the frontend's contract reviewable in a diff.

**5. Security in the middle, not in review.** `tower-http` for CSP with per-request nonces, frame
options, HSTS; session in an `HttpOnly; Secure; SameSite=Lax` cookie (bearer tokens are for the API and
for CLI, not for a browser page); rate limiting through the estate's own `polaris_ratelimit` (which
exists, works, and is currently unused), with login throttled separately and by account as well as IP.
The renderer becomes a function `el(tag, props, children)` that cannot emit unescaped text; `innerHTML`
does not appear in the tree.

### Data model, by aggregate

    identity    users, sessions, tokens, claims
    project     projects, repository identity, stage history, archive
    board       columns, cards, moves
    consensus   criteria, alternatives, ballots, tallies, thresholds
    work        features, requests, documents, milestones
    record      events (append-only) + projections: audit, feeds, notifications

Six modules, each owning its tables and its migrations; no cross-module FK without a declared
projection; every migration forward-only and applied by the binary at startup under an advisory lock
(one lock, one code path, no "did you run psql").

### Conventions I would write down on day one

* Every collection is paginated, default 50, cursor, hard maximum stated in the API.
* Every mutating route is idempotency-keyed or refuses duplicates; the import/merge work in the estate
  taught this the expensive way.
* Every rule that can be stated as a property gets a property test: ranking is monotone in measured
  activity, consensus is monotone in votes, a stage never regresses without an event.
* Every list endpoint has a stated `EXPLAIN` budget in CI against a seeded 100k-row database — the
  current store tests run against an empty one, which is the only reason the 11.7 MB list was a surprise.
* `cargo-mutants` on `concord-domain` nightly, because that crate is where a wrong rule is silently
  wrong and every test still passes.

### What I would still do the same

The gate as one command; the instruments that name a class (`route_readers`, `api_paths`, the UI-mutant
suites); adopting the reference schema instead of inventing one; typed store errors; the ADR habit; and
the rule that a defect is reproduced before it is fixed. Those are the parts of this repo that are
already better than most production code I have read, and none of the rewriting above is worth
sacrificing them for.

## If it were mine to sequence (deployable at every step)

1. **Policy table + auth extractors** (one week). Mechanical, no behaviour change, and it converts the
   most dangerous default in the codebase from "unauthenticated" to "unclassifiable".
2. **Security baseline** (two days, user-visible): cookie session, CSP nonce, rate limit, login
   throttle. Each is one PR with a probe that fails without it.
3. **Split `lib.rs` by resource** (mechanical, then reviewable): `api/projects.rs`, `api/board.rs`, …
   Land it with zero behaviour change and the gate green, so the diff is provable.
4. **Typed responses for the 20 routes the UI actually calls**, emit OpenAPI, generate the JS client.
   Stop at 20; the rest converts as it is touched.
5. **Events + audit projection** for the six write paths that matter (archive, move, vote, claim,
   feature state, document edit), then the rest.
6. **Pagination convention** enforced by a gate check on route handlers that take a collection.
7. **Domain crate extraction** for stage, consensus and claim — the three rules that are the product.
8. Kill the four dead vendored crates: publish or delete, then honest `Cargo.toml`.

Steps 1, 2 and 4 are worth more than the other five combined, and none of them requires a rewrite.
