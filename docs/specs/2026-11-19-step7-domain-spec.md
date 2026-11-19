# Step 7 — the domain crate stops being a comment

Date: 2026-11-19
Status: spec accepted; plan in `.hermes/plans/2026-11-19_040000-step7-domain.md`.

## Problem

Measured at `df43fd0`:
- `crates/domain/src/lib.rs` is one doc-comment line; `cargo test -p concord-domain --lib` → `running 0 tests`.
- `cargo tree -p concord-domain | grep -c sqlx` → `0` (the boundary is correct but empty).
- `grep -rn "sqlx::query" crates/db/src --include="*.rs" | wc -l` → `561`; the store owns the rules as SQL strings.
- Stage inference is pure in `concord_ranking::lifecycle::stage`, but the domain crate exposes none of it.
- Consensus counting is a loop inside `concord_db::consensus::tally`; claim eligibility is inline in
  `api/project_lifecycle.rs`. Neither can be tested without a database or an HTTP handler.

## Goals

1. Put three product rules in `concord-domain` as pure functions: stage inference, consensus counting, claim
   eligibility.
2. Give each rule exhaustive tests over its small input domain. D-120: exhaustive enumeration replaces `proptest`
   here — the domain is a handful of enum variants, so all cases can be listed, and no new dependency is needed.
3. Keep `concord-domain` free of `sqlx` (`cargo tree | grep -c sqlx` remains 0) and prove it in the gate.
4. Reuse the domain functions from the existing store/handler paths, so they are the rules the product actually runs,
   not a parallel implementation.
5. Turn on `sqlx::query!` with committed `.sqlx` for **new** store code only. Existing 561 dynamic queries stay as
   they are; converting them is a separate measurable migration.

## Non-goals

- Rewriting all 561 dynamic queries in this row.
- Moving `concord_ranking::lifecycle::stage` out of ranking. Domain re-exports the verdict under a domain name.
- Changing the wire shape of stage, tally or claim responses.
- Taking a database dependency in `concord-domain`.

## Decisions

- D-120: exhaustive tests over enum-sized inputs, not `proptest`, for the three rules.
- D-121: `concord-domain` depends on `concord-governance` and `concord-ranking` (both pure) and owns the exported
  rule names; `concord-db` calls into it, so there is one implementation of each rule.
- D-122: `sqlx::query!` is adopted for new store code with `cargo sqlx prepare`, not retrofitted across the existing
  dynamic queries in this row.

## Success criteria

1. `cargo test -p concord-domain --lib`: at least 3 rule tests, 0 failed, with explicit case counts in the log.
2. `cargo tree -p concord-domain | grep -c sqlx` → 0.
3. `cargo check -p concord-db --locked --offline` passes with no `DATABASE_URL` (the existing dynamic queries already
   compile offline; this proves the new domain boundary did not introduce a compile-time DB dependency).
4. Store/handler code calls the domain functions; a mutation replacing a call with a stale copy makes a targeted test
   fail.
5. Gate `PASS`, `pages: 132/132 ok`, stamped deploy, CI green.

## Order

1. Add row 7 and D-120..D-122; commit spec and plan.
2. Domain functions and exhaustive tests; RED baseline first.
3. Wire stage, tally and claim paths to the domain functions; mutation proof.
4. Add the no-sqlx assertion to the gate.
5. Gate, deploy, live probe, docs/vault/handoff.
