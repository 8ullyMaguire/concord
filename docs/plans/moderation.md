# Moderation — implementation plan

**Date:** 2026-11-11 · **Spec:** `docs/specs/moderation-spec.md` · **Branch:** `master`
**Status:** M1 is **built, verified and deployed** (2026-11-11, commit `4c8be43`, live
`pre-merge-2026-11-05-21-g7f28992`, schema 34). M2–M4 are still plan. The deviation and its
reason are recorded at the end of §2.

## 0. How to use this plan

Milestones run in order; each ends with a gate that must be *observed*, not assumed. The Go
package in Appendix A was extracted into a scratch module and compiled before being written
here (`gofmt -l` clean, `go vet` clean, `go test` ok, 4 test functions), so its symbols are
real. The store and handler code in §2 was written against signatures read in this tree on
2026-11-11 and has **not** been compiled — expect typos, not invented APIs; if one step's
code does not compile, fix the typo and note it in the commit body.

House rules this plan obeys, because breaking one of them is how the last wave went wrong:

- never `git stash` to get a clean run (it has already cost one merge in this repo);
- `make verify`, not `go test ./...`, before every milestone commit;
- if a gate fails, the doc numbers move in the same commit (`scripts/check_doc_numbers.py`
  reads 14 of them);
- one milestone, one commit, one subject in the imperative mood.

## 1. Order, and why

| # | milestone | why here |
|---|---|---|
| M1 | one act, one log: arena removal, restore, undo | the owner's actual request; it also proves the act path end to end before any UI exists to hide a broken one |
| M2 | the modlog: cursor, filters, undo rows | needs acts to exist before it has anything to render; the cursor must land before the log has enough rows to page |
| M3 | the controls: feature page panel, standings link, toast | last, so the UI is built against a working API and can be exercised with `dogfood` |
| M4 | extraction to `polaris-ledger-go` | a second Go adopter is what makes it extraction rather than projection; until then `internal/ledger` *is* the shared thing, with the crate's vectors |

M1 is the only milestone below with complete code. M2–M4 record ordering and acceptance
criteria; their detail is written immediately before they start, against the tree as it is
then — a plan that fixes M3's DOM ids today would be wrong by the time M1 lands.

---

## 2. M1 — the act path

### Step 1 — migration

**File:** `internal/db/migrations/0034_moderation.sql` (new; 0033 is the newest)

```sql
-- An act that reverses another. Nullable, because most acts reverse nothing, and the
-- self-reference keeps "undone by" in the log instead of in a second table that could
-- disagree with it.
ALTER TABLE audit_log ADD COLUMN undoes INTEGER REFERENCES audit_log(id);

-- Keyset paging. The existing index is (project_id, created_at); a cursor compares
-- (created_at, id), and without the id in the index two acts in the same millisecond can
-- repeat or skip a row across pages.
CREATE INDEX idx_audit_keyset ON audit_log(project_id, created_at DESC, id DESC);
```

**Verify** (SQLite refuses a second ALTER in some old versions, so apply to a copy first):

```bash
cd /home/alvaro/mnt/thinkcentre/personal/documents/code/projects/concord
cp /tmp/t.db /tmp/mig.db 2>/dev/null; rm -f /tmp/mig.db
sqlite3 /tmp/mig.db < internal/db/migrations/0001_init.sql 2>/dev/null || true
sqlite3 /tmp/mig.db 'ALTER TABLE audit_log ADD COLUMN undoes INTEGER REFERENCES audit_log(id); CREATE INDEX idx_audit_keyset ON audit_log(project_id, created_at DESC, id DESC); PRAGMA index_list(audit_log);'
```

Expected: `idx_audit_keyset` listed. Then let the real migrator do it:

```bash
go test ./internal/db/ -run TestMigrate -count=1
```

Expected: `ok  \tconcord/internal/db`.

### Step 2 — the rules package

**Files:** `internal/ledger/ledger.go` and `internal/ledger/ledger_test.go` — **exactly**
Appendix A and Appendix B, which are compiled and green.

**Verify:**

```bash
cd /home/alvaro/mnt/thinkcentre/personal/documents/code/projects/concord
gofmt -l internal/ledger && go vet ./internal/ledger/ && go test ./internal/ledger/ -count=1 -v
```

Expected (4 tests, all PASS, and no file listed by `gofmt -l`):

```
=== RUN   TestValidate
--- PASS: TestValidate (0.00s)
=== RUN   TestProject
--- PASS: TestProject (0.00s)
=== RUN   TestCursorRoundTripsAndOrdersStrictly
--- PASS: TestCursorRoundTripsAndOrdersStrictly (0.00s)
=== RUN   TestDecodeRefusesRatherThanStartingAtPageOne
--- PASS: TestDecodeRefusesRatherThanStartingAtPageOne (0.00s)
ok  	concord/internal/ledger
```

### Step 3 — the trust threshold

**File:** `internal/config/config.go`, beside `TrustLevelMin` (`internal/config/trust.go:14`
if the file is separate — put it next to the function, not in a new file).

