package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// Audit handler and route, from docs/plans/audit-log.md step A2.
//
// These tests exist because the observable behaviour of an HTTP endpoint is
// more than the JSON it returns: it is the status code, the headers and the
// body, and a test that does not check the shape of the failure can never see a
// missing header or a body that says something different from what the reader
// expects.
//
// The two distinct questions in the audit log are resolved by two tests:
//
//   - `TestAnAuditRowCarriesADisplayNameNotAnEmail`: the viewer shows the actor's
//     display name, not their email or username, and never invents one for a
//     dangling foreign key (a user deleted after they acted).
//   - `TestAPrivateProjectsAuditLogIsNotReadable`: a private project's log
//     returns the exact same body as a missing project, so no existence oracle
//     leaks through the status.
//
// The idea's own searchability is exercised by three more:
//
//   - `TestAnActionFilterNarrowsTheList`: the list after filtering contains only
//     rows with that action — and the negative assertion is the load-bearing one.
//   - `TestTheCountReflectsTheFilteredTotal`: the "N of TOTAL" line reflects the
//     filter, not the table total, because a filter that returns every row is
//     the inert kind that passes a "the right rows came back" check.
//   - `TestTheSearchFilterIsNamedQAndNotText`: the parameter is `q`, and the
//     wrong name must NOT narrow — otherwise a typo is invisible until a page
//     built against the documented name silently shows every row.
//
// A negative test that can only pass by accident is the strongest one this file
// has, because the alternative — seeding exactly one row and asserting the list
// has length one — passes under the mutant that drops the WHERE clause and
// returns the whole table, so it reads as correct when nothing changed.
//
// Each test is written so a mutant that breaks the rule it guards fails it, and
// a green run that was never seen red is not evidence.

// auditPage is the shape handleListProjectAudit writes.
type auditPage struct {
	Entries []store.AuditEntry `json:"entries"`
	Total   int                `json:"total"`
	Offset  int                `json:"offset"`
	Limit   int                `json:"limit"`
}

// getAudit fetches an audit log at a project's slug and decodes the page.
//
// The slug is a string because {project_id} on this route is a SLUG, resolved by
// requireProjectID through GetProject(ctx, slug). An earlier version of this
// file built the URL from strconv.FormatInt(p.ID) and every test in it 404'd:
// the numeric id is not a valid project_id on ANY route in this API. That is the
// mistake phase3.md S1.1 records, and a test that sends an id where production
// sends a slug exercises a URL no client will ever request.
//
// auth "" means the caller's own identity (see the note on authGet), which is
// right for a project the harness user owns.
func getAudit(t *testing.T, ts *httptest.Server, path string) auditPage {
	t.Helper()
	code, raw := authGet(t, ts, path, "")
	if code != http.StatusOK {
		t.Fatalf("GET %s: status %d, body %s", path, code, raw)
	}
	var page auditPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("GET %s: bad JSON %v, body %s", path, err, raw)
	}
	return page
}

// seedAuditProject creates a public project owned by the harness user and
// returns it. CreateProject leaves visibility at the column default, which
// migration 0009 pins to 'public'.
func seedAuditProject(t *testing.T, st *store.DB, slug string) store.Project {
	t.Helper()
	p, err := st.CreateProject(t.Context(), auditOwnerID(t, st), slug, "Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("create project %s: %v", slug, err)
	}
	return p
}

// auditOwnerID looks the harness user up by the username newTestServerWithStore
// creates. The store has no "give me the first user" and the tests must not
// assume id 1 — an id that happens to be 1 today is an accident of insertion
// order, and a fixture that depends on it breaks the moment a migration seeds a
// row.
func auditOwnerID(t *testing.T, st *store.DB) int64 {
	t.Helper()
	var id int64
	if err := st.QueryRowContext(t.Context(),
		`SELECT id FROM users WHERE username = ?`, "testuser").Scan(&id); err != nil {
		t.Fatalf("look up the harness user: %v", err)
	}
	return id
}

