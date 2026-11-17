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

**Landed 2026-11-16 — step 1a: the table, the checker and the gate (this is the half that cannot break the site).**

`crates/app/src/policy.rs` exists, with a different vocabulary than the sketch above and for a measured reason:
`Policy { Public, Optional, Session, Account, Operator }` and **`ROUTES` as `(method, path, policy, enforced)`**.
`policy_for` matches the path axum registered, exactly — no prefix rule, so a route cannot inherit a neighbour's
policy — and `auth_gate` in `lib.rs` reads it: a route with no row answers `500` naming the problem, which is the
defect this step exists for. `scripts/parity/route_policies.py` derives the table from the handlers (following one
level of the context helpers this tree uses), prints it, and compares it with the router on every gate run:

```
api routes registered: 203
page routes (server-rendered, outside the API policy table): 15
routes with a declared policy: 203
routes with no policy: 0
policies for routes that no longer exist: 0
route_policies: PASS — every registered route declares who may call it
```

**Why `enforced` is a column.** The derivation is a description of what each handler does, and on 2026-11-15 it was
**measured against the live instance**: the 34 routes whose handlers looked like they needed a session were called
anonymously, and **22 answered 200**. Turning that table into enforcement in one deploy would have 401'd the project
page's own reads. So enforcement is per row and began where the measurement was unambiguous — the four reads that
already answered `401` (`auth/me`, `notifications`, `projects/{slug}/invites`, `projects/{slug}/votes/next`) and
`GET /api/v1/admin-ledger`, which answered **`200` to an anonymous caller**: the one hole in the 216 routes. The
count of enforced rows is a ratchet in `policy.rs`'s tests: it may grow, never shrink.

**Evidence, on the commit the deploy ships (`de88ab0`), from GitHub's runner** — thinkcentre was at load average 82 from
unrelated work, so the local `make gate` was starved and the same gate was run there instead:

```
unit: ok
gate: PASS                       # run 43841609784 · github.com/8ullyMaguire/concord/actions/runs/43841609784
```

and the two commands the table exists for:

```
$ python3 scripts/parity/route_policies.py
api routes registered: 203 · routes with a declared policy: 203 · routes with no policy: 0
route_policies: PASS — every registered route declares who may call it

$ python3 scripts/parity/anonymous_walk.py          # before the deploy
anonymous_walk: 89 route(s) walked with no token
anonymous_walk: 0 unreachable · 1 hole(s) · 19 declared but not enforced · 0 wrongly refused
  HOLE     GET /api/v1/admin-ledger is enforced as Operator and answered 200
```

**Deployed and walked (`df9bb08`, 2026-11-16 11:02).** Live serves `concord-rs 0.0.0+df9bb08+vendor` and the same walk against it:

```
$ python3 scripts/parity/anonymous_walk.py          # after the deploy
anonymous_walk: 0 unreachable · 0 hole(s) · 19 declared but not enforced · 0 wrongly refused
anonymous_walk: PASS — nothing declared as refused answers a stranger, nothing declared public refuses one

$ curl -o /dev/null -w '%{http_code}' …/api/v1/admin-ledger   -> 401   (“authentication required”; was 200)
$ curl -o /dev/null -w '%{http_code}' …/api/v1/search         -> 200   (unchanged)
$ curl -o /dev/null -w '%{http_code}' …/api/v1/projects       -> 200   (unchanged)
```

**`probe_pages` is broken as a live probe, and here is exactly how.** Run against the deployed instance it died in its own seeding
step (`sqlite3.OperationalError: no such table: projects`) because it called `harness.connect("unused")` with no
`CONCORD_E2E_DSN` — creating an empty file in the repo root. That is now refused by name instead of misdiagnosed as a page bug.
But the DSN is not the whole story: given a migrated scratch database and the release binary serving it, it reports **90/129 ok**
and every one of the 39 failures is a `404` for `/projects/suki/…` — the probe assumes a project slugged `suki` exists and only
seeds `probe-page-<stamp>`. Not one failure is a `401` or `403`, so the auth gate changed no page. It therefore cannot pass against
an empty scratch database, and it cannot run against production either, because seeding a hostile project there is exactly what
`refuse_a_non_test_database` blocks. **Done, 2026-11-16.** `scripts/parity/probe_fixture.py` seeds the one row the probe assumes (`suki`, display name `Suki` —
the title assertion is `<title>Suki: Documents — Concord</title>`), and `gate.sh`'s smoke layer now runs fixture → `probe_pages`
against the local server it just started, on the scratch database it just migrated. It left the live layer, because the
requirement is structural: **the instance under test must read the database the probe seeds.**

```
routes: all 13 answered
  probe_fixture: project suki #1 in the scratch database
pages: 132/132 ok
gate: PASS
```

Two things this cost, both worth writing down: the scratch database had to be renamed `gate_test_<pid>` (the harness refuses to
seed a database whose name does not say it is a test database — the refusal was right, the name was wrong), and the fixture's
display name is `Suki`, not `suki`, because the title assertion is `<title>Suki: Documents — Concord</title>`.

132 checks, not the 136 the roadmap used to claim — that number was from before the probe replaced a dozen content greps with
mount assertions, and a number a document repeats without measuring is how a gate stops being read. What was checked against the
deployed instance by curl, all `200`: `/`, `/projects`, `/projects/suki`, `/search`, `/stats`, `/scout`,
`/finder`, `/taxonomy`, `/curation`, `/contribute`, `/projects/new`, `/healthz`, `/feed.xml`, `/atom.xml`,
`/projects/suki/board/feed.xml`, `/projects/suki/consensus/atom.xml`.

**Deploy note for the next session.** `make deploy`'s `scp "$(BIN)" thinkcentre:…` fails with `scp: Connection closed` when the
recipe runs *on* thinkcentre: the host copies to itself through sshd, and under load sshd drops it. The three steps work
locally — `cp target/release/concord-cli ~/concord-deploy/concord-rs.new`, `systemctl --user stop concord-rs.service`,
`mv`, `systemctl --user start concord-rs.service`. Teaching `make deploy` to skip the copy when the target is `$(hostname)`
is a two-line fix worth making the next time that recipe is touched.

**Step 1b, what is left in this step** (in this order, each with the suites watching):