```go
// ModTrustMin is the instance-wide trust level that may moderate a project's arena without
// holding a role in it. It is separate from TrustLevelMin (the gate on direct feature-status
// changes) on purpose: the two powers are different, and an operator may want to hand out
// one without the other.
//
// Measured 2026-11-11: the live instance has 88 accounts, 87 at trust_level 0 and one at 1,
// and trust_config.max_level is 1. The default of 1 therefore means "the owner and whoever
// the owner promotes"; raising it is an operator decision, not a side effect of this feature.
func ModTrustMin() int {
	if v := os.Getenv("CONCORD_MOD_TRUST_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		slog.Warn("CONCORD_MOD_TRUST_MIN is not an integer, using the default", "value", v)
	}
	return 1
}
```

**Verify:** `go test ./internal/config/ -count=1` → `ok`.

### Step 4 — the store, in one transaction per act

**File:** `internal/store/moderation.go` (new)

```go
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ModerationAct is one act before it is a row. Entity/EntityID address what the act was done
// to; they are audit_log's own columns, so the log needs no second index to answer "what
// happened to this feature".
type ModerationAct struct {
	Action   string
	Entity   string
	EntityID int64
	Reason   string
	// Prev is the state the act is about to replace, so the restore path never has to guess
	// what a reversal should put back.
	Prev map[string]any
	// Undoes is the audit row this act reverses, or 0.
	Undoes int64
}

// ActResult is the appended row.
type ActResult struct {
	ID        int64
	ProjectID int64
	Action    string
	At        float64
}

// actJSON is audit_log.detail for one act. The three shapes are documented in the spec §4;
// this type exists so a typo in a key is a compile error rather than a null in the page.
type actJSON struct {
	Reason string         `json:"reason"`
	Prev   map[string]any `json:"prev,omitempty"`
	Undoes int64          `json:"undoes,omitempty"`
}

func insertAct(ctx context.Context, tx *sql.Tx, projectID, actorID int64, act ModerationAct) (ActResult, error) {
	if act.Reason == "" {
		return ActResult{}, fmt.Errorf("%w: moderation act %q has no reason", ErrInvalid, act.Action)
	}
	detail, err := json.Marshal(actJSON{Reason: act.Reason, Prev: act.Prev, Undoes: act.Undoes})
	if err != nil {
		return ActResult{}, fmt.Errorf("encode act detail: %w", err)
	}
	at := float64(time.Now().Unix())
	var undoes any
	if act.Undoes != 0 {
		undoes = act.Undoes
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at, undoes)
		VALUES (?,?,?,?,?,?,?,?)`,
		projectID, actorID, act.Action, act.Entity, act.EntityID, string(detail), at, undoes)
	if err != nil {
		return ActResult{}, fmt.Errorf("write act %q: %w", act.Action, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ActResult{}, fmt.Errorf("act id: %w", err)
	}
	return ActResult{ID: id, ProjectID: projectID, Action: act.Action, At: at}, nil
}

// RemoveFeatureFromArena takes a feature out of one arena: the entry row goes, the feature's
// status becomes rejected, and one act records both prior values.
//
// The feature is NOT deleted. Its complaints, board card and any decision record address it;
// a delete would leave them pointing at nothing and would make the owner's "undo" request
// unimplementable without a tombstone. See spec D1.
func (d *DB) RemoveFeatureFromArena(ctx context.Context, projectID, arenaID, featureID, actorID int64, reason string) (ActResult, error) {
	f, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return ActResult{}, err // already ErrNotFound-wrapped
	}
	if f.ProjectID != projectID {
		return ActResult{}, fmt.Errorf("%w: feature %d", ErrNotFound, featureID)
	}
	entry, err := d.GetArenaEntry(ctx, arenaID, "feature", featureID)
	if err != nil {
		return ActResult{}, err
	}
	if entry.IsBaseline {
		return ActResult{}, fmt.Errorf("%w: the baseline entry of an arena cannot be removed", ErrInvalid)
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return ActResult{}, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	now := float64(time.Now().Unix())
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM arena_entries WHERE arena_id=? AND entity_type='feature' AND entity_id=?`,
		arenaID, featureID); err != nil {
		return ActResult{}, fmt.Errorf("remove arena entry: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE features SET status='rejected', updated_at=? WHERE id=? AND project_id=?`,
		now, featureID, projectID); err != nil {
		return ActResult{}, fmt.Errorf("reject feature: %w", err)
	}

	act, err := insertAct(ctx, tx, projectID, actorID, ModerationAct{
		Action:   "arena.remove",
		Entity:   "feature",
		EntityID: featureID,
		Reason:   reason,
		Prev: map[string]any{
			"status":   f.Status,
			"arena_id": arenaID,
			"elo_r":    entry.R,
			"elo_rd":   entry.RD,
			"sigma":    entry.Sigma,
			"games":    entry.Games,
		},
	})
	if err != nil {
		return ActResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ActResult{}, fmt.Errorf("commit: %w", err)
	}
	return act, nil
}

