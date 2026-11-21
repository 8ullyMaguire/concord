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
| e2e suites the gate runs | 30 of 30 (`RECORDED_UNRUN = 0`) | the suites that could catch a refusal path are the ones nobody runs |

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

**Why.** 81 `Json<Value>` handlers across `api/*.rs` plus one in `lib.rs`, and 152 `json!` sites, mean the contract is discovered at runtime: four trailing-slash
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
| 1 | authorization as a table + audit + gate | **done at `1de65dd` (row 1b re-proof).** After the D-10 method-aware revert and all step-3 module moves: `cargo test -p concord-app --lib policy` 7 passed; 0 failed; `route_policies: PASS — every registered route declares who may call it`, 203 registered / 203 declared / 0 missing. Live `/version` equality True (`concord-rs 0.0.0+1de65dd+vendor`); `anonymous_walk: 0 unreachable · 0 hole(s) · 0 declared but not enforced · PASS`; `probe_security_headers: PASS`. The gate and the live walk agree: no declared private route answers a stranger, no public route refuses one. |
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
| 3 | split `lib.rs` by resource, zero behaviour change | **done**: two pure modules landed live (`ed1488c` meta, next records); `api/meta.rs` — `healthz`, `version`, 3 routes; `lib.rs` 10,043 → 10,020 lines. `git show --stat`: 41 added in `api/meta.rs`, 25 removed from `lib.rs`. Policy scanner follows `api/*.rs`. Gate `pages 132/132 · rate PASS`, live both health paths `200 {"status":"ok"}`, walk PASS, CI `44063176583`. Second module `api/records.rs` live (`d96eeb8`): 52 lines moved, `lib.rs` 9,967 lines, kinds `200 dict 1`, search `200 list 2`, CI `44065751641`. Third module `api/board.rs` live (`4ea99f5`): 97 lines moved, `lib.rs` 9,866 lines, board `200 dict cards 329`, move anon `403 consent_required`, CI `44067946502`. Fourth module `api/notifications.rs` live (`79240b0`): 45 lines moved, `lib.rs` 9,810 lines, notifications anon `401`, CI `44070134200`. Fifth module `api/invites.rs` live (`cd9f396`): 175 lines moved, `lib.rs` 9,627 lines, invites anon `401`, redeem anon `401`, CI `44072419927`. Sixth module `api/badges.rs` live (`66e38b5`): 191 lines moved, `lib.rs` 9,436 lines, `/badge/instance.svg` `200` 838 bytes, missing `404`, CI `44074509285`. Seventh module `api/solutions.rs` live (`41303eb`): 261 lines moved, `lib.rs` 9,164 lines, project solutions `200 dict 4`, feature solutions `200`, CI `44076613424`. Eighth module `api/arena_votes.rs` live (`5ad8ff3`): 125 lines moved, `lib.rs` 9,036 lines, vote anon `403 consent_required`, CI `44078651040`. Ninth module `api/coverage.rs` live (`bbb2d46`): 114 lines moved, `lib.rs` 8,915 lines, claim/contest anon `403 consent_required`, CI `44080948764`. Tenth module `api/feature_votes.rs` live (`2bb0667`): 104 lines moved, `lib.rs` 8,811 lines, vote anon `403 consent_required`, CI `44083552359`. Eleventh module `api/merge_requests.rs` live (`97ee6b7`): 182 lines moved, `lib.rs` 8,623 lines, merge list `200 []`, approve anon `403 consent_required`, CI `44085897053`. Twelfth module `api/documents.rs` live (`d02e7ff`): 155 lines moved, `lib.rs` 8,457 lines, doc list `200 list 175`, doc one `200 dict`, CI `44089787805`. Thirteenth module `api/alternatives.rs` live (`6eef770`): 256 lines moved, `lib.rs` 8,190 lines, alternatives list `200 dict 2`, create anon `403 consent_required`, CI `44093504753`. Fourteenth module `api/ops_reads.rs` live (`06eeb8f`): 47 lines moved, `lib.rs` 8,141 lines, ledger anon `401`, rising `200 list 0`, lists `200 list 2`, CI `44098171339`. Fifteenth module `api/identity.rs` live (`012fad1`): 589 lines moved, `lib.rs` 7,550 lines, guest `200 dict 8`, consent anon `200`, CI `44102040582`. Sixteenth module `api/contributions.rs` live (`a92a3d7`): 403 lines moved, `lib.rs` 7,144 lines, contribute `200 dict 5`, work items `200 list 329`, CI `44106144002`. Seventeenth module `api/field_reports.rs` live (`9634925`): 129 lines moved, `lib.rs` 7,008 lines, report panel `200 dict 5`, anon POST `403`, CI `44109701061`. Eighteenth module `api/emergency_holds.rs` live (`735d7f4`): 57 lines moved, `lib.rs` 6,948 lines, anon release `403`, CI `44114112184`. Nineteenth module `api/taxonomy.rs` live (`5d0a04b`): 174 lines moved, `lib.rs` 6,766 lines, tags `200 list 68`, anon propose `403`, CI `44118300295`. Twentieth module `api/ops_writes.rs` live (`96b433b`): 58 lines moved, `lib.rs` 6,705 lines, recompute anon `403`, CI `44123442614`. Twenty-first module api/forgejo_webhook.rs live (6daf820): 397 lines moved, lib.rs 6,319 lines, anon delivery 503 no secret configured, CI 44127516322. Twenty-second module api/scout.rs live (7207cc1): 149 lines moved, lib.rs 6170 lines, scout 200 dict 7, CI 44133293671. Twenty-third module api/catalog.rs live (213364c): 20 lines moved, lib.rs 6149 lines, projects 200 list 73, project suki 200 dict, CI 44137911389. Twenty-fourth module api/accounts.rs live (f75a021): 79 lines moved, lib.rs 6070 lines, register anon empty 422, CI 44142046977. Twenty-fifth module api/project_lifecycle.rs live (986d263): 274 lines moved, lib.rs 5789 lines, anon writes 403, CI 44150720321. Twenty-sixth module api/features.rs live (373dfc5): 257 lines moved, lib.rs 5521 lines, features GET 200 (329), anon writes 403, CI 44162505969. Twenty-seventh module api/complaints.rs live (b4be69b): 209 lines + 2 routes moved, lib.rs 5301 lines, complaints GET 200 (332), anon writes 403, CI 44174995983. Twenty-eighth move complaint verdicts into api/complaints.rs live (52bdda8): 62 lines + 2 routes, lib.rs 5231 lines, reject/merge anon 403, CI 44182701701. Twenty-ninth move api/milestones.rs + mount fix live (fef1a22/b635794): 227 lines + 27 route lines, lib.rs 4978 lines, milestones GET 200 (0), roadmap 200, anon writes 403, CI 44194647904. Thirtieth module api/project_settings.rs live (62982b5): 159 lines + 4 routes, lib.rs 4810 lines, tags GET 200, anon writes 403, CI 44199542168. Thirty-first module api/criteria.rs live (edcfda8): 278 lines + 7 routes, lib.rs 4517 lines, criteria/profiles/charter GET 200, anon writes 403, CI 44203187335. Thirty-second module api/lists.rs live (4fa3539): 261 lines + 6 routes, lib.rs 4242 lines, lists GET 200 (2), anon writes 403, CI 44214531180. Thirty-third module api/feature_meta.rs live (bd9c570): 173 lines + 4 routes, lib.rs 4075 lines, feature tags GET 200 dict 2, anon writes 403, CI 44223857215. Thirty-fourth module api/feature_work.rs live (120099f): 231 lines + 5 routes, lib.rs 3824 lines, deps GET 200 dict 3, evidence GET 200 list 0, anon writes 403, CI 44228621160. Thirty-fifth module api/consensus.rs live (7a695a6): 225 lines + 6 routes, lib.rs 3576 lines, consensus GET 200 list 0, anon POST 403, CI 44236198926. Thirty-sixth module api/requests.rs live (13411cd): 323 lines + 4 routes, lib.rs 3240 lines, requests GET 200 list 0, anon POST 403, CI 44242070130. Thirty-seventh module api/answers.rs live (cd0b084): 217 lines + 3 routes, lib.rs 3013 lines, anon writes 403, CI 44245668629. Thirty-eighth module api/comments.rs live (416c07d): 95 lines + 3 routes, lib.rs 2904 lines, thread GET 200 list 0, anon writes 403, CI 44248263346. Thirty-ninth module api/moderation.rs live (baab193): 246 lines + 3 routes, lib.rs 2649 lines, curation GET 200 dict 2, anon writes 403, CI 44251396868. Fortieth module api/stats.rs live (48f3feb): 137 lines + 5 GET routes, lib.rs 2507 lines, all five 200, CI 44254461696. Forty-first module api/strategy.rs live (1672c43): 256 lines + 7 routes, lib.rs 2223 lines, anon reads 401 identity required, anon writes 403, CI 44257658683. Forty-second module api/solution_calls.rs live (8af9d4f): 128 lines + 3 routes, lib.rs 2083 lines, both spellings 200 dict 9, anon writes 403, CI 44260547188. Forty-third module api/consensus_outcomes.rs live (b744df9): 65 lines + 1 route, lib.rs 2015 lines, anon POST outcome 403, CI 44263456328. Forty-fourth module api/overrides.rs live (80fbdc1): 108 lines + 3 routes, lib.rs 1895 lines, anon writes 403, CI 44267064538. Forty-fifth move api/emergency_holds.rs completed (f20d15b): 75 lines + 1 route, lib.rs 1816 lines, anon writes 403, CI 44270030258. Forty-sixth move: objection resolution into api/consensus.rs (0532598): lib.rs 1785 lines, anon PUT 403, CI 44273128808. Forty-seventh move: arena reads into api/arena_votes.rs (08cb2f8): lib.rs 1615 lines, next-pair 400, verification 200, CI 44276693296. Forty-eighth move: request readers into api/requests.rs (1c97a64): lib.rs 1558 lines, anon reads 200/400, CI 44280391173. Forty-ninth move: arena_votes into api/arena_votes.rs (002e443): lib.rs 1512 lines, votes GET 200, CI 44284031903. Fiftieth move: api/audit.rs (7ecee40): lib.rs 1429 lines, audit GET 200, actions 200, bad kind 400, CI 44287885695. Fifty-first move: api/priorities.rs (614a916): lib.rs 1407 lines, priorities GET 200, CI 44291266171. Fifty-second move: complaint links into api/complaints.rs (a94f1d9): lib.rs 1304 lines, anon writes 403, CI 44295564056. Fifty-third move: complaint readers into api/complaints.rs (cda55eb): lib.rs 1222 lines, 400/400/200, CI 44298656846. Fifty-fourth move: feature_tags into api/feature_meta.rs (8b6cefb): lib.rs 1204 lines, tags GET 200, CI 44315044140. Fifty-fifth move: feature-work readers into api/feature_work.rs (9e91f6d): lib.rs 1168 lines, deps/evidence GET 200, CI 44318663362. Fifty-sixth move: milestone reader into api/milestones.rs (771c54b): lib.rs 1142 lines, milestones/abc 400, CI 44335023821. || 4 | typed responses + OpenAPI + generated client (20 routes) | open Fifty-seventh move: list readers into api/lists.rs (671fbd5): lib.rs 1074 lines, /lists/2 200 (both spellings), CI 44341220717. || 5 | events + audit projection (six write paths) | open Fifty-eighth move: feature tallies into api/arena_votes.rs (d5e3a5d): lib.rs 1052 lines, tallies GET 200, CI 44348001599. || 6 | one pagination convention, gate-enforced | open Fifty-ninth move: composite rank into api/solutions.rs (6df6012): lib.rs 947 lines, POST rank 200/400, CI 44355093345. || 7 | domain crate for stage / consensus / claim; `query!` on new SQL | open Sixtieth move: new api/curation.rs (7d0bdfb): lib.rs 883 lines, both curation GETs 200, 47 modules, CI 44360045399. || 8 | publish or delete the four dead vendored crates | open Sixty-first move: auth_me into api/identity.rs (0f26ed7): lib.rs 867 lines, guest bearer 200, CI 44366351396. || 9 | wire the 21 unrun e2e suites (`RECORDED_UNRUN` → 0) | open — proves steps 1, 2 and 5 from outside Sixty-second move: new api/capabilities.rs (a365e7e): lib.rs 850 lines, capabilities GET 200, 48 modules, CI 44370914143. Sixty-third move: new api/members.rs (bd6e92d): lib.rs 829 lines, members GET 200, 49 modules, CI 44374727897. Sixty-fourth move: criteria readers joined api/criteria.rs (0a3f169): lib.rs 795 lines, GET criteria/profiles 200, CI 44378690776. Sixty-fifth move: settings/lists readers joined their modules (88436e8): lib.rs 762 lines, languages/lists GET 200, CI 44382982996. Sixty-sixth move: project_tags joined api/project_settings.rs (5ad4adc): lib.rs 743 lines, tags GET 200, CI 44386811337. Final structural slice d444f48: root_routes 0, finder/feeds route tables moved, all 20 paths identical, CI 44392405151. |
| 10 | browse/detail visual polish (2026-11-18) | **done at `7080fab`** — live `/version` equality True. Gate `PASS`: `132/132 ok`, `route_policies: PASS`, `anonymous_walk: PASS (0 holes)`, `probe_security_headers: PASS`, CI `44406382358` success on head `7080fab`. `cargo test -p concord-web --lib`: 9 passed; 0 failed; `node --check project-render.js` exit 0. Browser matrix 1280/390 on `/projects`, `/search`, `/taxonomy`, `/projects/{slug}`: active nav 1 with `aria-current=page`, grid `display:grid`, chips styled, `overflow:0`, literal detail `null` text 0, health pills 73 on search, tab `scrollbar-width:none`. Stale-build premise disproved: served CSS MD5 `db05fb28b3dcd42333d3e107286394f8` matched the repo. Decisions D-90..D-94; all four RED gates reproduced from real logs before their fixes. |

