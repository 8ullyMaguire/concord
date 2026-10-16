package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// The two verdicts on a complaint: §M1's lifecycle is
// `Open -> Validated -> Linked -> Closed / Rejected`, and before this pass the API could
// only ever reach `Validated`.
//
// Two defects, one of them invisible for the whole life of the feature:
//
//  1. `POST …/complaints/{id}/reject` did not exist. `store.RejectComplaint` had been
//     written for M1, closed the complaint, applied the spam penalty and refused a
//     non-open row -- and no production code and no test called it, ever. A spam
//     complaint could be ignored or VALIDATED and never recorded as rejected.
//  2. Both verdicts were gated on `getActorID(r) != 0` -- "is there an actor?" -- while
//     the spec says "Validation is a collaborator action" (§M1). Since guests exist,
//     that gate admits every nameless visitor on the instance. It also never read the
//     project half of the URL, so a complaint id was reachable through ANY project's
//     URL.
//
// The tests below assert the gate from both sides, because a gate tested only from the
// allowed side is a gate that may admit everybody.

type verdictActor struct {
	id    int64
	token string
}

// verdictFixture makes a project with one open complaint and returns it along with an
// account that is signed in but is NOT a member of the project.
func verdictFixture(t *testing.T, slug string) (*httptest.Server, *store.DB, string, int64, verdictActor) {
	t.Helper()
	ts, st := newTestServerWithStore(t)
	// auth "" on this harness means ANONYMOUS (it sends the X-No-Auth sentinel), so the
	// project's owner is named explicitly.
	owner := registerAndLogin(t, ts, slug+"-owner")

	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": slug, "name": slug, "description": "verdict fixture"})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create project %s: %d %s", slug, code, body)
	}
	pid := bodyInt64(t, body, "id")
	cid := fileComplaint(t, ts, owner, slug, pid, "the exporter drops the last row")

	// The outsider is a real registered account, so the request is authenticated and the
	// server can tell "no actor" from "an actor who is not a member of this project".
	out, err := st.CreateUser(context.Background(), slug+"-outsider", "Verdict Outsider")
	if err != nil {
		t.Fatalf("create outsider: %v", err)
	}
	return ts, st, slug, cid, verdictActor{id: out.ID, token: "Bearer " + tokenFor(t, st, out.Username)}
}

// fileComplaint files a complaint through the API (the payload the project page posts,
// which carries project_id and author_id in the body) and returns its id.
func fileComplaint(t *testing.T, ts *httptest.Server, owner, slug string, pid int64, title string) int64 {
	t.Helper()
	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/"+slug+"/complaints", owner,
		map[string]any{"project_id": pid, "title": title, "body": "one row short",
			"severity": 3, "frequency": 2.0})
	if code != http.StatusCreated {
		t.Fatalf("file complaint in %s: %d %s", slug, code, body)
	}
	id := bodyInt64(t, body, "id")
	if id == 0 {
		t.Fatalf("complaint created without an id: %s", body)
	}
	return id
}

// complaintStatus reads the row the handlers write, so the assertion is about what was
// STORED and not only about what was answered.
func complaintStatus(t *testing.T, st *store.DB, cid int64) string {
	t.Helper()
	c, err := st.GetComplaint(context.Background(), cid)
	if err != nil {
		t.Fatalf("read complaint %d: %v", cid, err)
	}
	return c.Status
}

// TestTheComplaintVerdictsNeedAProjectMember walks both verdicts from both sides.
func TestTheComplaintVerdictsNeedAProjectMember(t *testing.T) {
	ts, st, slug, cid, outsider := verdictFixture(t, "verdict-gate")
	id := strconv.FormatInt(cid, 10)

	for _, verdict := range []string{"validate", "reject"} {
		path := "/api/v1/projects/" + slug + "/complaints/" + id + "/" + verdict

		// (1) No credential at all: 401. A refusal that answers 403 to an anonymous
		// caller sends them off to fix the wrong thing.
		if code, body := authJSON(t, ts, http.MethodPost, path, "", map[string]any{}); code != http.StatusUnauthorized {
			t.Errorf("%s without a credential: got %d (%s), want 401", verdict, code, body)
		}

		// (2) Signed in, not a member: 403, and the refusal names what was wanted.
		code, body := authJSON(t, ts, http.MethodPost, path, outsider.token, map[string]any{})
		if code != http.StatusForbidden {
			t.Errorf("%s as a non-member: got %d (%s), want 403 -- a signed-in stranger is "+
				"not the collaborator the spec names", verdict, code, body)
		}
		if !strings.Contains(string(body), "contributor") {
			t.Errorf("%s as a non-member was refused without naming the role it wanted: %s",
				verdict, body)
		}
	}

	// Neither refusal wrote anything.
	if status := complaintStatus(t, st, cid); status != "open" {
		t.Fatalf("a refused verdict changed the complaint: status is now %q", status)
	}
}