// RestoreFeatureToArena reverses one arena.remove. The values come from the act being undone,
// never from the caller: a caller who could name the rating could also invent one.
func (d *DB) RestoreFeatureToArena(ctx context.Context, projectID, actorID, undoOf int64, reason string) (ActResult, error) {
	var action, entity, detail string
	var entityID sql.NullInt64
	err := d.QueryRowContext(ctx, `
		SELECT action, entity, entity_id, detail FROM audit_log
		WHERE id=? AND project_id=?`, undoOf, projectID).Scan(&action, &entity, &entityID, &detail)
	if errors.Is(err, sql.ErrNoRows) {
		return ActResult{}, fmt.Errorf("%w: act %d", ErrNotFound, undoOf)
	}
	if err != nil {
		return ActResult{}, fmt.Errorf("read act %d: %w", undoOf, err)
	}
	if action != "arena.remove" || entity != "feature" || !entityID.Valid {
		return ActResult{}, fmt.Errorf("%w: act %d is %q and cannot be restored", ErrInvalid, undoOf, action)
	}
	var already int
	if err := d.QueryRowContext(ctx,
		`SELECT count(*) FROM audit_log WHERE undoes=?`, undoOf).Scan(&already); err != nil {
		return ActResult{}, fmt.Errorf("check reversal: %w", err)
	}
	if already > 0 {
		return ActResult{}, fmt.Errorf("%w: act %d has already been reversed", ErrInvalid, undoOf)
	}

	var prev struct {
		Status string  `json:"status"`
		Arena  int64   `json:"arena_id"`
		EloR   float64 `json:"elo_r"`
		EloRD  float64 `json:"elo_rd"`
		Sigma  float64 `json:"sigma"`
		Games  int     `json:"games"`
	}
	var wrapper struct {
		Prev json.RawMessage `json:"prev"`
	}
	if err := json.Unmarshal([]byte(detail), &wrapper); err != nil || len(wrapper.Prev) == 0 {
		return ActResult{}, fmt.Errorf("%w: act %d carries no prior state", ErrInvalid, undoOf)
	}
	if err := json.Unmarshal(wrapper.Prev, &prev); err != nil {
		return ActResult{}, fmt.Errorf("%w: act %d's prior state is unreadable", ErrInvalid, undoOf)
	}
	if prev.Status == "" || prev.Arena == 0 {
		return ActResult{}, fmt.Errorf("%w: act %d's prior state is incomplete", ErrInvalid, undoOf)
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return ActResult{}, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	now := float64(time.Now().Unix())
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd, sigma, games, updated_at)
		VALUES (?, 'feature', ?, ?, ?, ?, ?, ?)
		ON CONFLICT(arena_id, entity_type, entity_id) DO UPDATE SET
			r=excluded.r, rd=excluded.rd, sigma=excluded.sigma,
			games=excluded.games, updated_at=excluded.updated_at`,
		prev.Arena, entityID.Int64, prev.EloR, prev.EloRD, prev.Sigma, prev.Games, now); err != nil {
		return ActResult{}, fmt.Errorf("restore arena entry: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE features SET status=?, updated_at=? WHERE id=? AND project_id=?`,
		prev.Status, now, entityID.Int64, projectID); err != nil {
		return ActResult{}, fmt.Errorf("restore status: %w", err)
	}
	act, err := insertAct(ctx, tx, projectID, actorID, ModerationAct{
		Action:   "arena.restore",
		Entity:   "feature",
		EntityID: entityID.Int64,
		Reason:   reason,
		Undoes:   undoOf,
	})
	if err != nil {
		return ActResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ActResult{}, fmt.Errorf("commit: %w", err)
	}
	return act, nil
}
```

Both of the things this code depends on were measured in the tree rather than assumed:

- `d.BeginTx(ctx, nil)` — `DB` embeds `*sql.DB`, and eight store files already use the
  two-argument form (`internal/store/features.go:96`);
- `PRIMARY KEY (arena_id, entity_type, entity_id)` — `migrations/0017_arenas.sql:139`, so
  the `ON CONFLICT` target in the restore path is a real key and not a wish.

`UpsertArenaEntry` (`arenas.go:451`) takes **no** rating arguments — it writes the defaults —
which is why the restore path writes its own INSERT: the numbers being restored are the whole
point of the act.

**Verify (the store tests are §2 step 7):**

```bash
go build ./internal/store/ && echo BUILD-OK
```

### Step 5 — the handlers

**File:** `internal/httpapi/moderation.go` (new)

```go
package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"git.polarisocial.xyz/concord/concord/internal/ledger"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

