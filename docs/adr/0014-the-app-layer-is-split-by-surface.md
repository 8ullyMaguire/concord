# 0014 — The split of `crates/app` is pure moves, gated by the recorded corpus

Date: 2026-11-15. Status: accepted. Scope: **the method for step 3 of `docs/plans/refactor-roadmap.md`**, and the
alternatives it refuses. The program itself (eight steps, ordered by risk removed per hour) is that file, not this one —
`docs/reviews/2026-11-15-implementation-critique.md` carries the measurements behind it.

## What this ADR exists to settle

Two questions about one file, and both have a cheap wrong answer.

`crates/app/src/lib.rs` is 427 KB: 217 handlers, 179 routes, 82 of them answering `Json<Value>`, 152 `json!` sites, 122
`bearer_user(...)` call sites, and 4 unit tests. It is the layer where the product's decisions are made, and it has less
coverage than `concord-ranking` (43 tests) and less type discipline than the schema (which at least has `CHECK`s).

```
$ wc -c crates/app/src/lib.rs                                   -> 427 KB
$ grep -c '\.route(' crates/app/src/lib.rs                       -> 179
$ grep -c 'bearer_user' crates/app/src/lib.rs                    -> 122
$ grep -c 'Json<Value>' crates/app/src/lib.rs                    -> 82
$ grep -rc '#\[test\]\|#\[tokio::test\]' crates/app/src/lib.rs   -> 4
```

The wrong answers are (a) rewrite it, and (b) split it "properly" — meaning while also fixing the auth model, the types, the
error shape and the audit trail. Both turn a mechanical move into a change, and a change into something that cannot be
reviewed line by line.

## Decision

1. **The split is pure moves, one module per commit.** `api/projects.rs`, `api/board.rs`, `api/consensus.rs`, `api/work.rs`,
   `api/identity.rs`, `api/records.rs` (step 3's names), each with its own `routes()`, assembled by `router()`. No renames,
   no re-formatting, no opportunistic fixes in the same commit. `git show --stat` must show a move: the same lines in,
   the same lines out, `lib.rs` under 20 KB at the end.
2. **The gate for a move is the recorded corpus, replayed.** Behaviour-preserving is a claim; the corpus is the measurement.
   Re-record it, then replay it:

   ```sh
   python3 scripts/parity/record.py --seed scripts/parity/seed.txt --out crates/parity/corpus.json --previous crates/parity/corpus.json
   CANDIDATE=https://concord.polarisocial.xyz cargo run -p concord-parity -- --corpus crates/parity/corpus.json
   ```

   Baseline taken 2026-11-15 before any of this work: `22 shapes -> crates/parity/corpus.json`, then
   `0 shape(s) disagreed of 15 checked, 6 skipped` (the skips are declared `unstable`/`expected_change`). Plus
   `probe_pages 136/136`, `probe_project_segments 86/86`, `make gate`. A step that cannot hold those is reverted, not
   argued with.
3. **The auth model, the types, the error contract and the audit trail are steps 1, 4, 5 and 6 of the program** — separate,
   ordered, each with its own measurement. Doing them inside the move would make `git show --stat` unreadable and the
   regression unattributable.
4. **The 21 unrun e2e suites are part of the program, and they are step 9.** `scripts/gate.sh` runs 8 of the tree's 29
   `*_e2e.py` suites; `tests/e2e/harness_selftest_test.py` now ratchets the number (`RECORDED_UNRUN = 21`). Steps 1, 2 and 5
   add refusal paths (401/403, 429, event writes) that only a suite can prove from outside; the suites they need are among
   the unwired ones.

## Rejected alternatives, and why each lost

* **A rewrite (SvelteKit + OpenAPI-typed client, or a fresh Axum app).** 44 e2e suites and eight wired probes encode three
  weeks of found defects — the claim button's GET, four trailing slashes, un-mounted roots, `[data-slug]`. A rewrite keeps
  that knowledge in prose and throws away the code that proves it, and it cannot be stopped halfway.
* **Splitting *and* fixing in the same commits.** The move is the one change in this program that can be proved correct by
  `git show --stat` and a corpus replay. Mixing in a behaviour change makes both claims weaker: nobody can tell whether a
  disagreement is the split or the fix.
* **A service/use-case layer between `concord-app` and `concord-db`.** Every handler becomes a two-line pass-through plus a
  struct mapping: the same file count, more indirection, no new test surface. Handlers are testable today (`State<PgPool>`
  and a scratch database); what makes them readable is 20 lines instead of 200, which the split delivers.
* **Consolidating the 79 tables.** A live schema's table count is not a defect, and editing a baseline production already
  ran is how a deploy works on a laptop and fails on the host. The defect was schema evolution without a mechanism, fixed by
  `0002`.
* **Filling `crates/domain` for its own sake.** Program step 7 extracts the three rules that are the product (stage
  transitions, consensus tallies, claim eligibility) and nothing else. A domain crate that re-declares the store's structs
  is a quarter of work returning a layer of mapping code.

## Consequences

* The wire does not move while the files do; the corpus replay is the proof and it runs in seconds.
* Each module move is independently revertible.
* The split buys no features. It buys the ability to change the product without a browser, which is what makes the program's
  other steps — and the three unbuilt product ideas — affordable.
