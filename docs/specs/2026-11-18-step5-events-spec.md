# Step 5 — events, and an audit trail that cannot forget

Date: 2026-11-18
Status: spec accepted; plan in `.hermes/plans/2026-11-18_235500-step5-events.md`.

## Problem

The product claims "measured, not claimed", but the record of who changed what is assembled by each handler:
`write_audit()` in `crates/app/src/lib.rs` and `feature_writes::add_audit()` are called after the state change,
outside its transaction. A handler that forgets the call produces an audit page that looks complete. The roadmap
names six write paths that matter: archive, card move, vote, claim, feature state, document edit.

Measured baseline on `436f476`:
- `cargo test -p concord-db --test store -- events`: `running 0 tests`, `0 passed; 29 filtered out`.
- `grep -c "create table events" crates/db/sql/*.sql`: `0`.
- `grep -rn "INSERT INTO audit\|audit(" crates/app/src crates/db/src --include="*.rs"` finds hand-written calls in
  at least 20 modules; each is a place where the record can be skipped.

## Goals

1. Add an append-only `events` table in migration `0003`, with an `actor_id`, `project_id`, `kind`, `entity`,
   `entity_id`, `detail` and server timestamp. No `UPDATE` or `DELETE` path exists in application code.
2. For the six named write paths, write exactly one event **in the same transaction** as the state change. If the
   event insert fails, the state change rolls back.
3. Make the audit page read events (or a projection of them), so a missing event is a missing audit row, not a
   row a reader has to infer from an unrelated table.
4. Add a named test for each of the six paths and a negative mutation proof: removing the event write makes the
   path's test fail, not silently pass.

## Non-goals

- Migrating all audit writes at once. The remaining write paths convert as they are touched.
- Changing the public audit page shape in this slice. The projection keeps the existing columns and ordering until
  the event-backed read is proven.
- Rewriting `/feeds` and notifications in this slice. They move to projections after the event table is proven.
- Any deletion or backfill of `audit_log`. It stays the legacy record until the new read is verified.

## Decisions

- D-106: `events` is append-only by convention and by grant. The migration creates no update/delete trigger in the
  first slice because the application has no such statement; a future grant change is the enforcement step.
- D-107: the first slice is the card-move path. It has a store-level helper with existing tests, so the same
  transaction can be proven against PostgreSQL without a browser or a credential.
- D-108: keep `projects::move_card(pool, …)` as the compatibility wrapper for the existing 7 store tests. Add
  `move_card_with_event(pool, …, actor_id)` as the event-backed path the HTTP handler calls. Internal helpers take
  `&mut PgConnection`, which implements `Executor`, so one transaction covers the update and the event.
- D-109: the event kind for a card move is `card.move` and its entity is `board_card`; entity id is the card id.
  The detail is the destination phase, because that is the fact a reader asks the audit page about.

## Success criteria

1. `cargo test -p concord-db --test store -- events` → at least 1 passed; 0 failed.
2. A negative mutation removing the event write from `move_card_with_event` makes that test fail; restoring it passes.
3. `psql "$DATABASE_URL" -c "select kind, count(*) from events group by 1 order by 2 desc"` shows `card.move`
   after the live probe.
4. `cargo test -p concord-app --lib policy` → 7 passed; 0 failed; gate `PASS`, pages `132/132 ok`.
5. Live: board move still answers the same status/shape; `/version` stamped with the new sha; anonymous walk 0 holes.

## Order

1. Add row 5 to the roadmap with baseline and D-106..D-109.
2. Migration `0003_events.sql` + `migrate.rs` registration + `concord_db::events::append`.
3. Refactor board helpers to `&mut PgConnection`; add `move_card_with_event`; wire the HTTP handler.
4. Store test proving one event per move, plus RED mutation.
5. Gate, deploy, live probe, docs/handoff/vault updates.
6. Repeat one path at a time: archive, vote, claim, feature state, document edit.
