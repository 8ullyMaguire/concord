# 0014 — The app layer is split by surface, not rewritten

Date: 2026-11-15. Status: accepted. Supersedes nothing; extends ADR 0012 (the API is a designed surface).

## The measurement that forced this

Taken on `64cb134`, on `port-rust`, from the tree rather than from memory:

```
$ wc -l crates/app/src/lib.rs              -> 10035
$ grep -c '\.route(' crates/app/src/lib.rs  -> 179
$ grep -c '^pub async fn \|^async fn \|^pub fn ' crates/app/src/lib.rs -> 219
$ grep -c 'concord_db::' crates/app/src/lib.rs -> 526
$ grep -rc '#\[test\]\|#\[tokio::test\]' crates/app/src/lib.rs -> 4
$ cat crates/domain/src/lib.rs             -> one line: a doc comment naming a plan
$ wc -l crates/web/assets/js/*.js | tail -1  -> 12477 in 42 files, no build step
$ grep -c 'create table' crates/db/sql/0001_baseline.sql -> 79
$ ls crates/db/sql/*.sql                   -> 2 (baseline + one migration)
```

One file holds the router, 179 handlers, the policy that decides what those handlers may do, and the
JSON literals that shape every response. It is tested by four unit tests. The layer that decides the
product's behaviour has less test coverage than the ranking crate has tests (43).

This is the Go service's shape, faithfully carried across by the port — the port did what it set out to
do, and the plan it followed said "S1: routes, then S2: routes" rather than "a surface, then the same
surface with types". Two years of a monolith became two weeks of the same monolith in a better language.

## Decision

Split `crates/app` by **surface**, in steps that cannot change behaviour, and refuse the two refactors
that look bigger and are worse.

1. **`lib.rs` becomes a router composer and nothing else.** One module per surface under
   `crates/app/src/routes/`: `projects, features, board, requests, documents, consensus, criteria,
   curation, taxonomy, admin, auth, search, stats, notifications, feeds`. Each owns its handler
   signatures; none owns a table. Target: no file over 700 lines, `lib.rs` under 150.
2. **Response shapes become types.** The `json!({ ... })` literals are the wire contract written as a
   string — a rename in one place is a silent shape change everywhere, and today the parity corpus
   finds out afterwards. The twelve most-read shapes move to `crates/app/src/shapes.rs` as
   `#[derive(Serialize)]` structs with a round-trip test each. Everything else keeps its literal until
   it is touched; this is a ratchet, not a sweep.
3. **One error contract.** The estate answers `{"error": {"code", "message"}}`; the Go service answered
   `{"error": "message", "code": "code"}`; nginx adds a third for unknown API paths. The app keeps the
   flat Go shape (every client already reads it), states it in one place, and `api.js` accepts exactly
   that plus nginx's — no third reader path, no `sentence()` guessing.
4. **Do not create a domain layer for its own sake.** `crates/domain` is an empty crate and it should
   stay empty or be deleted: the rules worth isolating are already pure and already live where they
   belong (`concord_ranking::lifecycle`, `concord_ranking::health`). A domain crate that re-declares
   the same structs is the refactor that consumes a quarter and returns a layer of mapping code.
5. **Do not rewrite the frontend.** 12,477 lines of shipping scripts behind 44 e2e suites are an asset,
   not a debt to be paid down wholesale. What they need is modules: explicit imports instead of
   `window` registration, one bundling/typecheck step instead of `node --check` per file. That is a
   conversion, file by file, with `probe_pages` 136/136 as the gate.
6. **Do not consolidate the 79 tables.** A live schema's table count is not a defect; the defect was
   having one baseline file and no migration mechanism, and that is fixed (ADR 0011's mechanism, used
   by `0002`). Editing a baseline that production already ran is how a deploy works on a laptop and
   fails on the host.

## Rejected alternatives, and why each lost

* **A rewrite into SvelteKit + an OpenAPI-typed client.** The strongest-sounding option and the worst
  measured one: 44 e2e suites encode three weeks of found defects (the claim button's GET, four
  trailing slashes, the un-mounted roots, `[data-slug]`), and a rewrite discards the code and keeps the
  knowledge in prose. It also cannot be done in steps, so it cannot be stopped halfway.
* **Leave it.** The cost is measured: the last four defects found in this file (item 9 of the review)
  were found by probes, not by tests, and each took a browser to see. A 10k-line file with four tests
  is a file nobody changes unless they must.
* **A `service`/`usecase` layer between app and db.** Every handler would become a two-line pass-through
  plus a struct mapping, which is the same file count with more indirection and no new test surface. The
  thing that makes handlers testable is a scratch database and `State<PgPool>`, both of which exist;
  the thing that makes them readable is 20 lines instead of 200. Nothing else is needed.
* **Split `concord-db` (64 files, 21.7k lines) at the same time.** One refactor at a time. `db` is
  already organised per aggregate, and its 47 tests are the reason it can wait.

## Consequences

* **The wire does not change.** Every step is behaviour-preserving and is proved by replaying the
  recorded corpus: `scripts/parity/probe_requests.py` currently holds 21 shapes and reports
  `0 disagreed`. That recording is the refactor's regression suite, and it is the reason the split can
  be done file by file with real evidence rather than hope.
* **Each step is reversible on its own** — a module move reverted is a file move, not a deploy.
* **Order matters:** freeze the corpus → split the router → type the shapes → one error contract →
  modules in the frontend → wire the 21 unrun suites. Test wiring is last because the suites it wires
  then cover the new file layout, not the old one.
* **What this does not buy:** new features, better performance, or fewer tables. It buys the ability to
  change the product without a browser, which is the only thing that makes the other nine ideas
  affordable.

## Order, files and gates

| step | change | files | gate |
|---|---|---|---|
| R1 | record the surface as the refactor's baseline | `scripts/parity/probe_requests.py` corpus | `0 disagreed of 21 checked` |
| R2 | `lib.rs` → `routes/*.rs`, one surface per file | `crates/app/src/lib.rs`, `crates/app/src/routes/` | `probe_pages 136/136` · `probe_project_segments 86/86` · replay `0 disagreed` · `cargo test --workspace` |
| R3 | the twelve most-read shapes become structs | `crates/app/src/shapes.rs` | replay `0 disagreed` · `scripts/parity/api_surface.py` |
| R4 | one error contract, stated once | `crates/app/src/error.rs`, `crates/web/assets/js/api.js` | a refused request answers `{error, code}` · replay `0 disagreed` |
| R5 | the frontend becomes ES modules | `crates/web/assets/js/*.js` (42 files) | `js: all 42 assets parse` · `probe_pages 136/136` · `probe_claim_button` |
| R6 | the 21 unrun suites are wired | `scripts/gate.sh`, `tests/e2e/harness_selftest_test.py` | `RECORDED_UNRUN` falls to 0 · every suite green in one `make gate-live` |

Nothing in this ADR is "complete" until R6's ratchet reads 0.