func TestAnAuditRowCarriesADisplayNameNotAnEmail(t *testing.T) {
	ts, st := newTestServerWithStore(t)

	p := seedAuditProject(t, st, "audit-display")
	if err := st.AddAudit(t.Context(), p.ID, auditOwnerID(t, st), "create_project", "project", 1, "created the project"); err != nil {
		t.Fatalf("add audit: %v", err)
	}

	page := getAudit(t, ts, "/api/v1/projects/audit-display/audit")
	if len(page.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(page.Entries))
	}
	name := page.Entries[0].ActorDisplayName
	if name == nil {
		t.Fatal("ActorDisplayName is nil; the viewer shows ids nobody can act on")
	}
	if *name != "Test User" {
		t.Errorf("ActorDisplayName = %q, want %q", *name, "Test User")
	}

	// The email assertion reads the RAW body, not the decoded struct. The point
	// is that no key anywhere in the response carries one, and decoding into a
	// typed struct cannot see a field the struct does not have — which is
	// exactly how an `email` field would ship unnoticed.
	_, raw := authGet(t, ts, "/api/v1/projects/audit-display/audit", "")
	if strings.Contains(string(raw), "@") {
		t.Errorf("an email address reached the audit response body: %s", raw)
	}
}

func TestAPrivateProjectsAuditLogIsNotReadable(t *testing.T) {
	ts, st := newTestServerWithStore(t)

	p := seedAuditProject(t, st, "audit-secret")
	if _, err := st.SetProjectVisibility(t.Context(), "audit-secret", "private"); err != nil {
		t.Fatalf("set visibility: %v", err)
	}
	if err := st.AddAudit(t.Context(), p.ID, auditOwnerID(t, st), "set_visibility", "project", 1, "w"); err != nil {
		t.Fatalf("add audit: %v", err)
	}

	// Both answers are captured whole and compared as bytes. A status-code
	// comparison passes against a 404 whose body is
	// {"error":"not found for project \"audit-secret\""} — the existence oracle,
	// printed in the body, invisible to any require.Equal on the code alone.
	missingCode, missing := authGet(t, ts, "/api/v1/projects/nonexistent/audit", "")
	privateCode, private := authGet(t, ts, "/api/v1/projects/audit-secret/audit", "")

	if missingCode != http.StatusNotFound {
		t.Errorf("a missing project answered %d, want 404", missingCode)
	}
	if privateCode != http.StatusNotFound {
		t.Errorf("a private project answered %d, want 404", privateCode)
	}
	if string(private) != string(missing) {
		t.Fatalf("private project log differs from a missing one:\n private: %s\n missing: %s", private, missing)
	}
}

func TestAnActionFilterNarrowsTheList(t *testing.T) {
	ts, st := newTestServerWithStore(t)

	p := seedAuditProject(t, st, "audit-filter")
	owner := auditOwnerID(t, st)
	for _, row := range []struct{ action, detail string }{
		{"set_visibility", "made public"},
		{"create_invite", "code a"},
		{"set_visibility", "made private"},
	} {
		if err := st.AddAudit(t.Context(), p.ID, owner, row.action, "project", 1, row.detail); err != nil {
			t.Fatalf("add audit %s: %v", row.action, err)
		}
	}

	page := getAudit(t, ts, "/api/v1/projects/audit-filter/audit?action=set_visibility")
	if len(page.Entries) != 2 {
		t.Fatalf("got %d entries for action=set_visibility, want 2", len(page.Entries))
	}
	// The negative assertion: a filter that returns every row reads as "the
	// filter works" when the only thing checked was the count. Here we look at
	// the rows themselves, so the test can only pass if the filter narrowed.
	for _, e := range page.Entries {
		if e.Action != "set_visibility" {
			t.Errorf("filter returned a %q row", e.Action)
		}
	}
}

