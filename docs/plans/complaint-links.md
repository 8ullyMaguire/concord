# Plan — the extra complaint link (spec `docs/specs/complaint-links-spec.md`)

Baseline before step 1, measured on this tree (`2cc5beb`): **1053 Go tests**, 289 e2e tests in 21
suite files, 35/35 UI mutants, 17 Go mutant arms / 122 mutants, schema 29, 82 tables.

Order: the store guard first (it is the rule), then the route, then the page, then the gates. A
route before the store guard would ship the cross-project join the pass exists to close.

---

## Step 1 — the store guard: `LinkComplaint` takes the project

File: `internal/store/features.go` (edit `LinkComplaint`, ~line 264).

```go
// LinkComplaint attaches a validated complaint to an existing feature (§354's extra link).
//
// projectID is a parameter rather than something the caller checks because a feature id and a
// complaint id are both GLOBAL: this function's whole body is "turn two ids into a row", and
// without the project it will join a feature in one project to a complaint in another. It is
// the same defect the nested complaint routes had -- a route that reads one id and acts on
// another -- one layer lower, where no route-level check can reach it.
//
// Idempotent: INSERT OR IGNORE, so a retried request leaves one row. The complaint must be
// `validated`; an open complaint is evidence nobody has accepted yet.
func (d *DB) LinkComplaint(ctx context.Context, projectID, featureID, complaintID int64) error {
	f, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return err
	}
	if f.ProjectID != projectID {
		return fmt.Errorf("%w: feature %d is not in project %d", ErrNotFound, featureID, projectID)
	}
	c, err := d.GetComplaint(ctx, complaintID)
	if err != nil {
		return err
	}
	if c.ProjectID != projectID {
		return fmt.Errorf("%w: complaint %d is not in project %d", ErrNotFound, complaintID, projectID)
	}
	if c.Status != "validated" {
		return fmt.Errorf("%w: complaint must be validated to link", ErrInvalid)
	}
	now := float64(time.Now().Unix())
	if _, err = d.ExecContext(ctx,
		`INSERT OR IGNORE INTO feature_complaints (feature_id, complaint_id) VALUES (?, ?)`,
		featureID, complaintID); err != nil {
		return err
	}
	_, err = d.ExecContext(ctx, `UPDATE features SET updated_at=? WHERE id=?`, now, featureID)
	return err
}
```

Verify: `go build ./... && go vet ./...` → exit 0, no output.

## Step 2 — the store tests

File: `internal/store/complaint_links_test.go` (new). Five claims, one test each, over a fixture
that builds two projects (so "another project" is a real second project, not a missing row):

- `TestLinkComplaintAttachesAValidatedComplaint` — link, then `GetFeatureComplaints` shows 2,
  and it is the row that grew (count, then ids).
- `TestLinkComplaintIsIdempotent` — twice, one row.
- `TestLinkComplaintRefusesAComplaintFromAnotherProject` — `errors.Is(err, ErrNotFound)` AND no
  row written (the refusal, not just the error).
- `TestLinkComplaintRefusesAFeatureFromAnotherProject` — `ErrNotFound`.
- `TestLinkComplaintRefusesAnUnvalidatedComplaint` — `ErrInvalid`, and the message names the rule.

Verify: `go test ./internal/store/ -run LinkComplaint -count=1 -v` → 5 PASS.

## Step 3 — the route

File: `internal/httpapi/complaint_links.go` (new): `handleLinkComplaint`, the request struct, and
the guard order from §4 of the spec. File: `internal/httpapi/server.go` — register beside the
existing GET, which is the paired read:

```go
// §354's extra link: a second complaint that bears on a feature already built. The GET
// beside it is the read; this is the write, and the ids are proved to be in one project
// twice over -- once here and once in the store, where the row is made.
r.Post("/api/v1/projects/{project_id}/features/{id}/complaints", s.handleLinkComplaint)
```

Verify: `go build ./... && go test ./internal/httpapi/ -run 'Route|Reader' -count=1` → the reader
inventory gate goes RED naming the features row (`Routes: 16` against 17 measured). Bump that row
to 17 and add the dedicated row (§6 of the spec) whose anchor is the rendered control:

```go
{Match: "/api/v1/projects/{project_id}/features/{id}/complaints", Reader: "feature.js",
	Anchor: `data-attach-complaint="' + esc(l.id) + '">Attach</button>`, Routes: 2,
	Note: "§354's extra link: the write, and the read that proves what it did"},
```

Then `go test ./internal/httpapi/ -run Reader -count=1` → PASS.

## Step 4 — httpapi tests

File: `internal/httpapi/complaint_links_test.go` (new): the happy path (200, both ids in the
response, an `audit_log` row naming them), the cross-project complaint (404, `ErrNotFound`'s
shape), the non-contributor (403), the unvalidated complaint (400), and the missing complaint
(404). Follow the fixture style in `internal/httpapi/feature_tags_test.go`.

Verify: `go test ./internal/httpapi/ -run LinkComplaint -count=1 -v` → PASS with no fixture errors.

## Step 5 — the page

Files: `internal/httpapi/assets/js/feature.js` (the Complaints section, ~line 403) and
`internal/httpapi/assets/css/style.css` only if a class is needed.

- Render, under the complaint list, a `<select id="attach-complaint">` of the complaints in the
  project that are `validated` and not already linked, plus ONE button
  `data-attach-complaint` — the select carries the id, so the rendered fragments the reader
  inventory can anchor on are the option markup
  (`'<option value="' + esc(l.id) + '">' + esc(l.title) + '</option>'`) and the button.
- The data source: the project's validated complaints, from the existing list route
  (`GET /api/v1/projects/{project_id}/complaints`, already read by `complaint.js`), fetched in the
  same `Promise.all` as the page's other reads; the handler filters to `status === 'validated'`
  and drops the ids already linked client-side, because the server's list is not filtered per
  feature.
- The write: `POST base + '/features/' + id + '/complaints'` with `{complaint_id: N}` through the
  page's existing `send()` helper (which re-reads the page on success — the count and the list
  must both change, and a locally patched list would show the old priority explanation).
- The refusal: the page's `fail()` into `[data-tag-error]`'s sibling — a new
  `[data-complaint-error]` paragraph in the section, so a 400/403 lands where the click was
  (`showCardRefusal` is for the kanban card; the section needs its own sink).

Verify: `node --check internal/httpapi/assets/js/feature.js` → exit 0.

## Step 6 — the browser test and the UI mutants

File: `tests/e2e/feature_e2e.py` — one test: seed a feature with one linked complaint and a second
validated, unlinked complaint; open the feature page; assert the select offers the second and not
the first; click Attach; assert the count in the section header went from 1 to 2 AND the new
complaint's title is in the list (a positive assertion — a torn-down document must not satisfy it).

File: `tests/e2e/feature_ui_mutants.py` — two mutants:

- the attach path swapped to the read route (`/complaints` without the POST verb → the GET runs,
  answers 200, nothing changes),
- the select never rendered (the control exists and does nothing).

Verify: `make e2e` → the suite passes and the mutants are killed; `tests/e2e/harness_selftest_test.py`
stays green (the count in `docs/REMAINING.md` is machine-checked).

## Step 7 — the tracker, the specs, and the numbers

- `docs/REMAINING.md` §26 (as-built: what shipped, the cross-project defect the pass found in the
  store, the decisions, what the mutation runs proved) and the §24.7 paragraph re-pointed from
  "Open" to "done in §26" — deleting the stale claim rather than leaving both.
- `docs/specs/frontend-spec.md` §3.4: the attach control in the feature page's required content.
- `docs/QUESTIONS.md`: Q4 (no unlink; a feature must keep ≥1).
- `docs/REMAINING.md` §8 header line: the new counts.

Verify: `python3 scripts/check_doc_numbers.py` → OK; `make verify` → exit 0.

---

## Rollback

Each step is a separate commit-worth of work, and the store signature change is the only
cross-cutting edit. Reverting steps 3–7 leaves the store guard in place, which is a strict
improvement on its own (it closes a cross-project join that nothing could reach — yet).