* **D-94 (web, 2026-11-18): act on the second vision pass without breaking wire hooks.** The 1280/390 matrix found a
  literal `null` text node on `/projects/{slug}` (`render()` returned `null` for an absent next milestone into
  `replaceChildren`), an unthemed native Search button, drifting card baselines and a tab-strip scrollbar. The render
  array now filters null children, Search uses `btn btn-primary`, cards reserve two title lines and three description
  lines, health is a pill, dates no longer break on hyphens, and the tab scrollbar is hidden. The gate's literal
  `"in the catalogue — every one you may see."` string and exact search anchor were preserved. Unit tests 9 passed;
  0 failed; `node --check` green.

* **D-93 (web, 2026-11-18): pin the live shell, not the dead builder.** The browser matrix after the first deploy
  measured `active: 0` on all four pages: `lib::document()`'s nav loop was dead code because `render()` calls
  `shell::document()`, whose nav is a static HTML template. The nav is now generated from one table in the live shell,
  preserving `data-notifications-link` and the sign-out button, and two unit tests assert the exact active anchor in
  the rendered document. The dead loop was reverted. `cargo test -p concord-web --lib` 9 passed; 0 failed.

* **D-92 (web, 2026-11-18): structural gate assertions outrank cosmetic class names.** The preserved full probe log
  named the last RED exactly: `FAIL   and finds the project: got False want True` — `probe_pages.py:255` requires the
  literal `<a href="/projects/suki">` in `/search?q=suki`. Search result anchors therefore keep their exact markup and
  are styled through `.project-list[data-grid="browse"] > li > a:first-child`. The taxonomy page likewise keeps
  `class="tags"` plus one `<li>` per API row; chip styling is additive via `data-cloud="1"`. Display dedupe was
  withdrawn: the gate requires every API row, so duplicate names remain visible and are recorded as a data issue, not
  hidden by CSS. Unit tests stayed 7 passed; 0 failed.

* **D-91 (web, 2026-11-18): keep the exact `class="project-list"` string.** The first gate went RED at 131/132:
  `probe_pages.py:156` counts that exact string on `/projects`, so adding `project-grid` as a second class broke a
  structural gate even though the CSS matched. The grid now hangs off `data-grid="browse"` and the CSS selector is
  `.project-list[data-grid="browse"]`. RED reproduced, fix applied, `cargo test -p concord-web --lib` 7 passed; 0
  failed and `cargo check -p concord-web --locked` green before re-running the gate.

* **D-90 (web, 2026-11-18): restyle by additive classes, not by editing shared selectors.** `.project-list` and `.tags`
  are also used by utility pages that the owner explicitly excluded, so `/projects` and `/search` gained
  `project-grid` and `/taxonomy` gained `tag-cloud`. Direction A (refined indigo/slate) was chosen from the four
  documented options because it preserves the measured contrast tokens. Taxonomy display names are deduplicated per
  namespace without rewriting imported numeric suffixes, and the top nav gained `active` + `aria-current="page"`
  from a pure `nav_is_active(path, href)` helper. Verified: `cargo test -p concord-web --lib` 7 passed; 0 failed,
  `route_policies: PASS`, `cargo check -p concord-web --locked` green.

| 4 | typed API contracts for the 20 UI routes | **done at `1de65dd`** — all 20 corpus routes typed; `/healthz` and `/version` answered typed structs, the other 18 returned existing typed DB projections or typed wrappers. `docs/contracts/openapi.json` generated with 37 schemas; `openapi_diff: PASS — 37 schemas, 0 paths, 0 breaking changes` and its mutation proof FAILs on a removed field. `cargo test -p concord-app --lib`: 18 passed; 0 failed. Gate `PASS` (`132/132 ok`), CI `44421966895` success; live search 200, documents full 200 with body, documents `?fields=meta` 200 without body (`grep -c` 1 vs 0). D-95..D-105. | Search gained a collision-free `SearchFacetCount` schema; document list uses an untagged `DocumentList` for full/meta shapes. 37 schemas, 18 unit tests passed. Awaiting gate/live proof. | 18 of 20 corpus routes typed; 30 schemas; `openapi_diff: PASS`; unit tests 18 passed. Gate `PASS` 132/132, CI `44420385683` success; live board/features/complaints/priorities/audit 200 with typed shapes. Remaining: documents list and search. | Slice 6 typed board, features (2), complaints, priorities and audit (2) from their DB projections; 30 schemas, 18 unit tests passed. Awaiting gate/live proof. 20 of 20 corpus routes have a typed projection except documents and search. | 13 of 20 corpus routes typed; 21 schemas; `openapi_diff: PASS`; unit tests 18 passed. Gate `PASS` 132/132, CI `44417822817` success; live `/projects` 200 list and `/projects/suki` 200 detail. Remaining: 7 routes. | Slice 5 typed `/projects` and `/projects/{slug}` from the existing `Project` projection; 21 schemas, 18 unit tests passed. Awaiting gate/live proof. 13 of 20 corpus routes typed. | 11 of 20 corpus routes typed; 20 schemas; `openapi_diff: PASS`; unit tests 18 passed. Gate `PASS` 132/132, CI `44415912498` success; live `/stats` 200 and `/stats/summary` 200 with census keys. Remaining: 9 routes. | Slice 4 typed `/stats` and `/stats/summary` by carrying `ToSchema` through every nested stats struct; 20 schemas, 18 unit tests passed. Awaiting gate/live proof. 11 of 20 corpus routes typed. | 9 of 20 corpus routes typed; 13 schemas; `openapi_diff: PASS`; unit tests 18 passed. Gate `PASS` 132/132, CI `44413903989` success; live `/curation` 200 with typed page, `/notifications` 401 by declared identity gate. Remaining: 11 routes. | Slice 3 typed `/curation` and `/notifications` from their DB page/inbox structs; 13 schemas, 18 unit tests passed. Awaiting gate/live proof. 9 of 20 corpus routes typed. | 7 of 20 corpus routes typed (2 meta + 5 small). `openapi_diff: PASS — 9 schemas, 0 paths, 0 breaking changes`; unit tests 18 passed. Gate `PASS` 132/132, CI `44411482591` success; live consensus/milestones/history/responsiveness `200 []`, roadmap `200` with Go-sorted keys. Remaining: 13 corpus routes. | Five more corpus routes typed by returning the existing DB row structs directly (`consensus`, `milestones`, `stats/history`, `stats/responsiveness`, `projects/{slug}/roadmap`); 9 schemas generated, unit tests 18 passed. Awaiting gate/live proof. Slice 1 proof remains at `c5e5ec4`. | `/api/v1/healthz` and `/api/v1/version` now answer typed `Serialize + ToSchema` structs; `Json<Value>` in `api/meta.rs` 0. utoipa 6 adopted; `docs/contracts/openapi.json` generated by `make openapi`; `openapi_diff.py` proved RED on a removed field. `cargo test -p concord-app --lib`: 18 passed; 0 failed. Gate `PASS` 132/132, CI `44409374916` success; live `/api/v1/healthz` `{"status":"ok"}`, `/api/v1/version` equality True. Remaining: 18 corpus routes. D-95..D-99. | Measured baseline at `25dc520`: 81 `Json<Value>` across `api/*.rs` + 1 in `lib.rs`, 152 `json!` sites; `scripts/parity/openapi_diff.py` is absent; `utoipa` is absent from `Cargo.lock`. The inventory is the 20 routes in `crates/parity/corpus.json`. Slice 1 types `/api/v1/healthz` and `/api/v1/version`, adds `utoipa` and checks in `docs/contracts/openapi.json` (D-95..D-98). |
* **D-99 (step 4, 2026-11-18): rehearsal caught two gate defects before commit.** `make openapi` first RED with
  `write the generated contract: Os { code: 2, kind: NotFound }` because Cargo runs tests with `crates/app` as cwd;
  the test now creates its parent and the Makefile passes `$(CURDIR)` into the output path. The mutation proof first
  PASSed because the mutant was passed as `--base`; with the mutant as `--current` it correctly prints
  `openapi_diff: FAIL — HealthResponse.status removed` and exits 1. Both corrections are in the generated gate, not
  in a prose footnote.

* **D-100 (step 4, 2026-11-18): type the DB row, do not clone it into a parallel response struct.** `Call`,
  `Milestone`, `MilestoneSlippage`, `Snapshot` and `ResponsivenessRow` already own the wire serialization, including
  the custom timestamp serializers. Adding `ToSchema` to those structs and returning `Json<Vec<T>>` keeps one source
  of truth. `/projects/{slug}/roadmap` needed a new wrapper because it is an object with totals; its fields are
  ordered `milestones, slippage, slug, totals`, matching the Go map's sorted keys rather than the old `json!`
  insertion order. All five routes keep their parsed JSON shape.

* **D-101 (step 4, 2026-11-18): paged readers keep their DB page type.** `/curation` returns `curation::Page` and
  `/notifications` returns `notifications::Inbox`; both already own the wire shape, so adding `ToSchema` to those
  structs and returning them directly avoids a second definition of the envelope. `openapi_diff` grew to 13 schemas.

* **D-102 (step 4, 2026-11-18): a typed aggregate needs its whole nested tree.** `/stats` and `/stats/summary`
  return `StatsPayload` and `Census`, whose nested `Measured`, `Latency`, `LatencySection`, `FacetCount` and
  `ProvenanceCount` must also derive `ToSchema` and be registered, or the generated document has dangling refs.
  The generated contract grew from 13 to 20 schemas.

* **D-103 (step 4, 2026-11-18): the catalogue projection is one type.** `/projects` and `/projects/{slug}` both read
  `concord_db::projects::Project`; adding `ToSchema` there and returning it directly gives both routes a contract
  without a parallel DTO. The custom timestamp serializer remains the single wire definition.

* **D-104 (step 4, 2026-11-18): a field's type needs `ToSchema` even when it is not a response root.** `Weights` is
  not serialized on its own, but `FeaturePriority` embeds it, so the generated contract needs a schema for it too.
  The compiler named the missing trait; deriving `ToSchema` on `Weights` fixed it without changing the wire.

* **D-105 (step 4, 2026-11-18): name-colliding schemas need an explicit OpenAPI name.** `stats::FacetCount` and
  `search::FacetCount` are different wire shapes with the same Rust name; `#[schema(as = SearchFacetCount)]` on the
  search type keeps both in one document. The attribute must follow the derive — placing it before produced
  `derive helper attribute is used before it is introduced`. The document list's `?fields=meta` branch became an
  untagged `DocumentList` with a body-less `DocumentMeta`, so both shapes are typed without changing either wire body.

