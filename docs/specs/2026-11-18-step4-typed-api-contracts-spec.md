# Step 4 — typed API contracts for the routes the UI calls

Date: 2026-11-18
Status: spec accepted for implementation; plan in `.hermes/plans/2026-11-18_221000-step4-typed-api-contracts.md`.

## Problem

Step 3 moved 203 route declarations out of `lib.rs` without changing behaviour. The contract is still
discovered at runtime: 81 `Json<Value>` handlers now live across `crates/app/src/api/*.rs` and one remains in
`lib.rs`; 152 `json!` sites construct responses by hand. The browser client and the parity corpus know the
shape only by example. A field can be dropped and every gate stays green until a reader sees the page.

The roadmap's original count (`82 Json<Value>` in `lib.rs`) is stale after Step 3. Measured on `25dc520`:
`grep -c 'Json<Value>' crates/app/src/lib.rs` → `1`; the API modules hold `81`. The roadmap section is
corrected in the same commit as this spec.

## Goals

1. Give the 20 routes recorded in `crates/parity/corpus.json` typed `Serialize + ToSchema` response structs.
   The corpus keys are the contract inventory: `/healthz`, `/version`, `/projects`, `/search`, `/stats`,
   `/stats/history`, `/stats/responsiveness`, `/stats/summary`, `/notifications`, `/curation`, plus the
   ten `/projects/concord/...` detail routes and `/projects/concord/features/:id`.
2. Emit a checked-in `docs/contracts/openapi.json` from `utoipa` in CI. A generated contract that is never
   committed is a second source of truth nobody reads.
3. Add `scripts/parity/openapi_diff.py`, the missing verification command named in the roadmap. It fails on a
   removed path, a removed response field, a narrowed enum, or a changed scalar type, and requires an explicit
   version bump recorded in `docs/contracts/VERSION` for any breaking change.
4. Keep every wire byte and every gate line unchanged. The new types describe the existing contract; they do
   not redesign it.

## Non-goals

- Converting the other ~154 routes. They convert as they are touched.
- Changing any response shape, status, error text, pagination rule or auth gate.
- Generating the browser client from OpenAPI in this step. The client consumes typed JSON but stays handwritten
  until the generated contract has been proven stable; generation is a later row.
- Renaming taxonomy tags, health scores, or any other owner-visible data. The visual pass recorded those as data
  issues, not contract changes.

## Constraints and decisions

- D-95: adopt `utoipa` 6 because the roadmap already names it, it is on crates.io (`cargo search utoipa` →
  `utoipa = "6.0.0"`), and no vendored crate in this workspace generates an OpenAPI document. The rejected
  alternative is hand-maintaining `openapi.json`: that is the same drift the types are meant to remove.
- D-96: the first slice types `/api/v1/healthz` and `/api/v1/version` only. They are the two routes every probe
  already reads, so a wire difference is caught by the existing corpus and gate. Larger list/aggregate routes
  follow one at a time.
- D-97: the generated document is checked in at `docs/contracts/openapi.json` and compared by a script; CI does
  not write to the working tree. This keeps the contract reviewable in a diff.
- D-98: `openapi_diff.py` treats a smaller schema as breaking and a larger one as additive. A scalar type change,
  removed `required` entry or removed path is breaking. Unknown is never zero: a missing baseline is a failure,
  not an empty contract.

## Success criteria (each with its command)

1. `grep -c 'Json<Value>' crates/app/src/api/meta.rs` → `0`.
2. `cargo test -p concord-app --lib openapi` → at least 1 passed; 0 failed.
3. `python3 scripts/parity/openapi_diff.py --base docs/contracts/openapi.json` → `openapi_diff: PASS` with the
   checked-in file unchanged.
4. Corpus replay: `python3 scripts/parity/replay.py` (or the gate's replay step) reports `0 shape(s) disagreed`;
   the live bodies of `/api/v1/healthz` and `/api/v1/version` remain byte-identical in shape.
5. `make gate` → `gate: PASS` and pages `132/132 ok`; CI green on the pushed head.
6. Live `/api/v1/version` equality True after `make -o release deploy`; `concord-rs.service` active,
   `concord.service` inactive.

## Order

1. Correct the roadmap's stale `82 → 62` count to `81 across api/ + 1 root` and add a Step 4 row.
2. Add `utoipa` and the schema types for the first two routes; generate and check in `openapi.json`.
3. Add `openapi_diff.py` and prove it can go RED on a removed field (mutation gate).
4. Continue one route group at a time through the corpus inventory, regenerating the contract after each.
5. After all 20 routes are typed, flip the browser client generation row in the roadmap.
