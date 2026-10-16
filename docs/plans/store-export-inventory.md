# Plan — the store's exported methods have no inventory (§27)

**Status:** executed 2026-10-16 (§27). Steps 1–5 landed; the gate's firings and the three
hand-deleted methods are recorded in `docs/REMAINING.md` §27.3–§27.4.

Spec: `docs/specs/store-export-inventory-spec.md`. Every step names its verification command and the
output that means it worked.

## Step 1 — the measurement

File: `internal/store/export_inventory_test.go` (new).

```go
// The store's exported methods, classified by who can reach them.
//
// §25 and §26 each found a complete store function that nothing in production called, and each
// found it by accident -- by grepping for it while doing something else. reader_coverage_test.go
// closed that hole on the HTTP side (a route with no reader in assets/js fails a test); this is the
// same measurement for the store, because the class of defect is the same and the store is where
// both of them lived.
//
// The classification is by NAME, over the AST:
//   LIVE      -- a call in production code (any non-test .go file under internal/ or cmd/)
//   TEST-ONLY -- a call only from _test.go files: exercised, so it looks alive in every test run,
//                and unreachable by anything a user does. Both §25's and §26's findings are here.
//   UNCALLED  -- no call anywhere.
```

Implementation sketch (complete, stdlib only):

- `measureStoreExports(t)` → `/ go/parser` walk of `../..` (the repo root), skipping `.git`,
  `bin`, `node_modules`, `frontend/build`; parse each `.go` file; for every `*ast.SelectorExpr`
  whose `Sel.Name` is an exported store method name, record (name, calling file, isTest).
- Store methods are collected the same way: `*ast.FuncDecl` with a receiver `*DB` and an exported
  name, in a non-test file under `internal/store`. (The receiver type is `*DB` — `d *DB`; a
  `FuncDecl` whose receiver selector is `*DB`.)
- `soundness` — the anchors: assert that `GetBoard` is LIVE with a caller in
  `internal/httpapi/handlers.go`, that `CreateFeature` is LIVE, and that `LinkComplaint` is LIVE
  (wired in §26). A walker that finds no callers anywhere fails here.

Verification: `go test ./internal/store/ -run TestStoreExportInventory -v` → prints the table and
fails with the names that are missing from the inventory (step 2 fills them in).

## Step 2 — the inventory, filled from the measurement

```go
// storeExports is the inventory. EXEMPT entries carry a reason; the reason is the deliverable --
// "no caller" without a reason is the state this pass exists to end.
var storeExports = map[string]string{
	"GetBoard":     live,      // handles.go:1921, feeds_handlers.go:230
	// ...
}
```

Registered as `live` when production calls it, `testOnly("reason")` / `uncalled("reason")`
otherwise. The test fails when a measured name is absent from the map, and when a `live` entry has
no production caller.

Verification: `go test ./internal/store/ -run TestStoreExportInventory -count=1` → `ok`.

## Step 3 — prove the gate can fail

Mutation arm `internal/store/export_inventory_mutants.json`, in `make gates`:

| mutant | how it fails |
| --- | --- |
| a `live` entry declared `testOnly` | the declaration contradicts the measurement |
| a `testOnly` entry declared `live` | there is no production caller to name |
| the soundness anchor weakened | the walker stops proving it finds callers |

Plus one by hand, which is the real case the gate exists for: add an exported method to `board.go`
with no caller, run the test, watch it name the method, delete it. Recorded in the pass notes.

## Step 4 — resolve what it finds, by hand

For every UNCALLED entry, in the diff (no scripted span deletion — §24.7 records that cost):

- `ApplyProjectTag` — superseded by `ApplyExistingProjectTag` (`internal/httpapi/projects.go:341`
  carries the decision); the doc comment still reads as the live path, which is the trap. **Delete.**
- the rest — one at a time; delete the ones with a named replacement, otherwise name them in the
  inventory with the reason they stay.

Verification after each: `go build ./... && go test ./internal/store/ -count=1`.

## Step 5 — docs and the full chain

- `docs/REMAINING.md` §27 with the measured table and every reason; §24.7's three-name list
  re-pointed at §27 (its claim was accurate — three names, out of fourteen and thirty-five).
- `docs/REMAINING.md` header line + §27.7 measured numbers.
- `make verify && make gates && make e2e` → all exit 0.
- commit, push both remotes, deploy both instances, live-probe.
