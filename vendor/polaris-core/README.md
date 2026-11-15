# polaris-core

The Polaris estate's shared HTTP contract, as Rust.

Right now that is one thing: the error envelope every `/api/**` response uses, exactly as
`estate/contracts/error.schema.json` declares it.

```json
{ "error": { "code": "not_found", "message": "no API route for /api/v1/x" } }
```

```rust
use polaris_core::ApiError;

async fn handler() -> ApiError {
    ApiError::not_found("no such post")
}

// A refusal that must say when to come back — the contract requires `details` for 429:
ApiError::rate_limited("vote budget spent", 3600)

// Domain detail goes under `details`, never beside `code`:
ApiError::forbidden("instance full").with_details(serde_json::json!({ "reason": "manual_cap" }))
```

## Why there is no `ApiError::of(code, message)`

`rate_limited` is the one code the schema constrains further — it must carry `details`. A general
constructor would be a public way to break the contract, so the public surface is one named
constructor per code plus `rate_limited(message, retry_after_secs)`. The cost is a fourteen-arm match
at the translation boundary in each product; that match is also the only place where a product's own
vocabulary meets the estate's, which is where you want to look when the two disagree.

## The tests are the contract, not a copy of it

`tests/contract.rs` validates every code against `tests/fixtures/error.schema.json`, which is a
byte-identical copy of the estate's file (`sha256 e657a237…`). One test compares the crate's code
list to the schema's enum, so a contract change that nobody carried here fails the crate rather than a
client. Another test asserts the schema rejects an extra key — otherwise "conforms" would be
decoration.

Mutation proof, run when the crate was written: removing the `details` from `rate_limited` fails
`rate_limited_always_carries_details`.

## Status

- gravity: adopted (its envelope is built here; the duplicate `json!({…})` is gone; the router's
  API-404 fallback uses `not_found`).
- The rest of the estate: measured, not yet migrated. `~/estate-gates/check-envelope.sh` prints the
  count on every gate run (advisory).

## Known follow-ups

- The crate has no remote yet, so `gravity` depends on it by path. Once the Forgejo repo exists, switch
  the dependency to the documented git form (`cargo git dependency`) and delete the path.