| 5 | events + audit trail that cannot forget | **done at `f971637`.** All six write paths append in the same transaction as the change: `card.move` (18eed17), `project.archive`/`project.unarchive` (bfacce2), `consensus.position` (9ed519e), `project.claim` (cb92097), `feature.status` (5af4a0f), `document.put` (f971637). Each has a targeted RED mutation: removing the append failed its named test (0 passed / 1 failed) and the file was restored. Final `gate: PASS` — 132/132 ok; live `/version` `concord-rs 0.0.0+f971637+vendor`; guest board move `200 {"status":"moved"}` with `last_event card.move:board_card:triaged`, `event_counts card.move|3`; anonymous walk 0 holes; headers PASS; CI `44448730621` success; `concord-rs` active, `concord` inactive. D-106..D-114 recorded. Live POSTs for archive/vote/claim/status/document need credentials `[REDACTED]` (owner: user), already in Blocked; their transactions are proved by store tests. |
| 6 | one pagination convention, enforced | **done at `9631f0a`.** Shared `PageQuery` (default 50, max 200, opaque `o:<offset>` cursor) wired into all 10 collection handlers; `collection_pages: PASS — 10 collection routes, 0 without PageQuery` (baseline 10 missing). `cargo test -p concord-app --lib pagination`: 4 passed; 0 failed (baseline 0). `gate: PASS` — `api: collection_pages: PASS`, `pages: 132/132 ok`, all 13 routes, rate PASS. Live `/version` `concord-rs 0.0.0+9631f0a+vendor`; `?limit=10000` → `400 {"code":"invalid_request","error":"limit must be 1..=200"}`; `?cursor=garbage` → 400 with the cursor message; default `/projects` → 200 with `items` 50, `projects` 73, `next o:50`; `?limit=1` → `next o:1`; `?limit=1&cursor=o:1` → 200 second row. anonymous walk 0 holes, headers PASS, CI `44452443868` success; `concord-rs` active, `concord` inactive. D-115..D-119. |
| 7 | domain crate stops being a comment | **done at `d2226c0`.** `concord-domain` owns stage re-export, `claim_eligible` and `tally_counts`; store and claim handler call them. Domain tests 3 passed; 0 failed (baseline 0); `cargo tree -p concord-domain | grep -c sqlx` 0; targeted store test casts every stance and a mutant ignoring `stand_aside` failed `the domain rule counts stand_aside` (0 passed / 1 failed), restored. `gate: PASS` with `domain: 0 sqlx deps`, `collection_pages: PASS`, `pages: 132/132 ok`. Live version `concord-rs 0.0.0+d2226c0+vendor`; `?limit=1` → `items 1`, `next o:1`; anonymous walk 0 holes; headers PASS; CI `44457649459` success; `concord-rs` active, `concord` inactive. D-120..D-122. |
| 8 | delete the corpses | **done at `be8ee25`.** Deleted `polaris-healthz`, `polaris-ledger`, `polaris-slug` (all 0 importers at `8357c02`) and their workspace entries; regenerated lockfile. `corpse check: PASS — 4 vendored crates, 0 with zero importers`; RED proof adding an unused crate gave `corpse check: FAIL — polaris-zzcorpse` (exit 1), removed. `gate: PASS` with `vendor: corpse check: PASS`, `domain: 0 sqlx deps`, `collection_pages: PASS`, `pages: 132/132 ok`. Live version `concord-rs 0.0.0+be8ee25+vendor`, anonymous walk 0 holes, headers PASS, CI `44460212015` success; `concord-rs` active, `concord` inactive. D-123/D-124. |
| 9 | the suites nobody runs | **done at `4cc98e1` — 29 of 29 e2e suites wired; `RECORDED_UNRUN = 0`.** `scout_e2e` was `26 passed in 28.84s` (baseline `26 failed in 285.93s`); live `concord-rs 0.0.0+4c46314+vendor`, gate `44752488674 success in 2m40s`. At `ed777c5`, `duplicates_e2e` is `5 passed in 15.72s`, `milestones_e2e` is `5 passed in 15.22s`, and `board_e2e` is `8 passed in 14.94s` (D-133..D-135); `project_panels_e2e` was repaired to `26 passed in 18.17s` (was 25 failed/1 passed) with `ConcordApi.list()` and a durable user record (D-131/D-132); `audit_undo_e2e` was wired with `3 passed in 12.11s` (was 3 setup errors); ratchet RED `assert 15 == 16` proved. Three real defects fixed in the same commit (D-128..D-130): nullable-FK/boolean seed translation, `/audit/actions` page-envelope compatibility, and `can_undo` computed from the caller's maintainer role instead of a hardcoded false. Earlier at `ac378ab`, `consensus_e2e` was repaired to `15 passed in 17.85s` (already wired). `make gate` PASS `132/132 ok` with `domain: 0 sqlx deps`, `collection_pages: PASS`, `vendor corpse check: PASS`. Remaining live drift measured at `ac378ab` before these fixes: `feature_e2e` is `15 passed in 7.59s` (D-136), `rank_links_e2e` is `4 passed in 24.83s` (D-137), `requests_e2e` is `18 passed in 10.41s` (D-138/D-139), `audit_e2e` is `11 passed in 11.36s` (D-140), and `probe_claim_button` passes. A full `gate-live` at `a3973cb` had `audit_e2e` as its only failure; that is now fixed. Full `gate-live` at `94ea2c0`: PASS — every wired check green, `pages: 132/132 ok`. Still 15 genuinely unwired suites; ratchet 15. Triage 2026-11-19: 12 suites fail in setup (arena, criteria, feeds, finder, guest, lists, merge, readers, scout, settings_themes, taxonomy, workitems); 3 fail on behavior (curation 5 failed, notifications 4 failed, settings 11 failed/8 passed). `notifications_e2e` expects a server-rendered unread count that the Rust port does not have; `curation_e2e` did not improve when its sign-in was moved before navigation and that unproven change was reverted.. No credential/owner/vendor block; per-suite harness/behavior fixes. |
| 10 | site-review repairs | **done at `c63f706` — `site_review_e2e: 10 passed in 15.40s`; gate `44778091113 success in 2m27s`.** Live `concord-rs 0.0.0+c63f706+vendor`: home count 73, `/lists` and 404 in shell, one Scout form, 0px overflow at 390px on criteria/documents. D-207..D-214. |

- **D-207 — page URLs get the HTML 404; `/api/**` keeps the JSON envelope.** The single API fallback answered raw JSON for a stale page link, with no shell and no way back.
- **D-208 — `/lists` is an honest page.** Lists live inside projects; the new page says so and links to `/projects` instead of returning a 404.
- **D-209 — the Scout shell replaced the legacy static form.** Two forms and two textareas on one page were a mount overlap, not a wizard.
- **D-210 — the project tab strip wraps.** Without `flex-wrap` and a gap the compact strip ran together and overflowed 241px at 390px.
- **D-211 — list-shape clients use `ConcordApi.list`.** Home's project count and the consensus calls index assumed a bare array and read a valid paged response as zero/empty.
- **D-212 — detail pages never end on a loading string.** A watchdog replaces a stale loading placeholder with an explicit failure sentence.

- **D-213 — structural probes are part of the repair.** The list cap must not put a `null` child into `replaceChildren`, and the loading watchdog must read `textContent` when a fake DOM has no `innerText`; both are asserted by the DOM probes.

- **D-214 — the home project count uses the search total.** The projects endpoint is paged at 50, so counting it printed 50 on a 73-project instance; the search response carries the true total.

- **D-215 — a signed-in browser is registered over HTTP, not assembled by hand.** The suite keeps the register token, adds the navigation cookie and the parsed `/auth/me` body to `localStorage`, then revokes the token through `/auth/logout`; no credential value is logged.
- **D-216 — 409 on `/votes/next` is an empty-state signal, not a JavaScript error.** The fixture project has fewer than two features; `rank.js` renders the friendly conflict message. The gate whitelists only that 409 and still fails on any other bad response.

- **D-217 — personalized documents are `no-store`.** The shell carries consent state and an unread count, and some pages carry reader-specific bodies. A browser cache that reuses an anonymous first paint after sign-in shows a signed-out page to a signed-in reader; every rendered document now sets `Cache-Control: no-store`.

- **D-218 — axe must see the settled page.** The home stagger animation's partial opacity made axe report false contrast failures; the gate sets `reduced_motion="reduce"` so the measured colour is the shipped colour.
- **D-219 — the `/lists` CTA is a button.** A bare indigo link inside a text block failed `link-in-text-block` at 2.83:1 with no underline; it now uses the existing `btn btn-secondary` class.

- **D-220 — visible labels are humanized, wire keys are not.** The charter still sends the server's snake_case names in `input[name]`; only labels and help text changed. Save/Reset are sticky and a dirty state is shown; Reset restores the last saved snapshot without a request.

- **D-221 — visible taxonomy is deduplicated; rows are not deleted.** Group ratified tags by namespace plus display name, strip trailing `#`/digits only from what the reader sees, and show a count badge. `data-tag` keeps the exact server key so proposals still name the right tag.

- **D-222 — bounded client views, unchanged server truth.** Filtering and pagination hide rows without deleting or reordering them; the list dedupe keeps the first (highest-ranked) row for a title. Card moves and list votes still work because controls re-render from the same API response.

- **D-223 — dismiss is not consent.** "Not now" hides the notice for the current browser session and changes no cookie; interactions stay blocked until the server records acceptance. Accept remains the only consent path.

- **D-224 — grouping is not hiding.** All existing header links and session hooks remain visible and in the same order; the footer duplicates destinations as plain anchors without client hooks, so session.js keeps exactly one header target.

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

* **D-89 (step 3, 2026-11-18): the root router owns no resource routes.** The last 20 `.route(` calls were finder's
  seven API routes and feeds' thirteen XML routes; both handlers already lived in root-owned modules, so their route
  tables moved into `routes()` functions in `finder.rs` and `feeds.rs`, merged from `lib.rs`. lib.rs 743 -> 719,
  with `root_routes: 0` and all 20 route paths byte-identical. The route-policy scanner and the `policy.rs`
  registered-route oracle both had to learn these two root modules (D-40/D-47): without them seven finder routes
  appeared stale. One test build caught `read_dir(dir)` consuming `dir` before `dir.parent()`; the parent is now
  captured first. Verified: cargo check green, policy 7 passed; 0 failed, route_policies PASS (49 modules,
  0 unmerged, 203 declared), replay 0 disagreed of 16 checked, 5 skipped.

* **D-88 (step 3, 2026-11-18): complete the settings module with its last reader.** `project_tags` joined
  api/project_settings.rs, which already mounted the GET route with the tag write (D-48/D-87). 18 handler lines moved;
  lib.rs 762 -> 743. Its guards (`project_for`, `bearer_user`, `readable`) were already imported for the writer.
  Byte-identity: project_tags_bytes_identical True, sha256 304061ad59b8d984. This is the last root-owned handler with
  a single module importer; what remains are shared helpers imported by several modules (D-41/D-51). Verified: cargo
  check green, policy 7 passed; 0 failed, route_policies PASS (49 modules, 0 unmerged), replay 0 disagreed of
  18 checked, 3 skipped.

* **D-87 (step 3, 2026-11-18): a reader with exactly one importer joins that module.** `project_languages` moved into
  api/project_settings.rs and `project_lists` into api/lists.rs; each module already mounted the GET route with its
  writes, so no route stanza changed (D-48/D-86). 31 handler lines moved; lib.rs 795 -> 762. `bearer_user`,
  `project_for` and `readable` were imported from the crate root. Byte-identity:
  project_languages_bytes_identical True sha256 b95672d64a765348, project_lists_bytes_identical True sha256
  57b34541c7181217. Verified: cargo check green, policy 7 passed; 0 failed, route_policies PASS (49 modules,
  0 unmerged), replay 0 disagreed of 18 checked, 3 skipped.

* **D-86 (step 3, 2026-11-18): D-48 readers complete the criteria module.** `list_criteria` and
  `list_criteria_profiles` were the last root-owned criteria handlers; api/criteria.rs already mounted both GET routes
  with the writes, so no route stanza changed (D-48/D-72). 33 handler lines moved; lib.rs 829 -> 795. `project_by_id`
  and `readable_project` were imported from the crate root; `get_charter` stays there because another module imports
  it. Byte-identity: criteria_readers_bytes_identical True, sha256 2b94019cb2229401. Verified: cargo check green,
  policy 7 passed; 0 failed, route_policies PASS (49 modules, 0 unmerged), replay 0 disagreed of 16 checked, 5 skipped.

* **D-85 (step 3, 2026-11-18): membership gets its own small module, not the accounts module.** api/accounts.rs owns
  registration, not project membership; no sibling owned the `/members` reader (D-68/D-69). api/members.rs was
  created, declared and mounted in the same edit (D-47). 20 handler lines moved; lib.rs 850 -> 829. `project_by_id`,
  `readable` and `bearer_user` were imported from the crate root. Byte-identity:
  list_members_bytes_identical True, sha256 0c9ba0b414fcd9d5. Verified: cargo check green, policy 7 passed; 0 failed,
  route_policies PASS (49 modules, 0 unmerged), replay 0 disagreed of 18 checked, 3 skipped.

* **D-84 (step 3, 2026-11-18): a catalogue read with its own route gets its own module.** `list_capabilities` was
  root-owned with a single GET and no owning module (D-68/D-69), so api/capabilities.rs was created, declared and
  mounted in the same edit (D-47). 16 handler lines moved; lib.rs 867 -> 850. `project_by_id`, `readable_project` and
  `internal` were imported from the crate root. Byte-identity:
  list_capabilities_bytes_identical True, sha256 ce470c3c7ab2d4f5. Verified: cargo check green, policy 7 passed;
  0 failed, route_policies PASS (48 modules, 0 unmerged), replay 0 disagreed of 16 checked, 5 skipped.

* **D-83 (step 3, 2026-11-18): complete the identity module with the caller lookup.** `auth_me` was the last
  root-owned identity handler; api/identity.rs already mounted guest, consent, login and logout (D-63/D-71). The
  handler and its `GET /api/v1/auth/me` route moved in (14 lines; lib.rs 883 -> 867). `Extension` was added to that
  module's axum imports; the route was appended after logout. Byte-identity:
  auth_me_bytes_identical True, sha256 5aab90c9722369c8. Verified: cargo check green, policy 7 passed; 0 failed,
  route_policies PASS (47 modules, 0 unmerged), replay 0 disagreed of 18 checked, 3 skipped.

* **D-82 (step 3, 2026-11-18): create a new module when no sibling owns the resource.** `list_curation` was
  root-owned with two GET stanzas and no curation module existed (D-68/D-69), so api/curation.rs was created and
  mounted in `router()` in the same edit (D-47). 60 handler lines moved; lib.rs 947 -> 883. `bearer_user` and
  `internal` were imported from the crate root; both route spellings moved with the handler. Byte-identity:
  list_curation_bytes_identical True, sha256 41ea505c011f7690. Verified: cargo check green, policy 7 passed; 0 failed,
  route_policies PASS (47 modules, 0 unmerged — the strengthened scanner sees the new module as mounted),
  replay 0 disagreed of 16 checked, 5 skipped.

* **D-81 (step 3, 2026-11-18): a ranking handler moves with its request struct, but not with a shared constant.**
  `CompositeRequest` and `composite_rank` moved from lib.rs into api/solutions.rs (103 lines; lib.rs 1052 -> 947)
  with the `POST /projects/{project_id}/rank/composite` route. `STANDINGS_PER_FEATURE` stayed at the crate root because
  api/solutions.rs already imported it (D-65); the cut stops immediately before it. No new imports were needed — the
  module already had State, Path, HeaderMap, Json and readable_project. Byte-identity:
  composite_rank_bytes_identical True, sha256 ab6ce7ddc1ddf566. Verified: cargo check green, policy 7 passed; 0 failed,
  route_policies PASS (46 modules, 0 unmerged), replay 0 disagreed of 18 checked, 3 skipped.

