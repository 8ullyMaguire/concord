package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// §4.7's write half at the edge (docs/specs/field-reports-write-spec.md).
//
// The read half has been routed since phase3 and the project page renders the panel from it, so
// what these tests pin is not "a handler exists" but the four rules the write path adds: WHO may
// write (any signed-in account, and the project's visibility still decides whether they can), WHICH
// report is being replied to (the URL's project and the report's project are the same one), WHAT is
// accepted (the store's enum and its migrated_to rule, refused with the field named), and whose
// reply is marked as the owner's (the membership table's answer, never the body's).

func fileReport(t *testing.T, srv *httptest.Server, token, slug string, body map[string]any) (int, []byte) {
	t.Helper()
	return authJSON(t, srv, http.MethodPost, "/api/v1/projects/"+slug+"/field-reports", token, body)
}

// userIDFor reads a user's id from the table the fixtures wrote to. The store has no by-username
// accessor that a test can reach without a login round trip, and this file needs an id for one
// thing only: to be the actor of a store-level removal, which §4.7 gives to somebody who is not a
// project member.
func userIDFor(t *testing.T, st *store.DB, username string) int64 {
	t.Helper()
	var id int64
	if err := st.QueryRowContext(context.Background(),
		`SELECT id FROM users WHERE username=?`, username).Scan(&id); err != nil {
		t.Fatalf("user id for %s: %v", username, err)
	}
	return id
}

func TestFieldReportWriteFilesAReport(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	owner := registerAndLogin(t, srv, "frep-owner")

	code, body := authJSON(t, srv, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "frep", "name": "frep", "description": "field reports"})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create project: %d %s", code, body)
	}

	code, body = fileReport(t, srv, owner, "frep", map[string]any{
		"outcome":     store.ReportWorked,
		"version":     "1.4.2",
		"environment": "arm64",
		"use_case":    "self-hosting a small instance",
		"advice":      "read the migration notes first",
	})
	if code != http.StatusCreated {
		t.Fatalf("file report: %d %s", code, body)
	}
	var report store.FieldReport
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if report.ID == 0 {
		t.Fatalf("report came back without an id: %s", body)
	}
	// The AUTHOR is the token's account, not a default and not a body field. This assertion came
	// from a surviving mutant -- `UserID: 1` in the handler was invisible to every other test in
	// this file, because the panel only ever shows the report's outcome and the reply's badge, and
	// neither depends on who filed it. "A report whose author is whatever the server felt like" is
	// not evidence, so the whole point of the write is pinned here.
	if want := userIDFor(t, st, "frep-owner"); report.UserID != want {
		t.Errorf("report author = %d, want the token's account %d", report.UserID, want)
	}

	// The panel's own read must see it, and its rate must stop being nil: this is the assertion
	// the pass exists for -- "the panel can only ever say No field reports yet" was the defect.
	code, listBody := authJSON(t, srv, http.MethodGet, "/api/v1/projects/frep/field-reports", owner, nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, listBody)
	}
	var panel map[string]any
	if err := json.Unmarshal(listBody, &panel); err != nil {
		t.Fatalf("decode panel: %v", err)
	}
	if got, _ := panel["sample_size"].(float64); got != 1 {
		t.Errorf("sample_size = %v after filing one report, want 1", panel["sample_size"])
	}
	if panel["outcome_rate"] == nil {
		t.Errorf("outcome_rate is nil after a worked report: %s", listBody)
	}

	// And it is written down.
	var n int
	if err := st.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE action='file_field_report' AND entity_id=?`,
		report.ID).Scan(&n); err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if n != 1 {
		t.Errorf("audit rows for file_field_report = %d, want 1", n)
	}
}

func TestFieldReportWriteRefusesAnUnknownOutcome(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	owner := registerAndLogin(t, srv, "frep-outcome")
	authJSON(t, srv, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "frep2", "name": "frep2", "description": "x"})

	code, body := fileReport(t, srv, owner, "frep2", map[string]any{"outcome": "worked-fine-mostly"})
	if code != http.StatusBadRequest {
		t.Fatalf("unknown outcome: got %d (%s), want 400", code, body)
	}
	// The message has to be usable: a caller who typed an outcome needs to see the four that exist.
	for _, want := range []string{"worked", "worked-with-caveats", "abandoned", "migrated-away"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("refusal does not name %q: %s", want, body)
		}
	}
	var n int
	st.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM field_reports`).Scan(&n)
	if n != 0 {
		t.Errorf("field_reports = %d after a refused report, want 0", n)
	}
}

func TestFieldReportWriteNeedsASuccessorWhenMigratedAway(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	owner := registerAndLogin(t, srv, "frep-migrated")
	authJSON(t, srv, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "frep3", "name": "frep3", "description": "x"})

	code, body := fileReport(t, srv, owner, "frep3", map[string]any{"outcome": store.ReportMigrated})
	if code != http.StatusBadRequest {
		t.Fatalf("migrated-away with no successor: got %d (%s), want 400", code, body)
	}
	if !strings.Contains(string(body), "migrated_to") {
		t.Errorf("refusal does not name the missing field: %s", body)
	}
	var n int
	st.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM field_reports`).Scan(&n)
	if n != 0 {
		t.Errorf("field_reports = %d after a refused report, want 0", n)
	}

	// With the successor named it lands, so the guard is the missing field and not the outcome.
	if code, body := fileReport(t, srv, owner, "frep3", map[string]any{
		"outcome": store.ReportMigrated, "migrated_to": "shinier-thing"}); code != http.StatusCreated {
		t.Fatalf("migrated-away with a successor: got %d (%s), want 201", code, body)
	}
}

func TestFieldReportWriteNeedsAnAccount(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	owner := registerAndLogin(t, srv, "frep-anon")
	authJSON(t, srv, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "frep4", "name": "frep4", "description": "x"})

	if code, body := fileReport(t, srv, "", "frep4", map[string]any{"outcome": store.ReportWorked}); code != http.StatusUnauthorized {
		t.Errorf("anonymous file: got %d (%s), want 401", code, body)
	}
	var n int
	st.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM field_reports`).Scan(&n)
	if n != 0 {
		t.Errorf("field_reports = %d after an anonymous attempt, want 0", n)
	}
}