type arenaActRequest struct {
	FeatureID int64  `json:"feature_id"`
	ArenaID   int64  `json:"arena_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
	Undoes    int64  `json:"undoes"`
}

// permitModeration answers whether this reader may act on this project.
//
// Two paths, because the owner asked for "a high-trust user or an admin" and this repo
// already has both: an instance-wide trust level (users.trust_level, CONCORD_MOD_TRUST_MIN)
// and a per-project role. Either is enough. A refusal says which it wanted.
func (s *Server) permitModeration(w http.ResponseWriter, r *http.Request, projectID int64, minRole string) bool {
	actorID := getActorID(r)
	if actorID == 0 {
		writeError(w, http.StatusUnauthorized, "this act needs a session")
		return false
	}
	if lvl, err := s.Store.GetTrustLevel(r.Context(), actorID); err == nil && lvl >= config.ModTrustMin() {
		return true
	}
	if err := s.requireRole(r.Context(), projectID, minRole, actorID); err != nil {
		mapError(w, err, "moderation")
		return false
	}
	return true
}

// handleArenaRemove implements POST /api/v1/projects/{slug}/moderation/arena/remove.
func (s *Server) handleArenaRemove(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	if !s.permitModeration(w, r, projectID, "reviewer") {
		return
	}
	var req arenaActRequest
	if !readJSON(w, r, &req) {
		return
	}
	if !req.Confirm {
		writeError(w, http.StatusBadRequest, "this act needs confirmation")
		return
	}
	if req.FeatureID == 0 || req.ArenaID == 0 {
		writeError(w, http.StatusBadRequest, "feature_id and arena_id are both required")
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if ledger.TrimmedReason(reason) == nil {
		writeError(w, http.StatusBadRequest, ledger.ReasonRequiredDefect.Error())
		return
	}
	act, err := s.Store.RemoveFeatureFromArena(r.Context(), projectID, req.ArenaID, req.FeatureID, getActorID(r), reason)
	if err != nil {
		mapError(w, err, "remove from arena")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"act":  act,
		"undo": map[string]any{"action": "arena.restore", "target": act.ID},
	})
}

// handleArenaRestore implements POST /api/v1/projects/{slug}/moderation/arena/restore and is
// what the undo control posts to. It takes the id of the act it reverses, not the feature's
// prior values: see the store method's comment.
func (s *Server) handleArenaRestore(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	if !s.permitModeration(w, r, projectID, "reviewer") {
		return
	}
	var req arenaActRequest
	if !readJSON(w, r, &req) {
		return
	}
	if !req.Confirm {
		writeError(w, http.StatusBadRequest, "this act needs confirmation")
		return
	}
	if req.Undoes == 0 {
		writeError(w, http.StatusBadRequest, "undoes is required")
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if ledger.TrimmedReason(reason) == nil {
		writeError(w, http.StatusBadRequest, ledger.ReasonRequiredDefect.Error())
		return
	}
	act, err := s.Store.RestoreFeatureToArena(r.Context(), projectID, getActorID(r), req.Undoes, reason)
	if err != nil {
		mapError(w, err, "restore to arena")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"act": act})
}
```

`errors` and `store` are imported only if used; drop whichever the compiler flags — this is
pseudocode-adjacent on imports alone, not on symbols (`requireProjectID`, `requireRole`,
`getActorID`, `readJSON`, `writeJSON`, `mapError`, `writeError` all exist in this package).

### Step 6 — routes

**File:** `internal/httpapi/server.go`, in the project subtree, above the arena routes:

```go
		// Moderation: one act, one audit row, undo is another act (spec §6). These are
		// POST-only on purpose — a GET that removes an entry is a crawler's favourite bug.
		r.Route("/moderation", func(r chi.Router) {
			r.Post("/arena/remove", s.handleArenaRemove)
			r.Post("/arena/restore", s.handleArenaRestore)
		})
```

**Verify:**

```bash
go build ./... && go run . --help >/dev/null 2>&1; grep -c "moderation/arena" internal/httpapi/server.go
```

Expected: build silent, `2`.

### Step 7 — tests, written to fail first

**File:** `internal/store/moderation_test.go` (new). Fixture: `openTestDB(t)` if that helper
exists in `internal/store` tests (`grep -n "func openTestDB\|func newTestDB" internal/store/*_test.go`);
otherwise the pattern in `internal/store/arenas_test.go` is the one to copy, because that file
already builds an arena and an entry.

```go
// The act, and its reversal, are one test because the property that matters is the pair:
// after remove+restore the entry's four numbers and the feature's status are exactly what
// they were, and the log holds two rows that say so.
func TestRemovingAndRestoringAnArenaEntryKeepsTheNumbers(t *testing.T) {
	d, uid, pid, fID := arenaFixture(t) // arenas_test.go:27 -- also creates the feature
	ctx := context.Background()
	a, err := d.EnsureArena(ctx, store.ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	if err := d.UpsertArenaEntry(ctx, a.ID, "feature", fID); err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	// UpsertArenaEntry writes the defaults; the numbers this test cares about are set
	// here, because "the restore put back exactly what was there" is the assertion.
	if _, err := d.ExecContext(ctx, `
		UPDATE arena_entries SET r=1532.5, rd=210.25, sigma=0.0625, games=4
		WHERE arena_id=? AND entity_type='feature' AND entity_id=?`, a.ID, fID); err != nil {
		t.Fatalf("seed numbers: %v", err)
	}

	first, err := d.RemoveFeatureFromArena(ctx, pid, a.ID, fID, uid, "spam: repeated link drops")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := d.GetArenaEntry(ctx, a.ID, "feature", fID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the entry survived the removal: %v", err)
	}
	got, _ := d.GetFeature(ctx, fID)
	if got.Status != "rejected" {
		t.Fatalf("status is %q, want rejected", got.Status)
	}

	if _, err := d.RestoreFeatureToArena(ctx, pid, uid, first.ID, "removal was mistaken"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	e, err := d.GetArenaEntry(ctx, a.ID, "feature", fID)
	if err != nil {
		t.Fatalf("the entry did not come back: %v", err)
	}
	if e.R != 1532.5 || e.RD != 210.25 || e.Sigma != 0.0625 || e.Games != 4 {
		t.Fatalf("restored numbers drifted: %+v", e)
	}
	if got, _ := d.GetFeature(ctx, fID); got.Status != "draft" {
		t.Fatalf("status is %q, want the prior draft", got.Status)
	}

	// A second reversal of the same act must refuse: the log is append-only, and "undo
	// twice" would re-insert an entry the second act did not remove.
	if _, err := d.RestoreFeatureToArena(ctx, pid, uid, first.ID, "again"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("double reversal: got %v, want ErrInvalid", err)
	}
}
```

**File:** `internal/httpapi/moderation_test.go` (new), using the existing server fixture
(`newTestServer`/`seedPanels` — `grep -n "func seedPanels" internal/httpapi/*_test.go`):

```go
// 403 before 200: the refusal is the security property, and a test that only proves the
// permitted path passes on a handler with no gate at all.
func TestArenaRemovalNeedsTrustOrRole(t *testing.T) {
	// contributor, trust 0 -> 403 with "reviewer" in the body
	// same user after SetTrustLevel(1) -> 201
	// stranger -> 401
	// private project + stranger -> 404
}
```

The four cases are the four rows of spec §9.2, and each is one assertion on `rec.Code` plus,
for the 403, one on the body containing `reviewer`.

**Verify:**

```bash
cd /home/alvaro/mnt/thinkcentre/personal/documents/code/projects/concord
go test ./internal/store/ -run 'Arena|Moderation' -count=1
go test ./internal/httpapi/ -run 'Moderation|ArenaRemoval' -count=1
```

Expected: `ok` twice. Then break it on purpose — comment out the `UPDATE features SET
status='rejected'` line and re-run; the store test must fail with `status is "draft", want
rejected`. Restore the line. A test that cannot fail is not evidence.

### Step 8 — the inventory row, then the gate

`internal/httpapi/reader_coverage_test.go` gains a row for `assets/js/moderation.js`
(M3), so add it in M3, not here — M1 has no JS. Run the full gate:

```bash
make verify
```

Expected: ends with the docs gate line and exit 0; if it complains about a test count,
update the five docs that state it in the same commit.

**Commit:**

```
api: removal from an arena is an act, and an act is reversible

POST /api/v1/projects/{slug}/moderation/arena/remove takes a feature out of one
arena: the entry row goes, the feature's status becomes rejected, and one
audit_log row carries the prior status and the entry's four numbers. The
reversal posts the act's id, not the numbers -- RestoreFeatureToArena reads them
from the log, so a caller cannot invent a rating.

Permission is either instance-wide trust (users.trust_level >= CONCORD_MOD_TRUST_MIN,
default 1) or the project role the spec names, because the repo already has both
axes and the owner asked for "a high-trust user or an admin". Measured on the live
instance 2026-11-11: 88 accounts, 87 at trust 0 and one at 1.

internal/ledger is the Go form of polaris-ledger's rules, with the crate's
sentences and a cursor that compares (at, id) -- not a port of its tables, because
the crate has none. Its four test functions were compiled in a scratch module
before this plan was written.
```

---

### M1 as built — what changed from this plan

Three deviations, each forced by something measured rather than preferred:

1. **The panel shipped with M1, not M3.** `TestEveryAPIRouteIsDeclaredWithAReaderOrAReason`
   refuses an API route with no reader, and it is right to: a route no page can reach is the
   defect that gate exists for. So `assets/js/moderation.js` and the feature-page section came
   with the routes. It is a panel, not a modlog UI.
2. **`arena_id` is optional in the body.** The feature page names the idea, not the arena, and
   an idea's arena is a property of the ranking. `store.ArenasForFeature` resolves one arena,
   and the handler refuses to guess when the idea is in more than one.
3. **No capability endpoint exists**, so the panel probes with the act itself: `confirm:false`
   returns 400 ("this act needs confirmation") when permitted and 401/403/404 when refused,
   before the body is read. One request, nothing mutated. A real capability read is the better
   fix and belongs in M3.

Verified: `go test ./...` green on thinkcentre (httpapi 284s, store 170s, 1228 tests), vet,
fmt-check, docs-check, e2e-sync-check. Both new tests were mutated to prove they can fail
(`status=status` → "status is \"draft\", want rejected"; trust floor +1000 → "promoted removal:
403, want 201"). Live on the origin: version `...g7f28992`, schema 34, the asset 200, the panel
in the feature page's HTML, and an anonymous POST after giving consent → `401 authentication
required`, which means the handler was reached and the gate ran.

## 3. M2 — the modlog (ordering + acceptance)

Order: store reader first (cursor + until + action filter), then the page, then the nav
label. Reasons: the reader's response shape is the page's contract, and the nav label is one
string that must not ship before the page can honour it.

Acceptance:

1. `GET …/audit?cursor=<the last row's next_cursor>` returns rows strictly older than it,
   under insertions: seed 5 acts, read page 1 with `limit=2`, insert a sixth act, read page 2
   →
   ```bash
   go test ./internal/httpapi/ -run 'AuditCursor' -count=1 -v
   ```
   expected: `--- PASS`, and the union of the pages has 5 distinct ids with none repeated.
2. `?until=` excludes later rows; `?action=arena.remove` returns only removals; `?q=` still
   searches detail text including reasons.
3. A malformed cursor is `400` with `ledger.ErrMalformedCursor`'s sentence, not page one.
4. The page renders an act row with: what happened, to which feature (a link), why, who, and
   an Undo control **only** when the projected row is an `arena.remove` that is not already
   undone. Non-act rows (the 20+ existing writers) render without any control.
5. `GET …/audit` response gains `next_cursor` and keeps `total`; a client that ignores
   `next_cursor` still pages by `offset` (deprecated, one release).
6. Nav label reads "Modlog", route unchanged.

## 4. M3 — the controls (ordering + acceptance)

Order: feature page panel → standings row → `/rank` links. The panel is the only
implementation; the other two link to it.

Acceptance:

1. On `/projects/ferrisfeed/features/{id}` as a reviewer: panel visible, confirmation inline
   (button becomes "Remove from arena? — this hides it from ranking" + Remove/Cancel), reason
   required, and on success the toast offers Undo for 60 s which posts `undoes`.
2. As a contributor the panel is absent, and the page does not fetch
   `/moderation/capabilities` in a loop (`dogfood` the page; one request).
3. `/ranking` rows get a "Moderate" link for readers with standing; no fetch, no second
   implementation.
4. `/rank` cards link to their idea page; the page still votes normally with no new click
   target near the vote buttons.
5. `assets/js/moderation.js` appears in `reader_coverage_test.go`'s inventory with its two
   routes, and `make a11y-check` stays green (the panel is a `<section>` with a heading and
   labelled inputs).

## 5. M4 — extraction (ordering + acceptance)

Trigger: a second Go member needs a log (orbital's link reports and suki's queue are the two
candidates — check before starting). Until then, extraction would be one adopter plus a
guess.

Acceptance: `polaris-ledger-go` (module path decided at that point) carries Appendix A and B
verbatim plus the crate's vector file, both suites read the same JSON, and concord's
`internal/ledger` becomes a one-line import with `git grep -c "func Validate" internal/ledger`
= 0.

## 6. What this plan does not decide

- Whether the author of a removed idea is told (spec §8: no inbox exists; first follow-on).
- Whether `feature.reject` from the feature page should be the same panel or the existing
  status route (it exists and is unchanged by M1; the panel can call it, which would need its
  own audit row of `feature.reject`).
- Anything about an instance-wide log.

---

## Appendix A — `internal/ledger/ledger.go` (compiled: gofmt clean, vet clean, tests ok)

```go
// Package ledger is the Go form of the estate's polaris-ledger crate: the rules a
// moderation log and its public projection must keep.
//
// It owns no table and no database handle, exactly as the crate does not. What it owns is
// the part that must not drift between the two languages:
//
//   - the private record and the public projection are different types, and Project is the
//     only way to a publishable row;
//   - anonymisation is a declared rule per action, never a boolean that changes what an
//     entry means;
//   - an act that removes standing carries a reason, and a bounded power carries its end;
//   - paging over an append-only log uses Cursor, which is strictly-before on (at, id).
package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ActorKind is who acted.
type ActorKind int

const (
	// ActorPerson is a person. Public is the handle a reader may see; Account is the stored
	// identifier and must never reach a public surface.
	ActorPerson ActorKind = iota
	// ActorPanel is a jury panel. Anonymised by construction: a panel has no single actor,
	// and publishing an arbitrary member would be a lie about who decided.
	ActorPanel
	// ActorSystem is the instance itself: a timer, a migration, a rule.
	ActorSystem
)

// Actor is the acting identity, privately.
type Actor struct {
	Kind    ActorKind
	Account string
	Public  string
}

// Person builds a person actor.
func Person(account, public string) Actor {
	return Actor{Kind: ActorPerson, Account: account, Public: public}
}

// Panel is the anonymised jury actor.
func Panel() Actor { return Actor{Kind: ActorPanel} }

// System is the instance acting on its own.
func System() Actor { return Actor{Kind: ActorSystem} }

// SubjectKind separates an account (published as its public face) from a thing.
type SubjectKind int

const (
	// SubjectAccount is a person-shaped subject.
	SubjectAccount SubjectKind = iota
	// SubjectObject is a thing: a work, a post, a proposal, a feature.
	SubjectObject
)

// Subject is what the act was done to.
type Subject struct {
	Kind    SubjectKind
	Account string
	Public  string
	// ObjectKind and ObjectID are the product's own vocabulary for a thing. The crate does
	// not police the kind, and neither does this.
	ObjectKind string
	ObjectID   string
}

// OfAccount is a subject that is a person.
func OfAccount(account, public string) Subject {
	return Subject{Kind: SubjectAccount, Account: account, Public: public}
}

// OfObject is a subject that is a thing.
func OfObject(kind, id string) Subject {
	return Subject{Kind: SubjectObject, ObjectKind: kind, ObjectID: id}
}

// Redaction is how one kind of action's actor may be published, declared per action by the
// product as data.
type Redaction int

const (
	// PublishHandle publishes the acting person's handle: moderation is attributable.
	PublishHandle Redaction = iota
	// PanelAnonymised publishes no actor and marks the entry as a panel decision.
	PanelAnonymised
	// WithholdActor publishes no actor at all.
	WithholdActor
)

// ReasonMode is whether an act must state why.
type ReasonMode int

const (
	// ReasonOptional is housekeeping nobody needs to justify.
	ReasonOptional ReasonMode = iota
	// ReasonRequired is an act that removes standing.
	ReasonRequired
)

// ExpiryMode is whether an act is a bounded power.
type ExpiryMode int

const (
	// ExpiryUnbounded stands until it is reversed.
	ExpiryUnbounded ExpiryMode = iota
	// ExpiryRequired must carry an end.
	ExpiryRequired
)

// Rules is the pair a product declares for one act.
type Rules struct {
	Reason ReasonMode
	Expiry ExpiryMode
}

// The three shapes the crate names, plus concord's act.
var (
	// Bounded is a power that must be justified and must end.
	Bounded = Rules{Reason: ReasonRequired, Expiry: ExpiryRequired}
	// Permanent is a permanent moderation decision.
	Permanent = Rules{Reason: ReasonRequired, Expiry: ExpiryUnbounded}
	// Housekeeping is an act nobody needs to justify.
	Housekeeping = Rules{Reason: ReasonOptional, Expiry: ExpiryUnbounded}
)

// Act is the private record. It is never serialised to a public surface.
type Act struct {
	Action    string
	Subject   Subject
	Actor     Actor
	Reason    *string
	ExpiresAt *time.Time
	At        time.Time
}

// Projected is what a public surface may show. It is the only type here with JSON tags, so
// "publish the private record by accident" is a compile error rather than a review finding.
type Projected struct {
	Action          string    `json:"action"`
	Subject         string    `json:"subject"`
	SubjectKind     string    `json:"subject_kind,omitempty"`
	SubjectResolved bool      `json:"subject_resolved"`
	Actor           *string   `json:"actor"`
	Panel           bool      `json:"panel"`
	Reason          *string   `json:"reason"`
	At              time.Time `json:"at"`
}

// Defect is what is wrong with an act. Each variant names the rule, so a caller can surface
// it rather than log it.
type Defect int

const (
	// ReasonRequiredDefect means the act removes standing and states no reason.
	ReasonRequiredDefect Defect = iota
	// ExpiryInvalid means a bounded power carries an end that is not after its start.
	ExpiryInvalid
	// ExpiryRequiredDefect means a bounded power carries no end.
	ExpiryRequiredDefect
)

// The sentences the Rust crate produces, so a client reading either sees one vocabulary.
func (d Defect) Error() string {
	switch d {
	case ReasonRequiredDefect:
		return "this act requires a reason, and none was given"
	case ExpiryInvalid:
		return "this act's expiry is not after the moment it was taken"
	case ExpiryRequiredDefect:
		return "this act is a bounded power and carries no expiry"
	}
	return "unknown rule defect"
}

// Unwrap exists so errors.Is/As work on the sentinel values.
func (d Defect) Unwrap() error { return nil }

// FromUnix is an instant from unix seconds, the boundary concord stores.
func FromUnix(secs int64) time.Time { return time.Unix(secs, 0).UTC() }

// TrimmedReason collapses a whitespace-only reason to nil, which is the one representation
// of "no reason given".
func TrimmedReason(reason string) *string {
	for _, r := range reason {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return &reason
		}
	}
	return nil
}

// Validate proves an act against the rules a product declared.
func Validate(act Act, rules Rules) error {
	if rules.Reason == ReasonRequired {
		if act.Reason == nil || TrimmedReason(*act.Reason) == nil {
			return ReasonRequiredDefect
		}
	}
	switch {
	case rules.Expiry == ExpiryRequired && act.ExpiresAt == nil:
		return ExpiryRequiredDefect
	case act.ExpiresAt != nil && !act.ExpiresAt.After(act.At):
		return ExpiryInvalid
	}
	return nil
}

// Project is the public projection of an act.
//
// A panel is anonymised whatever the rule says: there is no single person to name. A
// withheld actor stays withheld even when the rule would publish a handle — the stricter of
// the two wins, because the failure mode of getting this wrong is publishing a name.
func Project(act Act, rule Redaction) Projected {
	var actor *string
	panel := false
	switch {
	case act.Actor.Kind == ActorPanel:
		panel = true
	case act.Actor.Kind == ActorPerson && rule == PublishHandle:
		handle := act.Actor.Public
		actor = &handle
	}
	var subject string
	var subjectKind string
	resolved := false
	if act.Subject.Kind == SubjectAccount {
		subject, subjectKind, resolved = act.Subject.Public, "account", true
	} else {
		subject, subjectKind = act.Subject.ObjectID, act.Subject.ObjectKind
	}
	// Copy the reason so a caller cannot mutate the projection's source through the pointer.
	var reason *string
	if act.Reason != nil {
		r := *act.Reason
		reason = &r
	}
	return Projected{
		Action:          act.Action,
		Subject:         subject,
		SubjectKind:     subjectKind,
		SubjectResolved: resolved,
		Actor:           actor,
		Panel:           panel,
		Reason:          reason,
		At:              act.At.UTC(),
	}
}

// Cursor is a position in an append-only log: (at, id), compared strictly.
//
// At alone is not a cursor. Two entries can share a millisecond, and a `< at` comparison
// then either repeats a row on the next page or skips one — and a log that skips a row is
// not an audit.
type Cursor struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
}

// NewCursor builds a cursor.
func NewCursor(at time.Time, id string) Cursor { return Cursor{At: at.UTC(), ID: id} }

// Before reports whether c is strictly before other in newest-first ordering: earlier
// time, or the same time and a smaller id.
func (c Cursor) Before(other Cursor) bool {
	if !c.At.Equal(other.At) {
		return c.At.Before(other.At)
	}
	return c.ID < other.ID
}

// Encode is the wire form: RFC 3339, a '|', then the id.
func (c Cursor) Encode() string { return c.At.UTC().Format(time.RFC3339Nano) + "|" + c.ID }

// ErrMalformedCursor is returned rather than a silent first page, because a silent first
// page turns a paging bug into an infinite scroll.
var ErrMalformedCursor = errors.New("this cursor is not a position in the log")

// Decode parses what Encode produced. A malformed cursor is an error, never page one.
func Decode(raw string) (Cursor, error) {
	bar := strings.IndexByte(raw, '|')
	if bar < 0 {
		return Cursor{}, ErrMalformedCursor
	}
	at, err := time.Parse(time.RFC3339Nano, raw[:bar])
	if err != nil {
		return Cursor{}, ErrMalformedCursor
	}
	id := raw[bar+1:]
	if id == "" {
		return Cursor{}, ErrMalformedCursor
	}
	return NewCursor(at, id), nil
}

// EncodeJSON is a convenience for the handler's response.
func (c Cursor) EncodeJSON() (json.RawMessage, error) {
	b, err := json.Marshal(c.Encode())
	if err != nil {
		return nil, fmt.Errorf("encode cursor: %w", err)
	}
	return b, nil
}
```

## Appendix B — `internal/ledger/ledger_test.go` (compiled, 4 functions PASS)

```go
package ledger

import (
	"errors"
	"testing"
	"time"
)

// The vectors below are the contract with the Rust crate. When the crate gains a case, this
// table gains the same case in the same commit, or the mirror has drifted.
func TestValidate(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	reason := "spam: repeated link drops"
	blank := "   "
	future := at.Add(time.Hour)
	past := at.Add(-time.Hour)

	cases := []struct {
		name  string
		act   Act
		rules Rules
		want  error
	}{
		{"permanent without reason", Act{At: at}, Permanent, ReasonRequiredDefect},
		{"permanent with blank reason", Act{At: at, Reason: &blank}, Permanent, ReasonRequiredDefect},
		{"permanent with reason", Act{At: at, Reason: &reason}, Permanent, nil},
		{"housekeeping without reason", Act{At: at}, Housekeeping, nil},
		{"bounded without expiry", Act{At: at, Reason: &reason}, Bounded, ExpiryRequiredDefect},
		{"bounded with future expiry", Act{At: at, Reason: &reason, ExpiresAt: &future}, Bounded, nil},
		{"bounded already expired", Act{At: at, Reason: &reason, ExpiresAt: &past}, Bounded, ExpiryInvalid},
		{"unbounded with an expiry is allowed", Act{At: at, Reason: &reason, ExpiresAt: &future}, Permanent, nil},
	}
	for _, c := range cases {
		got := Validate(c.act, c.rules)
		if !errors.Is(got, c.want) && got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestProject(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	reason := "spam"
	base := Act{
		Action:  "arena.remove",
		Subject: OfObject("feature", "123"),
		Actor:   Person("acct-9", "ana"),
		Reason:  &reason,
		At:      at,
	}

	published := Project(base, PublishHandle)
	if published.Actor == nil || *published.Actor != "ana" {
		t.Errorf("a published handle must name the actor, got %v", published.Actor)
	}
	if published.Subject != "123" || published.SubjectKind != "feature" || published.SubjectResolved {
		t.Errorf("object subject: got %+v", published)
	}

	withheld := Project(base, WithholdActor)
	if withheld.Actor != nil || withheld.Panel {
		t.Errorf("a withheld actor must be absent and not a panel: %+v", withheld)
	}

	panel := Project(Act{Action: "panel.vote", Subject: OfAccount("acct-9", "ana"), Actor: Panel(), At: at}, PublishHandle)
	if panel.Actor != nil || !panel.Panel {
		t.Errorf("a panel is anonymised whatever the rule says: %+v", panel)
	}
	if panel.Subject != "ana" || !panel.SubjectResolved {
		t.Errorf("an account subject publishes its public face: %+v", panel)
	}
}

func TestCursorRoundTripsAndOrdersStrictly(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	a := NewCursor(at, "10")
	b := NewCursor(at, "11")
	c := NewCursor(at.Add(time.Second), "1")

	if !a.Before(b) || b.Before(a) {
		t.Errorf("the same instant orders by id: %v vs %v", a, b)
	}
	if !b.Before(c) || c.Before(b) {
		t.Errorf("a later instant wins: %v vs %v", b, c)
	}
	if a.Before(a) {
		t.Error("a cursor is not before itself")
	}

	raw := a.Encode()
	back, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if !back.At.Equal(a.At) || back.ID != a.ID {
		t.Errorf("round trip: got %v, want %v", back, a)
	}
}

func TestDecodeRefusesRatherThanStartingAtPageOne(t *testing.T) {
	for _, raw := range []string{"", "nonsense", "2026-11-11T00:00:00Z", "2026-11-11T00:00:00Z|", "not-a-time|7"} {
		if _, err := Decode(raw); !errors.Is(err, ErrMalformedCursor) {
			t.Errorf("decode %q: got %v, want ErrMalformedCursor", raw, err)
		}
	}
}
```

---

## M2, as shipped: the curation queue

M2 was planned as `/projects/{slug}/moderation` plus `GET .../moderation/log`. It shipped as
`/curation` instead — a list over **every** reviewable kind, not only acts — because taxonomy
proposals and strategic weights are the larger queues and neither had a page. See
`docs/plans/curation-queue.md` (the plan) and `docs/REMAINING.md` §36 (what it cost and what it
left owed). The per-project modlog is explicitly *not* built; the audit page reads the acts
today, without reasons or an Undo control.