* **D-80 (step 3, 2026-11-18): a panel's count reader belongs with the panel's arena module.** `feature_tallies` was
  root-owned only because its GET route was in the root router; the doc comment names it the arena panel's per-feature
  counts, so it moved into api/arena_votes.rs (20 lines; lib.rs 1074 -> 1052) with that route. The module was already
  mounted, so only its route table changed. `readable_project` was added to its imports; `project_by_id` and `internal`
  were already there. The `null`-not-`[]` behaviour for a project with no features was not tidied (D-35).
  Byte-identity: feature_tallies_bytes_identical True, sha256 0932cc3514cc7c11. Verified: cargo check green, policy
  7 passed; 0 failed, route_policies PASS (46 modules, 0 unmerged), replay 0 disagreed of 16 checked, 5 skipped.

* **D-79 (step 3, 2026-11-18): a resource owns its reader routes too, even when they live in the root router.**
  `readable_list`, `get_list` and `get_list_entries` were the last root-owned handlers of api/lists.rs (65 lines;
  lib.rs 1142 -> 1074). The two root GET stanzas for `/lists/{list}` and its trailing-slash twin moved with them; the
  `/lists/{list}/entries` route was already in the module. `numeric_project` and `readable` were imported from the
  crate root; they are shared and stay there (D-41/D-43). Byte-identity:
  list_readers_bytes_identical True, sha256 fd38656b5025091d.
  Verified: cargo check green, policy 7 passed; 0 failed, route_policies PASS (46 modules, 0 unmerged),
  replay 0 disagreed of 18 checked, 3 skipped.

* **D-78 (step 3, 2026-11-18): a single-importer reader belongs in the module that mounts it.** `get_milestone` was
  the last root-owned handler of api/milestones.rs; only that module imported it (D-51), so it moved in (25 lines;
  lib.rs 1168 -> 1142). Its GET route was already in the module's shared MethodRouter with the writes (D-48), so no
  route stanza changed. `numeric_project` was imported from the crate root; `write_error` stays shared (D-41/D-43).
  Byte-identity: get_milestone_bytes_identical True, sha256 5d1e82d84800e5bf.
  Verified: cargo check green, policy 7 passed; 0 failed, route_policies PASS (46 modules, 0 unmerged),
  replay 0 disagreed of 16 checked, 5 skipped.

* **D-77 (frontend, 2026-11-18): finish the CSS-only alignment items.** The second vision pass called the page
  launch-quality but named three cheap leftovers: Explore link hints sat at different heights, the dark-band CTA was
  underlined like a link, and long catalogue names could break the grid. Fixed with flex alignment on `.grid-4 .card`,
  `text-decoration: none` on `.flip-section .btn`, and `overflow-wrap: anywhere` on catalogue links. Rejected:
  restructuring the catalogue data to add missing stage badges — that changes API presentation and belongs to a data
  decision, not a visual micro-pass.

* **D-76 (frontend, 2026-11-18): act on the vision review's cheap, concrete findings.** After the first live pass a
  vision review named the duplicated hero wordmark, the orphaned "code.", six text-only cards with no visual anchor, a
  "Browse projects" label on the Features card, and a flip band with no action. Fix: remove the redundant hero brand,
  `text-wrap: balance` on the headline, CSS `data-icon` tiles on the six cards, correct the Features link label, and
  add one primary CTA to the flip band. Rejected: shipping an SVG icon system (the CSP and node-only renderer rules
  make text + CSS the cheaper safe route) and rewriting consent copy (a separate decision with its own version).
  Verified by a second browser pass and screenshots, not by assertion.

* **D-74 (frontend, 2026-11-18): a mutation-test probe must never reach a shipped asset.** The browser showed
  `ReferenceError: mutant is not defined` at `project-render.js:37`, so every project overview rendered "Could not load
  project" while all fourteen API fetches returned 200. Removing the undefined token from `panelEmpty()` restores the
  page. Rejected: adding a `var mutant` shim — that would keep an instrumentation hook in production code and hide the
  next real break.

* **D-75 (frontend, 2026-11-18): re-emulate the Go landing composition; this supersedes D-17's "compact" scope.** The
  owner explicitly asked for the frontend to look as good as possible and to emulate the Go implementation. The Rust
  home had only a bare `<ul>`; the Go page had a hero, six decision cards, a live stats strip, four Explore cards and a
  flip band, and all its CSS and `index.js` were already ported. Decision: port that composition as the static
  `GO_LANDING` literal above the existing catalogue (the catalogue probe still counts exactly one `project-list`), and
  load `skeleton.js` + `index.js`. Rejected: the D-17 minimal headline/paragraph (measured visually too sparse), a
  full CSS rewrite (the design system is already present), and a client-rendered hero (node-only renderer rule).
  ADR 0018 remains the record of the original narrower decision; this row records why it changed.

* **D-73 (step 3, 2026-11-18): D-48 root-owned readers finish the module in a second slice.** feature_deps and
  feature_evidence were the last root-owned handlers of api/feature_work.rs; their GET routes already lived with the
  writes. The two handlers moved in (35 lines; lib.rs 1204 -> 1168) with no route edit. Compiler named bearer_user,
  feature_for, project_for and readable (D-23), all imported. Byte-identity:
  feature_work_readers_bytes_identical True, sha256 1ea1ac05ecb4e687. Verified: policy 7 passed; 0 failed,
  route_policies PASS (46 modules, 0 unmerged), replay 0 disagreed of 18 checked, 3 skipped.
* **D-72 (step 3, 2026-11-18): a D-48 handler move needs no route edit.** feature_tags was the last root-owned
  handler of api/feature_meta.rs; its GET route already lived in the module with the tag writes. The handler moved in
  (17 lines; lib.rs 1222 -> 1204) and the module doc was corrected; no route stanza changed. Compiler named bearer_user,
  feature_for, project_for and readable (D-23), all imported. Byte-identity:
  feature_tags_block_bytes_identical True, sha256 c56bf561bf79879a. Verified: policy 7 passed; 0 failed, route_policies
  PASS (46 modules, 0 unmerged), replay 0 disagreed of 16 checked, 5 skipped.
* **D-71 (step 3, 2026-11-18): complaint readers complete the complaint resource.** get_complaint,
  complaint_features, feature_complaints and their three GET routes joined api/complaints.rs (78 lines; lib.rs 1304 ->
  1222). numeric_project stays at the crate root because milestone and list readers still use it (D-43); compiler named
  project_by_id, readable, numeric_project and feature_for (D-23), all imported. Byte-identity:
  complaint_reader_block_bytes_identical True, sha256 0896c4cf3050725c. Verified: policy 7 passed; 0 failed,
  route_policies PASS (46 modules, 0 unmerged), replay 0 disagreed of 18 checked, 3 skipped.
* **D-70 (step 3, 2026-11-18): feature/complaint links belong to the complaint resource.**
  LinkComplaintRequest, link_complaint, unlink_complaint and their POST/DELETE routes joined api/complaints.rs (94 lines;
  lib.rs 1407 -> 1304). FeatureQuery and write_error stay at the crate root (D-43); the compiler named json!, which was
  imported alongside write_error and Deserialize (D-23). Byte-identity:
  complaint_link_block_bytes_identical True, sha256 3db1b24605a99afd. Verified: policy 7 passed; 0 failed,
  route_policies PASS (46 modules, 0 unmerged, 203 declared), replay 0 disagreed of 16 checked, 5 skipped.
* **D-69 (step 3, 2026-11-18): a public read of its own store table gets its own small module.**
  list_priorities reads concord_db::priorities, not features, so it moved into a new api/priorities.rs with its route
  (18 lines; lib.rs 1429 -> 1407). Declared in api/mod.rs and merged before features (D-47). Byte-identity:
  priorities_block_bytes_identical True, sha256 1924d043649ded40. Verified: policy 7 passed; 0 failed, route_policies
  PASS (46 modules, 0 unmerged, 203 declared), replay 0 disagreed of 18 checked, 3 skipped.