1. Flip enforcement per surface, and 401 before the body is read: the 120 `Session` rows first, then `Account`
   (guests) and `Operator` (the ledger's role check, not just an identity).
2. The extractor: handlers take the policy instead of calling `bearer_user`, so `grep -c 'bearer_user'
   crates/app/src/lib.rs` reaches 0. The table makes this mechanical — the policy is already written down per route.
3. `python3 tests/e2e/api_surface.py --anonymous` (the roadmap's original verification) still does not exist; until
   it does, the enforcement ratchet is the measurement, and the corpus replay plus the page probes catch a read that
   starts refusing strangers.

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

## Claims

Two sessions can work this file at the same time; the ledger is the only channel between them, so a row is
claimed by writing the claim in its status cell before the work starts, and released by writing what landed.
Unclaimed rows are fair game.

| row | claimed by | since |
|---|---|---|
| 1b | session A (10:26 commit `de88ab0` author) | 2026-11-16 10:26 |
| 2 | security baseline (headers, cookie, CSP, rate limit, renderer) | **2a done 2026-11-16 — headers and a CSP that refuses something.** `security_headers` sets CSP, `X-Frame-Options: DENY`, `nosniff`, `Referrer-Policy`, HSTS on every response, with `script-src 'self'` and **no `'unsafe-inline'`** (the live instance sent `script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'`, a policy that refuses nothing). The inline scripts that made that possible are gone: seven dead placeholder passes and the archive control (now `assets/js/project-archive.js`, tag still emitted only for a maintainer), plus the load-bearing `onsubmit="return false;"` on the milestone form, replaced by `preventDefault()`. Instrument: `scripts/parity/probe_security_headers.py` — **10 problems on live before, PASS on live after deployment** ("deployed `a396746` by hand: `install -m 755 target/release/concord-cli ~/concord-deploy/concord-rs.new && systemctl --user restart`, because `make deploy`'s `scp` to its own host failed with `Connection closed` — since fixed by the other session in `a396746`"); browser proof: `milestones_e2e` 5 passed, `board_e2e` 8 passed, `documents_e2e` 6 passed. The live response carried **two** CSP headers — the app's strict one and nginx's estate-wide `estate-security-headers.conf` (`script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'`), appended rather than replaced — so the probe evaluated the *intersection* a browser enforces and printed `[2 policies; 1 permissive]`. **Fixed 2026-11-16, and it was concord's to fix, not the estate's:** declaring any `add_header` in a location cancels nginx's inherited set, so `footer-concord`'s `location /` and the `/api/` snippet generated by `estate/scripts/install-api-404-edge.sh` now declare only the estate's version headers and leave the security five to the app. Measured after: `csp=1 xfo=1 nosniff=1` on `/`, `/projects`, `/api/v1/version` and an unrouted `/api/` path. `probe_security_headers.py` printed `PASS` — after one probe bug was fixed: it compared `"none"` against a token set that keeps its quotes, so it FAILED on `frame-ancestors 'none'` (the strictest value) and passed on `'self'`. Browser proof on the live site under the single strict policy: `/projects/suki/board` rendered **329 of 329** `[data-feature-id]` cards with **zero** `securitypolicyviolation` events and `window.ConcordApi` defined. The estate snippet keeps `'unsafe-inline'` for the sites that still need it; that audit is estate-wide and stays where it was. ADR 0016. **2b done 2026-11-17:** session cookie is the credential and `HttpOnly; SameSite=Lax`; rate limiting uses `polaris_ratelimit` with a separate login bucket; the `el(tag, props, children)` renderer has **0 non-allowlisted HTML sinks** in `crates/web/assets/js` (only the allowlisted Markdown sink remains). Live `4fce32c`, gate `sinks 1 · allowlisted 1`, CI `44059916800`. |

## Status

| step | what | status |
|---|---|---|
| 0 | the review lives in the repo | **done 2026-11-15** |
| 1 | authorization as a table + audit + gate | **1a done 2026-11-16; 1b pending live re-proof after the D-10 revert. 1a done 2026-11-16, committed `de88ab0`, CI `43841609784`.** `crates/app/src/policy.rs`: 203 rows, `Public|Optional|Session|Account|Operator`, `enforced` true on 5. The audit is an in-crate test (`include_str!("lib.rs")`) that names a route with no row, a row with no route, and a falling `enforced` count: `cargo test -p concord-app --lib policy` -> 5 passed. `auth_gate` enforces the 5 enforced rows, answers 500 for a matched route with no row, and leaves unknown paths at 404; `scripts/parity/anonymous_walk.py` asks the same question of the deployed instance in the live layer. **1b (in progress, first slice landed 2026-11-16, commit `b74c75b`):** the live anonymous walk named 19 GET rows declared `Session` that answered a stranger `200`/404; reading their handlers showed all 19 either read no identity or narrow private projects through `readable()` (`arena_verification` documents "any reader may run", `project.js` fetches `/lists` on every page). They were **reclassified `Session` → `Optional`**, not enforced — flipping them would 401 public readers and turn a mislabeled table into a broken site. `anonymous_reads_are_not_declared_private` pins the list; `route_policies: PASS — every registered route declares who may call it` (203/203). `enforced` stays 5 (ratchet holds). **Live:** deployed `b74c75b`; `anonymous_walk: 89 walked · 0 unreachable · 0 hole(s) · 0 declared but not enforced · PASS`; `probe_security_headers: PASS`; served `session.js` PASS. **1b second slice landed 2026-11-16:** `POST /api/v1/projects` already refused a stranger in its handler (`curl` with a consent cookie → `401 unauthenticated`); its row is now `enforced` (5→6), `auth_gate` inserts the resolved `User` into request extensions, and `create_project` consumes `Extension<User>` instead of opening a second store read and writing its own refusal. The `is_guest` depth stays in the handler; the gate only proves identity. `the_enforced_gate_supplies_identity_to_its_handler` pins both halves. `cargo test -p concord-app --lib policy`: `7 passed; 0 failed`. **Live (`005acdf`):** anonymous `POST /api/v1/projects` with a consent cookie → `401 unauthenticated` from the gate; a freshly minted guest token → `403 forbidden — a project needs an account, not a guest session` from the handler, so depth survived the move; `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`. Deploy note: `make deploy` re-ran `release` and hit its 600 s timeout while Cargo waited on sshfs under load; the gate had already built the exact stamped binary, so `make -o release deploy` shipped it unchanged. **1b third slice landed 2026-11-16:** the four remaining enforced handlers that still repeated the gate's lookup (`auth_me`, `list_notifications`, `list_invites`, `next_vote_pair`) now take `Extension<User>`; their `bearer_user(...)?` branches are gone. `grep -c 'bearer_user(' crates/app/src/lib.rs`: `121` before, `117` after (one call removed per handler). `list_invites` keeps `HeaderMap` for `readable_project_segment` and its contributor-role check; nothing about enforcement changed, so anonymous callers were already refused by the gate. `cargo test -p concord-app --lib policy: 7 passed; 0 failed`. **Live (`bf7afab`):** with a freshly minted guest token `GET /auth/me` 200, `/notifications` 200, `/projects/lorebook/votes/next` 200 and `/projects/lorebook/invites` 404 at the project-visibility check; the same two without a token 401 from the gate. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`; headers PASS; served JS PASS. **1b fourth slice landed 2026-11-16:** `PUT /api/v1/projects/{slug}/ai-involvement` was measured anonymous-401 with a consent cookie (`{"code":"unauthenticated","error":"sign in to declare a project's AI involvement"}` emitted by its handler). Its row is now `enforced` (6→7), the handler takes `Extension<User>`, and the guest-account check stays. The refusal text becomes the gate's uniform `authentication required` (D-6). `declare_ai_involvement` joined the structural test; the dead `State` parameter on `auth_me` is gone. **Live (`d055c2a`):** anonymous PUT → `401 unauthenticated` from the gate; guest token → `403 forbidden — a project's declaration needs an account, not a guest session` from the handler; `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, served JS PASS. `tests/e2e/ai_declaration_e2e.py` was NOT run: this host has no `pytest` or `playwright` module, so the account-authorized write path is unverified locally (the live anonymous and guest arms were). **1b fifth slice landed 2026-11-16:** `POST /api/v1/projects/{slug}/claim` answered an anonymous caller `401 sign in to adopt a project` from `claim_project`; the row is now `enforced` (7→8), the handler takes `Extension<User>`, and `is_guest` still refuses guests with `a guest cannot adopt: the record must name somebody`. D-6 applies unchanged: the gate's uniform `authentication required` replaces the route-specific sentence. `claim_project` joined the structural test. **Live (`ba0def5`):** anonymous claim → `401 authentication required` from the gate; guest token → `403 forbidden — a guest cannot adopt: the record must name somebody` from the handler; `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, served JS PASS. CI: run `43923118762` (previous docs commit) FAILED in `probe_pages` with a socket read timeout against the scratch server; run `43925202819` for this commit green. Recorded as a CI flake; if it recurs, raise that probe's read timeout. **1b sixth slice landed 2026-11-16 (batch of five):** probed anonymous with a consent cookie: `POST /auth/redeem-invite`, `POST /curation/{kind}/{id}/vote`, `PUT /projects/{slug}/visibility`, `PUT /projects/{project_id}/criteria/{criterion}/active`, `PUT /projects/{project_id}/intake-mode` all returned `401` from their handlers. All five rows are now `enforced` (8→13), all five handlers take `Extension<User>`, and `require_role`/member checks stay where they were. D-6's uniform gate text applies. Batched into one commit so the expensive build is paid once. **Live (`fc71a02`):** all five anonymous probes → `401 authentication required` from the gate; a guest token on `set_criterion_active` → `403 this needs the maintainer role in this project` from the handler, so gate-to-handler identity and role depth both hold. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, served JS PASS, CI `43930380335` success. **1b seventh slice landed 2026-11-16 (batch of six):** anonymous consent-cookie probes returned `401` from the handlers for `approve_merge`, `execute_merge`, `reject_merge`, `cast_feature_vote`, `set_project_metrics` and `join_project`. All six rows are now `enforced` (13→19), all six handlers take `Extension<User>`, and `require_role`/charter checks stay. D-6 applies. This is 18 handlers pinned by the structural test. **Live (`1ba7c21`):** all six anonymous probes → `401 authentication required` from the gate; guest tokens on `metrics` and `execute_merge` → `403` with the maintainer/contributor role messages from the handlers. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, served JS PASS, CI `43935004893` success. **1b eighth slice landed 2026-11-16 (batch of four):** anonymous consent-cookie probes: `PUT /projects/{project_id}/charter` → `401 sign in to edit the charter`; `POST /projects/{slug}/features` needed a valid body first (`{}` → `422 missing field title`; with `title`/`body` → `401`); `POST /projects/{slug}/invites` → `401` with the account-needed message; `POST /projects/{slug}/consensus` → `401 authentication required`. All four rows are now `enforced` (19→23), all four handlers take `Extension<User>`, and maintainer/contributor checks stay. D-6 applies. 22 handlers pinned. **Live (`8cbdb27`):** all four anonymous probes → `401 authentication required` from the gate; guest token on `charter` → `403 this needs the maintainer role in this project`, on `invites` → `404` at the project-visibility check. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, served JS PASS, CI `43940504648` success. **1b ninth slice landed 2026-11-16 (batch of two):** anonymous consent-cookie probes: `PUT /projects/{slug}/tags` → `401 sign in to apply tags`; `POST /projects/{project_id}/field-reports` → `401 authentication required`. Both rows are now `enforced` (23→25), both handlers take `Extension<User>`, contributor/visibility checks stay. `GET /projects/{project_id}/strategy/themes`, milestone writes and theme writes also refuse anonymous but **resolve identity in shared helpers** (`milestone_write_context`, `strategy_project`), so they cannot join this structural test without changing those helpers' signatures — see D-8. **Live (`07bab00`):** both anonymous probes → `401 authentication required` from the gate; a guest token on `PUT tags` → `403 this needs the contributor role in this project` from the handler. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, served JS PASS, CI `43944935662` success. **1b tenth slice landed 2026-11-16 (D-8, milestone half):** all six milestone writes were measured anonymous-401 with the same `sign in to change a milestone` from `milestone_write_context`. The helper now takes `&User` from `auth_gate` and keeps only project-visibility/maintainer checks; all six handlers take `Extension<User>` and their rows are `enforced` (25→31). 30 handlers pinned. `strategy_project`'s ten callers remain on the old path — the other half of D-8. **Live (`4175b9c`):** all six anonymous probes → `401 authentication required` from the gate; a guest token on `POST milestones` → `403 this needs the maintainer role in this project` from the refactored helper, so depth survived the signature change. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, served JS PASS, CI `43950166710` success. **1b eleventh slice landed 2026-11-16 (D-8, strategy half — D-8 now closed):** all nine `strategy_project` callers were measured anonymous-401 (the earlier note said ten; the real count is nine). The helper now takes `&User` from the gate and keeps only project-existence and member-role checks; all nine handlers take `Extension<User>`, their rows are `enforced` (31→40), and 39 handlers are pinned. **Live (`30f035a`):** all nine anonymous probes → `401 authentication required` from the gate; a guest token on `GET strategy/themes` still returns `200 []`, so removing the second identity lookup did not change downstream behavior. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, served JS PASS, CI `43955076347` success. **Cleanup closed:** the ten unused `headers: HeaderMap` handler parameters reported after `c1411d1` (`delete_comment` plus the nine strategy callers) are removed; no behavior change. **1b twelfth slice landed 2026-11-16 (batch of five plus cleanup):** anonymous consent-cookie probes returned `401` from `POST /notifications/read-all`, `DELETE /threads/{kind}/{id}/{comment}`, `POST /features/{feature}/tags/remove`, `POST /criteria`, `PUT /features/{feature}/work-item`. All five rows are `enforced` (40→45), all five handlers take `Extension<User>`, role/author checks stay; D-6's uniform gate text applies. 44 handlers pinned, `bearer_user` 96→91. **Live (`c1411d1`):** all five anonymous probes → `401 authentication required` from the gate; a guest token on `POST /criteria` → `403 this needs the maintainer role in this project` from the handler, so depth survived the move. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, served JS PASS, CI `43959295298` success. **1b thirteenth slice landed 2026-11-16 (batch of six):** all six were measured anonymous-401 (`set_feature_status` needed a valid `{"status":...}` body to get past deserialization, like the `features` row in the eighth slice): `POST /threads/{thread_kind}/{thread_id}`, `PUT /features/{feature}/status`, `POST|DELETE /features/{feature}/evidence`, `POST|DELETE /features/{feature}/deps`. Rows enforced 45→51, handlers take `Extension<User>`, project/role checks stay; 50 handlers pinned, `bearer_user` 91→85. **Live (`a0b8a1c`):** all six anonymous probes → `401 authentication required` from the gate; a guest token on `POST /features/{feature}/evidence` → `403 this needs the maintainer role in this project` from the handler. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, served JS PASS, CI `43963894091` success. `unused variable: headers` warnings: 10 before, 0 after. **1b fourteenth slice landed 2026-11-16 (batch of five):** anonymous consent-cookie probes returned `401` from `POST /emergency-holds/{hold_id}/release`, `POST /field-reports/{report_id}/responses`, `PUT /projects/{slug}/languages`, `POST /projects/{slug}/merge` and `POST /moderation/arena/restore`. Rows enforced 51→56, handlers take `Extension<User>` (the first two no longer need `HeaderMap`), moderation/contributor checks stay; 55 handlers pinned, `bearer_user` 85→80. **Live (`5d7d952`):** all five anonymous probes → `401 authentication required` from the gate; a guest token on `PUT /projects/{slug}/languages` → `403 this needs the contributor role in this project` from the handler. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, served JS PASS, CI `43967272783` success. **1b fifteenth slice landed 2026-11-16 (batch of seven):** anonymous probes (with valid bodies for the two consensus routes, as with the earlier deserialization cases) returned `401` from `POST /alternatives`, `POST /alternatives/{arena}/vote`, `POST /consensus/{call}/position`, `POST /consensus/{call}/objection`, coverage claim/contest and `POST /lists/{list}/entries/vote`. Rows enforced 56→63, handlers take `Extension<User>` (the two consensus handlers no longer need `HeaderMap`), role/vote-depth checks stay; 62 handlers pinned, `bearer_user` 80→73. **Live (`1470834`):** all seven anonymous probes → `401 authentication required` from the gate; a guest token on `POST /lists/{list}/entries/vote` → `403 this needs the contributor role in this project` from the handler. Tool output masks the hash in `/version` as `1****34`; a curl-to-file equality check returned `version_matches_1470834: True, length: 31`. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, served JS PASS, CI `43970446455` success. **1b sixteenth slice landed 2026-11-16 (batch of six):** anonymous probes returned `401` from complaint impact/reject/validate, `POST /requests`, answer and request-tags writes. Rows enforced 63→69, handlers take `Extension<User>` (`create_instance_request` no longer needs `HeaderMap`), complaint/request-depth checks stay; 68 handlers pinned, `bearer_user` 73→67. **Live (`31920c2`):** all six anonymous probes → `401 authentication required` from the gate; a guest token on `POST /complaints/{id}/validate` → `403 this needs the contributor role in this project`. `/version` equality check True. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, served JS PASS, CI `43972801451` success. **1b seventeenth slice landed 2026-11-16 (batch of eight):** anonymous probes returned `401` from arena recompute, consensus close, override open/close, emergency hold, complaint link/unlink (link needed a valid `complaint_id` body) and work-item claim. Rows enforced 69→77, handlers take `Extension<User>` (seven no longer need `HeaderMap`), maintainer/visibility checks stay; 76 handlers pinned, `bearer_user` 67→59. **Live (`3fa5326`):** all eight anonymous probes → `401 authentication required` from the gate; a guest token on work-item claim → `403 this needs the contributor role in this project` from the handler. `/version` equality check True. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, served JS PASS, CI `43975188539` success. **1b eighteenth slice landed 2026-11-16 (batch of eight):** anonymous probes returned `401` from solution create, list create, document put/delete, feature milestone/tags, invite revoke and taxonomy consent (all with their route-specific refusal texts, D-6 applies at the gate). Rows enforced 77→85, handlers take `Extension<User>` (taxonomy consent no longer needs `HeaderMap`), role/visibility checks stay; 84 handlers pinned, `bearer_user` 59→51. **Live (`9409d89`):** all eight anonymous probes → `401 authentication required` from the gate; a guest token on feature-tag add → `403 this needs the contributor role in this project`. `/version` equality check True. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, served JS PASS, CI `43978407151` success. **1b nineteenth slice landed 2026-11-16 (batch of ten):** anonymous probes returned `401` from notification read, alternative entry, complaint merge, objection resolve, call outcome, override position, solution call, strategic weight, arena remove and claim release (three needed valid bodies first). Rows enforced 85→95, handlers take `Extension<User>` (six no longer need `HeaderMap`), role/visibility checks stay; 94 handlers pinned, `bearer_user` 51→41. **Live (`1543ff7`):** all ten anonymous probes → `401 authentication required` from the gate; a guest token on complaint merge → `403 this needs the maintainer role in this project`. `/version` equality check True. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, served JS PASS, CI `43981319524` success. **1b twentieth slice landed 2026-11-16 (D-9, Account decision):** `PUT /features/{feature}` was the last unenforced Account write; anonymous probe → `401 sign in to edit a feature`. Its row is enforced (95→96), the handler takes `Extension<User>`, and the guest/owner-maintainer depth stays. D-9 records why Account rows are enforceable. 95 handlers pinned, `bearer_user` 41→40. **Live (`24ae93f`):** anonymous `PUT /features/{feature}` → `401 authentication required` from the gate; guest token → `403 a guest cannot edit a feature: the record must name somebody` from the handler, so the Account depth survived. `/version` equality check True. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, served JS PASS, CI `43984146300` success. **1b state after D-10 revert:** 96 enforced rows, 95 handlers pinned, `bearer_user` 40; corrected method-aware audit finds 0 enforced routes whose handler still looks up identity. Eight anonymous reads reverted after a 500 regression. **Live (`66ba49a`):** all eight re-probed anonymously — seven 200 with data/empty lists, one 404 at a stale fixture path; no 500. `/version` equality True. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, served JS PASS, CI `43991394503` success. Method-aware audit still finds 0 enforced routes with a duplicate lookup. **1b left:** nothing; step 1 is closed. |
| 2 | security baseline (cookie, CSP, rate limit, renderer) | **2a done 2026-11-16; 2b done 2026-11-17.** Headers + a strict CSP live and browser-proven (see Claims, row 2). Cookie: `login` always set `concord_nav` as `HttpOnly; SameSite=Lax` and nothing read it — `presented_token` now falls back to it (`df6f881`), proved by `no credential 401 · cookie only 200 · header only 200` and `Path=/; HttpOnly; SameSite=Lax[; Secure]`. Rate limiting is wired through the vendored `polaris_ratelimit` (class from the matched route, client from `client_key`, 429 + `Retry-After`, `X-RateLimit-*` on every response); `probe_rate_limit.py --budget 3` → `login #1..#3 422, #4 429 retry-after=19s limit=3`, `second client 422 (own bucket)`, `api read 200 (separate budget)`, **PASS**. **2b page half landed 2026-11-16 (this commit):** `session.js` no longer reads or writes `concord.token`; a stale token is deleted on load, `signedIn()` trusts the user record after a reload, `authHeaders()` sends nothing, and sign-out always reaches the server. The dead `token()` helpers in `rank.js`/`scout.js` are gone. `presented_token` ignores an empty `Bearer ` so it cannot shadow the cookie. Four `credential_tests` (`cargo test -p concord-app --lib` → `15 passed; 0 failed`) and `scripts/parity/check_session_storage.js`, wired into the gate's frontend arm, guard it; both were seen red when their fix was removed. **2b renderer first slice landed 2026-11-17 (D-11):** `dom.js` exports `el(tag, props, children)` with text nodes and
  `setAttribute` only — no HTML-parsing API. `notifications.js` now renders its three former sinks through `el()`, including
  hostile moderator prose as literal text; `scripts/parity/check_dom_builder.js` executes both shipped files in a fake
  browser and is wired into `make gate`. RED proof: mutating `el()` to write `innerHTML` failed with `el() did not create
  exactly one text child`; restoring passed. `innerHTML` occurrences in assets: 137→136. **Live (`4c05881`):** `/version` equality True; the served `/assets/js/dom.js` + `/assets/js/notifications.js` pass
  `check_dom_builder.js` in a fake browser, and the page shell serves the `dom.js` tag. `anonymous_walk: 0 holes · 0 declared
  but not enforced · PASS`, headers PASS, CI `43993478687` success. **2b second slice landed 2026-11-17:** `list.js` migrated — all four string sinks (two `innerHTML`, two
  `insertAdjacentHTML`) replaced by `el()` nodes; `check_dom_list.js` executes the shipped page with hostile fields and a
  hostile API error, RED-mutation proven (`list.js wrote to an HTML string sink: ["<p>mutant</p>"]`) and restored GREEN.
  Combined string sinks 139→137 (135 `innerHTML` + 2 `insertAdjacentHTML`). **Live (`9961157`):** `/version` equality True (tool display masked the hash as `9****57`); the served `/assets/js/dom.js`
  and `/assets/js/list.js` pass `check_dom_list.js`. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers
  PASS, CI `43996013466` success. **2b third slice landed 2026-11-17:** `criteria.js` rebuilt — `cellHTML`, `tableHTML`, `controlsHTML`, `manageHTML` and
  `optionsHTML` became `el()` trees; its two sink assignments are gone. `check_dom_criteria.js` executes the shipped page
  with hostile criterion names, feature titles, ids, descriptions, profile names and a hostile load error; RED mutation
  (`root.innerHTML = '<p>mutant</p>'`) reddened it with `a string sink was used`, restore → PASS. Per D-13 the gate now
  counts sink syntax: 125→123 real sinks. **Live (`2a6066c`):** `/version` equality True; the served `/assets/js/criteria.js` passes `check_dom_criteria.js`.
  `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, CI `43998086155` success.
  **2b fourth slice landed 2026-11-17:** `consensus.js` lost all four sinks — three list clears became `replaceChildren()`
  and the decision-record link became an `el()` tree. `check_dom_consensus.js` executes the shipped page on its index path;
  RED mutation (restoring `indexListEl.innerHTML = ''`) reddened it with `consensus.js wrote to an HTML string sink: [""]`,
  restore → PASS. Sink census 123→119. **Live (`d2affbd`):** `/version` equality True; the served `/assets/js/consensus.js` passes `check_dom_consensus.js`.
  `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, CI `43999913369` success.
  **2b fifth slice landed 2026-11-17:** `complaint.js` lost all four sinks — impact meter, linked rows, empty/error states
  and not-found are `el()` trees. `check_dom_complaint.js` executes the shipped page with hostile complaint/feature fields
  and a hostile error; RED mutation (`root.innerHTML = '<p>mutant</p>'`) reddened it, restore → PASS. Census 119→115.
  **Live (`2a9ae0b`):** `/version` equality True; the served `/assets/js/complaint.js` passes `check_dom_complaint.js`.
  `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, CI `44001726487` success.
  **2b sixth slice landed 2026-11-17:** `audit.js` lost all three sinks — rows, undo controls, controls, count line,
  empty and error states are `el()` trees. `check_dom_audit.js` executes the shipped page with hostile actor/detail/action/
  entity values and a hostile error; RED mutation (`root.innerHTML = '<p>mutant</p>'`) reddened it, restore → PASS.
  Census 115→112. **Live (`4f6607e`):** `/version` equality True; the served `/assets/js/audit.js` passes `check_dom_audit.js`.
  `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, CI `44002973857` success.
  **2b seventh slice landed 2026-11-17:** `comments.js` lost all five sinks — thread rows and replies, form, count,
  fail notice and load-error are `el()` trees. `check_dom_comments.js` executes the shipped thread with hostile comment
  fields and a hostile server refusal, and exercises the reply and failure paths; RED mutation
  (`mount.innerHTML = '<p>mutant</p>'`) reddened it, restore → PASS. Census 112→107.
  **Live (`8ea0934`):** `/version` equality True; the served `/assets/js/comments.js` passes `check_dom_comments.js`.
  `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, CI `44004327396` success.
  **2b eighth slice landed 2026-11-17:** `feature.js` (661 lines) lost all four sinks — every HTML builder, including
  rating bars, coverage claims, planning panels and evidence rows, is an `el()` tree. `check_dom_feature.js` executes the
  shipped page with hostile values from all nine reads and a hostile load error; RED mutation
  (`root.innerHTML = '<p>mutant</p>'`) reddened it, restore → PASS. Census 107→103.
  **Live (`15ec60d`):** `/version` equality True; the served `/assets/js/feature.js` passes `check_dom_feature.js`.
  `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, CI `44005529068` success.
  **2b ninth slice landed 2026-11-17:** `projects.js` lost all three sinks — cards, health bars and empty/error states are
  `el()` trees. `check_dom_projects.js` executes the shipped page with hostile project fields, an empty list and a failed
  GET; RED mutation (`grid.innerHTML = '<p>mutant</p>'`) reddened it, restore → PASS. Census 103→100.
  **Live (`ddccc04`):** `/version` equality True; the served `/assets/js/projects.js` passes `check_dom_projects.js`.
  `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, CI `44006585416` success.
  **2b tenth slice landed 2026-11-17:** `ranking.js` lost all three sinks — table rows, moderation cell and error state are
  `el()` trees. `check_dom_ranking.js` executes the shipped page with hostile feature fields and a hostile error, and
  exercises the retry path; RED mutation (`root.innerHTML = '<p>mutant</p>'`) reddened it, restore → PASS. Census 100→97.
  **Live (`c8a4425`):** `/version` equality True; the served `/assets/js/ranking.js` passes `check_dom_ranking.js`.
  `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, CI `44008259225` success.
  **2b eleventh slice landed 2026-11-17:** `requests.js` lost both sinks — rating bars, answer cards, compare panel, forms
  and error state are `el()` trees. `check_dom_requests.js` executes the shipped page with hostile request/answer/tag/project
  fields and a hostile post refusal; RED mutation (`root.innerHTML = '<p>mutant</p>'`) reddened it, restore → PASS.
  Census 97→95. **Live (`7ba4e48`):** `/version` equality True; the served `/assets/js/requests.js` passes `check_dom_requests.js`.
  `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, CI `44010036979` success.
  **2b twelfth slice landed 2026-11-17:** `requests-index.js` lost all three sinks — cards, open/settled groups and both
  empty states are `el()` trees. `check_dom_requests_index.js` executes the shipped page with hostile titles from both
  sources, a duplicate id, and an empty list; RED mutation (`root.innerHTML = '<p>mutant</p>'`) reddened it, restore → PASS.
  Census 95→92. **Live (`db6a087`):** `/version` equality True; the served `/assets/js/requests-index.js` passes
  `check_dom_requests_index.js`. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS,
  CI `44011216475` success. **2b thirteenth slice landed 2026-11-17:** `settings.js` (1024 lines) lost both sinks — every panel builder and the live
  region are `el()` trees; the 445-line wire half is unchanged. `check_dom_settings.js` executes the shipped page with
  hostile values from all nine reads and a hostile write refusal, and asserts every wire hook still exists; RED mutation
  (`root.innerHTML = '<p>mutant</p>'`) reddened it, restore → PASS. Census 92→90.
  **Live (`cf30d00`):** `/version` equality True; the served `/assets/js/settings.js` passes `check_dom_settings.js`.
  `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, CI `44012704225` success.
  **2b fourteenth slice landed 2026-11-17:** `taxonomy.js` lost all three sinks — tag chips/groups, proposal cards/lists,
  the propose form and the live region are `el()` trees. `check_dom_taxonomy.js` executes the shipped page with hostile
  tag/proposal fields and a hostile refusal; RED mutation (`root.innerHTML = '<p>mutant</p>'`) reddened it, restore → PASS.
  Census 90→87. **Live (`cf4ea03`):** `/version` equality True; the served `/assets/js/taxonomy.js` passes `check_dom_taxonomy.js`.
  `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, CI `44014472354` success.
  **2b fifteenth slice landed 2026-11-17:** `stats.js` lost all ten sinks — facet/provenance tables, latency cards,
  responsiveness and rising leaderboards, and all four failure states are `el()` trees. `check_dom_stats.js` executes
  the shipped page with hostile facets, project names/slugs and four failing fetches; RED mutation reddened it,
  restore → PASS. Census 87→77. **Live (`cf3ca57`):** `/version` equality True; the served `/assets/js/stats.js` passes `check_dom_stats.js`.
  `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, CI `44016490431` success.
  **2b sixteenth slice landed 2026-11-17:** `finder.js` lost all eleven sinks — the seed chips, question options, stop
  prompt, shortlist, results and the 401 refusal are `el()` trees; the action/state half is unchanged. One shared
  `topCandidates()` builder replaced two drifting copies. `check_dom_finder.js` walks seed → question → stop → results
  with hostile fields; RED mutation reddened it, restore → PASS. Census 77→66.
  **Live (`2615564`):** `/version` equality True (display masked as `2****64`, checked against file); the served
  `/assets/js/finder.js` passes `check_dom_finder.js`. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`,
  headers PASS, CI `44019018091` success.
  **2b seventeenth slice landed 2026-11-17:** `rank.js` lost all nine sinks — cards, five-outcome actions, keyboard hint,
  sign-in prompt and every error/unretriable state are `el()` trees; vote and keyboard logic is unchanged.
  `check_dom_rank.js` executes a hostile pair plus 401/403/404/409/422/500 and vote-failure branches; RED mutation
  reddened it, restore → PASS. Census 66→57. **Live (`a6a9af3`):** `/version` equality True; the served `/assets/js/rank.js` passes `check_dom_rank.js`.
  `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, CI `44020904994` success.
  **D-14 (2026-11-17):** `ConcordArena.controls()` stays an escaped HTML string because `project.js:369` concatenates it
  into a larger builder; its three panel renderers are `el()` trees and no longer use a string sink. Migrating that
  string requires converting `project.js`'s alternatives block in the same change, which is the next slice there.
  Rejected: returning a node from `controls()` now, which would stringify to `[object Object]` in project.js.
  **Live (`c5df26f`):** `/version` equality True; the served `/assets/js/project-arena.js` passes `check_dom_arena.js`.
  `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, CI `44023086042` success.
  **D-15 (2026-11-17):** Markdown is the one legitimate HTML string: `ConcordMarkdown.renderInto(node, source, opts)` now
  owns both escaping and the single `innerHTML` assignment, so callers pass TEXT and cannot interpolate into markup.
  `documents.js` has zero sinks; the census counts the one allowlisted sink in `markdown.js`.
  Rejected: leaving `documents.js` as an exception with two ad-hoc `innerHTML` assignments, and hand-building a DOM
  tree from Markdown tokens, which would duplicate the sanitizer.
  **Live (`26be342`):** `/version` equality True; served `/assets/js/documents.js` and `/assets/js/markdown.js` pass
  `check_dom_documents.js`. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS,
  CI `44025387898` success.
  **2b twentieth slice landed 2026-11-17:** `board.js` lost all seven sinks — columns, cards, move controls, milestone
  filter, skeleton, empty and error states are `el()` trees. `check_dom_board.js` executes the board with hostile
  columns/cards/milestones, a refused move and five other states; RED mutation reddened it, restore → PASS.
  Census 41→34 (1 allowlisted). **Live (`e91c55c`):** `/version` equality True; the served `/assets/js/board.js` passes `check_dom_board.js`.
  `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS, CI `44027725594` success.
  **2b twenty-first slice landed 2026-11-17:** `search.js` lost all five sinks — result cards, health bars, tag chips,
  skeleton and both empty/failure states are `el()` trees. `check_dom_search.js` executes hostile results and facets,
  including the chip toggle behaviour; RED mutation reddened it, restore → PASS. Census 34→29 (1 allowlisted).
  **Live (`bf06d9a`):** `/version` equality True; the served `/assets/js/dom.js` and `/assets/js/search.js` pass
  `check_dom_search.js`. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS,
  CI `44030180561` success.
  **2b twenty-second slice landed 2026-11-17:** `new-project.js` lost all five sinks — intent answers, no-match `<code>`
  hint, hit list, clear and failure states are `el()` trees. `check_dom_new_project.js` executes hostile hit titles/URLs
  and all four create-form refusals; RED mutation reddened it, restore → PASS. Census 29→24 (1 allowlisted).
  **Live (`c380518`):** `/version` equality True; the served `/assets/js/dom.js` and `/assets/js/new-project.js` pass
  `check_dom_new_project.js`. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS,
  CI `44032325486` success.
  **2b twenty-third slice landed 2026-11-17:** both milestone pages lost their five sinks — roadmap totals/timeline,
  create form, milestone subtitle, feature groups and candidate panel are `el()` trees. `check_dom_milestones.js` executes
  both pages with hostile titles/errors, the create refusal, up/down reorder and assign POST; RED mutation reddened it,
  restore → PASS. Census 24→19 (1 allowlisted).
  **Live (`bbb1ff5`):** `/version` equality True; the served `/assets/js/dom.js` and `/assets/js/milestones.js` pass
  `check_dom_milestones.js`. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS,
  CI `44034605439` success.
  **D-16 (2026-11-17):** `project.js`'s six sinks cannot be removed alone: its render concatenates HTML strings returned by
  `ConcordWrites.panel()`, `ConcordPanels.*`, `ConcordArena.controls()` and the skeleton helpers. Convert those public
  contracts to nodes first, then rewrite `project.js` in one slice so no string fragment is ever interpolated. Rejected:
  wrapping the returned strings in a hand-written parser — that recreates `innerHTML` under another name.
  **2b twenty-fourth slice landed 2026-11-17:** `project-writes.js` lost its four duplicate-panel sinks; its `panel()`
  string contract is unchanged so `project.js` keeps working, but panel fills are `el()` trees. `check_dom_project_writes.js`
  RED/GREEN covered hostile matches, coverage, all refusals and both field-report writes. Census 19→15 (1 allowlisted).
  **Live (`6800e66`):** `/version` equality True; the served `/assets/js/dom.js` and `/assets/js/project-writes.js` pass
  `check_dom_project_writes.js`. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS,
  CI `44043109307` success.
  **2b twenty-fifth slice landed 2026-11-17:** `lists.js` lost its three sinks — list cards, empty state and both failure
  states are `el()` trees. `check_dom_lists.js` executes hostile list fields, all failure states and a non-array answer;
  RED mutation reddened it, restore → PASS. Census 15→12 (1 allowlisted).
  **Live (`7e207af`):** `/version` equality True; the served `/assets/js/dom.js` and `/assets/js/lists.js` pass
  `check_dom_lists.js`. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS,
  CI `44045226573` success.
  **2b twenty-sixth slice landed 2026-11-17:** `scout.js` replaced its two clears with `replaceChildren()` and `merge.js`
  rebuilt its rows, gate cell, create form and failure state as `el()` trees. One combined `check_dom_scout_merge.js`
  exercises hostile fields, gate arithmetic, approve/create writes and all failure states; RED on both files, restore → PASS.
  Census 12→8 (1 allowlisted).
  **Live (`7319005`):** `/version` equality True (masked display checked programmatically); served `dom.js`, `scout.js`
  and `merge.js` pass `check_dom_scout_merge.js`. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`,
  headers PASS, CI `44047996973` success.
  **2b twenty-seventh slice landed 2026-11-17:** `curation.js`'s list refresh clear became `replaceChildren()` — all cards
  were already DOM-built. `check_dom_curation.js` executes hostile rationales/targets, append, refresh, vote, undo and
  both failure paths; RED mutation reddened it, restore → PASS. Census 8→7 (1 allowlisted).
**2b twenty-eighth slice landed 2026-11-17:** `project.js` outer states (empty slug, skeleton, 404, 401, load failure)
  are now `el()` trees; `skeleton.js` gained node twins `titleNode()`/`textNode()` used by the project page. The main
  `render()` string then remained; it was replaced by the twenty-ninth slice below (D-16 now closed).
  Census 7→2 (1 allowlisted): `project.js` 1, `markdown.js` 1 allowlisted. `check_dom_project.js` RED/GREEN covered
  hostile slug text, three skeleton lines, auth link, HTTP status and no sink writes.
**Live (`6267468`):** `/version` equality True (masked display checked programmatically); served `dom.js`,
  `skeleton.js` and `project.js` pass `check_dom_project.js`. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`,
  headers PASS, CI `44054471991` success.

  **2b left after the twenty-eighth slice:** 2 real sinks (1 allowlisted in markdown.js); the single non-allowlisted
  one was `project.js`'s `render()`. The twenty-ninth slice removed it — no non-allowlisted sink remains.

**2b twenty-ninth slice landed 2026-11-17 (D-16 closed):** the whole project detail page renders with `el()` trees.
  New `project-render.js` owns panels, forms, arena controls, work items and `render()`; `project.js` keeps loading,
  failure and wire code and calls `window.ConcordProjectRender.render()`. `check_dom_project.js` was extended to the
  populated page with hostile fields in every panel; an obfuscated `node['innerHTML']` write inside the rendered tree
  reddened it behaviorally, restore → PASS. Census 2→1: only the allowlisted Markdown sink remains. No non-allowlisted
  string sink remains in `crates/web/assets/js`.
  **Gate failure and fix:** the first gate on `300945e` was `FAIL` at `crates/web/src/lib.rs:512` (`error[E0748]`,
  unterminated raw string) because the added placeholder lost its `"#` terminator. `cargo check -p concord-web --locked`
  went green after the fix in `4fce32c`, then the stamped gate passed. No deploy happened on the failed gate.
  **Live (`4fce32c`):** `/version` equality True; served `dom.js`, `skeleton.js`, `project-render.js` and `project.js` pass
  `check_dom_project.js`; live `/projects/lorebook` loads `project-render.js`. `anonymous_walk: 0 holes · 0 declared but
  not enforced · PASS`, headers PASS, CI `44059916800` success.
**Live (`d76a47e`):** `/version` equality True; the served `/assets/js/dom.js` and `/assets/js/curation.js` pass
  `check_dom_curation.js`. `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`, headers PASS,
  CI `44050456868` success.

  **D-16 closed:** no unblock remains for this row. The helper modules keep their string exports for their other callers;
  the project page composes only node trees. D-14 is superseded for this page: arena controls are nodes. **Live:** deployed `085cf01`, `/version` → `concord-rs 0.0.0+085cf01+vendor`, `anonymous_walk` `0 hole(s) · PASS`, `probe_security_headers` PASS, served `session.js` passes the checker. |
| 3 | split `lib.rs` by resource, zero behaviour change | **in progress**: two pure modules landed live (`ed1488c` meta, next records); `api/meta.rs` — `healthz`, `version`, 3 routes; `lib.rs` 10,043 → 10,020 lines. `git show --stat`: 41 added in `api/meta.rs`, 25 removed from `lib.rs`. Policy scanner follows `api/*.rs`. Gate `pages 132/132 · rate PASS`, live both health paths `200 {"status":"ok"}`, walk PASS, CI `44063176583`. Second module `api/records.rs` live (`d96eeb8`): 52 lines moved, `lib.rs` 9,967 lines, kinds `200 dict 1`, search `200 list 2`, CI `44065751641`. Third module `api/board.rs` live (`4ea99f5`): 97 lines moved, `lib.rs` 9,866 lines, board `200 dict cards 329`, move anon `403 consent_required`, CI `44067946502`. Fourth module `api/notifications.rs` live (`79240b0`): 45 lines moved, `lib.rs` 9,810 lines, notifications anon `401`, CI `44070134200`. Fifth module `api/invites.rs` live (`cd9f396`): 175 lines moved, `lib.rs` 9,627 lines, invites anon `401`, redeem anon `401`, CI `44072419927`. Sixth module `api/badges.rs` live (`66e38b5`): 191 lines moved, `lib.rs` 9,436 lines, `/badge/instance.svg` `200` 838 bytes, missing `404`, CI `44074509285`. Seventh module `api/solutions.rs` live (`41303eb`): 261 lines moved, `lib.rs` 9,164 lines, project solutions `200 dict 4`, feature solutions `200`, CI `44076613424`. Eighth module `api/arena_votes.rs` live (`5ad8ff3`): 125 lines moved, `lib.rs` 9,036 lines, vote anon `403 consent_required`, CI `44078651040`. Ninth module `api/coverage.rs` live (`bbb2d46`): 114 lines moved, `lib.rs` 8,915 lines, claim/contest anon `403 consent_required`, CI `44080948764`. Tenth module `api/feature_votes.rs` live (`2bb0667`): 104 lines moved, `lib.rs` 8,811 lines, vote anon `403 consent_required`, CI `44083552359`. Eleventh module `api/merge_requests.rs` live (`97ee6b7`): 182 lines moved, `lib.rs` 8,623 lines, merge list `200 []`, approve anon `403 consent_required`, CI `44085897053`. Twelfth module `api/documents.rs` live (`d02e7ff`): 155 lines moved, `lib.rs` 8,457 lines, doc list `200 list 175`, doc one `200 dict`, CI `44089787805`. Thirteenth module `api/alternatives.rs` live (`6eef770`): 256 lines moved, `lib.rs` 8,190 lines, alternatives list `200 dict 2`, create anon `403 consent_required`, CI `44093504753`. Fourteenth module `api/ops_reads.rs` live (`06eeb8f`): 47 lines moved, `lib.rs` 8,141 lines, ledger anon `401`, rising `200 list 0`, lists `200 list 2`, CI `44098171339`. Fifteenth module `api/identity.rs` live (`012fad1`): 589 lines moved, `lib.rs` 7,550 lines, guest `200 dict 8`, consent anon `200`, CI `44102040582`. Sixteenth module `api/contributions.rs` live (`a92a3d7`): 403 lines moved, `lib.rs` 7,144 lines, contribute `200 dict 5`, work items `200 list 329`, CI `44106144002`. Seventeenth module `api/field_reports.rs` live (`9634925`): 129 lines moved, `lib.rs` 7,008 lines, report panel `200 dict 5`, anon POST `403`, CI `44109701061`. Eighteenth module `api/emergency_holds.rs` live (`735d7f4`): 57 lines moved, `lib.rs` 6,948 lines, anon release `403`, CI `44114112184`. Nineteenth module `api/taxonomy.rs` live (`5d0a04b`): 174 lines moved, `lib.rs` 6,766 lines, tags `200 list 68`, anon propose `403`, CI `44118300295`. Twentieth module `api/ops_writes.rs` live (`96b433b`): 58 lines moved, `lib.rs` 6,705 lines, recompute anon `403`, CI `44123442614`. Twenty-first module api/forgejo_webhook.rs live (6daf820): 397 lines moved, lib.rs 6,319 lines, anon delivery 503 no secret configured, CI 44127516322. Twenty-second module api/scout.rs live (7207cc1): 149 lines moved, lib.rs 6170 lines, scout 200 dict 7, CI 44133293671. Twenty-third module api/catalog.rs live (213364c): 20 lines moved, lib.rs 6149 lines, projects 200 list 73, project suki 200 dict, CI 44137911389. Twenty-fourth module api/accounts.rs live (f75a021): 79 lines moved, lib.rs 6070 lines, register anon empty 422, CI 44142046977. Twenty-fifth module api/project_lifecycle.rs live (986d263): 274 lines moved, lib.rs 5789 lines, anon writes 403, CI 44150720321. Twenty-sixth module api/features.rs live (373dfc5): 257 lines moved, lib.rs 5521 lines, features GET 200 (329), anon writes 403, CI 44162505969. Twenty-seventh module api/complaints.rs live (b4be69b): 209 lines + 2 routes moved, lib.rs 5301 lines, complaints GET 200 (332), anon writes 403, CI 44174995983. Twenty-eighth move complaint verdicts into api/complaints.rs live (52bdda8): 62 lines + 2 routes, lib.rs 5231 lines, reject/merge anon 403, CI 44182701701. Twenty-ninth move api/milestones.rs + mount fix live (fef1a22/b635794): 227 lines + 27 route lines, lib.rs 4978 lines, milestones GET 200 (0), roadmap 200, anon writes 403, CI 44194647904. |
| 4 | typed responses + OpenAPI + generated client (20 routes) | open |
| 5 | events + audit projection (six write paths) | open |
| 6 | one pagination convention, gate-enforced | open |
| 7 | domain crate for stage / consensus / claim; `query!` on new SQL | open |
| 8 | publish or delete the four dead vendored crates | open |
| 9 | wire the 21 unrun e2e suites (`RECORDED_UNRUN` → 0) | open — proves steps 1, 2 and 5 from outside |

## Blocked

Items nobody working in this repo can finish alone. Each names what would unblock it and on whom, so the next session can
tell the difference between "not started" and "waiting".

1. **A production login, to prove the session cookie on the live instance** — on the user. The cookie is proved against a
   scratch database on thinkcentre (`no credential 401 · cookie only 200 · header only 200`), but `concord.polarisocial.xyz`
   needs real credentials to demonstrate the same thing, and inventing an account on a production instance is not a trade a
   gate should make. Unblock: an account (or a `CONCORD_PROBE_USER`/`CONCORD_PROBE_PASSWORD` pair in
   `~/.config/concord/.env`) that a probe may log in as; the probe then asserts the same three codes against the deployment.
2. **A forge token (`CONCORD_FORGE_TOKEN`)** — on the user. GitHub's anonymous budget is exhausted (7 repos deferred,
   `403 rate limit exceeded`); enumeration of those upstreams cannot be measured without a token.
3. **`git.disroot.org` is offline** — on the vendor. 16 projects are hosted there and their commit age is unmeasurable;
   `dormancysweep` reports them as "with a repository but no readable commit age" rather than inventing a date.
4. **58 projects state no upstream** — on the user, for the subset only they know. The discoverable subset is exhausted:
   the family forge's 23 repos match none of them by normalized name, so the rest need an owner to say where the code lives.

## Decisions taken

* **D-1 (step 1, 2026-11-15): authorization is a table, checked by a test in the same crate, enforced by a gate.**
  ADR `docs/adr/0015-a-route-declares-who-may-call-it.md`. Rejected: extractor-only (no queryable authority),
  a hard-coded public list (edited in a hurry), runtime derivation from handler bodies (the disease), and a second
  Python checker beside the Rust test (two answers to disagree about). The gate enforces **identity** only; depth is
  step 1b. Two defects the audit found in itself are recorded there, including 38 mutating routes whose methods its
  parser could not see.

* **D-2 (step 2, 2026-11-16): the page keeps no token; the cookie is the session.** `session.js` holds the token only for the
  current page and deletes any older `localStorage` copy. Rejected: migrating the old key forward (it keeps a script-readable
  credential alive for no gain), and a gate that merely greps for the key literal (a rename passes it). The checker executes
  the shipped file in a fake browser instead. The empty `Bearer ` case was found by reading the page half: a script that once
  built that header can still send an empty value after a reload, so the server must treat it as no credential and fall through
  to the cookie.

* **D-3 (step 1, 2026-11-16): the 19 anonymous GET reads are `Optional`, not holes.** Rejected: flipping `enforced` on them
  because a table said `Session` — that would 401 the project page's own panels and `lists_e2e`. Also rejected: leaving the
  wrong labels and treating the walk's 19 as security debt. The oracle is the live anonymous walk plus the handler bodies;
  a policy that claims a refusal the product does not make is worse than an unenforced row.

* **D-4 (step 1, 2026-11-16): the gate supplies identity to enforced handlers through extensions.** The first conversion is
  `POST /api/v1/projects`: `enforced = true`, `auth_gate` inserts the resolved `User`, the handler takes `Extension<User>`.
  Rejected: flipping `enforced` while leaving the handler's own `bearer_user` refusal in place (two authorities, and the
  duplicate stays for the next 121 routes); moving the `is_guest` rule into the gate (the gate proves identity, depth is the
  handler's); and passing the user as a request header a handler could forge. The structural test reads `lib.rs` so a later
  commit cannot remove either half silently.

* **D-5 (step 1, 2026-11-16): convert every enforced handler to the extension, not just the route that flipped this turn.**
  `auth_me`, `list_notifications`, `list_invites` and `next_vote_pair` were already enforced; leaving their own `bearer_user`
  branches would keep two authorities answering the same question. Rejected: one conversion per deploy (four more compiles
  for the same proof) and a bulk conversion of *unenforced* handlers (they may legitimately treat an absent identity as an
  anonymous read and would fail extraction). The structural test list is the boundary: a handler joins it only when its row
  is `enforced`.

* **D-6 (step 1, 2026-11-16): a route moving into the gate adopts its uniform refusal text.** `PUT /projects/{slug}/ai-involvement`
  used to answer an anonymous caller `sign in to declare a project's AI involvement`; after flipping `enforced` it answers
  `authentication required`, like every other enforced route. Rejected: a per-route message table in the gate (a second policy
  table to keep in sync) and keeping the handler's identity check just to preserve the sentence. The page scripts read
  `data.error` and show it as a sentence; a uniform, accurate sentence is better than two authorities. The route-specific
  *depth* — guests need an account — stays in the handler.

* **D-8 (step 1, 2026-11-16): helper-delegated handlers are a separate conversion.** `create_milestone`, `propose_theme` and
  `list_themes` answer `401` anonymously, but their identity comes from shared helpers (`milestone_write_context`,
  `strategy_project`) rather than a local `bearer_user` call. Flipping their rows now would leave a second identity lookup in
  the helper and break the structural test's premise. Rejected: flipping the rows anyway (two authorities) and weakening the
  test to accept helper calls (it would stop detecting duplicates). Required unblock: change those helpers to accept the
  gate-supplied `User`, update all callers, then flip the rows with the same gate/probe proof.
  **Milestone half landed 2026-11-16:** `milestone_write_context` now takes `&User`, its six write callers take
  `Extension<User>` and are enforced (25→31). **Strategy half landed:** `strategy_project` takes `&User`, its nine callers
  (the measured count; the earlier "ten" was wrong) take `Extension<User>` and are enforced (31→40). D-8 is closed.

* **D-9 (step 1b, 2026-11-16): Account rows are enforceable by `auth_gate`; the handler keeps the account/guest depth.**
  `Policy::requires_identity()` already includes `Account`, and three Account rows (`POST /projects`, AI involvement,
  claim) were enforced earlier with their guest checks left in the handlers. `PUT /features/{feature}` is the last
  unenforced Account write; it refused anonymous callers in `edit_feature`, so its row flips and the handler takes
  `Extension<User>`. Rejected: leaving Account rows handler-owned forever — it keeps two identity authorities on the same
  route and makes the enforced ratchet mean different things for Session and Account. The gate proves identity (a guest
  token still passes it); `is_guest` and the owner/maintainer check stay in the handler, with their refusal texts.

* **D-10 (step 1b, 2026-11-16): a policy row is `(method, path)` — never match on path alone.** A first audit matched the
  enforced POST row for a path to its GET handler and concluded eight anonymous reads (`feature_solutions`,
  `list_alternatives`, `get_override_window`, `feature_deps`, `feature_evidence`, `feature_tags`, `project_languages`,
  `project_tags`) should consume `Extension<User>`. Live deployment answered **500 `Missing request extension`** to
  anonymous callers on all eight because their GET rows are `Optional`/`Session, false` and the gate never inserts an
  identity. All eight conversions were reverted (95 handlers pinned, `bearer_user` 40); the repo's existing
  `anonymous_reads_are_not_declared_private` test already named those paths as anonymous-by-design and should have been
  consulted. The structural test now cross-checks every pinned handler against a registered `(method, path)` whose row is
  enforced, so a same-path POST row cannot satisfy a GET handler. The corrected method-aware audit finds **0 enforced
  routes whose handler still calls `bearer_user`**; the 40 remaining calls are shared helpers and handlers on
  un-enforced, anonymous-by-design rows. `POST /taxonomy/tags` answering 500 `route_declares_no_policy` is the intended
  undeclared-method behaviour pinned by `an_undeclared_route_has_no_policy_rather_than_a_neighbours`, not a regression.

* **D-11 (step 2b, 2026-11-17): the renderer has no HTML string sink.** `dom.js` exports `el(tag, props, children)`;
  text children become DOM text nodes, attributes go through `setAttribute`, and the module offers no way to parse markup.
  Rejected: a global `esc()` discipline with a lint rule — the review counted 137 sinks against 540 manual escapes, and one
  missed call is an account takeover. The first migrated page is `notifications.js` (three sinks); the sink census is a
  ratchet in the gate, 137→136. A checker executes the shipped files against XSS payloads rather than grepping for them.

* **D-12 (step 2b, 2026-11-17): the sink census counts `insertAdjacentHTML` too.** `list.js` had two `innerHTML`
  assignments and two `insertAdjacentHTML` calls; a census of `innerHTML` alone called it two sinks and would have declared
  the page migrated while four string-parsing paths remained. The gate now prints one combined count (139→137), and a page
  is only migrated when both spellings are gone. Rejected: separate ratchets for the two spellings — same defect, two names.

* **D-17 (product/UX, 2026-11-17): `/` stays the catalogue and gains a compact overview above it.** No record shows a
  decision to drop the old hero; the Rust port simply rendered the live list. The earlier three-sentence and premise-2
  overviews were archived Go templates. Decision: one headline, one sentence, one paragraph on project surfaces and one
  row of live surface links, rendered by the existing `home()` builder; the 50+ project list stays below. Rejected:
  restoring the seven-section Go hero verbatim, moving the catalogue to `/projects`, and catalogue-only with no
  explanation. Full reasoning in `docs/adr/0018-home-page-overview.md`. Implementation is a separate, unstarted slice.

* **D-48 (step 3, 2026-11-17): settings writes move with their GET+PUT routes; readers stay root-owned.**
  api/project_settings.rs owns apply_project_tags, tag_completeness, set_project_languages and set_intake_mode plus four
  routes (159 lines; lib.rs 4978 -> 4810). project_tags and project_languages handlers stay at the root (D-41/D-43) but
  their GET+PUT routes move with the writes (D-20). The D-47 merge was added in the same edit: route_policies reports
  `api route modules: 30; unmerged: 0`. Verified: policy 7 passed; 0 failed, route_policies PASS (203 declared), replay
  0 disagreed of 16 checked, 5 skipped.
* **D-47 (step 3, 2026-11-17): a declared module must be mounted in router().**
  Live milestones answered 404 on fef1a22 although the stamped gate said PASS: api/milestones.rs had routes() but lib.rs
  never called `.merge(api::milestones::routes())`. route_policies pretended to check registration by scanning every
  api/*.rs file, so an unmounted module still looked registered. The scanner now requires every module declaring routes()
  to be merged into lib.rs. RED before the fix: unmerged: 1 - milestones, route_policies: FAIL (exit 1); GREEN after:
  api route modules: 29; unmerged: 0, route_policies: PASS. The exact missing merge was added. Rejected: trusting the
  132/132 page check as evidence for API routes it never probes.
* **D-46 (step 3, 2026-11-17): a method chain takes `.post(...)` after a moved `get(...)`, not a second `axum::routing::`.**
  **Twenty-ninth move live (fef1a22, repaired in b635794):** milestones and roadmap moved; git show --stat 276 added in
  api/milestones.rs, 254 changed in lib.rs. First deploy answered 404 because the routes() module was never merged;
  the strengthened scanner caught it (unmerged: 1), the merge was added, and the repaired deploy on b635794 answers:
  anon GET /projects/suki/milestones `200 list 0`, GET /roadmap `200` with zeroed totals, POST milestones and PUT status
  both `403 consent_required`. /version equality True; walk PASS; headers PASS; CI 44194647904.
  api/milestones.rs owns the two milestone readers, roadmap, milestone_write_context/refusal/ids/fields, six writes and
  seven routes (227 lines plus 27 route lines; lib.rs 5231 -> 4977). Rewriting the extracted route stanzas with a naive
  string replacement produced `.get(...).axum::routing::patch(...)` and a duplicated roadmap route; cargo check named both.
  D-20 still holds: get_milestone stays root-owned, but its GET+PATCH route lives with the milestone writes. Shared
  write_error and FeatureQuery stay root-owned (D-41/D-43). Verified: policy 7 passed, route_policies PASS (203 declared),
  replay 0 disagreed of 16 checked, 5 skipped.

* **D-45 (step 3, 2026-11-17): destructive verdict code moves byte-for-byte, with its routes.**
  The reject and merge_complaints handlers (62 lines) moved into the existing api/complaints.rs together with their two
  routes; lib.rs 5301 -> 5231. SHA-256 of the rstripped verdict block in HEAD:lib.rs and in api/complaints.rs matches —
  the maintainer check before merge and the exact merge call were not tidied (D-35). Shared complaint helpers stay
  root-owned. Verified: policy 7 passed, route_policies PASS (203 declared), replay 0 disagreed of 16 checked, 5 skipped.

* **D-44 (step 3, 2026-11-17): a write slice can carry its sibling mutation handlers and their routes.**
  api/complaints.rs owns the public list, create_complaint, add_complaint_impact and validate_complaint plus four routes
  (209 lines plus two routes; lib.rs 5521 -> 5301 — corrected from 5309, the eight route lines also left lib.rs). The compiler named 14 missing imports and two routes still bound in lib.rs; all were
  fixed by moving those routes with the handlers, not by re-exporting handlers from lib.rs. Shared complaint helpers and
  FeatureQuery stay root-owned (D-41/D-43). Reject/merge/other verdict handlers remain in lib.rs with their own routes.
  Verified: cname clean, policy 7 passed, route_policies PASS (203 declared), replay 0 disagreed of 16 checked, 5 skipped.

* **D-43 (step 3, 2026-11-17): cut at the next resource's doc comment, and leave shared types at the root.**
  api/features.rs owns the feature catalogue, FileFeatureRequest, file/edit/status handlers and three routes
  (257 lines; lib.rs 5789 -> 5521). The first cut swept `list_complaints`, `list_milestones` and `roadmap` into the module and
  cname reported E0425 for those handlers; the checkout was reverted and the cut redrawn at the complaints doc comment.
  FeatureQuery and write_error stay root-private (D-41): complaints, milestones and roadmap share the query struct, and
  complaint handlers share the error mapper. Verified: 7 passed, route_policies PASS (203 declared), replay 0 disagreed of
  18 checked, 3 skipped. Rejected: moving the three shared handlers and their routes just to keep the first cut.

* **D-42 (step 3, 2026-11-17): policy test lookups must follow every moved handler.**
  api/project_lifecycle.rs owns CreateProjectRequest, ArchiveRequest, project_maintainer and five lifecycle handlers plus
  five routes (274 lines; lib.rs 6070 -> 5789). The policy test reddened at create_project exists because it still searched
  only SOURCE (lib.rs); it now searches the same sources list used for auth_gate. Mutation proof: replacing the module
  is_guest( with is_guest_disabled( makes the build fail with E0425, and restoring it returns 7 passed; 0 failed —
  the check is load-bearing. route_policies: PASS (203 declared), replay 0 disagreed of 16 checked, 5 skipped.
  Rejected: dropping the create_project guest-check assertion to make the test green.

* **D-41 (step 3, 2026-11-17): a shared constant stays at the crate root; child modules can see it.**
  api/accounts.rs owns RegisterRequest, register and the registration route (79 lines; lib.rs 6149 -> 6070). TOKEN_NOTE
  remains a root-private const because api/identity.rs's login also uses it; Rust child modules can read a parent's private
  items, so no visibility promotion was needed. The D-40 scanner fix held: route_policies parsed 203 declared routes with
  0 orphans and 0 missing. Verified: policy 7 passed; 0 failed, replay 0 disagreed of 18 checked, 3 skipped.
  Rejected: copying TOKEN_NOTE into the module — two copies of a user-facing token warning can drift.

* **D-40 (step 3, 2026-11-17): scanners must not use a handler name as a structural boundary.**
  Moving `list_projects` into `api/catalog.rs` made route_policies.py fail with `ValueError: substring not found` at
  `source.index("async fn list_projects(", start)` — the scanner had used the first handler as router()'s end marker.
  Fixed by searching for the first function after `router()` instead. Proven positively: the scanner now parses all three
  method routes on `/projects` (`POST create_project`, `GET list_projects`, `GET get_project`) and reports
  `routes with a declared policy: 203`, `routes with no policy: 0`, `route_policies: PASS`.
  `api/catalog.rs` owns the two catalogue reads (20 lines; `lib.rs` 6,170 -> 6,149); POST creation stays in `lib.rs` and axum
  merges the methods (D-20). Policy `7 passed; 0 failed`, replay `0 disagreed of 16 checked, 5 skipped`.

* **D-39 (step 3, 2026-11-17): a section that ends the file can move whole.**
  api/scout.rs owns scout_report, apply_coverage and ScoutQuery (149 lines); lib.rs 6319 -> 6170. Coverage application is
  not a separate resource here — it is a private helper called only by the report. Compiler-named imports (State, bearer_user)
  fixed the first check. Verified: policy 7 passed; 0 failed, route_policies PASS, replay 0 disagreed of 18 checked, 3 skipped.
  Rejected: leaving apply_coverage in lib.rs because it touches capability state; the report is its only caller.

* **D-38 (step 3, 2026-11-17): a helper physical location is not its ownership.**
  app_mutant was written inside the webhook section but imported by badges, ops_writes and others via crate::app_mutant.
  Moving the webhook reddened with unresolved import crate::app_mutant; the fix hoists the helper to the crate root as the
  single definition and imports it into the module. Webhook module: handler, HMAC/hex/constant-time helpers, RFC3339/date
  parser and one route (397 lines); lib.rs 6705 -> 6308 plus the hoisted helper. Every refusal stays identical 401.
  Verified: policy 7 passed; 0 failed, route_policies PASS, replay 0 disagreed of 18 checked, 3 skipped.
  Rejected: duplicating app_mutant in the module — two copies of a mutation switch would let a probe control one caller not another.

* **D-37 (step 3, 2026-11-17): the arena replay move is guarded by the mutation contract.**
  `api/ops_writes.rs` owns `recompute_arena` and its route (58 lines; `lib.rs` 6,766 → 6,705). The
  `app_mutant("recompute-count-rows")` control branch and the membership-before-role check are byte-identical; both are mutation
  probes. Verified: policy `7 passed; 0 failed`, `route_policies: PASS`, replay `0 disagreed of 16 checked, 5 skipped`.
  Concurrency: a subagent lease (`sa-0-022cf733`) stayed active during this turn; each write checked HEAD and clean status, and
  the commit carries only the files named above. If that session lands a conflicting edit, rebase this move on it rather than
  overwriting it. Rejected: moving the webhook with the replay merely because they shared a section comment.

* **D-36 (step 3, 2026-11-17): the taxonomy proposal rate-limit helper is part of the resource.**
  `api/taxonomy.rs` owns `list_tags`, `list_taxonomy_proposals`, `propose_taxonomy_change`, `contributor_somewhere` and
  `consent_taxonomy_proposal` plus three routes (174 lines; `lib.rs` 6,948 → 6,766). The first check reddened with 15 errors
  naming `Deserialize`, `Query`, `HeaderMap`, `StatusCode`, `bearer_user` and `role_rank`; fix imports only, no logic.
  Verified: policy `7 passed; 0 failed`, `route_policies: PASS`, replay `0 disagreed of 18 checked, 3 skipped`.
  Rejected: leaving `contributor_somewhere` in `lib.rs` as "general" — its only caller is the proposal write, and the rate
  limit belongs with the resource it protects.

* **D-35 (step 3, 2026-11-17): a deliberately mutation-sensitive route moves with its refusal mapper.**
  `api/emergency_holds.rs` owns `release_emergency_hold` and `hold_refusal` (57 lines) plus the release route;
  `lib.rs` 7,008 → 6,948. The `CONCORD_MUTANT=hold-any-role` branch and the hold-before-role ordering stayed byte-identical —
  the mutation gate proves that ordering, so a "cleanup" here would silently break it. No RED: policy `7 passed; 0 failed`,
  `route_policies: PASS`, replay `0 disagreed of 16 checked, 5 skipped`. Rejected: hoisting the role check above the hold
  lookup while moving; the route's documented 404 regression is the reason the order exists.

* **D-34 (step 3, 2026-11-17): a resource's read half may sit thousands of lines from its writes.**
  `api/field_reports.rs` owns `list_field_reports` (17 lines, formerly beside the capability panel) and
  `create_field_report`/`respond_to_field_report` (112 lines, formerly beside the emergency hold) plus their two route stanzas.
  `lib.rs` 7,144 → 7,008. No visibility promotion was needed: `project_by_id`, `readable_project`, `readable_project_segment`
  and `write_audit` were already `pub(crate)` from earlier moves (D-25). GET+POST on `/field-reports` stay in one MethodRouter.
  Verified: policy `7 passed; 0 failed`, `route_policies: PASS`, replay `0 disagreed of 18 checked, 3 skipped`.
  Rejected: moving only the writes and leaving the read in `lib.rs` — one resource, two owners, and a future edit could split
  the panel's shape from the write that populates it.

* **D-33 (step 3, 2026-11-17): cut a source section at the next resource's doc comment, even mid-comment-block.**
  `api/contributions.rs` owns the feed's four handlers, the two reads under it and three helpers (403 lines;
  `lib.rs` 7,550 → 7,144). Field reports and the emergency hold live in the same source region but are separate resources,
  so they stayed in `lib.rs`; their routes are untouched. Compiler-named imports (`HeaderMap`, `Deserialize`, `Value`,
  `Path`, `Query`, `State`, `Extension`, `Json`, `Router`, `internal`, `readable_project_segment`) fixed the move; no policy
  row changed. Verified: policy `7 passed; 0 failed`, `route_policies: PASS`, replay `0 disagreed of 18 checked, 3 skipped`.
  Rejected: moving field reports with the feed, which would have dragged `list_field_reports` from a different section and
  broken the one-method-router-per-path rule.

* **D-32 (step 3, 2026-11-17): the identity section moves after its oracle, and its whole surface becomes `pub(crate)`.**
  `a26e5e5` first made `policy.rs`'s `auth_gate` lookup module-aware (brace-matching the whole function): `7 passed; 0 failed`.
  Then the 589-line section moved into `api/identity.rs`; `lib.rs` 8,141 → 7,550 (589 section lines removed, 5 import lines added). The router imports `auth_gate`,
  `interaction_gate`, `flatten_error_bodies`, `security_headers`, `acting_user` and `presented_token`; other handlers and the
  inline cookie tests live on the same surface, so every item in the section was promoted `pub(crate)` rather than one at a
  time. Compiler-named imports (`policy`, `TOKEN_NOTE`, `Value`, `IntoResponse`) fixed the first check. Verified:
  `cargo test -p concord-app --lib policy` `7 passed; 0 failed`, `credential_tests` `4 passed; 0 failed`,
  `route_policies: PASS`, replay `0 disagreed of 16 checked, 5 skipped`. Rejected: moving the section before the oracle
  change, which would have made the pin test report a missing gate.

* **D-31 (step 3, 2026-11-17): identity is deferred until the middleware move can be proven as one slice.**
  The `consent gate, guest identities, and login` section holds cross-cutting middleware (`auth_gate`, `interaction_gate`,
  `security_headers`) used by every route, and `policy.rs`'s pinned-handler test anchors on `SOURCE.find("async fn auth_gate(")`.
  Moving it needs a coordinated test-oracle change, not a pure move; taking the next smallest resource instead.
  `api/ops_reads.rs` owns the three operator reads (47 lines; `lib.rs` 8,190 → 8,141), split before the emergency-hold
  write per D-28. No RED: policy `7 passed; 0 failed`, `route_policies: PASS`, replay `0 disagreed of 18 checked, 3 skipped`.
  Unblock for identity: make the policy test's `auth_gate` lookup module-aware first, then move the section in one commit.

* **D-30 (step 3, 2026-11-17): two refusal maps in one section can belong to one resource.**
  `api/alternatives.rs` owns `list_alternatives`, `create_alternative`, `add_alternative_competitor`,
  `cast_alternatives_vote`, `alternative_arena_id`, `alternative_refusal` and `arena_vote_refusal` (256 lines;
  `lib.rs` 8,457 → 8,190). The second refusal table is the vote store's, but its only caller in this section is the
  alternatives vote, so it moved too. First check reddened with 21 errors (missing `Deserialize`, `StatusCode`, `readable`,
  `require_role`); imports fixed, then policy `7 passed; 0 failed`, `route_policies: PASS`, replay
  `0 disagreed of 16 checked, 5 skipped`. Rejected: leaving `arena_vote_refusal` in `lib.rs` "for other arenas" — no other
  caller exists, and a dead helper in the root would hide the resource boundary.

* **D-29 (step 3, 2026-11-17): move the limit with the route that uses it, and drop the stale import.**
  `api/documents.rs` owns `list_documents`, `get_document`, `put_document`, `delete_document`, `DocumentListQuery`,
  `PutDocumentRequest`, `DOCUMENT_BODY_LIMIT` and `document_refusal` (155 lines; `lib.rs` 8,623 → 8,457). The reads were
  near line 610 and the writes inside §5.7, but D-24 says keep a resource together. RED: `no DefaultBodyLimit in axum::routing`
  — it is `axum::extract::DefaultBodyLimit`. After the move `lib.rs` kept an unused `DefaultBodyLimit` import; removed it
  so a pure move leaves no warning. Policy `7 passed; 0 failed`, `route_policies: PASS`, replay `0 disagreed of 18 checked, 3 skipped`.

* **D-28 (step 3, 2026-11-17): a source section can contain two resources; cut at the next resource's doc comment.**
  The `§5.7 merge requests` marker also contained document writes after `merge_refusal`. `api/merge_requests.rs` takes
  only the merge block (182 lines; `lib.rs` 8,811 → 8,623), stopping at `/// The document body limit`; document writes stay
  for their own module. First check RED `cannot find type StatusCode`; fixed by importing `axum::http::StatusCode`.
  Policy `7 passed; 0 failed`, `route_policies: PASS`, replay `0 disagreed of 18 checked, 3 skipped`. Rejected: moving the
  document block with the merge block just because they shared a section marker — that would split one resource later.

* **D-27 (step 3, 2026-11-17): the two arenas keep separate modules even though they share a vote shape.**
  `api/feature_votes.rs` owns the §5.1 feature-vote request, `cast_feature_vote` and `feature_vote_refusal` (104 lines;
  `lib.rs` 8,915 → 8,811). It is deliberately not merged with `api/arena_votes.rs`: the arenas rank different entities with
  different competitor and weight rules, and the route paths stay distinct. No RED; policy `7 passed; 0 failed`,
  `route_policies: PASS`, replay `0 disagreed of 18 checked, 3 skipped`. Rejected: a shared `votes.rs` that would put two
  different refusal tables in one file.

* **D-26 (step 3, 2026-11-17): private helpers used only inside a section move with the section.**
  `api/coverage.rs` owns the §6.3/§6.4 request structs, `claim_coverage`, `contest_coverage`, `solution_in_feature` and
  `coverage_refusal` (114 lines; `lib.rs` 9,036 → 8,915). `solution_in_feature` is not promoted: no handler left in
  `lib.rs` calls it. The three borrowed helpers were already `pub(crate)`, so the first check was green. Policy
  `7 passed; 0 failed`, `route_policies: PASS`, replay `0 disagreed of 16 checked, 5 skipped`. Rejected: promoting a
  private helper merely because it moved — only cross-module callers justify `pub(crate)` (D-23/D-25).

* **D-25 (step 3, 2026-11-17): prior visibility promotions make later moves cheaper; no new promotion needed.**
  `api/arena_votes.rs` owns the §5.1 solution-vote request struct, handler and refusal (125 lines; `lib.rs` 9,164 → 9,036).
  `bearer_user`, `caller_feature`, `readable_project_segment` and `internal` were already `pub(crate)` from earlier moves,
  so the first `cargo check` was green. Policy `7 passed; 0 failed`, `route_policies: PASS`, parity replay
  `0 disagreed of 18 checked, 3 skipped`. Rejected: re-promoting or re-exporting helpers "for symmetry" — promote only
  when the compiler reports an unresolved dependency (D-23).

* **D-24 (step 3, 2026-11-17): move a resource's reads and write together, even when its sections are far apart.**
  `api/solutions.rs` owns `project_solutions`, `feature_solutions`, `one_solution` (extracted from ~4,070–4,200) plus
  `create_solution`/`solution_refusal` (from §6.3): 261 lines; `lib.rs` 9,436 → 9,164. All three route stanzas now come
  from the module, so the GET+POST `/features/{id}/solutions` MethodRouter stays in one file. Three resolvers
  (`project_by_id`, `readable_project`, `caller_feature`) and the `STANDINGS_PER_FEATURE` constant were borrowed, not copied;
  one RED (`cannot find STANDINGS_PER_FEATURE`) fixed by importing it. Policy `7 passed; 0 failed`, `route_policies: PASS`,
  replay `0 disagreed of 18 checked, 3 skipped`. Rejected: moving only the write and leaving its reads in `lib.rs`, which
  D-20's rationale forbids.

* **D-23 (step 3, 2026-11-17): moving code does not move its dependencies; let the compiler name them.**
  `// ── §3.4's badges` moved whole into `api/badges.rs` (191 lines; `lib.rs` 9,627 → 9,436). The first check reddened with
  9 errors — six `internal`, one `readable`, one `StatusCode`, plus `app_mutant` — because the handler's imports lived in
  `lib.rs`. `app_mutant` was promoted `pub(crate)`; the module imports the other three from `axum`/`crate`. Then
  `cargo check` green, policy `7 passed; 0 failed`, `route_policies: PASS`, parity replay `0 disagreed of 18 checked, 3 skipped`.
  Rejected: copying `app_mutant` into the module — four other handlers in `lib.rs` still call it, so a copy could mutate a
  different branch of behavior.

* **D-22 (step 3, 2026-11-17): move a whole commented section, including its private helper.**
  `// ── invites (§8.2)` through `invite_refusal` moved as one 175-line block into `api/invites.rs`; `lib.rs` 9,810 → 9,627.
  `readable_project_segment` and `require_role` became `pub(crate)` so the module borrows the shared visibility rules.
  The two-method `/invites` route stayed `get(list_invites).post(create_invite)` inside the module's `routes()` — splitting
  it would let GET and POST drift into different modules. No RED this move: `cargo check` green, policy `7 passed; 0 failed`,
  `route_policies: PASS`, parity replay `0 disagreed of 16 checked, 5 skipped`. Rejected: moving handlers but leaving
  `invite_refusal` behind, which would leave one resource's refusal logic in two files.

* **D-21 (step 3, 2026-11-17): the pinned-handler test must search `api/*.rs` too, and use the same route oracle.**
  Moving `list_notifications`/`mark_notification_read`/`mark_all_notifications_read` into `api/notifications.rs`
  (45 lines; `lib.rs` 9,810) reddened the test twice: first `mark_all_notifications_read exists` (the body lookup read only
  `SOURCE`), then `mark_all_notifications_read is pinned but no (method, path) row is enforced` (the route scan did too).
  The test now searches `SOURCE` plus `api/*.rs` sources for the body, and uses `router_body()`'s already-blanked
  combined source for routes. No policy row was relaxed: `7 passed; 0 failed`, `route_policies: PASS`, parity replay
  `0 disagreed of 18 checked, 3 skipped`. Rejected: exempting module-owned handlers from the pin — that would make the
  gate's identity promise unverifiable for exactly the handlers being moved.

* **D-20 (step 3, 2026-11-17): a module may own a route that was inside another `MethodRouter` chain.**
  `api/board.rs` owns `get_board`, `move_card`, `BoardQuery` and `MoveQuery` (97 lines; `lib.rs` 9,866). The board PUT was
  one stanza of a long chain; it was removed there and registered by `api::board::routes()` at the GET board position.
  Paths are distinct, so route order does not change dispatch; policy `7 passed; 0 failed`, `route_policies: PASS`.
  `api/mod.rs` declares `//!` module docs before any `pub mod` item — placing `pub mod board;` first gave E0753
  (`//!` after an item). Rejected: qualifying a single handler in `lib.rs` while leaving its sibling in a module,
  which would split one resource across two files.

* **D-19 (step 3, 2026-11-17): nested `api/*.rs` import from `crate::`, not `super::`.**
  `api/records.rs` owns `document_kinds` and `search_documents` (52 lines moved; `lib.rs` 9,967 lines).
  `super::{bearer_user, …}` resolved to `api`, not the crate root — RED `E0432: no AppState in api`;
  `use crate::{bearer_user, internal, project_for, readable, AppState}` restored a green check.
  `project_for` became `pub(crate)` so the module borrows the existing visibility rule instead of copying it.
  D-18's scanner already covers `api/*.rs`: policy `7 passed; 0 failed`, `route_policies: PASS`, parity replay
  `0 shape(s) disagreed of 18 checked, 3 skipped`. Rejected: duplicating the helpers inside the module,
  which would let their visibility logic drift from the handlers left in `lib.rs`.

* **D-18 (step 3, 2026-11-17): module routes are typed `Router<AppState>`, and the policy oracle follows `api/*.rs`.**
  The first pure move (`api/meta.rs`: `healthz`, `/healthz`, `/api/v1/healthz`, `/api/v1/version`) initially declared
  `routes() -> Router`, which forced the whole merged chain to `Router<()>` and produced 177 E0308 errors. Typing it
  `Router<AppState>` restored `cargo check -p concord-app --locked` to green. The move then made the policy test red:
  `2 stale row(s): ["GET /api/v1/healthz", "GET /api/v1/version"]` (`6 passed; 1 failed`). Both scanners —
  `policy.rs::router_body()` and `scripts/parity/route_policies.py` — now read `crates/app/src/api/*.rs`; neither
  policy row was relaxed. After the fix: `7 passed; 0 failed` and `route_policies: PASS`. Rejected: leaving these routes
  in `lib.rs` to keep the test green, and deleting the policy rows — both make the oracle lie about the wire.
  ADR 0014's corpus baseline was re-recorded first: 22 shapes, replay `0 shape(s) disagreed of 15 checked, 6 skipped`.
  **Live (`ed1488c`):** `/version` equality True; `/healthz` and `/api/v1/healthz` both `200 {"status":"ok"}`;
  `anonymous_walk: 0 holes · 0 declared but not enforced · PASS`; headers PASS; CI `44063176583` success.
  `git show --stat HEAD`: `api/meta.rs` 41 added, `lib.rs` 27 changed (25 removed); no route shape edited.
  **Second module (`d96eeb8`):** `document_kinds`/`search_documents` + 2 routes moved; `git show --stat` 75 added in
  `api/records.rs`, 57 changed in `lib.rs` (52 handler lines removed). Live `/documents/kinds` `200 dict 1`,
  `/documents/search?q=test&limit=2` `200 list 2`; `/version` equality True; walk PASS; headers PASS; CI `44065751641`.
  **Third module (`4ea99f5`):** `get_board`/`move_card` + both query structs + 2 routes moved; `git show --stat` 123 added in
  `api/board.rs`, 103 changed in `lib.rs` (97 handler lines removed). Live board `200 dict cards 329`;
  anonymous `PUT …/move?to=done` `403 consent_required` (registered and gated, not 404); `/version` equality True;
  walk PASS; headers PASS; CI `44067946502`.
  **Fourth module (`79240b0`):** all three notification handlers + 3 routes moved; `git show --stat` 68 added in
  `api/notifications.rs`, 60 changed in `lib.rs` (45 handler lines removed). Live `/api/v1/notifications`
  `401 {"error":"authentication required"}`; both POST routes `403 consent_required` (registered, not 404);
  `/version` equality True; walk PASS; headers PASS; CI `44070134200`.
  **Fifth module (`cd9f396`):** whole invite section + 3 route stanzas moved; `git show --stat` 204 added in `api/invites.rs`,
  189 changed in `lib.rs` (175 section lines removed). Live `/invites` `401 unauthenticated` and `/auth/redeem-invite`
  `401 unauthenticated`; `/version` equality True; walk PASS; headers PASS; CI `44072419927`.
  **Sixth module (`66e38b5`):** badge section + `/badge/{file}` moved; `git show --stat` 210 added in `api/badges.rs`,
  195 changed in `lib.rs` (191 section lines removed). Live `/badge/instance.svg` `200` 838 bytes (SVG starts
  `<svg xmlns=…`); `/badge/no-such-project-xyz.svg` `404 {"error":"not found"}` with no slug echo; `/version`
  equality True; walk PASS; headers PASS; CI `44074509285`.
  **Seventh module (`41303eb`):** solution reads + write + refusal moved; `git show --stat` 293 added in `api/solutions.rs`,
  280 changed in `lib.rs` (261 handler lines removed). Live `/projects/suki/solutions` `200 dict 4`;
  `/features/1746/solutions` `200 {'feature_id': 1746, 'solutions': []}`; anonymous POST `403 consent_required`
  (registered, not 404); `/version` equality True; walk PASS; headers PASS; CI `44076613424`.
  **Eighth module (`5ad8ff3`):** solution-vote section + route moved; `git show --stat` 147 added in `api/arena_votes.rs`,
  130 changed in `lib.rs` (125 section lines removed). Live anonymous `POST /features/1746/solutions/vote`
  `403 consent_required` (registered, not 404); `/version` equality True; walk PASS; headers PASS; CI `44078651040`.
  **Ninth module (`bbb2d46`):** coverage section + both routes moved; `git show --stat` 141 added in `api/coverage.rs`,
  123 changed in `lib.rs` (114 section lines removed). Live anonymous claim and contest both `403 consent_required`
  (registered, not 404); `/version` equality True; walk PASS; headers PASS; CI `44080948764`.
  **Tenth module (`2bb0667`):** feature-vote section + route moved; `git show --stat` 126 added in `api/feature_votes.rs`,
  106 changed in `lib.rs` (104 section lines removed). Live anonymous `POST /features/1746/vote`
  `403 consent_required` (registered, not 404); `/version` equality True; walk PASS; headers PASS; CI `44083552359`.
  **Eleventh module (`97ee6b7`):** merge block + 4 routes moved; `git show --stat` 210 added in `api/merge_requests.rs`,
  190 changed in `lib.rs` (182 block lines removed). Live `/projects/suki/merge` `200 []`; anonymous PUT
  `/merge/1/approve` `403 consent_required`; `/version` equality True; walk PASS; headers PASS; CI `44085897053`.
  **Twelfth module (`d02e7ff`):** document reads + writes and their 8 MB limit moved; `git show --stat` 187 added in
  `api/documents.rs`, 170 changed in `lib.rs` (155 lines removed). Live `?fields=meta` `200 list 175`, document 97
  `200 dict`, anonymous PUT `403 consent_required`; `/version` equality True; walk PASS; headers PASS; CI `44089787805`.
  **Thirteenth module (`6eef770`):** S2 alternatives section + 3 routes moved; `git show --stat` 289 added in
  `api/alternatives.rs`, 269 changed in `lib.rs` (256 section lines removed). Live `/projects/suki/alternatives`
  `200 dict 2`; anonymous POST `403 consent_required`; `/version` equality True; walk PASS; headers PASS; CI `44093504753`.
  **Fourteenth module (`06eeb8f`):** three operator reads moved; `git show --stat` 67 added in `api/ops_reads.rs`,
  51 changed in `lib.rs` (47 lines removed). Live anonymous `/admin-ledger` `401` (its enforced operator row),
  `/stats/rising` `200 list 0`, `/lists` `200 list 2`; `/version` equality True; walk PASS; headers PASS; CI `44098171339`.
  **Fifteenth module (`012fad1`):** identity/middleware moved; `git show --stat` 618 added in `api/identity.rs`,
  601 changed in `lib.rs`. Live anonymous `/guest` `200 dict 8`; `/consent` `200 {"consent":"1","ok":true}`;
  `/auth/login` `422` (missing `username`, validation intact); `/auth/logout` `204`; `/version` equality True; walk PASS;
  headers PASS; CI `44102040582`.
  **Sixteenth module (`a92a3d7`):** feed + two reads + three helpers moved; `git show --stat` 440 added in
  `api/contributions.rs`, 408 changed in `lib.rs`. Live anonymous `/contribute/next` `200 dict 5`, `/work-items`
  `200 list 329`, `/votes/next` `401`; `POST /join` `403 consent_required`. A GET on the POST-only `/join` path answers
  `500 route_declares_no_policy` from the policy middleware — this is the intended refusal for an undeclared method, not a
  regression (the corpus declares only POST there). `/version` equality True; walk PASS; headers PASS; CI `44106144002`.
  **Seventeenth module (`9634925`):** read half and write half moved; `git show --stat` 159 added in `api/field_reports.rs`,
  138 changed in `lib.rs`. Live anonymous `/projects/suki/field-reports` GET `200 dict 5`; POST `403 consent_required`;
  `/responses` POST `403 consent_required`; `/version` equality True; walk PASS; headers PASS; CI `44109701061`.
  **Eighteenth module (`735d7f4`):** release handler + refusal mapper moved; `git show --stat` 79 added in
  `api/emergency_holds.rs`, 62 changed in `lib.rs`. Live anonymous POST `/emergency-holds/999999/release`
  `403 consent_required` (nonexistent id chosen so no real hold could be touched); `/version` equality True; walk PASS;
  headers PASS; CI `44114112184`.
  **Nineteenth module (`5d0a04b`):** five handlers + three routes moved; `git show --stat` 207 added in `api/taxonomy.rs`,
  184 changed in `lib.rs`. Live anon `/taxonomy/tags` `200 list 68`, `/taxonomy/proposals` `200 list 0`, POST proposals
  `403 consent_required`, POST consent `403 consent_required`; `/version` equality True; walk PASS; headers PASS; CI `44118300295`.
  **Twentieth module (`96b433b`):** arena replay + route moved; `git show --stat` 81 added in `api/ops_writes.rs`,
  63 changed in `lib.rs`. Live anon POST on a nonexistent project/arena `403 consent_required` (no real replay triggered);
  `/version` equality True; walk PASS; headers PASS; CI `44123442614`.
  **Twenty-first module (6daf820):** webhook + helpers moved; git show --stat 410 added in api/forgejo_webhook.rs,
  410 changed in lib.rs (397 lines removed, app_mutant hoisted). Live anon POST /hooks/forgejo answers 503
  no webhook secret configured; deliveries are refused until one is set — the deliberate configured-or-off refusal, not a
  regression; signature verification needs CONCORD_WEBHOOK_SECRET set on the server. /version equality True;
  walk PASS; headers PASS; CI 44127516322.
  **Twenty-second module (7207cc1):** scout + coverage helper + query moved; git show --stat 170 added in api/scout.rs,
  151 changed in lib.rs. Live scout ?idea=offline kanban with wip limits: 200 dict 7; /version equality True;
  walk PASS; headers PASS; CI 44133293671.
  **Twenty-third module (213364c):** catalogue reads moved; git show --stat 41 added in api/catalog.rs,
  23 changed in lib.rs, 6 changed in route_policies.py (D-40). Live anon /projects `200 list 73`, /projects/suki
  `200 dict suki`, unknown slug `404 No such API route.` (the pre-existing not-found envelope, already documented at
  lib.rs:3454). /version equality True; walk PASS; headers PASS; CI 44137911389.
  **Twenty-fourth module (f75a021):** registration moved; git show --stat 100 added in api/accounts.rs,
  81 changed in lib.rs. Live anon POST /auth/register with {} answers 422 missing field username (validation intact;
  no account created). /version equality True; walk PASS; headers PASS; CI 44142046977.
  **Twenty-fifth module (986d263):** lifecycle handlers moved; git show --stat 304 added in api/project_lifecycle.rs,
  283 changed in lib.rs, 8 changed in policy.rs (D-42). Live anon POST /projects, POST /projects/suki/archive and
  PUT /projects/suki/ai-involvement all `403 consent_required`. /version equality True; walk PASS; headers PASS;
  CI 44150720321.
  **Twenty-sixth module (373dfc5):** features moved; git show --stat 288 added in api/features.rs, 270 changed in
  lib.rs. Live anon GET /projects/suki/features `200 list 329`; POST features and PUT status both `403 consent_required`.
  /version equality True; walk PASS; headers PASS; CI 44162505969.
  **Twenty-seventh module (b4be69b):** complaints moved; git show --stat 244 added in api/complaints.rs, 222 changed in
  lib.rs. Live anon GET /projects/suki/complaints `200 list 332`; POST complaints and POST impact both `403 consent_required`.
  /version equality True; walk PASS; headers PASS; CI 44174995983.
  **Twenty-eighth move (52bdda8):** reject and merge handlers plus routes moved; git show --stat 72 added in
  api/complaints.rs, 70 changed in lib.rs. Live anon POST reject and merge both `403 consent_required`.
  /version equality True; walk PASS; headers PASS; CI 44182701701.

* **D-17 (step 2b, 2026-11-17): the project page owns its node renderers; D-14/D-16 are closed.** `project-render.js`
  builds every panel, form, arena control and work item with `el()`. Rejected: parsing the old concatenated HTML strings
  with a whitelist tokenizer (that is `innerHTML` under another name), and waiting for `ConcordPanels.*`,
  `ConcordArena.controls()` and `ConcordWrites.panel()` to return nodes — those modules also serve other callers, and the
  project page no longer composes their markup. Their wire exports stay. The shell loads the renderer before the page
  script. A behavioral mutant (`node['innerHTML'] = …` on a node inside the tree) reddened the checker.

* **D-13 (step 2b, 2026-11-17): the sink census counts write syntax, not the word in a comment.** After `criteria.js`
  lost its two sink assignments, a textual count still reported 137 because the file's new header comment says the word
  `innerHTML`. The gate now greps `.innerHTML =` and `.insertAdjacentHTML(`; measured from git: 125 real sinks before this
  slice, 123 after (criteria 2→0). D-12 still counts both spellings; this only decides what counts as a sink.

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