func TestTheCountReflectsTheFilteredTotal(t *testing.T) {
	ts, st := newTestServerWithStore(t)

	p := seedAuditProject(t, st, "audit-count")
	owner := auditOwnerID(t, st)
	for _, row := range []struct{ action, detail string }{
		{"set_visibility", "a"},
		{"set_visibility", "b"},
		{"create_invite", "c"},
	} {
		if err := st.AddAudit(t.Context(), p.ID, owner, row.action, "project", 1, row.detail); err != nil {
			t.Fatalf("add audit %s: %v", row.action, err)
		}
	}

	page := getAudit(t, ts, "/api/v1/projects/audit-count/audit?action=create_invite")
	if len(page.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(page.Entries))
	}
	if page.Total != 1 {
		t.Errorf("Total = %d, want 1: the total must describe the filtered set, not the table", page.Total)
	}
	// The total is not the page size. The caller asked for no limit and the
	// server used the default, so a Total equal to Limit would be guessing at
	// the filtered size from a single page.
	if page.Total == page.Limit {
		t.Errorf("Total (%d) equals the page limit: this is a guess at the filtered size", page.Total)
	}

	// The unfiltered page is the control. Without it, a store that returned
	// Total: 1 unconditionally satisfies both assertions above.
	unfiltered := getAudit(t, ts, "/api/v1/projects/audit-count/audit")
	if unfiltered.Total != 3 {
		t.Errorf("unfiltered Total = %d, want 3", unfiltered.Total)
	}
	if len(unfiltered.Entries) != 3 {
		t.Errorf("unfiltered entries = %d, want 3", len(unfiltered.Entries))
	}
}

// TestTheSearchFilterIsNamedQAndNotText pins the parameter name.
//
// Not a style assertion. The store field is `Text` and a first draft read
// `text` off the query string; a page written against `q` then silently
// searches nothing and the endpoint answers 200 with every row — a failure that
// reads as "search does not work yet" rather than as a typo.
func TestTheSearchFilterIsNamedQAndNotText(t *testing.T) {
	ts, st := newTestServerWithStore(t)

	p := seedAuditProject(t, st, "audit-search")
	owner := auditOwnerID(t, st)
	for _, detail := range []string{"code alpha", "code beta"} {
		if err := st.AddAudit(t.Context(), p.ID, owner, "create_invite", "invite", 1, detail); err != nil {
			t.Fatalf("add audit %s: %v", detail, err)
		}
	}

	byQ := getAudit(t, ts, "/api/v1/projects/audit-search/audit?q=alpha")
	if len(byQ.Entries) != 1 {
		t.Errorf("?q=alpha returned %d entries, want 1", len(byQ.Entries))
	}

	// The wrong name must NOT narrow. If it did, both spellings would work and
	// the typo would stay invisible until a caller depended on the documented
	// one — which is the exact shape of the bug this test exists to kill.
	byText := getAudit(t, ts, "/api/v1/projects/audit-search/audit?text=alpha")
	if len(byText.Entries) != 2 {
		t.Errorf("?text=alpha returned %d entries, want 2: an undocumented parameter must not filter", len(byText.Entries))
	}
}

// TestAnEmptyQuerySearchesNothingRatherThanEverything is the A1 store rule at
// the HTTP edge: `q=` must not become LIKE '%%'.
//
// The store test covers the clause; this covers the handler, because an empty
// query parameter is the natural state of a text box the reader has focused and
// not yet typed into.
func TestAnEmptyQuerySearchesNothingRatherThanEverything(t *testing.T) {
	ts, st := newTestServerWithStore(t)

	p := seedAuditProject(t, st, "audit-emptyq")
	if err := st.AddAudit(t.Context(), p.ID, auditOwnerID(t, st), "create_project", "project", 1, "hello"); err != nil {
		t.Fatalf("add audit: %v", err)
	}

	page := getAudit(t, ts, "/api/v1/projects/audit-emptyq/audit?q=")
	if len(page.Entries) != 1 {
		t.Fatalf("?q= returned %d entries, want 1: an empty search is no filter, not a filter matching nothing", len(page.Entries))
	}
	if page.Total != 1 {
		t.Errorf("Total = %d for an empty search, want 1", page.Total)
	}
}