* **D-68 (step 3, 2026-11-18): audit reads get their own resource module.** No existing api/*.rs owned the audit
  log (write_audit is only a helper), so AuditQuery, list_audit, list_audit_actions and two routes moved into a new
  api/audit.rs (78 lines; lib.rs 1512 -> 1429), declared in api/mod.rs and merged in router(). Byte-identity:
  audit_block_bytes_identical True, sha256 0dd21d50596e83c8. Verified: policy 7 passed; 0 failed, route_policies PASS
  (45 modules, 0 unmerged, 203 declared), replay 0 disagreed of 18 checked, 3 skipped.
* **D-67 (step 3, 2026-11-18): the last root-owned handler of a module belongs in that module.** arena_votes and
  its GET `/projects/{project_id}/arenas/{arena_id}/votes` route joined api/arena_votes.rs (41 lines; lib.rs 1558 ->
  1512). No import changes were needed; STANDINGS_PER_FEATURE stays at root (D-65). Byte-identity:
  arena_votes_block_bytes_identical True, sha256 1f125eb35f9a9127. Verified: policy 7 passed; 0 failed, route_policies
  PASS (44 modules, 0 unmerged), replay 0 disagreed of 16 checked, 5 skipped.
* **D-66 (step 3, 2026-11-18): shared request guards stay at the root while their routes move.**
  require_readable_request and request_answers are imported by api/answers.rs, so they remain root-owned (D-43). The
  four route-owned readers — list_project_requests, list_instance_requests, one_request and request_tags — plus the
  `/requests/{request_id}` route joined api/requests.rs (52 lines; lib.rs 1615 -> 1558). Compiler named project_by_id and
  readable_project (D-23). All four blocks byte-identical to HEAD (D-45): 8952a918ec7699f1, 600fe3261a03269e,
  b300b0f0c407a467, b5d752a5372944cf. Verified: policy 7 passed; 0 failed, route_policies PASS (44 modules, 0 unmerged),
  replay 0 disagreed of 18 checked, 3 skipped.
* **D-65 (step 3, 2026-11-18): a shared constant outranks the resource boundary.** PairSide, arena_next_pair and
  arena_verification plus two routes joined api/arena_votes.rs (164 lines; lib.rs 1785 -> 1615). STANDINGS_PER_FEATURE
  is imported by api/solutions.rs, so it was returned to the crate root (D-41/D-43); project_by_id and readable were
  imported. Moved handlers remain byte-identical to HEAD: arena_read_block_bytes_identical True, sha256
  7f44b836b3d984c3. Verified: policy 7 passed; 0 failed, route_policies PASS (44 modules, 0 unmerged), replay 0
  disagreed of 16 checked, 5 skipped.
* **D-64 (step 3, 2026-11-18): objection resolution belongs with the consensus resource.**
  ResolveObjectionRequest and resolve_consensus_objection plus the PUT route joined api/consensus.rs (26 lines; lib.rs
  1816 -> 1785). No new module declaration. The block is byte-identical to HEAD:
  objection_block_bytes_identical True, sha256 fae744b90264dd54 (D-45). require_maintainer stays at the root (D-51).
  Verified: policy 7 passed; 0 failed, route_policies PASS (44 modules, 0 unmerged), replay 0 disagreed of 18 checked,
  3 skipped.
* **D-63 (step 3, 2026-11-18): complete an existing module instead of creating a near-duplicate.**
  get_call_hold, HoldRequest and place_emergency_hold plus their GET+POST route moved into api/emergency_holds.rs
  (75 lines; lib.rs 1895 -> 1816). No new module declaration. The block is mutation-sensitive, so it was proven
  byte-for-byte against HEAD: hold_block_bytes_identical True, sha256 8efd25edf2540f57 (D-35/D-45). Compiler named json!,
  require_maintainer and write_error (D-23). Verified: policy 7 passed; 0 failed, route_policies PASS (44 modules,
  0 unmerged), replay 0 disagreed of 18 checked, 3 skipped.
* **D-62 (step 3, 2026-11-18): keep the emergency-hold resource separate from the override window.**
  api/overrides.rs owns get_override_window, open/cast/close and three routes (108 lines; lib.rs 2015 -> 1895). The cut
  stops before get_call_hold; release_emergency_hold remains in api/emergency_holds.rs. Compiler named write_error and
  require_maintainer (D-23). Module merged in the same edit (D-47). Verified: policy 7 passed; 0 failed, route_policies
  PASS (44 modules, 0 unmerged), replay 0 disagreed of 16 checked, 5 skipped.
* **D-61 (step 3, 2026-11-18): leave a reader where a sibling module already imports it.**
  api/consensus_outcomes.rs owns CallOutcomeRequest, AmendmentRequest, default_solution_type and
  record_call_outcome plus one route (65 lines; lib.rs 2083 -> 2015). get_charter stays root-owned because
  api/criteria.rs mounts it (D-51); the cut starts at CallOutcomeRequest. Compiler named State, project_for,
  require_maintainer and parse_id (D-23). Module merged in the same edit (D-47). Verified: policy 7 passed; 0 failed,
  route_policies PASS (43 modules, 0 unmerged), replay 0 disagreed of 18 checked, 3 skipped.
* **D-60 (step 3, 2026-11-18): one handler pair can serve two spellings of the same route.**
  api/solution_calls.rs owns readiness, open and fallback plus three routes (128 lines; lib.rs 2223 -> 2083). The Go
  `/consensus` and port `/solution-call` paths share one MethodRouter (D-20); feature_for stays at the crate root.
  Compiler named StatusCode and write_error (D-23). Module merged in the same edit (D-47). Verified: policy 7 passed;
  0 failed, route_policies PASS (42 modules, 0 unmerged), replay 0 disagreed of 16 checked, 5 skipped.
* **D-59 (step 3, 2026-11-18): shared refusal mappers stay at the root, the rest of the resource moves.**
  api/strategy.rs owns StrategyCaller, strategy_project, weight/theme proposal reads and writes, theme_refusal and seven
  routes (256 lines; lib.rs 2507 -> 2223). taxonomy_refusal and strategy_refusal have importers in api/moderation.rs and
  api/feature_meta.rs, so they stay at the crate root (D-51); the cut stops before `readable`. Compiler named State and
  project_by_id (D-23). Module merged in the same edit (D-47). Verified: policy 7 passed; 0 failed, route_policies PASS
  (41 modules, 0 unmerged), replay 0 disagreed of 18 checked, 3 skipped.
* **D-58 (step 3, 2026-11-18): anchor a slice on its doc comment when no section marker survives.**
  api/stats.rs owns stats, summary, search, history, responsiveness and five public GET routes (137 lines; lib.rs
  2649 -> 2507). The first script aborted on a missing `// ──` anchor before any write; the retry used the
  `GET /api/v1/stats` doc comment (D-50 discipline held). Compiler named bearer_user (D-23). Module merged in the same
  edit (D-47). Verified: policy 7 passed; 0 failed, route_policies PASS (40 modules, 0 unmerged), replay 0 disagreed of
  18 checked, 3 skipped.
* **D-57 (step 3, 2026-11-18): moderation acts and curation votes share one resource.**
  api/moderation.rs owns ArenaActRequest, mod_trust_min, permit_moderation, arena remove/restore, CurationVoteRequest and
  curation_vote plus three routes (246 lines; lib.rs 2904 -> 2649). The curation queue reader stays root-owned. Compiler
  named write_error and taxonomy_refusal (D-23). Module merged in the same edit (D-47). Verified: policy 7 passed;
  0 failed, route_policies PASS (39 modules, 0 unmerged), replay 0 disagreed of 16 checked, 5 skipped.
* **D-56 (step 3, 2026-11-18): the labelled complaint section now holds only shared helpers.** Accept the
  resource where it lives: the thread-comment slice was the next coherent handler group. api/comments.rs owns
  thread_comments, post/delete/vote comment and three routes (95 lines; lib.rs 3013 -> 2904). The cut stops before
  ArenaActRequest. Compiler named Value and StatusCode (D-23). Module merged in the same edit (D-47). Verified: policy 7
  passed; 0 failed, route_policies PASS (38 modules, 0 unmerged), replay 0 disagreed of 18 checked, 3 skipped.
* **D-55 (step 3, 2026-11-18): the answer resource includes its accept badge helpers.**
  api/answers.rs owns refusal_error, accept/unaccept, answer_request, vote_answer and three routes (217 lines; lib.rs
  3240 -> 3013). The slice begins at the accept refusal mapper and ends at the complaint section. Reader request_answers
  stays root-owned with its GET+POST route moved (D-48). Compiler named State and readable_project (D-23). Module merged
  in the same edit (D-47). Verified: policy 7 passed; 0 failed, route_policies PASS (37 modules, 0 unmerged), replay 0
  disagreed of 16 checked, 5 skipped.
* **D-54 (step 3, 2026-11-18): cut a combined section at the next resource's doc comment.**
  The filing section held requests and answers; api/requests.rs took the request half — duplicate gate, project/instance
  filing, similar search, tags and four routes (323 lines; lib.rs 3576 -> 3240). The cut stops at the
  `POST /requests/{id}/answers` doc comment; answer_request and vote_answer remain root-owned for the next move.
  Reader handlers stay root-owned with their routes moved (D-48). Compiler named readable_project_segment, write_audit,
  require_readable_request and IntoResponse (D-23). Module merged in the same edit (D-47). Verified: policy 7 passed;
  0 failed, route_policies PASS (36 modules, 0 unmerged), replay 0 disagreed of 16 checked, 5 skipped.
* **D-53 (step 3, 2026-11-18): move a resource's readers and writers in two slices around shared helpers.**
  api/consensus.rs owns list/get consensus plus four consensus writes and six routes (225 lines; lib.rs 3824 -> 3576).
  The two slices stop before `project_for`/`parse_id` and before `ResolveObjectionRequest`; `require_maintainer` stays
  root-owned. The compiler named json!, bearer_user, write_error and parse_id as missing imports (D-23). Merged in the
  same edit (D-47). Verified: policy 7 passed; 0 failed, route_policies PASS (35 modules, 0 unmerged), replay 0 disagreed
  of 16 checked, 5 skipped.
* **D-52 (step 3, 2026-11-18): the last section can run to the end of the file.**
  api/feature_work.rs owns feature_work_refusal, seven work/dependency/evidence handlers and five routes (231 lines;
  lib.rs 4075 -> 3824). The cut ran from the section marker to EOF (D-39). Sibling grep found no other importers of
  feature_work_refusal (D-51); caller_feature, feature_deps and feature_evidence remain root-owned, with their routes moved
  into the module. Module merged in the same edit (D-47). Verified: cname clean, policy 7 passed; 0 failed,
  route_policies PASS (34 modules, 0 unmerged), replay 0 disagreed of 16 checked, 5 skipped.
* **D-51 (step 3, 2026-11-18): a shared helper found by the compiler goes back to the crate root.**
  api/feature_meta.rs owns four feature metadata writes and routes (173 lines; lib.rs 4242 -> 4075 after the helper
  return). `caller_feature` was inside the moved slice but five other api modules import it from the root; the compiler
  named each one. It was moved back to lib.rs and imported by the new module, along with root-owned `strategy_refusal`
  and StatusCode. The move script asserted all inputs before writing any file (D-50). Verified: cname clean, policy 7
  passed; 0 failed, route_policies PASS (33 modules, 0 unmerged), replay 0 disagreed of 16 checked, 5 skipped.
* **D-50 (step 3, 2026-11-18): finish an aborted move from HEAD, never from the half-edited working tree.**
  A guard assert in the move script fired after lib.rs and api/mod.rs were written but before api/lists.rs existed. The
  module file was rebuilt by slicing the original block from `git show HEAD:crates/app/src/lib.rs`, not by guessing at the
  partial tree. api/lists.rs owns create_list, create_list_entry, vote_list_entry and the metrics/visibility writes plus
  six routes (261 lines; lib.rs 4517 -> 4242). Readers stay root-owned; the module was merged in the same edit (D-47).
  Verified: cname clean, policy 7 passed; 0 failed, route_policies PASS (32 modules, 0 unmerged), replay 0 disagreed of
  16 checked, 5 skipped.
* **D-49 (step 3, 2026-11-17): the criteria section also owns the charter write and its helper.**
  api/criteria.rs owns criteria_refusal, five criteria/charter writes, apply_charter_fields and seven routes (278 lines;
  lib.rs 4810 -> 4517). The criteria readers, list_criteria_profiles and get_charter stay root-owned, with their GET
  routes moved alongside the writes (D-20/D-48). Module merged in the same edit (D-47): route_policies reports 31
  modules, 0 unmerged. Verified: policy 7 passed; 0 failed, route_policies PASS (203 declared), replay 0 disagreed of
  18 checked, 3 skipped.
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
  **Thirtieth module (62982b5):** settings writes moved; git show --stat 194 added in api/project_settings.rs, 170
  changed in lib.rs. Live anon GET /projects/suki/tags `200 list 0`, /tags/completeness `200 dict` with the score
  fields intact, /languages `200 list 0`; PUT tags and PUT intake-mode both `403 consent_required`. /version equality
  True; route_policies 30 modules 0 unmerged; walk PASS; headers PASS; CI 44199542168.
  **Thirty-first module (edcfda8):** criteria and charter writes moved; git show --stat 326 added in api/criteria.rs,
  295 changed in lib.rs. Live anon GET /projects/suki/criteria `200 list 0`, /criteria-profiles `200 list 0`,
  /charter `200 dict 22`; POST criteria and PUT charter both `403 consent_required`. /version equality True;
  route_policies 31 modules 0 unmerged; walk PASS; headers PASS; CI 44203187335.
  **Thirty-second module (4fa3539):** list writes moved; git show --stat 306 added in api/lists.rs, 277 changed in
  lib.rs. Live anon GET /projects/suki/lists `200 list 2`; POST lists, PUT metrics and PUT visibility all
  `403 consent_required`. /version equality True; route_policies 32 modules 0 unmerged; walk PASS; headers PASS;
  CI 44214531180.
  **Thirty-third module (bd9c570):** feature metadata writes moved; git show --stat 187 added in api/feature_meta.rs,
  169 changed in lib.rs. Live anon GET /projects/suki/features/5784/tags `200 dict 2`; POST tags and PUT strategic-weight
  both `403 consent_required`. /version equality True; route_policies 33 modules 0 unmerged; walk PASS; headers PASS;
  CI 44223857215.
  **Thirty-fourth module (120099f):** feature work writes moved to EOF; git show --stat 272 added in
  api/feature_work.rs, 253 changed in lib.rs. Live anon GET /projects/suki/features/5784/deps `200 dict 3` and
  /evidence `200 list 0`; POST claim and PUT work-item both `403 consent_required`. /version equality True;
  route_policies 34 modules 0 unmerged; walk PASS; headers PASS; CI 44228621160.
  **Thirty-fifth module (7a695a6):** consensus reads and writes moved in two slices; git show --stat 268 added in
  api/consensus.rs, 250 changed in lib.rs. Live anon GET /projects/suki/consensus `200 list 0`; POST `403
  consent_required`. GET /projects/suki/consensus/abc `400 call id must be an integer` proves the `{call}` route
  matches; `/consensus/1` `404 No such API route.` is the generic not-found envelope for a call that does not exist,
  the same envelope an unknown slug gets. /version equality True; route_policies 35 modules 0 unmerged; walk PASS;
  headers PASS; CI 44236198926.
  **Thirty-sixth module (13411cd):** request filing moved; git show --stat 360 added in api/requests.rs, 338 changed in
  lib.rs. Live anon GET /api/v1/requests `200 list 0` and /api/v1/projects/suki/requests `200 list 0`; POST /requests
  `403 consent_required`. /version equality True; route_policies 36 modules 0 unmerged; walk PASS; headers PASS;
  CI 44242070130.
  **Thirty-seventh module (cd0b084):** answer resource moved; git show --stat 250 added in api/answers.rs, 229 changed
  in lib.rs. Live anon POST /requests/1/answers, PUT /requests/1/accept and POST /answers/1/vote all `403
  consent_required`, proving the three routes are mounted. GET /requests/1/answers `404` is the generic not-found
  envelope for a request that does not exist (see D-53). /version equality True; route_policies 37 modules 0 unmerged;
  walk PASS; headers PASS; CI 44245668629.
  **Thirty-eighth module (416c07d):** thread comments moved; git show --stat 128 added in api/comments.rs, 111 changed
  in lib.rs. Live anon GET /projects/1/threads/feature/1 `200 list 0` (an unknown kind such as `comments` is `400
  invalid thread_kind`, by design); anon POST, DELETE and vote all `403 consent_required`. /version equality True;
  route_policies 38 modules 0 unmerged; replay 0 disagreed of 18 checked, 3 skipped; walk PASS; headers PASS;
  CI 44248263346.
  **Thirty-ninth module (baab193):** moderation and curation votes moved; git show --stat 279 added in
  api/moderation.rs, 257 changed in lib.rs. Live anon GET /api/v1/curation `200 dict 2`; anon POST arena remove, arena
  restore and curation vote all `403 consent_required`. /version equality True; route_policies 39 modules 0 unmerged;
  replay 0 disagreed of 16 checked, 5 skipped; walk PASS; headers PASS; CI 44251396868.
  **Fortieth module (48f3feb):** stats/search moved; git show --stat 163 added in api/stats.rs, 144 changed in lib.rs.
  Live anon GET /api/v1/search `200 dict 6`, /stats `200 dict 4`, /stats/summary `200 dict 11`, /stats/history
  `200 list 0`, /stats/responsiveness `200 list 0`. /version equality True; route_policies 40 modules 0 unmerged;
  replay 0 disagreed of 18 checked, 3 skipped; walk PASS; headers PASS; CI 44254461696.
  **Forty-first module (1672c43):** strategy resource moved; git show --stat 306 added in api/strategy.rs, 286 changed
  in lib.rs. Live anon GET /projects/1/strategy/themes, /weight-proposals and /theme-proposals all `401` (identity
  required by strategy_project, not the not-found envelope); anon POST themes `403 consent_required`. /version
  equality True; route_policies 41 modules 0 unmerged; replay 0 disagreed of 18 checked, 3 skipped; walk PASS;
  headers PASS; CI 44257658683.
  **Forty-second module (8af9d4f):** solution calls moved; git show --stat 163 added in api/solution_calls.rs, 142
  changed in lib.rs. Live anon GET /projects/suki/features/5784/solution-call and /consensus both `200 dict 9`;
  anon POST `403 consent_required`; /consensus/abc/fallback `400 call id must be an integer` proves the fallback
  route matches. /version equality True; route_policies 42 modules 0 unmerged; replay 0 disagreed of 16 checked,
  5 skipped; walk PASS; headers PASS; CI 44260547188.
  **Forty-third module (b744df9):** call outcomes moved; git show --stat 89 added in api/consensus_outcomes.rs, 70
  changed in lib.rs. Live anon POST /projects/suki/consensus/1/outcome `403 consent_required`. GET on this POST-only
  path returns the pre-existing generic `500 route_declares_no_policy`; three other POST-only routes gave the same
  answer, so it is not a move regression (D-61). /version equality True; route_policies 43 modules 0 unmerged; replay
  0 disagreed of 18 checked, 3 skipped; walk PASS; headers PASS; CI 44263456328.
  **Forty-fourth module (80fbdc1):** override windows moved; git show --stat 143 added in api/overrides.rs, 122 changed
  in lib.rs. Live anon POST /projects/suki/consensus/1/override, /override/position and /override/close all `403
  consent_required`. GET on the missing call returned the generic not-found envelope (D-53); the three 403s prove the
  routes are mounted. /version equality True; route_policies 44 modules 0 unmerged; replay 0 disagreed of 16 checked,
  5 skipped; walk PASS; headers PASS; CI 44267064538.
  **Forty-fifth move (f20d15b):** hold read and placement joined the release handler in api/emergency_holds.rs;
  git show --stat 98 added/changed in that module, 79 removed from lib.rs. Live anon POST /consensus/1/hold and
  /emergency-holds/1/release both `403 consent_required`; GET on call 1 `404` is the generic missing-call envelope.
  Byte-identity proof of the mutation-sensitive block: hold_block_bytes_identical True, sha256
  8efd25edf2540f57. /version equality True; route_policies 44 modules 0 unmerged; replay 0 disagreed of 18 checked,
  3 skipped; walk PASS; headers PASS; CI 44270030258.
  **Forty-sixth move (0532598):** objection resolution joined api/consensus.rs; git show --stat 31 added in that
  module, 31 removed from lib.rs. Live anon PUT /projects/suki/consensus/objections/1/resolve `403 consent_required`.
  objection_block_bytes_identical True, sha256 fae744b90264dd54. /version equality True; route_policies 44 modules
  0 unmerged; replay 0 disagreed of 18 checked, 3 skipped; walk PASS; headers PASS; CI 44273128808.
  **Forty-seventh move (08cb2f8):** arena read slice joined api/arena_votes.rs; git show --stat 186 added in that
  module, 172 removed from lib.rs. Live /projects/1/arenas/1/next-pair `400 invalid_request` (arena needs two entries)
  and /verification `200` with agrees true — both prove the routes are mounted. STANDINGS_PER_FEATURE stayed at the
  crate root; byte-identity of the handlers: sha256 7f44b836b3d984c3. /version equality True; route_policies 44
  modules 0 unmerged; replay 0 disagreed of 16 checked, 5 skipped; walk PASS; headers PASS; CI 44276693296.
  **Forty-eighth move (1c97a64):** four request readers and the /requests/{request_id} route joined api/requests.rs;
  git show --stat 61 added in that module, 57 removed from lib.rs. Live /requests and /projects/suki/requests `200 []`;
  /requests/abc, /requests/abc/tags and /requests/abc/answers each `400 invalid request id`, proving all three routes are
  mounted (D-53); a numeric id with no record returns the generic not-found envelope. Shared guards stayed at root.
  /version equality True; byte-identity hashes 8952a918ec7699f1, 600fe3261a03269e, b300b0f0c407a467,
  b5d752a5372944cf; route_policies 44 modules 0 unmerged; replay 0 disagreed of 18 checked, 3 skipped; walk PASS;
  headers PASS; CI 44280391173.
  **Forty-ninth move (002e443):** the vote log joined api/arena_votes.rs; git show --stat 46 added in that module, 46
  removed from lib.rs. Live /projects/1/arenas/1/votes `200` with `arena_id 1, count 0, votes []`. Byte-identity sha256
  1f125eb35f9a9127; no import changes; STANDINGS_PER_FEATURE stayed at root (D-65). /version equality True;
  route_policies 44 modules 0 unmerged; replay 0 disagreed of 16 checked, 5 skipped; walk PASS; headers PASS;
  CI 44284031903.
  **Fiftieth move (7ecee40):** new api/audit.rs took AuditQuery, two readers and two routes; git show --stat 106 added
  in that module, 85 removed from lib.rs, plus the declaration in api/mod.rs. Live /projects/suki/audit `200` with
  entries, /audit/actions `200` with the vocabulary, and `?kind=bogus` `400 invalid_request`. Byte-identity sha256
  0dd21d50596e83c8. /version equality True; route_policies 45 modules 0 unmerged; replay 0 disagreed of 18 checked,
  3 skipped; walk PASS; headers PASS; CI 44287885695.
  **Fifty-first move (614a916):** new api/priorities.rs took list_priorities and its public GET route; git show --stat
  40 added in that module, 24 removed from lib.rs, plus the declaration in api/mod.rs. Live
  /projects/suki/priorities `200` with ranked features (elo_r, pain_score). Byte-identity sha256
  1924d043649ded40. /version equality True; route_policies 46 modules 0 unmerged; replay 0 disagreed of 18 checked,
  3 skipped; walk PASS; headers PASS; CI 44291266171.
  **Fifty-second move (a94f1d9):** the feature/complaint link writers joined api/complaints.rs; git show --stat 108
  added in that module, 103 removed from lib.rs. Live anon POST /projects/suki/features/1/complaints and DELETE
  /complaints/1 both `403 consent_required`. Byte-identity sha256 3db1b24605a99afd; FeatureQuery and write_error
  stayed at the crate root. /version equality True; route_policies 46 modules 0 unmerged; replay 0 disagreed of 16
  checked, 5 skipped; walk PASS; headers PASS; CI 44295564056.
  **Fifty-third move (cda55eb):** three complaint readers and their GET routes joined api/complaints.rs; git show
  --stat 95 added in that module, 82 removed from lib.rs. Live /projects/suki/complaints/abc and /complaints/abc/features
  both `400 complaint id must be an integer`, and /projects/suki/features/2128/complaints `200` with real entries —
  all three routes are mounted. Byte-identity sha256 0896c4cf3050725c; numeric_project stayed at root. /version
  equality True; route_policies 46 modules 0 unmerged; replay 0 disagreed of 18 checked, 3 skipped; walk PASS;
  headers PASS; CI 44298656846.
  **Fifty-fourth move (8b6cefb):** the last root-owned feature-meta handler joined api/feature_meta.rs; git show --stat
  24 changed in that module, 18 removed from lib.rs. No route edit was needed: the GET route already lived with the
  tag writes (D-48). Live /projects/suki/features/2128/tags `200` with feature_id and tags. Byte-identity sha256
  c56bf561bf79879a. /version equality True; route_policies 46 modules 0 unmerged; replay 0 disagreed of 16 checked,
  5 skipped; walk PASS; headers PASS; CI 44315044140.
  **Fifty-fifth move (9e91f6d):** the last root-owned feature-work readers joined api/feature_work.rs; git show --stat
  40 changed in that module, 36 removed from lib.rs. No route edit: both GETs already lived with the writes (D-48).
  Live /projects/suki/features/2128/deps `200` with blocked_by/blocks/open_blockers and /evidence `200 []`. Byte-identity
  sha256 1ea1ac05ecb4e687. /version equality True; route_policies 46 modules 0 unmerged; replay 0 disagreed of 18
  checked, 3 skipped; walk PASS; headers PASS; CI 44318663362.
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

- **D-128 — the seed shim treats 0 in a nullable `*_id` as NULL, and 0/1 in a boolean as FALSE/TRUE.** SQLite fixtures use
  sentinel zeros; PostgreSQL enforces FKs and real booleans. Narrow to columns whose schema says the conversion is safe.
- **D-129 — the audit page accepts both a bare array and a page envelope for `/audit/actions`.** Step 6 wrapped the
  vocabulary in `page_json`; the client must not blank the filter because a collection convention changed.
- **D-130 — `can_undo` is computed by the audit handler from the caller's role, not hardcoded false in the store.** The
  read is still public; only the offer of a write is gated, and only on an un-reversed `arena.remove` row.

- **D-131 — `ConcordApi.list()` normalizes both `{items,next}` and `{entries}` page envelopes to rows.** Step 6 changed
  collection shapes; a client that only needs rows must not render an error from a valid response. Callers that need totals
  keep the page object.
- **D-132 — the project-panel verdict fixture stores a durable `concord.user` record.** A script-readable token is deleted by
  `session.js`; without the user record the browser is signed out even though its navigation cookie resolves.

- **D-133 — the duplicates fixture seeds its sign-in before the first navigation.** `session.js` deletes a
  script-readable token and relies on the durable user record; a token written after the first `goto` is gone by the time
  the project page loads, so the filing forms never render. Cookie, token and user record must precede navigation.

- **D-134 — board and feature collection reads also normalize page envelopes.** The shared `ConcordApi.list()` is now
  used by `board.js` and `feature.js` for `/features`, `/complaints` and `/milestones`; `Array.isArray` guards were the
  original symptom of the Step-6 shape change, not a reason to weaken the pages.
- **D-135 — the board and milestones fixtures seed the durable user record before navigation.** Same signing contract as
  D-132/D-133: cookie + token + user record precede the first `goto`, or write controls are hidden.

- **D-136 — the feature tags fixture also needs the durable user record.** Every suite that writes from a browser
  page seeds token, navigation cookie and `concord.user` before the first load; `session.js` deletes the token and uses
  the record as its only durable sign-in signal.

- **D-137 — the ranking page and its fixture follow the same two contracts as the other wired suites.** `ranking.js`
  normalizes `/priorities` and `/tallies` with `ConcordApi.list()`, and `_page()` seeds the navigation cookie, token and
  durable user record before the first `goto`.

- **D-138 — `project.js` flattens the render fragments before `replaceChildren`.** `ConcordProjectRender.render()`
  returns section fragments as nested arrays; spreading them directly into `replaceChildren` stringifies each array
  into `[object HTMLDivElement]`, which silently hid the entire Requests section and its links.
- **D-139 — `requests.js` normalizes the catalogue response with `ConcordApi.list()`.** The project lookup for answer
  headings uses `/api/v1/projects`, which answers the Step-6 catalogue envelope; without unwrapping, every answer
  fell back to `Project <id>` instead of a followable named link.

- **D-140 — the audit filter vocabulary uses `ConcordApi.list()`.** Its hand-written unwrap knew only the legacy
  `{entries}` shape; Step 6's `/audit/actions` now answers `{items,next}`, so the action select stayed empty. One shared
  helper now covers bare arrays, audit pages and Step-6 pages.

- **D-141 — the e2e shim translates SQLite cursor, float epochs and real primary keys.** Older suites call
  `sqlite3.connect()` and `con.cursor()`, bind `time.time()` floats into `timestamptz`, and use `INSERT OR REPLACE`
  on tables whose key is not `id`. The adapter now covers all three.
- **D-142 — an arena's label lookup maps contest type to entry entity type.** `alternatives`/`use-case` compare
  projects, `feature-priority` compares features; passing the arena type straight to `label_of` produced `entity N`.
- **D-143 — `arena_e2e` is not wired yet.** Setup errors are gone and 4 of 5 tests pass; the remaining vote-application
  failure is a product defect, so the ratchet stays at 15.

- **D-144 — wait for the arena vote response before the reload.** `arena_e2e` clicked submit and then waited for
  `load`; the reload could resolve on the old document and navigate away before the POST finished. The test now
  asserts the `200` response first. With that, `arena_e2e` is `5 passed in 21.49s` and is wired; ratchet 15 → 14.

- **D-145 — `make -o release deploy` skips the build, so it can reinstall a stale binary.** It exited `0` while live
  version stayed `94ea2c0`. Always run `make release` first, then `make -o release deploy`, and assert `/api/v1/version`.
- **D-146 — `concord-rs.service` is a user unit.** `systemctl is-active concord-rs.service` reports "could not be found";
  the real check is `systemctl --user is-active concord-rs.service`. `concord.service` must stay inactive.

- **D-147 — quoted SQLite identifiers are normalized before catalogue lookups.** `"values"` was treated as missing
  and appended a second time; finder/scout no longer error in setup.
- **D-148 — no-parameter SQL is executed without a params tuple.** psycopg2 treats a literal `%` in `LIKE 'prefix%'`
  as a placeholder when any params are supplied.
- **D-149 — the seed cursor is iterable.** `for row in con.execute(...)` is ordinary sqlite3 usage and now works.
- **D-150 — `feeds_e2e` is wired.** `4 passed in 3.69s`; ratchet 14 → 13.

- **D-152 — `settings_themes_e2e` is not an envelope bug.** `GET /strategy/theme-proposals` already answers a bare
  array. Wrapping it in `ConcordApi.list()` left the suite at `3 failed in 52.93s`; the change was reverted. The first
  test creates its proposal through the form, so an empty table before submit is expected. Next: inspect the form POST.

- **D-153 — strategy write routes resolve project slugs.** Six handlers took `Path<i64>` while the client builds URLs
  from the slug; `POST /strategy/themes` answered `400 Invalid URL: Cannot parse `e2e-themes` to a `i64``. They now
  use the same `project_by_id` resolver as the read routes. `settings_themes_e2e` went from `3 failed` to
  `1 passed / 2 failed in 29.47s`; suite not wired yet.

- **D-154 — settings first paint is not a `signedIn` race.** Using the API token as an additional signed-in signal
  left `settings_themes_e2e` at `1 passed / 2 failed in 44.79s`; reverted. Debug proof: no JS errors, page-context GET
  returns the seeded `battery-life` proposal, but `[data-theme-proposals]` is absent. The remaining defect is in the
  settings render/state path, not in auth detection or collection shape.

- **D-155 — first paint reads the stored token directly.** Instrumentation showed `s.themeProposals === null`
  because `ConcordSession` initialises after `settings.js` first `load()`. Reading `concord.token` /
  `concord.user` from storage makes `mine()` fetch private collections on first paint.
  `settings_themes_e2e` went `1 passed / 2 failed` -> `2 passed / 1 failed in 35.00s`; not wired yet.

- **D-156 — consent eligibility is a server-state discrepancy, not a fixture gap.** The browser POST is
  `403 /strategy/themes/1/consent`; the same seed's `merge_requests` are merged, memberships are valid, and the
  replicated `eligible_collaborators` query returns `[1, 2, 3]`. Next: log `who.user.id`, `pending.project_id` and
  `eligible_ids` inside `themes::consent` on a candidate build. No speculative fixture rewrite.

- **D-157 — theme fixture needs a durable owner record.** `session.js` replaced the fixture token with a guest
  identity (`id 4`), so consent was made as a guest and refused. Seeding `concord.user` with the owner record fixed
  the consent step; `settings_themes_e2e` still `2 passed / 1 failed in 49.85s` because the ratified theme row is
  absent after reload even though `GET /strategy/themes` returns it.

- **D-158 — `settings_themes_e2e` is order-dependent.** With the candidate alone the consent/ratification test
  passes (`1 passed in 72.78s`, `DEBUG_THEMESLEN 1`, `DEBUG_TABLE YES`); in the full suite it fails at the same
  post-reload assertion (`2 passed / 1 failed in 76.67s`). Do not wire it until the shared-seed contamination is
  identified. Instrumentation was removed.

- **D-159 — the consent test selected the wrong pending proposal.** Test 1 leaves a pending proposal for another tag;
  test 2's `SELECT id FROM strategic_theme_proposals WHERE status='pending'` picked that row and ratified the wrong
  theme. Selecting by `theme_tag=MUTED_TAG` makes the suite order-independent: `3 passed in 35.48s`. D-158 resolved;
  ratchet 13 → 12.

- **D-160 — complaints are a second work-item kind.** `work_items()` previously returned only features, so the panel could
  never render an open complaint as "needs validating". It now unions complaints with an empty size and no claim.
- **D-161 — a draft is not work.** Draft features are excluded from the work panel: nobody has agreed to do them, so they
  must not be offered as pick-up-able tasks.
- **D-162 — work-panel order.** Complaints lead (validation is the cheapest real contribution), then sized features before
  unsized ones, then id. The old id-only order contradicted the panel's own documentation.
- **D-163 — workitems fixture needs a durable user.** A token alone is replaced by a guest identity; seed `concord.user`
  like the other signed-in suites so claims and sizing controls appear.

- **D-164 — the unread badge is server-rendered.** `render_with_unread()` paints `data-unread-count` into the nav from the same
  store count the API returns, on `/projects` and `/notifications`. Zero renders no badge.
- **D-165 — `/notifications` is a real inbox page.** It mounts `notifications-root` and `notifications.js` for a signed-in reader
  and says "sign in" for everyone else; looking never marks a row read.
- **D-166 — the inbox fixture needs a durable user.** Without `concord.user`, session.js mints a guest whose bearer shadows the
  navigation cookie on POST, so "mark read" 404s on the guest's own inbox.

- **D-167 — auth pages use the script's own field contract.** `/login` and `/register` now emit `login-form`/`login-*`
  and `register-form`/`reg-*` and load `auth.js`; the old IDs made the real form inert.
- **D-168 — a guest token bridges navigation in `sessionStorage`.** A guest has no HttpOnly cookie, so the bearer must survive
  the trip to `/register` for in-place promotion. Login tokens remain in memory only because their cookie is durable.

- **D-169 — the session-storage probe needed `sessionStorage`.** The fake browser had no such object, so the constructor's catch
  cleared every reloaded session after the guest-token bridge. Gate `44642556287` failed at that arm; adding the storage to the shim
  restores `session storage: PASS — no token in localStorage, reload stays signed in, no bearer header without a token`.

- **D-170 — `/taxonomy` mounts its interactive panel.** The server-rendered tag list alone left no proposal/consent surface;
  the page now mounts `taxonomy-root`, `taxonomy-live`, and `taxonomy.js`.
- **D-171 — taxonomy.js no longer calls an undefined `token()`.** The proposer check resolves the current user id at render time.
- **D-172 — a 202 consent is not a ratification.** The store returns the still-pending proposal and a count in `message`; the page
  previously read a nonexistent `ratified` flag and told the reader the change had been applied.
- **D-173 — taxonomy fixtures pass the navigation cookie and clear the stale guest bearer, matching the other signed-in suites.**

- **D-174 — merge rows are unwrapped at the client boundary.** The API serves `{request, gate}` per merge request; the page expected
  a flat row, so title/status/feature_id were blank and the create form never mounted. `ConcordApi.list()` plus a request/gate flatten
  fixes both.
- **D-175 — merge fixture needs a durable user.** Without `concord.user`, `session.js` reports signed out and hides Confirm/Merge/Reject.

- **D-176 — the merge structural probe needed the real API shape.** It supplied flat rows and no `ConcordApi.list()`, so after the
  request/gate unwrap the gate reported "the merge row disappeared". Gate `44657400518` failed at that arm; the shim now supplies
  `{request, gate}` rows and `list()`. Local arm: `dom scout+merge pages: PASS — hostile fields are text, gates and counts verbatim, no string sink`.

- **D-177 — `/curation` mounts its interactive queue.** The public server notice alone offered no filters, consent or undo; the page now
  mounts `curation-root`, the filters, live list and `curation.js`.
- **D-178 — `can_vote` is now derived per viewer.** It was hard-coded `false`, so no control could ever appear. Proposals use their own
  eligible sets; acts use the moderation door. D-179 covers the signed-in fixture's navigation cookie.
- **D-180 — below-quorum consent is a 202, not a 409.** The old conflict body carried the success sentence, so the client treated a
  recorded vote as a failure and never refreshed. The 202 carries the server's verdict and count.
- **D-181 — the queue defaults to `open`.** Undone acts belong to the log; the page sends `state=all` when the reader opens it.

- **D-182 — `/projects/{slug}/lists/{id}` mounts `list.js`.** The server-rendered entries alone gave no ratings, bands,
  categories or comparison controls; the page now mounts `list-root` and the script.
- **D-183 — an unknown list is a page, not a bare 404.** The reader is told "There is no list at this address" inside the same
  mount, so a stale link still lands somewhere that explains itself.

- **D-184 — Join uses `signedIn()`, not a bearer header.** Cookie-authenticated readers were told they had no way to join.
- **D-185 — objection verdicts accept the panel's statuses.** The panel offered resolved/overridden/vetoed/withdrawn while the store
  accepted only upheld/withdrawn/answered; all four are now accepted, with the legacy two retained.
- **D-186 — an objector may withdraw their own objection.** The four verdicts remain maintainer-only; the refusal now names the role.
- **D-187 — coverage `contested` is a bigint count, not a bool.** Decoding it as bool made every feature page answer 500.
- **D-188 — the settings panel normalises the paged features read.** The propose form had no feature to select.
- **D-189 — proposing a strategic weight requires a contributor role.** The handler's own comment claimed a gate it never had.
- **D-190 — the settings DOM probe must serve the paged features shape and `ConcordApi.list`.** A fake API left at the old `get`-only surface turned a green suite into a structural gate failure.

- **D-191 — the three strategy GETs are public; their writes are not.** `GET /strategy/weight-proposals`, `/strategy/themes` and `/strategy/theme-proposals` now carry `Policy::Optional`, because §6.2 puts a proposed change on the public record and the e2e reader asserts an anonymous visitor sees it. The consent/propose/withdraw POSTs stay `Session`. The policy ratchet drops `96 -> 93`; every other enforced route still refuses a stranger. The pinned-handler list drops the three readers because they no longer take `Extension<User>`.

- **D-192 — the settings shell uses the visibility-aware page resolver.** `project_of` is public-only, so a member with a valid `concord_nav` cookie still got a 404 for a protected project's settings. The page now resolves the project and checks `readable_for` with the navigation reader, as the other subpages do.
- **D-193 — settings fixture DB rows are dict-like.** `dict(fetchall())` on two `sqlite3.Row`-ish records produced `{'language': 'pct'}`; building the map from `r["language"]`/`r["pct"]` reads the values the test actually asserts.

- **D-194 — the criteria page mounts its interactive shell.** The static table had no `#criteria-root`, `#criteria-notice` or `criteria.js`; all 13 tests failed on the missing `.crit-table`. Both page branches now mount them.
- **D-195 — composite rank uses the numeric project id.** `/criteria`, `/priorities` and `/criteria-profiles` accept a slug; `POST /rank/composite` only accepts an id. The client resolves the id once from `GET /projects/{slug}` and keeps the two surfaces distinct.
- **D-196 — `/priorities` is paged.** The criteria page called `.map()` on the page envelope; `ConcordApi.list()` unwraps both shapes.
- **D-197 — seed `concord.user` before navigation.** A token alone is not a signed-in browser; seeding after load loses a race with `/guest`, which mints a guest cookie and demotes the actor.
- **D-198 — guests may vote in public comparisons.** A guest is an actor in a public comparison, not a trespasser. The contributor-role gate still applies to account holders, writes to criteria still need maintainer.

- **D-199 — the criteria DOM probe serves the shipped client surface.** The fake `ConcordApi` lacked `list()` and a numeric project id, so the page died before rendering hostile text.

- **D-200 — the finder page supplies the wizard's static shell.** The page described the flow but mounted only an empty `finder-root`; `finder.js` needs the full set of IDs the DOM probe asserts. The shell now ships those IDs and the script tag.
- **D-201 — `finder-stop` is a button, and skip is a real control.** A wrapper `<div>` let a Playwright click land on an empty child with no handler; the stop control is now a `<button>` and the skip mode has its own button.

- **D-202 - keep the finder page's session sentence.** The static shell replaced the copy the page probe asserts; the sentence and the interactive mount must coexist.

- **D-203 — the scout page supplies the interactive shell and closes its static textarea.** The old form emitted `<textarea>` as a void element, so the browser swallowed the mounted shell as text. The tag is now closed and the report slots ship with the page.
- **D-204 — scout reads the parsed API body.** `ConcordApi.get` resolves JSON and rejects non-2xx; scout.js still called `.text()`/`.ok` on a raw `Response`, so every report stayed hidden.
- **D-205 — the save fixture seeds a full signed-in browser.** A register token alone left `concord.user` unset and the save ran as a guest; the fixture now sets the navigation cookie and user before navigation.

- **D-206 — the scout DOM shim resolves parsed JSON.** The fake `ConcordApi.get` still returned a raw `Response`, so the capability row vanished after the shipped client moved to the shared parsed-body contract.

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

## Frontend visual pass — measured proof (2026-11-18)

Three guarded code commits, all live on `concord-rs.service` (`concord.service` inactive):

- `db1a122` — Go landing composition ported above the catalogue; `project-render.js` mutant token removed (D-74/D-75). `git show --stat`: 7 files changed, 135 insertions(+), 5 deletions(-), `lib.rs` +54.
- `83799ae` — vision-driven polish: duplicate hero brand removed, balanced headline, six `data-icon` tiles, corrected Features link, flip-band CTA. 4 files changed, 45 insertions(+), 8 deletions(-).
- `e2b29bf` — final CSS alignment: Explore hints all at one offset, dark-band CTA undecorated, long catalogue names wrap. 3 files changed, 20 insertions(+).

```
cargo check -p concord-app --locked: Finished `dev` profile
cargo test -p concord-web: 3 passed; 0 failed
cargo test -p concord-app --lib policy: 7 passed; 0 failed
route_policies: PASS — 46 modules, 0 unmerged, 203 declared
gate e2b29bf: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
live e2b29bf: /version equality True · / 200 · /projects/suki 200
browser 1280×900: hero 1 · decision cards 6 · stats 73/40/10 · aria-busy false · Explore cards 4 · project-list 1
browser 390×844: scrollWidth 390 == clientWidth 390 (no horizontal overflow)
browser project page: errors [] · "Could not load project" false · panels 664
CSS alignment: Explore hint offsets [1725,1725,1725,1725] · flip CTA text-decoration none
vision verdict: "coherent and launch-quality"; remaining badge inconsistency recorded as a data decision
CI 44330323408 success (gate, port-rust)
```

### Fifty-sixth move live proof (771c54b, api/milestones.rs complete)

`git show --stat 771c54b`: api/milestones.rs 33 changed, lib.rs 26 removed, lib.rs 1168 -> 1142.
No route edit: the GET already shared the MethodRouter with the six writes (D-48).

```
cargo check -p concord-app --locked: Finished `dev` profile
cargo test -p concord-app --lib policy: 7 passed; 0 failed
route_policies: PASS — 46 modules, 0 unmerged, 203 declared
parity replay: 0 shape(s) disagreed of 16 checked, 5 skipped
get_milestone_bytes_identical: True · sha256 5d1e82d84800e5bf
gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
live 771c54b: /version equality True · milestones list 200 (0 on suki) · /milestones/abc 400 handler envelope
anonymous_walk: 0 holes · 0 declared but not enforced · PASS
probe_security_headers: PASS · concord-rs active · concord inactive
CI 44335023821 success
```

### Fifty-seventh move live proof (671fbd5, api/lists.rs complete)

`git show --stat 671fbd5`: api/lists.rs 80 changed, lib.rs 68 removed, lib.rs 1142 -> 1074.
Two root GET stanzas moved with the handlers; the entries route was already module-owned.

```
cargo check -p concord-app --locked: Finished `dev` profile
cargo test -p concord-app --lib policy: 7 passed; 0 failed
route_policies: PASS — 46 modules, 0 unmerged, 203 declared
parity replay: 0 shape(s) disagreed of 18 checked, 3 skipped
list_readers_bytes_identical: True · sha256 fd38656b5025091d
gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
live 671fbd5: /version equality True · /lists/2 200 · /lists/2/ 200 · /lists/2/entries 200 · /lists/abc 400
anonymous_walk: 0 holes · 0 declared but not enforced · PASS
probe_security_headers: PASS · concord-rs active · concord inactive
CI 44341220717 success
```

### Fifty-eighth move live proof (d5e3a5d, feature tallies)

`git show --stat d5e3a5d`: api/arena_votes.rs 29 changed, lib.rs 22 removed, lib.rs 1074 -> 1052.
The module was already mounted; only its route table gained `/tallies`.

```
cargo check -p concord-app --locked: Finished `dev` profile
cargo test -p concord-app --lib policy: 7 passed; 0 failed
route_policies: PASS — 46 modules, 0 unmerged, 203 declared
parity replay: 0 shape(s) disagreed of 16 checked, 5 skipped
feature_tallies_bytes_identical: True · sha256 0932cc3514cc7c11
gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
live d5e3a5d: /version equality True · /projects/suki/tallies 200 with per-feature rows
anonymous_walk: 0 holes · 0 declared but not enforced · PASS
probe_security_headers: PASS · concord-rs active · concord inactive
CI 44348001599 success
```

### Fifty-ninth move live proof (6df6012, composite rank)

`git show --stat 6df6012`: api/solutions.rs 108 changed, lib.rs 105 removed, lib.rs 1052 -> 947.
The shared `STANDINGS_PER_FEATURE` constant stayed at the crate root (D-65).

```
cargo check -p concord-app --locked: Finished `dev` profile
cargo test -p concord-app --lib policy: 7 passed; 0 failed
route_policies: PASS — 46 modules, 0 unmerged, 203 declared
parity replay: 0 shape(s) disagreed of 18 checked, 3 skipped
composite_rank_bytes_identical: True · sha256 ab6ce7ddc1ddf566
gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
live 6df6012: /version equality True · POST /projects/1/rank/composite {} -> 200 {"count":0,"results":[]}
live: bad weight key -> 400 weights keys must be criterion ids: abc (handler envelope)
anonymous_walk: 0 holes · 0 declared but not enforced · PASS
probe_security_headers: PASS · concord-rs active · concord inactive
CI 44355093345 success
```

### Sixtieth move live proof (7d0bdfb, api/curation.rs)

`git show --stat 7d0bdfb`: new api/curation.rs 80 added, api/mod.rs 1 added, lib.rs 66 removed, lib.rs 947 -> 883.
The module was declared and mounted in the same edit (D-47).

```
cargo check -p concord-app --locked: Finished `dev` profile
cargo test -p concord-app --lib policy: 7 passed; 0 failed
route_policies: PASS — 47 modules, 0 unmerged, 203 declared
parity replay: 0 shape(s) disagreed of 16 checked, 5 skipped
list_curation_bytes_identical: True · sha256 41ea505c011f7690
gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
live 7d0bdfb: /version equality True · /api/v1/curation 200 · /api/v1/curation/ 200
anonymous_walk: 0 holes · 0 declared but not enforced · PASS
probe_security_headers: PASS · concord-rs active · concord inactive
CI 44360045399 success
```

### Sixty-first move live proof (0f26ed7, auth_me)

`git show --stat 0f26ed7`: api/identity.rs 18 changed, lib.rs 16 removed, lib.rs 883 -> 867.
The module was already mounted; its route table gained `/api/v1/auth/me`.

```
cargo check -p concord-app --locked: Finished `dev` profile
cargo test -p concord-app --lib policy: 7 passed; 0 failed
route_policies: PASS — 47 modules, 0 unmerged, 203 declared
parity replay: 0 shape(s) disagreed of 18 checked, 3 skipped
auth_me_bytes_identical: True · sha256 5aab90c9722369c8
gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
live 0f26ed7: /version equality True · anonymous auth/me 401 authentication required
live: generated guest bearer -> auth/me 200 with id, username, display_name, role member
anonymous_walk: 0 holes · 0 declared but not enforced · PASS
probe_security_headers: PASS · concord-rs active · concord inactive
CI 44366351396 success
```

### Sixty-second move live proof (a365e7e, api/capabilities.rs)

`git show --stat a365e7e`: new api/capabilities.rs 36 added, api/mod.rs 1 added, lib.rs 19 removed, lib.rs 867 -> 850.
The module was declared and mounted in the same edit (D-47).

```
cargo check -p concord-app --locked: Finished `dev` profile
cargo test -p concord-app --lib policy: 7 passed; 0 failed
route_policies: PASS — 48 modules, 0 unmerged, 203 declared
parity replay: 0 shape(s) disagreed of 16 checked, 5 skipped
list_capabilities_bytes_identical: True · sha256 ce470c3c7ab2d4f5
gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
live a365e7e: /version equality True · /projects/suki/capabilities 200 with asserted_count 0 and capability rows
anonymous_walk: 0 holes · 0 declared but not enforced · PASS
probe_security_headers: PASS · concord-rs active · concord inactive
CI 44370914143 success
```

### Sixty-third move live proof (bd6e92d, api/members.rs)

`git show --stat bd6e92d`: new api/members.rs 40 added, api/mod.rs 1 added, lib.rs 23 removed, lib.rs 850 -> 829.
The module was declared and mounted in the same edit (D-47).

```
cargo check -p concord-app --locked: Finished `dev` profile
cargo test -p concord-app --lib policy: 7 passed; 0 failed
route_policies: PASS — 49 modules, 0 unmerged, 203 declared
parity replay: 0 shape(s) disagreed of 18 checked, 3 skipped
list_members_bytes_identical: True · sha256 0c9ba0b414fcd9d5
gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
live bd6e92d: /version equality True · /projects/suki/members 200 with owner row
anonymous_walk: 0 holes · 0 declared but not enforced · PASS
probe_security_headers: PASS · concord-rs active · concord inactive
CI 44374727897 success
```

### Sixty-fourth move live proof (0a3f169, api/criteria.rs readers)

`git show --stat 0a3f169`: api/criteria.rs 40 changed, lib.rs 34 removed, lib.rs 829 -> 795.
No route stanza changed: both GET routes were already mounted in the module (D-48).

```
cargo check -p concord-app --locked: Finished `dev` profile
cargo test -p concord-app --lib policy: 7 passed; 0 failed
route_policies: PASS — 49 modules, 0 unmerged, 203 declared
parity replay: 0 shape(s) disagreed of 16 checked, 5 skipped
criteria_readers_bytes_identical: True · sha256 2b94019cb2229401
gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
live 0a3f169: /version equality True · /projects/suki/criteria 200 [] · /criteria/ 200 []
live: /projects/suki/criteria-profiles 200 [] · /criteria-profiles/ 200 []
anonymous_walk: 0 holes · 0 declared but not enforced · PASS
probe_security_headers: PASS · concord-rs active · concord inactive
CI 44378690776 success
```

The first probe guessed `/criteria/profiles`, which returned the generic `404 No such API route.`; the module's real
path is `criteria-profiles`. The corrected probe returns 200, proving the handler is mounted.

### Sixty-fifth move live proof (88436e8, settings and lists readers)

`git show --stat 88436e8`: api/project_settings.rs 22 changed, api/lists.rs 18 changed, lib.rs 33 removed,
lib.rs 795 -> 762. No route stanza changed: both GET routes were already mounted with the writes (D-48).

```
cargo check -p concord-app --locked: Finished `dev` profile
cargo test -p concord-app --lib policy: 7 passed; 0 failed
route_policies: PASS — 49 modules, 0 unmerged, 203 declared
parity replay: 0 shape(s) disagreed of 18 checked, 3 skipped
project_languages_bytes_identical: True · sha256 b95672d64a765348
project_lists_bytes_identical: True · sha256 57b34541c7181217
gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
live 88436e8: /version equality True · /projects/suki/languages 200 []
live: /projects/suki/lists 200 · /projects/suki/lists/ 200 with list rows
anonymous_walk: 0 holes · 0 declared but not enforced · PASS
probe_security_headers: PASS · concord-rs active · concord inactive
CI 44382982996 success
```

### Sixty-sixth move live proof (5ad4adc, project_tags)

`git show --stat 5ad4adc`: api/project_settings.rs 23 changed, lib.rs 19 removed, lib.rs 762 -> 743.
No route stanza changed: the GET route was already mounted with the tag write (D-48).

```
cargo check -p concord-app --locked: Finished `dev` profile
cargo test -p concord-app --lib policy: 7 passed; 0 failed
route_policies: PASS — 49 modules, 0 unmerged, 203 declared
parity replay: 0 shape(s) disagreed of 18 checked, 3 skipped
project_tags_bytes_identical: True · sha256 304061ad59b8d984
gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
live 5ad4adc: /version equality True · /projects/suki/tags 200 []
anonymous_walk: 0 holes · 0 declared but not enforced · PASS
probe_security_headers: PASS · concord-rs active · concord inactive
CI 44386811337 success
```

The trailing-slash probe `/projects/suki/tags/` returned the generic `404 No such API route.`; the route is declared
only at the canonical path, as it was before the move. This is not a regression.

### Step 3 complete (d444f48, root router owns no resource routes)

`git show --stat d444f48`: finder.rs 21 changed, feeds.rs 20 added, lib.rs 28 removed (743 -> 719), policy.rs and
route_policies.py extended for the two root modules. All 20 remaining route declarations moved into their existing
modules; `route_paths_identical: True`, `root_routes: 0`.

```
cargo check -p concord-app --locked: Finished `dev` profile
cargo test -p concord-app --lib policy: 7 passed; 0 failed
route_policies: PASS — 49 modules, 0 unmerged, 203 declared
parity replay: 0 shape(s) disagreed of 16 checked, 5 skipped
gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
live d444f48: /version equality True
live: /api/v1/finder/questions 200 · /feed.xml 200 · /projects/suki/feed.xml 200 · /projects/suki/board/atom.xml 200
anonymous_walk: 0 holes · 0 declared but not enforced · PASS
probe_security_headers: PASS · concord-rs active · concord inactive
CI 44392405151 success
```

D-89 also records the two gate-infrastructure fixes required by the move: the scanner and policy oracle had to read
`finder.rs` and `feeds.rs`, and a test-build move error (`read_dir(dir)` consumed `dir`) was fixed by capturing the
parent before the loop. Step 3 is now marked Done; what remains in `lib.rs` are shared helpers and AppState (D-41).


## Suggestions after row 10 — not committed work

These are the next highest-value items from the 34-route review. None is a roadmap row yet: row 10 closed the defects that were worth fixing now, and these are ordered options rather than promises. Pick the first one that can be finished this turn; record any decision in the Decisions section in the same commit.

1. **done at `728492f` — `signed_in_visual_e2e: 3 passed in 8.91s`; gate `44891512058 success in 2m57s`; live `c63f706`.** Minted a real account session over register + `/auth/me`, walked home, notifications, settings, board, rank and consensus at 1440px and 390px, and revoked the token. 12 readings, 12 screenshots, 0 gate copy, 0px overflow. Evidence: `docs/evidence/2026-11-21-signed-in-visual.json`; D-215/D-216.
2. **done at `b3af1ab` — `signed_in_visual_e2e: 4 passed in 21.32s`; gate `44893030252 success in 2m41s`; live `b3af1ab`.** Root cause was not the cookie path: personalized HTML had no `Cache-Control`, so a previously cached anonymous first paint could be reused after sign-in. D-217 adds `Cache-Control: no-store` to every server-rendered document. RED proved `cache-control None`; GREEN proved `no-store`.
3. **done at `e9e28bf` — `a11y_axe_e2e: 2 passed in 25.02s`; gate `44894559372 success in 2m52s`; live `e9e28bf`.** Vendored axe-core 4.10.2; canary `button-name` detected; 15 routes x 4 modes swept. Initial report had 8 findings: stagger animation in flight (probe timing) and one real `/lists` CTA violation (D-218/D-219).
4. **done at `14726d9` — `settings_fields_e2e: 4 passed in 12.60s`; gate `44896168591 success in 2m54s`; live `14726d9`.** Humanized labels, inline help for the four ranking fields, sticky Save/Reset and an honest dirty state; D-220.
5. **done at `6f136e4` — `taxonomy_clarity_e2e: 2 passed in 20.27s`; gate `44897561649 success in 3m24s`; live `6f136e4`.** Dedupe by `(namespace, visible name)`, show ratified counts, strip trailing id suffixes from labels and tooltips while keeping `data-tag` exact; D-221.
6. **done at `27a80f3` — `board_list_nav_e2e: 2 passed in 2.80s`; gate `44899810595 success in 3m10s`; live `27a80f3`.** Board title filter + 50/show-all cap; list dedupe by title, filter and 25-entry pagination preserving ranked order; D-222.
7. **done at `7ff7918` — `header_consent_e2e: 3 passed in 15.81s`; gate `44901072456 success in 2m49s`; live `7ff7918`.** Compact dismissible notice; Accept still records consent; 44px brand/nav targets within the old header height; D-223.
8. **done at `f015a7f` — `nav_footer_e2e: 2 passed in 13.82s`; gate `44902180234 success in 2m27s`; live `f015a7f`.** Three labelled nav groups with all nine original links and session hooks intact; grouped footer with 11 plain links; D-224.
9. Presentation polish: normalise dates and humanise `snake_case` statuses on `/stats` and `/documents`. Evidence: routes 11 and 24.
