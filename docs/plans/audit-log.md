# Plan — Audit log viewer (idea #25)

Implements `docs/specs/audit-log-spec.md`. Written 2026-10-05, before any code.
Baseline measured the same day:

```
make verify    # green: vet + 718 top-level tests + 148 subtests, exit 0, 10 packages
go 1.27.1
live: 127.0.0.1:8006, v0.4.0-voting-56-g3ffae12, schema 22, 78 tables
```

Every step names its verification command and the output that means it worked.

## Step A1 — the store method, replacing `GetAuditLog`

**Files:** `internal/store/audit.go` **(new)**,
`internal/store/audit_test.go` **(new)**,
`internal/store/complaints.go` (edit — **delete** `GetAuditLog` and `AuditLogEntry`).

Spec R1: the old signature (`LIMIT 100`, no filters, no total) cannot express the
feature, and leaving it would be the second representation of one fact. Delete it.

```go
// AuditFilter is what the viewer narrows on. Every field is optional; the zero
// value matches the whole log.
//
// Text is a LIKE and not an FTS5 MATCH on purpose (spec §3): `detail` is
// free text written by 20+ call sites with no shared vocabulary, so an FTS5
// tokeniser would silently drop most of what a user types.
type AuditFilter struct {
	ProjectID  int64
	Action     string
	ActorID    int64
	EntityType string
	EntityID   int64
	Text       string
	Since      float64
	Offset     int
	Limit      int
}

// AuditPage is one page plus the total the page is drawn from.
//
// Total is not decoration: a client that renders "50 of ?" is guessing, and a
// client that pages to an empty page has no way to tell "past the end" from
// "nothing matches".
type AuditPage struct {
	Entries []AuditLogEntry `json:"entries"`
	Total   int             `json:"total"`
	Offset  int             `json:"offset"`
	Limit   int             `json:"limit"`
}

// AuditEntry is AuditLogEntry plus the resolved actor display name.
//
// Display name only, never username or email, for the reason admin_ledger is
// public: an audit row is meant to be read. The email rule is spec §5.1's and is
// enforced by a test that asserts the string is absent from the whole body.
type AuditEntry struct {
	AuditLogEntry
	ActorDisplayName *string `json:"actor_display_name"`
}
```

`ListAudit(ctx, f AuditFilter) (AuditPage, error)`:

```go
func (d *DB) ListAudit(ctx context.Context, f AuditFilter) (AuditPage, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	where := []string{"project_id = ?"}
	args := []any{f.ProjectID}
	if f.Action != "" {
		where = append(where, "action = ?")
		args = append(args, f.Action)
	}
	if f.ActorID != 0 {
		where = append(where, "actor_id = ?")
		args = append(args, f.ActorID)
	}
	if f.EntityType != "" {
		where = append(where, "entity = ?")
		args = append(args, f.EntityType)
	}
	if f.EntityID != 0 {
		where = append(where, "entity_id = ?")
		args = append(args, f.EntityID)
	}
	if f.Since > 0 {
		where = append(where, "created_at >= ?")
		args = append(args, f.Since)
	}
	// An EMPTY text must emit no LIKE at all. LIKE '%%' matches every row, so a
	// blanket LIKE would be a full scan of the whole project log that returns
	// exactly the rows the unfiltered query returns -- correct output, and a
	// cost that reads as a slow endpoint. The clause is conditional for that
	// reason, and TestAnEmptyQuerySearchesNothingRatherThanEverything kills it.
	if f.Text != "" {
		where = append(where, "(detail LIKE ? OR action LIKE ?)")
		args = append(args, "%"+f.Text+"%", "%"+f.Text+"%")
	}
	clause := " WHERE " + strings.Join(where, " AND ")

	// MaxOpenConns(1) (docs/PLAN.md rule 6): the COUNT cannot run while the rows
	// cursor is open, or the whole request deadlocks. Count first, then read --
	// both are single queries and neither holds a cursor while the other runs.
	var total int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`+clause, args...).Scan(&total); err != nil {
		return AuditPage{}, err
	}

	rows, err := d.QueryContext(ctx, `
		SELECT id, project_id, actor_id, action, entity, entity_id, detail, created_at
		FROM audit_log`+clause+`
		ORDER BY created_at DESC, id DESC
		LIMIT ? OFFSET ?`, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return AuditPage{}, err
	}
	defer rows.Close()
	// ... scan into []AuditLogEntry, then AFTER rows.Close() resolve the names.

	return AuditPage{Entries: entries, Total: total, Offset: f.Offset, Limit: f.Limit}, nil
}
```

**Two traps to write down, because both are the codebase's own:**

1. **`created_at` is not unique.** Several writers stamp `float64(time.Now().Unix())`
   inside one request, so ties are real. Ordering by `created_at DESC` alone makes
   paging non-deterministic: page 1 and page 2 can show the same row. `id DESC` is
   the tiebreak, and it is in both queries.
2. **`Detail` is nullable** (`audit_log.detail TEXT` with no `NOT NULL`), and
   `addAudit` writes `""` for several actions, so `NULL` and `""` are both live
   states. Scan through `sql.NullString`.

**Actor names are resolved after the cursor closes**, using the existing
`DisplayNamesFor(ctx, ids) (map[int64]string, error)` from `consensus_feed.go` — one
query for the whole page, not one per row. Reused, not rewritten.

**Verify**

```bash
go test ./internal/store/ -run 'Audit' -v -count=1 2>&1 | tail -40
```
expect `--- PASS` on each of A1's seven store tests, and specifically
`--- PASS: TestTheTotalIsTheFilteredTotalNotTheTableTotal`.

Then the deletion is proven, not asserted:

```bash
grep -rn "GetAuditLog" --include='*.go' . ; echo "expect: no output"
```

## Step A2 — the handler and route

**Files:** `internal/httpapi/audit.go` **(new)**,
`internal/httpapi/audit_test.go` **(new)**,
`internal/httpapi/server.go` (edit).

```go
// handleListProjectAudit serves the project-scoped audit log.
//
// Unauthenticated, for the same reason admin_ledger is (server.go:535): a
// privileged action nobody can read is not auditable. The project gate is
// requireProjectID, so a private project's log 404s with the same body as a
// nonexistent project -- no existence oracle.
func (s *Server) handleListProjectAudit(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	f := store.AuditFilter{ProjectID: projectID}
	q := r.URL.Query()
	f.Action = q.Get("action")
	f.EntityType = q.Get("entity_type")
	f.Text = q.Get("q")
	if v := q.Get("actor_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.ActorID = n
		}
	}
	// ... entity_id, since, offset, limit the same way, each a ParseX whose
	// error is ignored rather than fatal: a bad query parameter narrows to
	// nothing the caller asked for, it does not fail the request. The same
	// choice handleListAdminLedger makes at handlers.go:480.
	page, err := s.Store.ListAudit(r.Context(), f)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