func TestFieldReportReplyIsMarkedByMembershipNotByTheBody(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	owner := registerAndLogin(t, srv, "frep-badge")
	outsider := registerAndLogin(t, srv, "frep-outsider")
	authJSON(t, srv, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "frep5", "name": "frep5", "description": "x"})

	code, body := fileReport(t, srv, owner, "frep5", map[string]any{
		"outcome": store.ReportAbandoned, "caveats": "the config refuses to load"})
	if code != http.StatusCreated {
		t.Fatalf("file: %d %s", code, body)
	}
	var report store.FieldReport
	json.Unmarshal(body, &report)
	reply := "/api/v1/projects/frep5/field-reports/" + strconv.FormatInt(report.ID, 10) + "/responses"

	// The project's member (its creator) replies: owner badge, resolved server-side.
	code, body = authJSON(t, srv, http.MethodPost, reply, owner, map[string]any{"body": "tracked as #41"})
	if code != http.StatusCreated {
		t.Fatalf("owner reply: %d %s", code, body)
	}
	var resp store.FieldOwnerResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if !resp.IsOwner {
		t.Errorf("a project member's reply is not marked as the owner's: %s", body)
	}

	// A signed-in non-member replies, and CLAIMS to be the owner. The claim must not survive:
	// is_owner's entire value is that the server is the one who decides.
	code, body = authJSON(t, srv, http.MethodPost, reply, outsider,
		map[string]any{"body": "I saw this too", "is_owner": true})
	if code != http.StatusCreated {
		t.Fatalf("outsider reply: %d %s", code, body)
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if resp.IsOwner {
		t.Errorf("a non-member's reply carries the owner badge because the body asked for it: %s", body)
	}
	var isOwner int
	if err := st.QueryRowContext(context.Background(),
		`SELECT is_owner FROM field_report_responses WHERE id=?`, resp.ID).Scan(&isOwner); err != nil {
		t.Fatalf("read stored badge: %v", err)
	}
	if isOwner != 0 {
		t.Errorf("stored is_owner = %d for a non-member's reply, want 0", isOwner)
	}
}

func TestFieldReportReplyRefusesAReportFromAnotherProject(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	owner := registerAndLogin(t, srv, "frep-cross")
	authJSON(t, srv, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "frep6", "name": "frep6", "description": "x"})
	authJSON(t, srv, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "frep7", "name": "frep7", "description": "y"})

	code, body := fileReport(t, srv, owner, "frep6", map[string]any{"outcome": store.ReportWorked})
	if code != http.StatusCreated {
		t.Fatalf("file: %d %s", code, body)
	}
	var report store.FieldReport
	json.Unmarshal(body, &report)

	// The report is real, and the URL names a project the caller may write to -- the only thing
	// that can refuse is the proof that the report belongs to the project in the path.
	path := "/api/v1/projects/frep7/field-reports/" + strconv.FormatInt(report.ID, 10) + "/responses"
	if code, body := authJSON(t, srv, http.MethodPost, path, owner, map[string]any{"body": "hi"}); code != http.StatusNotFound {
		t.Fatalf("reply through another project's URL: got %d (%s), want 404", code, body)
	}
	var n int
	st.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM field_report_responses`).Scan(&n)
	if n != 0 {
		t.Errorf("responses = %d after a refused reply, want 0", n)
	}
}

func TestFieldReportReplyRefusesARemovedReport(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	owner := registerAndLogin(t, srv, "frep-removed")
	outsider := registerAndLogin(t, srv, "frep-remover")
	authJSON(t, srv, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "frep8", "name": "frep8", "description": "x"})

	code, body := fileReport(t, srv, owner, "frep8", map[string]any{"outcome": store.ReportAbandoned})
	if code != http.StatusCreated {
		t.Fatalf("file: %d %s", code, body)
	}
	var report store.FieldReport
	json.Unmarshal(body, &report)

	// Removal is §4.7's quorum act and the store refuses a project member for it, so the remover
	// here is a signed-in non-member: the handler has no route for this yet, which is why the
	// removal goes through the store -- the reply guard is what is under test.
	uid := userIDFor(t, st, "frep-remover")
	if err := st.RemoveFieldReport(context.Background(), report.ID, uid, "fabricated"); err != nil {
		t.Fatalf("RemoveFieldReport: %v", err)
	}

	path := "/api/v1/projects/frep8/field-reports/" + strconv.FormatInt(report.ID, 10) + "/responses"
	if code, body := authJSON(t, srv, http.MethodPost, path, outsider, map[string]any{"body": "hi"}); code != http.StatusNotFound {
		t.Fatalf("reply to a removed report: got %d (%s), want 404", code, body)
	}
	var n int
	st.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM field_report_responses`).Scan(&n)
	if n != 0 {
		t.Errorf("responses = %d after a refused reply, want 0", n)
	}
}