// TestAMalformedFilterIsIgnoredRatherThanFatal pins the parse-error policy.
//
// A 400 on a typo is worse than a wide page in a viewer that is read by hand:
// the reader has no way to tell "you mistyped" from "this project has no
// history", and the audit log is the surface where being unable to read it
// matters most.
func TestAMalformedFilterIsIgnoredRatherThanFatal(t *testing.T) {
	ts, st := newTestServerWithStore(t)

	p := seedAuditProject(t, st, "audit-malformed")
	if err := st.AddAudit(t.Context(), p.ID, auditOwnerID(t, st), "create_project", "project", 1, "hello"); err != nil {
		t.Fatalf("add audit: %v", err)
	}

	for _, qs := range []string{
		"?limit=abc", "?offset=xyz", "?actor_id=nope", "?since=forever",
		"?entity_id=NaN", "?limit=", "?q=",
	} {
		page := getAudit(t, ts, "/api/v1/projects/audit-malformed/audit"+qs)
		if len(page.Entries) != 1 {
			t.Errorf("%s returned %d entries, want 1 (the unfiltered log)", qs, len(page.Entries))
		}
	}
}

// TestALimitAboveTheCeilingIsClampedNotHonouredAtTheEdge checks the clamp
// survives the handler: a caller asking for a million rows is told, in the
// response, what page size it actually got. It asks about an empty project so
// the assertion is about the reported size, never about row counts.
func TestALimitAboveTheCeilingIsClampedNotHonouredAtTheEdge(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	seedAuditProject(t, st, "audit-limit")

	page := getAudit(t, ts, "/api/v1/projects/audit-limit/audit?limit=100000")
	if page.Limit != store.AuditMaxLimit {
		t.Errorf("Limit = %d, want the ceiling %d: a caller asked for a million rows and got something else",
			page.Limit, store.AuditMaxLimit)
	}

	// And the same clamp below the floor: no limit at all is the documented
	// default, not "everything".
	def := getAudit(t, ts, "/api/v1/projects/audit-limit/audit")
	if def.Limit != store.AuditDefaultLimit {
		t.Errorf("Limit = %d for an unset limit, want the default %d", def.Limit, store.AuditDefaultLimit)
	}
	if def.Offset != 0 {
		t.Errorf("Offset = %d, want 0", def.Offset)
	}
}

// TestPagingDoesNotRepeatARowWhenTwoEntriesShareATimestamp is the third
// mutation gate, at the HTTP edge.
//
// created_at is a float64 of time.Now().Unix(), so two AddAudit calls in one
// request stamp the same value. Ordering by created_at DESC alone leaves those
// rows unordered and LIMIT/OFFSET can serve the same row on both pages. This
// asserts on the SET of ids across two pages rather than on their counts,
// because two pages of the right size with a row repeated is exactly the bug
// and it is invisible to any count-based assertion.
func TestPagingDoesNotRepeatARowWhenTwoEntriesShareATimestamp(t *testing.T) {
	ts, st := newTestServerWithStore(t)

	p := seedAuditProject(t, st, "audit-tie")
	owner := auditOwnerID(t, st)
	// Three rows, all at ONE timestamp, which is what makes the id tiebreak the
	// only thing ordering them.
	const shared = 9000.0
	for i := 0; i < 3; i++ {
		if _, err := st.ExecContext(t.Context(), `
			INSERT INTO audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
			VALUES (?, ?, 'create_project', 'project', 1, 'tie', ?)`,
			p.ID, owner, shared); err != nil {
			t.Fatalf("seed tied audit row: %v", err)
		}
	}

	first := getAudit(t, ts, "/api/v1/projects/audit-tie/audit?limit=2")
	if len(first.Entries) != 2 {
		t.Fatalf("page 1 returned %d entries, want 2", len(first.Entries))
	}
	second := getAudit(t, ts, "/api/v1/projects/audit-tie/audit?limit=2&offset=2")
	if len(second.Entries) != 1 {
		t.Fatalf("page 2 returned %d entries, want 1", len(second.Entries))
	}

	ids := map[int64]bool{}
	for _, e := range append(append([]store.AuditEntry{}, first.Entries...), second.Entries...) {
		if ids[e.ID] {
			t.Errorf("audit row %d appears on both pages: the id tiebreak is missing", e.ID)
		}
		ids[e.ID] = true
	}
	if len(ids) != 3 {
		t.Errorf("paging yielded %d distinct rows, want 3", len(ids))
	}
}

