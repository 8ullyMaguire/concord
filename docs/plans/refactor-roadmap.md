# Concord — the refactor program (opened 2026-11-15)

The post-port roadmap is closed: rows open 0, the review's ten engineering items done, and the two remaining blockers
(`CONCORD_FORGE_TOKEN`, the 58 projects with no stated upstream) are outside the code. What is left is not features. It is
the shape of the code, and it was measured before it was written down —
`docs/reviews/2026-11-15-implementation-critique.md` carries every number and the command behind it.

This file is the **plan** for that work: nine steps, each deployable on its own, each with the measurement that justifies it
and the command that proves it landed. It is ordered by risk removed per hour, not by tidiness. (Step 9 was added on 2026-11-15 when
the meta-tests were repaired and measured that the gate runs 8 of the tree's 29 e2e suites.)

## Non-goals

Written down because a refactor is where scope goes to die.

* **No rewrite.** The product, the schema's content, and the UX stay. 46,738 lines are not replaced; they are re-homed.
* **No framework, no bundler, no SPA.** The frontend stays server-rendered with vanilla modules. What changes is that it
  gains one renderer that cannot emit unescaped text — not a toolchain.
* **No eleventh crate.** The crate count goes **down** (eleven → six) because eleven decomposed by port slice rather than
  by responsibility.
* **No behaviour change in steps 1 and 3.** If the gate output changes for a reason other than the named one, the step is
  wrong, not the gate.
* **Not a veto on features.** Steps 2 and 4 are worth doing first precisely because they are cheap; a product fix that
  cannot wait should not wait for this plan.

## The measurements this plan answers to

| measurement | number | why it is a risk |
|---|---|---|
| `crates/app/src/lib.rs` | 427 KB, 217 handlers, 179 routes | one file is one review blind spot |
| `bearer_user(...)` call sites | 122 | authentication is opt-in per handler |
| handlers answering `Json<Value>` / `json!` sites | 82 / 152 | the client's contract is guesswork |
| SQL queries compile-checked | 0 of 330 | a column rename is a runtime 500 |
| `crates/domain` | 1 line | the crate graph claims a layout the code lacks |
| hand-written audit inserts | 16, with 3 per-module helpers, 0 triggers | the audit trail can silently be incomplete |
| security headers / rate limiting | 0 / none | token in `localStorage`; unlimited login attempts |
| collection endpoints paginated | 0 (`list_projects` takes no query) | a list already shipped 11.7 MB |
| observability | 10 `tracing` calls, no `/metrics` | a 16-deferred ingests is a stderr line, not a metric |
| vendored crates imported by nothing | 4 of 7 (1,383 lines) | a copy that will drift, indistinguishable from a dependency |
| e2e suites the gate runs | 8 of 29 (`RECORDED_UNRUN = 21`) | the suites that could catch a refusal path are the ones nobody runs |

## Step 0 — the repo carries its own review

**Done 2026-11-15.** The critique is `docs/reviews/2026-11-15-implementation-critique.md`, so the argument lives with the
code it argues about. Verify:

```sh
test -f docs/reviews/2026-11-15-implementation-critique.md && grep -c '^###' docs/reviews/2026-11-15-implementation-critique.md
# 9  (the nine criticisms)
```

## Step 1 — authorization becomes a table (the highest-value week)

**Why.** 122 `bearer_user(...)` calls mean the default is "unauthenticated": a route added without a check compiles, boots,
answers, and no test fails. The compensating helper (`project_maintainer`) is the right instinct, and it is still only a
convention.

**First task (exact).** Add `crates/app/src/policy.rs`:

```rust
pub enum Actor { Anonymous, User(i64), Guest(i64) }
pub enum Policy {
    Public,                                 // no session needed
    Session,                                // any signed-in user, guests included
    Member(Action),                         // signed in, not a guest, membership checked
    Maintainer(Action),                     // signed in, not a guest, maintainer or better
    Operator,                               // an operator account
}
pub fn policy_for(method: &str, path: &str) -> Option<Policy>   // the table, one line per route
```

**Then**, in order, in the same step: an axum extractor `Authn`/`Authz` that resolves the actor once per request; every
handler takes `Authz<Policy>` instead of calling `bearer_user`; `main.rs` asserts at startup that every registered route
resolves to a `Some(policy)` and panics naming the route that does not.

**Verification (must fail before, pass after):**

```sh
cargo test -p concord-app --lib policy              # the table covers 179/179 registered routes
python3 tests/e2e/api_surface.py --anonymous        # every non-Public route answers 401/403, not 200
```

Expected: `policy: 179 routes, 179 policies, 0 unclassified` and `api_surface: 174 routes walked anonymously, 0 unexpected 200`.

**Done when** `grep -c 'bearer_user' crates/app/src/lib.rs` is 0 and the e2e suite is green.

## Step 2 — the security baseline (two days, user-visible)

**Why.** No CSP, no frame options, no rate limiting anywhere, session token in `localStorage`, unlimited login attempts.

Four PRs, each with the probe that fails without it:

1. `HttpOnly; Secure; SameSite=Lax` session cookie for the browser; bearer tokens stay for the API and the CLI.
2. CSP with a per-request nonce (all 42 scripts become nonce-carrying `<script>` tags), `X-Frame-Options: DENY`, HSTS.
3. Rate limiting through the estate's `polaris_ratelimit` (592 lines, currently imported by **zero** files): per-IP and
   per-token buckets on `/api/v1/*`, and a separate per-account bucket on `/api/v1/auth/login`.
4. The renderer helper — `el(tag, props, children)` — so no new file can add an `innerHTML` sink.

**Verification:**

```sh
curl -sI https://concord.polarisocial.xyz/ | grep -iE 'content-security-policy|x-frame-options|strict-transport'
for i in $(seq 1 40); do curl -s -o /dev/null -w '%{http_code} ' -X POST \
  -d '{"email":"x@y","password":"wrong"}' -H 'content-type: application/json' \
  https://concord.polarisocial.xyz/api/v1/auth/login; done; echo
# before: 40 × 401        after: 401 … then 429
grep -rc 'innerHTML' crates/web/assets/js/*.js | grep -v ':0' | wc -l    # 42 → 0
```

## Step 3 — split `lib.rs` by resource (mechanical, then reviewable)

**Why.** 427 KB and 217 handlers in one file is one review blind spot and one merge conflict generator.

**Method matters more than the target.** Move modules and change nothing else: `api/projects.rs`, `api/board.rs`,
`api/consensus.rs`, `api/work.rs`, `api/identity.rs`, `api/records.rs`, with `router()` assembling from each module's
`routes()`. No renames, no re-formatting, no opportunistic fixes in the same commit — one commit per module, `git show
--stat` proving a pure move.

**Verification:**

```sh
wc -c crates/app/src/lib.rs          # < 20 KB (the router and the state only)
make gate                            # gate: PASS, unchanged
git show --stat HEAD | tail -3       # only moves: same line count in, same line count out
```

## Step 4 — the API gets a type (the 20 routes the UI calls)

**Why.** 82 `Json<Value>` handlers and 152 `json!` sites mean the contract is discovered at runtime: four trailing-slash
404s swallowed by `.catch`, a tags endpoint answering a bare array where the client documented `{tags: [...]}`.

**Do 20, not 174.** Typed `Serialize + ToSchema` structs for the routes the UI actually calls; `utoipa` emits
`openapi.json` in CI; the browser client is generated from it. The rest convert as they are touched.

**Verification:**

```sh
python3 scripts/parity/openapi_diff.py --base origin/main   # no breaking change, or a version bump
grep -c 'Json<Value>' crates/app/src/lib.rs                 # 82 → 62 after the first 20
python3 scripts/parity/api_paths.py                          # still 0 drift, now with fewer exceptions
```

## Step 5 — events, and an audit trail that cannot forget

**Why.** 16 hand-written `INSERT INTO audit`s and three private per-module `audit()` helpers. The product's claim is
"measured, not claimed"; the record of who changed what is assembled by the hand that changes it, and an incomplete audit
page looks exactly like a complete one.

**Scope discipline:** an append-only `events` table written in the same transaction as the state change, for the six write
paths that matter — archive, card move, vote, claim, feature state, document edit. Projections follow: the audit page reads
`events`; `/feeds` and notifications read projections instead of fanning out on write.

**Verification:**

```sh
cargo test -p concord-db --test store -- events        # each of the six paths writes exactly one event
psql "$DATABASE_URL" -c "select kind, count(*) from events group by 1 order by 2 desc"   # non-empty, six kinds
# and the negative: remove the event write from move_card → the honest failure must be a named test, not a silent gap
```

## Step 6 — one pagination convention, enforced

**Why.** `list_projects(State(state))` takes no query and returns everything; the documents list already shipped 11.7 MB.

`Page<T> { items, next: Option<Cursor> }`, default 50, hard max stated in the API; a gate layer that fails when a handler
returning a collection does not take a `Page` parameter.

**Verification:**

```sh
python3 scripts/parity/collection_pages.py     # every collection route takes a page; 0 exceptions or a justified allow entry
curl -s 'https://concord.polarisocial.xyz/api/v1/projects?limit=10000' | head -c 200   # 400, "limit must be 1..=200"
```

## Step 7 — the domain crate stops being a comment

**Why.** `crates/db` is 49% of the code and holds the rules; `crates/domain` is one line. Rules that span aggregates have
nowhere to live, and 0 of 330 queries are compile-checked.

**Extract the three rules that are the product** — stage transitions, consensus tallies, claim eligibility — as pure
functions with property tests; the store keeps the adapter role. Then turn on `sqlx::query!` + `cargo sqlx prepare` for
the store crate's new code, with `.sqlx` committed, so a renamed column is a compile error.

**Verification:**

```sh
cargo tree -p concord-domain | grep -c sqlx        # 0, asserted by a CI job
cargo test -p concord-domain --lib                 # property tests, proptest cases > 0
cargo check -p concord-db --locked --offline       # passes with no DATABASE_URL: the SQL is compiled, not guessed
```

## Step 8 — delete the corpses

**Why.** `polaris_ledger` (466), `polaris_ratelimit` (592, until step 2), `polaris_slug` (169), `polaris_healthz` (156) —
1,383 vendored lines imported by nothing. A copy that will drift and cannot be told from a dependency.

Per crate: publish it to the family forge and switch to `git = …`, or delete it from `vendor/` and the workspace. ADR 0013
is superseded when the last one is either published or gone.

**Verification:**

```sh
for c in vendor/*/; do n=$(grep -rl "$(basename $c | tr - _)" --include=*.rs crates/ | wc -l); echo "$n $c"; done
# every remaining vendored crate: n > 0
```

## Step 9 — the suites nobody runs

**Why.** `scripts/gate.sh` executes 8 of the tree's 29 `*_e2e.py` suites; the other 21 are run by nobody, and the ratchet in
`tests/e2e/harness_selftest_test.py` (`RECORDED_UNRUN = 21`) exists so that number cannot grow silently. Steps 1, 2 and 5 of this
program add refusal paths — 401/403, 429, event writes — that can only be proved from outside the process, and several of the
suites that would prove them are in the unwired set.

**Method.** One suite per commit, in an order chosen by what the earlier steps need rather than by alphabet: `lifecycle_e2e`
(a refusal path), `settings_e2e` and `settings_themes_e2e` (auth), `lists_e2e`, `milestones_e2e`, `workitems_e2e`, `criteria_e2e`,
`curation_e2e`, `feeds_e2e`, `notifications_e2e`, `taxonomy_e2e`, `merge_e2e`, `new_project_e2e`, `readers_e2e`, `rank_links_e2e`,
`scout_e2e`, `finder_e2e`, `guest_e2e`, `arena_e2e`, `audit_e2e`, `audit_undo_e2e`. Each is wired into `gate.sh`'s live loop and
`RECORDED_UNRUN` drops by one in the same commit. A suite that cannot run for a reason outside the code is recorded in the Blocked
list with that reason, and the ratchet drops anyway — an unwired suite nobody can wire is a decision, not a failure.

**Verification:**

```sh
grep -c 'RECORDED_UNRUN = ' tests/e2e/harness_selftest_test.py   # the number, falling by one per commit
cargo test -p concord-app --lib 2>/dev/null; true
CONCORD_E2E_CANDIDATE=$PWD/target/debug/concord-cli scripts/gate.sh --live https://concord.polarisocial.xyz | tail -3
# before: "suites: 26 · 15 · 8 · 5 · 18 · 15 · 7 · 5 passed"  after: 29 suites, RECORDED_UNRUN 0
```

## Status

| step | what | status |
|---|---|---|
| 0 | the review lives in the repo | **done 2026-11-15** |
| 1 | authorization as a table + extractors + startup assertion | open — highest value |
| 2 | security baseline (cookie, CSP, rate limit, renderer) | open — cheapest risk removed |
| 3 | split `lib.rs` by resource, zero behaviour change | open |
| 4 | typed responses + OpenAPI + generated client (20 routes) | open |
| 5 | events + audit projection (six write paths) | open |
| 6 | one pagination convention, gate-enforced | open |
| 7 | domain crate for stage / consensus / claim; `query!` on new SQL | open |
| 8 | publish or delete the four dead vendored crates | open |
| 9 | wire the 21 unrun e2e suites (`RECORDED_UNRUN` → 0) | open — proves steps 1, 2 and 5 from outside |

## Decisions proposed (not taken)

These are the five decisions the review argues for. They are recorded here rather than as ADRs because they are not yet
the repository's, and an ADR that records an intention is a lie with a number on it. Each becomes an ADR when its step
lands.

1. `concord-domain` is pure and CI-enforced (no `sqlx`, no `axum` in its tree).
2. State is a projection of an append-only `events` table; writes emit events in the same transaction.
3. A route's policy is declared in a table and asserted at startup, and a route without a policy does not boot.
4. Responses are typed and `openapi.json` is a build artifact; a breaking change without a version bump fails CI.
5. The session is a cookie; bearer tokens are for machines. Rejected alternative: keep `localStorage` and rely on CSP —
   rejected because a 137-sink codebase with 540 manual escapes is not a place to bet an account on discipline.
