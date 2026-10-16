# Spec — the store's exported methods have no inventory

**Status:** IMPLEMENTED 2026-10-16 in §27 (`internal/store/export_inventory_test.go`, `make gates`
arm `export_inventory_mutants.json`). Written before any code. What the pass found — 227 LIVE, 24
TEST-ONLY, 3 UNCALLED of 254 — and every reason a method stays is in `docs/REMAINING.md` §27.

## The problem

Three passes in a row have found the same class of defect, and the class is only ever found by
accident:

- §25: `ThemeWeightTotal` had a complete implementation and no caller; `feature_tags` had no INSERT
  outside a Go test; a whole tracker note said a column was missing when the writer was.
- §26: `store.LinkComplaint` had a complete body since M2 and no caller, so `feature_complaints`
  could only be written at creation.

Each time the finding arrived because someone happened to grep for the function while doing
something else. `internal/httpapi/reader_coverage_test.go` closed exactly this hole on the HTTP side:
every registered route must be named in a declared inventory with a reader in `assets/js`, so a route
that no page consumes fails a test instead of waiting for an accident. **The store has no equivalent.**

`docs/REMAINING.md` §24.7 names three store methods with no caller (`GetMember`, `GetBoardCard`,
`ApplyProjectTag`) — a sample taken by hand, three passes ago, in a tree that has since gained
sixteen more store files. A hand-kept list of a measurable property is a list that is wrong the
moment the next file lands.

## What the measurement must do

Classify every exported `*store.DB` method by who reaches it, using the AST rather than text:

- **LIVE** — at least one caller in production code (`internal/httpapi`, `internal/store` itself,
  `cmd/`), excluding `_test.go`.
- **TEST-ONLY** — reached from `_test.go` files and from nothing in production. This is the class
  both of the last two passes' findings belonged to: the function is *exercised*, so it looks alive
  in every test run, and nothing a user does can reach it.
- **UNCALLED** — no caller anywhere, not even a test.

The measurement must be a **test**, so it runs in `make verify`, and it must be **declared against an
inventory** so that neither of these can happen quietly:

1. a new uncalled export appears — the inventory does not contain it, the test fails, and the author
   either wires it or names it;
2. the inventory rots — an entry declaring LIVE that production no longer calls fails the same way.

## What is deliberately NOT in scope

- **No deletion sweep.** A method with no caller is not automatically dead: `CoverageClaims` is a
  helper the store itself uses, `Confirmations` and `GetMergeApprovals` may be the read half of a
  surface nobody built yet (the §26 shape). Each UNCALLED entry is named in the inventory with a
  reason, and deletions happen one at a time, by hand, with the suite as the check — the note in
  §24.7 records what a scripted span deletion cost the last time it was tried.
- **No change to what any endpoint does.** This pass adds a measurement and names what it finds.

## Acceptance

1. `internal/store/export_inventory_test.go` measures the classification above with `go/ast` and
   fails when an export is missing from the inventory or when a declared-LIVE export has no
   production caller.
2. The measurement's own soundness is asserted: a set of anchors known to be live (with their
   calling files) must be classified LIVE, so a walker that silently stops finding callers fails
   loudly instead of reporting a clean, wrong, all-dead tree. (The first attempt at this measurement,
   a text scan, called 249 of 254 methods uncalled including `CreateFeature` and `GetBoard`; a
   measurement that is wrong in the direction of noise is worse than none.)
3. Both failure modes are proved by execution (plant an export, watch the test name it; declare a
   live method TEST-ONLY, watch the test refuse), not by reading the code.
4. `docs/REMAINING.md` §27 records the measured landscape and each named entry's reason; §24.7's
   three-name list is re-pointed at the measurement that supersedes it.

## Named limits

- The classification is per *name*. A method reached only through an interface value is LIVE (the
  call site still names it), but a method called through reflection or a string would be
  misclassified; nothing in this tree does that, and the anchors in (2) are what would catch it.
- `RaceCondition`-style indirect entry points (`cmd/` wiring) are counted as production, which is the
  intent: the question is "does anything the user can reach call this".
