# ADR 0015 — a route declares who may call it, the declaration is checked against the source, and the gate enforces it

**Status:** accepted, 2026-11-15. Step 1 of `docs/plans/refactor-roadmap.md`.

## Problem

Authentication was a line a handler was supposed to contain. Measured on 2026-11-15: **122 `bearer_user(…)` call
sites** across 217 handlers and **no declaration anywhere** of which routes require an identity.

The failure mode is not stylistic. A route added without the line compiles, boots, answers, and nothing fails. The
only instrument that would notice is an e2e suite that happens to walk that route anonymously — and those suites
exist because somebody remembered to write them. `scripts/parity/route_readers.py` aimed the right *kind* of
instrument at the neighbouring question (does a page *call* a route); the dangerous question was the one it did not
ask.

## Decision

A table, a check that cannot drift from the build, and a gate. Each alone is insufficient.

1. **The declaration is a table.** `crates/app/src/policy.rs` holds
   `ROUTES: &[(&str /*method*/, &str /*path*/, Policy, bool /*enforced*/)]` — **203 rows**, one per registered
   (method, route) on the API surface. `Policy` is `Public | Optional | Session | Account | Operator`: `Optional` is
   the row that says "an identity narrows the answer, an anonymous caller is still answered", which is what the
   server-rendered pages rely on and what a binary public/not-public table gets wrong.
   The `bool` is not a second opinion: it says whether the **gate** is the thing refusing yet. **`enforced` is true on
   5 rows** and the test ratchets it upward only — `enforced >= ENFORCED` fails if a route that refused a stranger
   yesterday passes one today. Widening it is step 1b, one surface at a time, with the e2e suites watching, because
   moving a refusal in front of a handler changes the message and what the body costs.
2. **The check is a test in the same crate, not a script beside it.** `policy.rs`'s tests parse `router()` out of
   `lib.rs` with `include_str!("lib.rs")` — string literals and comments blanked first, parentheses counted, the path
   read from the raw text at the same offsets, which is the same walk `scripts/parity/route_policies.py` does so the
   two cannot quietly disagree — and they report three failures **by name**: a registered route with no row (the
   defect), a row with no route (a table that stops being read), and an **enforced count that fell**: `enforced >=
   ENFORCED` fails if a route that refused a stranger yesterday passes one today.
   `cargo test --workspace` — the unit layer of `make gate` — is the enforcement point, so there is exactly one
   authority. A **live** layer asks the same question of the running instance: `scripts/parity/anonymous_walk.py`
   walks the deployed routes anonymously and fails when a route declared to refuse a stranger answers it. A table
   nobody walks is a table that describes the code's shape, not the deployment's behaviour.
   `scripts/parity/route_policies.py` remains as the *derivation aid* that produced the first draft (reading 203
   handlers by hand is a day's work); it is wired to nothing.
3. **The gate enforces it before the handler runs.** `auth_gate` in `lib.rs` matches the request against the path
   **axum registered** (`MatchedPath`, so `{slug}` cannot be spoofed), and:
   * a page route (not `/api/…`) passes — pages are `concord_web`'s and decide their own visibility;
   * `Public` and `Optional` rows pass, and an unenforced row passes whatever it declares (see `enforced` above);
   * an enforced row that requires an identity refuses an anonymous caller with
     `ApiError::unauthenticated("authentication required")` — the same envelope and message the handlers use;
   * **a matched route with no row is `500`, not "no check"**: a route nobody declared is a deployment bug.
   * a path axum never matched keeps its **404** — a missing declaration must not turn every unknown URL into a 500.

The gate sits *inside* the interaction gate, so an anonymous mutation still receives the consent refusal it receives
today and then the auth refusal, in that order.

## What the audit found once it was honest

Two defects in the check itself, both found by running it rather than by reading it:

* **Chained methods were invisible.** `registered_routes` sliced past `.route(` before counting parentheses, so the
  count started at the first *nested* call: `get(x).post(y)` was read as `get(x)`. **38 mutating routes had no
  detected method at all**, which meant the check could pass while a `POST` had no row — an instrument that reports
  PASS while blind. Fixed by keeping the opening paren in the slice.
* **Two routes had no row**: `GET`/`POST /api/v1/projects/{project_id}/threads/{thread_kind}/{thread_id}`. The fixed
  parser named them, and they were classified from their handlers (`GET` public, `POST` session).

That is the argument for the check being a test that runs on every commit rather than a document that was true once.

## What the first measured walk changed about the design

The derivation said **120** routes need a session. That number was a reading of the code's shape, so it was measured
against the live instance before anything enforced it: the 34 routes whose handlers *looked* like they required an
identity were called with no token on 2026-11-16, and **22 answered `200`** — `/api/v1/search`, `projects/{slug}/tags`,
`{project_id}/lists`, `documents/kinds`. Shipping that table as enforcement would have 401'd the project page's own reads.

Two consequences, both now in the code:

* `enforced` stopped being an implementation detail and became the design: 5 rows, chosen because the measurement was
  unambiguous — four that already answered `401` (`auth/me`, `notifications`, `projects/{slug}/invites`,
  `projects/{slug}/votes/next`) and **one hole**, `GET /api/v1/admin-ledger`, which answered `200` to an anonymous
  caller: the only one of 216.
* The three `strategy/*` routes declared `Optional` answered `401`, so the table was wrong in the direction that bites
  readers; they are `Session` now, with the measurement recorded as the reason in the derivation's exceptions.

`scripts/parity/anonymous_walk.py` is that measurement as a command, and `gate.sh --live` runs it: it walks every GET
row with no token and fails when an enforced row answers a stranger or a `Public` row refuses one. First honest run:
`1 hole(s) · 19 declared but not enforced · 0 wrongly refused`.

## What is deliberately not in this change

The gate enforces **identity**, not depth. `Member`, `Readable`, `Maintainer`, `Role` and `Operator` are recorded
intent that handlers still enforce themselves, because moving those refusals from *after* the body is parsed to
*before* it is a behaviour change that wants the 29 e2e suites watching. That is step 1b, and the table is what makes
it a mechanical rewrite onto an `Actor` extractor instead of a re-derivation of who may do what.

## Rejected alternatives

* **An extractor only** (`Authz<Policy>` as a handler argument, no table). Compile-time shape is stronger than a
  runtime lookup, and the route-to-policy mapping would stay implicit in each signature — the table is what makes
  "which routes require a session" a question a *test* can answer. Step 1b adds the extractor on top of this table.
* **Require a session on everything except a hard-coded public list.** Same inversion, but a list edited in code is a
  list edited in a hurry, while a table row is checked by a test that names the missing route.
* **Derive the policy at runtime from whether the handler calls `bearer_user`.** That is what the table was derived
  *from*, once, with review. Doing it at boot would make "this route is public" an accident of how a body is written,
  which is the disease being cured.
* **A Python checker in `make gate` beside the Rust test.** Two instruments for one property is two answers to
  disagree about; the in-crate test cannot drift from the code it reads.

## Consequences

* Adding a route without a row now fails `cargo test` (and therefore `make gate` and CI) with the route named.
* The 122 in-handler checks are redundant for identity and are **not yet removed**: step 1b removes them together
  with the guest/role depth, in one pass, with the suites as the oracle.
* A route that changes its path must change its row; the checker names both directions.