// TestAMemberMayRecordEitherVerdict is the other direction, and the one that proves the
// Rejected state is reachable at all.
func TestAMemberMayRecordEitherVerdict(t *testing.T) {
	ts, st, slug, cid, outsider := verdictFixture(t, "verdict-member")
	owner := "Bearer " + tokenFor(t, st, "testuser")
	proj, err := st.GetProject(context.Background(), slug)
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	// Joining is what makes somebody a collaborator here (`JoinProject` grants the
	// contributor role) -- which is the property that makes this gate usable on a new
	// project: it does not demand a prior qualifying contribution, because one of the
	// qualifying contributions is a validated complaint that led to a shipped solution.
	if err := st.JoinProject(context.Background(), proj.ID, outsider.id); err != nil {
		t.Fatalf("join: %v", err)
	}

	code, body := authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/"+slug+"/complaints/"+strconv.FormatInt(cid, 10)+"/validate",
		outsider.token, map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("a member validating: got %d (%s), want 200", code, body)
	}
	if !strings.Contains(string(body), "validated") {
		t.Errorf("the response does not echo the state written: %s", body)
	}
	if status := complaintStatus(t, st, cid); status != "validated" {
		t.Errorf("the complaint is %q after a validate, want validated", status)
	}

	// A second complaint, rejected by the same member: the state that had no route.
	cid2 := fileComplaint(t, ts, owner, slug, proj.ID, "buy my unrelated product, links inside")
	code, body = authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/"+slug+"/complaints/"+strconv.FormatInt(cid2, 10)+"/reject",
		outsider.token, map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("a member rejecting: got %d (%s), want 200", code, body)
	}
	if !strings.Contains(string(body), "rejected") {
		t.Errorf("the response does not echo the state written: %s", body)
	}
	// §M1's Rejected state, reachable for the first time.
	if status := complaintStatus(t, st, cid2); status != "rejected" {
		t.Errorf("the complaint is %q after a reject, want rejected", status)
	}

	// Ending it refuses to run twice: the store refuses a verdict on a closed row, and
	// the API reports that as 400 rather than as a silent no-op or a success.
	code, body = authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/"+slug+"/complaints/"+strconv.FormatInt(cid2, 10)+"/reject",
		outsider.token, map[string]any{})
	if code != http.StatusBadRequest {
		t.Errorf("rejecting an already-rejected complaint: got %d (%s), want 400", code, body)
	}
}

// TestAVerdictOnAnotherProjectsComplaintIsNotFound holds the URL's two halves together.
//
// `…/projects/{project_id}/complaints/{id}/…` names two things, and the handlers used to
// read one of them: a complaint id is global, so any project's URL reached any
// complaint. The answer is the same 404 a nonexistent complaint gets, because a 403
// would confirm the id is real.
func TestAVerdictOnAnotherProjectsComplaintIsNotFound(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	owner := registerAndLogin(t, ts, "verdict-two-owner")
	pidA := createProjectReturningID(t, ts, owner, "verdict-a")
	createProjectReturningID(t, ts, owner, "verdict-b")
	// The complaint lives in verdict-a; verdict-b's URL asks for it.
	cid := fileComplaint(t, ts, owner, "verdict-a", pidA, "the exporter drops the last row")
	id := strconv.FormatInt(cid, 10)

	for _, verdict := range []string{"validate", "reject"} {
		code, body := authJSON(t, ts, http.MethodPost,
			"/api/v1/projects/verdict-b/complaints/"+id+"/"+verdict, owner, map[string]any{})
		if code != http.StatusNotFound {
			t.Errorf("%s through the WRONG project's URL: got %d (%s), want 404 -- a "+
				"complaint id is global and the URL's project is not decoration",
				verdict, code, body)
		}
	}
	if status := complaintStatus(t, st, cid); status != "open" {
		t.Errorf("the cross-project verdict was written anyway: status is %q", status)
	}
}

// createProjectReturningID makes a project through the API and returns its id, for tests
// that need the numeric half of the complaint payload.
func createProjectReturningID(t *testing.T, ts *httptest.Server, owner, slug string) int64 {
	t.Helper()
	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": slug, "name": slug, "description": "verdict fixture"})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create project %s: %d %s", slug, code, body)
	}
	return bodyInt64(t, body, "id")
}