// TestTheActionsEndpointListsWhatThePageOffersToFilterOn covers the filter
// dropdown's data source.
//
// This endpoint is what stops the page's <select> being empty. Without it the
// viewer still renders every row -- the fetch falls back to [] -- so a project
// with nine distinct actions and one with a single action look identical, and
// "you cannot filter here" is indistinguishable from "there is nothing to
// filter".
func TestTheActionsEndpointListsWhatThePageOffersToFilterOn(t *testing.T) {
	ts, st := newTestServerWithStore(t)

	p := seedAuditProject(t, st, "audit-actions")
	owner := auditOwnerID(t, st)
	for _, a := range []string{"create_invite", "set_visibility", "create_invite", "merge"} {
		if err := st.AddAudit(t.Context(), p.ID, owner, a, "project", 1, "d"); err != nil {
			t.Fatalf("add audit %s: %v", a, err)
		}
	}

	code, raw := authGet(t, ts, "/api/v1/projects/audit-actions/audit/actions", "")
	if code != http.StatusOK {
		t.Fatalf("GET audit/actions: status %d, body %s", code, raw)
	}
	var actions []string
	if err := json.Unmarshal(raw, &actions); err != nil {
		t.Fatalf("bad JSON %v, body %s", err, raw)
	}
	// Distinct and sorted, not one entry per row: a dropdown listing
	// "create_invite" three times is a different control, and the store sorts so
	// the order does not shift every time a vote lands.
	want := []string{"create_invite", "merge", "set_visibility"}
	if len(actions) != len(want) {
		t.Fatalf("got %d actions %v, want %d %v", len(actions), actions, len(want), want)
	}
	for i := range want {
		if actions[i] != want[i] {
			t.Errorf("actions[%d] = %q, want %q (full list %v)", i, actions[i], want[i], actions)
		}
	}
}

// TestTheActionsEndpointIsGatedLikeTheLogItDescribes: the action names of a
// private project's log are themselves information about what happened inside
// it, so this must 404 exactly as the log does -- and with the same body.
func TestTheActionsEndpointIsGatedLikeTheLogItDescribes(t *testing.T) {
	ts, st := newTestServerWithStore(t)

	seedAuditProject(t, st, "audit-actions-private")
	if _, err := st.SetProjectVisibility(t.Context(), "audit-actions-private", "private"); err != nil {
		t.Fatalf("set visibility: %v", err)
	}

	_, missing := authGet(t, ts, "/api/v1/projects/nope/audit/actions", "")
	_, private := authGet(t, ts, "/api/v1/projects/audit-actions-private/audit/actions", "")
	if string(private) != string(missing) {
		t.Fatalf("private project's action list differs from a missing project's:\n private: %s\n missing: %s", private, missing)
	}
}

// TestTheAuditPageRendersForAReadableProjectAndOnlyThat guards the page route,
// which resolves the slug ITSELF rather than through requireProjectID (that
// helper reads chi's {project_id}, which is empty on a page route).
func TestTheAuditPageRendersForAReadableProjectAndOnlyThat(t *testing.T) {
	ts, st := newTestServerWithStore(t)

	seedAuditProject(t, st, "audit-page")
	seedAuditProject(t, st, "audit-page-private")
	if _, err := st.SetProjectVisibility(t.Context(), "audit-page-private", "private"); err != nil {
		t.Fatalf("set visibility: %v", err)
	}

	code, body := authGet(t, ts, "/projects/audit-page/audit", "")
	if code != http.StatusOK {
		t.Fatalf("a readable project's audit page answered %d, want 200", code)
	}
	// The mount point, not the rows: the rows come from the API over fetch, and
	// a template that inlined them would be asserting on the wrong layer.
	if !strings.Contains(string(body), "audit-root") {
		t.Errorf("the audit page rendered without its mount point:\n%s", body)
	}

	goneCode, _ := authGet(t, ts, "/projects/no-such-project/audit", "")
	if goneCode != http.StatusNotFound {
		t.Errorf("a missing project's audit page answered %d, want 404", goneCode)
	}

	privCode, _ := authGet(t, ts, "/projects/audit-page-private/audit", "")
	if privCode != http.StatusNotFound {
		t.Errorf("a private project's audit page answered %d, want 404: the shell alone confirms the slug exists", privCode)
	}
}