```

Route, **top-level** with a `{project_id}` param — the phase3 S1.1 lesson is that
this parameter is a **slug** resolved by `requireProjectID`, not a numeric id:

```go
r.Route("/api/v1/projects/{project_id}/audit", func(r chi.Router) {
	r.Get("/", s.handleListProjectAudit)
})
```

**Verify**

```bash
go test ./internal/httpapi/ -run 'Audit' -v -count=1 2>&1 | tail -40
```
expect `TestAPrivateProjectsAuditLogIsNotReadable` (404, and the body is byte-identical
to a missing project's), `TestTheAuditLogCarriesADisplayNameNotAnEmail`.

## Step A3 — mutation gate

**Files:** `internal/store/audit_mutants.json` **(new)**, `Makefile` (edit).

Two mutants, both balance-checked so the build still compiles (a compile error is
not a RED — `phase3.md` S1.2 records a mutant that orphaned an import and failed to
BUILD, and was scored as a kill):

```json
[
  { "name": "an empty text search emits LIKE '%%' and scans everything",
    "old": "if f.Text != \"\" {", "new": "if true {" },
  { "name": "the total is counted unfiltered",
    "old": "`SELECT COUNT(*) FROM audit_log`+clause", "new": "`SELECT COUNT(*) FROM audit_log`" },
  { "name": "paging drops the id tiebreak and can repeat a row",
    "old": "ORDER BY created_at DESC, id DESC\n\t\tLIMIT ? OFFSET ?", "new": "ORDER BY created_at DESC\n\t\tLIMIT ? OFFSET ?" }
]
```

The third is the load-bearing one, and it needs a seed with **two rows sharing a
`created_at`** — otherwise the tiebreak is unobservable and the mutant survives for
the reason the Scout notes call out: the fixture never reached the rule.

```bash
make gates 2>&1 | grep -A3 'audit.go'
# expect: [audit.go] ---- 3/3 killed, 0 survived
```

## Step A4 — the page

**Files:** `internal/httpapi/templates/audit.html` **(new)**,
`internal/httpapi/assets/js/audit.js` **(new)**,
`internal/httpapi/server.go` (edit — add `"audit"` to the `pages` slice and a
`r.Get("/audit", …)` page route).

`ranking.html` is the template to copy: it is a nine-line mount point with a
skeleton, which is why the panels have no per-panel markup in the templates and why
a grep for `solutions` in `project.html` finds nothing.

Two rules, both already violated once on this codebase:

- **Never write `detail` into `innerHTML`.** `detail` is free text from 20+ call
  sites. `ranking.js` has an `esc()` and uses it in a template string; that is the
  house idiom and it is what audit.js does too.
- **`aria-busy` is cleared on every path**, including the error state. There is a
  test for it (`aria_busy_runtime_test.go`).

The filter controls are a `<select>` for action, a text input for `q`, and a
"showing N of TOTAL" line. **The count is not optional**: it is what tells a reader
their filter excluded 4,000 rows rather than matched 4,000.

**Verify**

```bash
go test ./internal/httpapi/ -run 'AuditPage' -v -count=1
make verify && echo OK
```

## Step A5 — Playwright

**Files:** `tests/e2e/audit_e2e.py` **(new)**, `pytest.ini` `python_files` (edit),
`Makefile` `e2e` target (edit — its own process, per the S1.4 three-part rule).

A new browser suite needs **all three** registrations or it is silently skipped, which
is how the 38 Finder tests went missing once.

Every browser context gets its own `X-Forwarded-For` or the per-visitor rate limiter
returns `{"error":"rate limit exceeded"}` as the page body — which reads as a broken
page, not a throttle.

The fixture seeds through **sqlite**, never through the endpoints under test: a
browser test whose data comes from the API cannot fail for the reason it exists.

| test | asserts |
|---|---|
| `test_the_audit_page_lists_the_seeded_entries` | both seeded actions appear |
| `test_filtering_by_action_narrows_the_list` | after filtering, the other action is **absent** — asserting only the count changed is how an inert filter passes |
| `test_a_filter_matching_nothing_says_so` | distinct from "no audit activity" |
| `test_the_count_reports_the_filtered_total` | the "N of M" line reflects the filter, not the table |

```bash
python3 -m pytest tests/e2e/audit_e2e.py -q     # expect 4 passed
```

## Final verification

```bash
make verify && make gates && make e2e
ss -ltn | grep -E '842[1-9]' || echo "no leaked e2e servers"
git status --short
```

## Commit order

One per step, `domain: summary`, `make verify` green before each. `git add -A` is
**not** run while `make gates` is executing — the Scout notes record a stale tree
produced by exactly that.