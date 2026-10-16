# Plan — the field-report write half (§28)

**Status:** executed 2026-10-16. Steps 1–6 landed (the 22 methods that stay named in the
inventory, and the three parts of the surface deliberately NOT routed, are in `docs/REMAINING.md`
§28.6).

Spec: `docs/specs/field-reports-write-spec.md`. Each step names its verification command and what
success looks like.

## Step 1 — the store guard that is missing

`RespondToFieldReport` loads the report and never looks at `removed`, so a hidden report can still
collect fresh replies: the quorum's decision is undone by a route that had not been written yet.
(The store is otherwise complete, which is why this is one clause and not a rewrite.)

File: `internal/store/field_reports.go`.

```go
	if report.Removed {
		return FieldOwnerResponse{}, fmt.Errorf(
			"%w: report %d was removed, so it takes no new replies", ErrNotFound, reportID)
	}
```

Verification: `go test ./internal/store/ -run FieldReport -count=1` → ok, plus a new test in
`internal/store/field_reports_test.go` that removes a report and asserts the reply is refused **and
that no row was written**.

## Step 2 — the routes

File: `internal/httpapi/field_reports_write.go` (new).

```go
type createFieldReportReq struct {
	Version, UseCase, Environment, Scale, Duration, Outcome, MigratedTo,
	Caveats, Workaround, Advice string
}

func (s *Server) handleCreateFieldReport(w http.ResponseWriter, r *http.Request)
func (s *Server) handleRespondToFieldReport(w http.ResponseWriter, r *http.Request)
func (s *Server) requireReportInProject(w http.ResponseWriter, r *http.Request, projectID int64) (store.FieldReport, bool)
```

Order in both handlers, and it is load-bearing: `requireWriteActor` (401) → `requireProjectID` (404,
which is also the visibility refusal) → the in-project proof → the body.

`server.go`, next to the existing read route:

```go
	r.Post("/api/v1/projects/{project_id}/field-reports", s.handleCreateFieldReport)
	r.Post("/api/v1/projects/{project_id}/field-reports/{report_id}/responses", s.handleRespondToFieldReport)
```

Both audit through `AddAudit` (`file_field_report`, `respond_field_report`) like the other writes.

Verification: `go build ./...`, then `go test ./internal/httpapi/ -run FieldReportWrite -count=1`.

## Step 3 — the handler tests

File: `internal/httpapi/field_reports_write_test.go` (new). Cases, one per rule in the spec's AC:
201 with the stored row; 400 for an unknown outcome (message names the four); 400 for
`migrated-away` with no successor (message names `migrated_to`); 401 anonymous; 404 for a report
addressed through another project's URL; `is_owner` from membership and not from the body; 404
answering a removed report; the audit rows.

Verification: `go test ./internal/httpapi/ -run FieldReportWrite -count=1 -v` → all pass.

## Step 4 — the UI

File: `internal/httpapi/assets/js/project.js` — inside `fieldReportsPanel`:

- a "File a field report" form (`#field-report-form`) with `#fr-outcome` (the four values),
  `#fr-version`, `#fr-use-case`, `#fr-environment`, `#fr-migrated-to` (labelled as required for
  `migrated-away`), `#fr-caveats`, `#fr-advice`, and a status sink `[data-fr-error]`;
- a reply form per report card (`form[data-respond-report="<id>"]`, input `[data-response-body]`);
- `wireFieldReports(slug)`, called from the same place the other panels are wired, using the
  page's existing `postJSON`.

Verification: `node --check internal/httpapi/assets/js/project.js` → no output.

## Step 5 — the browser tests and their mutants

`tests/e2e/project_panels_e2e.py`: one test that files a report through the form and asserts the panel
**positively** — the summary line appears ("100% of 1 reporter") and the report card is there with
its outcome badge — then replies as a member and asserts the `owner` badge renders on the reply.
Absences are not asserted (a panel that has not re-rendered satisfies an absence).

`tests/e2e/project_ui_mutants.py`: four mutants — the create posts to the read route (no method, wrong
path), the outcome select drops a value, the reply form sends `body` as the wrong field name, the
reply form is never wired.

Verification: `python3 tests/e2e/project_ui_mutants.py` → 4/4 killed (and the existing ones still
killed), source restored byte-identical.

## Step 6 — Go mutants, inventory, docs, chain

- `internal/httpapi/field_reports_write_mutants.json` + a `make gates` arm: the in-project guard, the
  auth-before-project ordering, `is_owner` taken from the body, the outcome passed through unchecked.
- `internal/store/field_reports_mutants.json` gains the removed-report reply guard.
- `internal/store/export_inventory_test.go`: drop the entries for `CreateFieldReport` and
  `RespondToFieldReport` (the gate refuses a LIVE method that carries an unreachable reason, so it
  demands this), and keep the other three with their reasons.
- `docs/REMAINING.md` §28 + the header line + the numbers.
- `make verify && make gates && make e2e` → all exit 0; commit, push both remotes, deploy both
  instances, probe live.
