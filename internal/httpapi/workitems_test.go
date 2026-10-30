package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// §4.5.7's work items over HTTP.
//
// The store tests already prove the database refuses a second live claim. These tests exist to prove
// the ROUTES enforce the same three rules, because each one has a way of passing a store test and
// failing here: the read can be walled behind auth, the claim can be reachable without a role, and
// the label write can be reachable by anyone with an account.

type workItemsFixture struct {
	ts          *httptest.Server
	db          *store.DB
	slug        string
	projectID   int64
	readyID     int64
	draftID     int64
	contributor string // Authorization header for a joined non-owner
	stranger    string // Authorization header for a signed-in non-member
}

func newWorkItemsFixture(t *testing.T) workItemsFixture {
	t.Helper()
	ts, st := newTestServerWithStore(t)
	ctx := context.Background()

	owner, err := st.GetUser(ctx, "testuser")
	if err != nil {
		t.Fatalf("read the test user: %v", err)
	}
	proj, err := st.CreateProject(ctx, owner.ID, "workitems", "Work items", "", "collective", "")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	complaint, err := st.CreateComplaint(ctx, proj.ID, owner.ID, "The export drops the last row", "body", 3, 1.0, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := st.ValidateComplaint(ctx, complaint.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	ready, err := st.CreateFeature(ctx, proj.ID, owner.ID, "Make the export complete", "body", "S", nil, nil, []int64{complaint.ID})
	if err != nil {
		t.Fatalf("CreateFeature(ready): %v", err)
	}
	if err := st.UpdateFeatureStatus(ctx, ready.ID, "ready"); err != nil {
		t.Fatalf("UpdateFeatureStatus(ready): %v", err)
	}
	draft, err := st.CreateFeature(ctx, proj.ID, owner.ID, "Only a proposal", "body", "M", nil, nil, []int64{complaint.ID})
	if err != nil {
		t.Fatalf("CreateFeature(draft): %v", err)
	}

	tokenFor := func(name string) string {
		u, err := st.CreateUser(ctx, name, name)
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", name, err)
		}
		if err := st.SetPassword(ctx, name, "password-"+name); err != nil {
			t.Fatalf("SetPassword(%s): %v", name, err)
		}
		_, tok, err := st.Login(ctx, name, "password-"+name)
		if err != nil {
			t.Fatalf("Login(%s): %v", name, err)
		}
		_ = u
		return "Bearer " + tok
	}
	contributorTok := tokenFor("helper")
	contributorUser, err := st.GetUser(ctx, "helper")
	if err != nil {
		t.Fatalf("read helper: %v", err)
	}
	if err := st.JoinProject(ctx, proj.ID, contributorUser.ID); err != nil {
		t.Fatalf("JoinProject: %v", err)
	}

	return workItemsFixture{
		ts: ts, db: st, slug: proj.Slug, projectID: proj.ID,
		readyID: ready.ID, draftID: draft.ID,
		contributor: contributorTok, stranger: tokenFor("outsider"),
	}
}

func (f workItemsFixture) path(suffix string) string {
	return "/api/v1/projects/" + f.slug + suffix
}

// The read is public on purpose: a work list nobody can read without an account cannot be the
// answer to "what can I contribute", because the person deciding whether to sign up is exactly the
// person who cannot read it yet.
func TestWorkItemsAreReadableWithoutAnAccount(t *testing.T) {
	f := newWorkItemsFixture(t)
	code, body := authJSON(t, f.ts, http.MethodGet, f.path("/work-items"), anonymousMarker, nil)
	if code != http.StatusOK {
		t.Fatalf("anonymous work-item read = %d: %s", code, body)
	}
	if !strings.Contains(string(body), "Make the export complete") {
		t.Fatalf("the ready feature is missing from the anonymous read: %s", body)
	}
	if strings.Contains(string(body), "Only a proposal") {
		t.Fatalf("a draft is offered as work somebody can pick up: %s", body)
	}
}

func TestClaimTakesTheItemAndASecondClaimantGets409(t *testing.T) {
	f := newWorkItemsFixture(t)
	claim := f.path("/features/" + itoa(f.readyID) + "/claim")

	code, body := authJSON(t, f.ts, http.MethodPost, claim, f.contributor, nil)
	if code != http.StatusCreated {
		t.Fatalf("first claim = %d, want 201: %s", code, body)
	}
	var item map[string]any
	if err := json.Unmarshal(body, &item); err != nil {
		t.Fatalf("claim response is not JSON: %s", body)
	}
	if item["claimed_by"] == nil {
		t.Fatalf("the claim response does not say who holds it: %s", body)
	}

	// the default testuser is the project's creator, so a 409 here is not a permission answer.
	resp, _ := doJSON(t, f.ts, http.MethodPost, claim, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second claim = %d, want 409", resp.StatusCode)
	}
}

func TestClaimRequiresMembership(t *testing.T) {
	f := newWorkItemsFixture(t)
	code, body := authJSON(t, f.ts, http.MethodPost,
		f.path("/features/"+itoa(f.readyID)+"/claim"), f.stranger, nil)
	if code != http.StatusForbidden {
		t.Fatalf("a non-member claimed work: %d, want 403: %s", code, body)
	}
	// control: the same call by a member must still work, or the 403 above proves nothing.
	code, body = authJSON(t, f.ts, http.MethodPost,
		f.path("/features/"+itoa(f.readyID)+"/claim"), f.contributor, nil)
	if code != http.StatusCreated {
		t.Fatalf("control claim by a member = %d, want 201: %s", code, body)
	}
}

func TestClaimRefusesADraftOverHTTP(t *testing.T) {
	f := newWorkItemsFixture(t)
	code, body := authJSON(t, f.ts, http.MethodPost,
		f.path("/features/"+itoa(f.draftID)+"/claim"), f.contributor, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("claiming a draft = %d, want 400: %s", code, body)
	}
}

func TestLabelIsMaintainerOnly(t *testing.T) {
	f := newWorkItemsFixture(t)
	path := f.path("/features/" + itoa(f.readyID) + "/work-item")
	payload := map[string]any{"difficulty": "M", "time_budget_minutes": 120, "needs_mentor": true}

	code, body := authJSON(t, f.ts, http.MethodPut, path, f.contributor, payload)
	if code != http.StatusForbidden {
		t.Fatalf("a contributor sized a task: %d, want 403: %s", code, body)
	}

	resp, sized := doJSON(t, f.ts, http.MethodPut, path, payload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the owner could not size a task: %d", resp.StatusCode)
	}
	raw, _ := json.Marshal(sized)
	if !strings.Contains(string(raw), `"difficulty":"M"`) {
		t.Fatalf("the label did not round-trip: %s", raw)
	}
	// and the read now shows the size, which is the whole point of writing it. The response is
	// pretty-printed, so the assertion carries the space writeJSON emits.
	_, list := authJSON(t, f.ts, http.MethodGet, f.path("/work-items"), anonymousMarker, nil)
	if !strings.Contains(string(list), `"time_budget_minutes": 120`) {
		t.Fatalf("the label is stored but not read back: %s", list)
	}
}

func TestReleaseFreesTheItemForSomebodyElse(t *testing.T) {
	f := newWorkItemsFixture(t)
	claim := f.path("/features/" + itoa(f.readyID) + "/claim")

	if code, body := authJSON(t, f.ts, http.MethodPost, claim, f.contributor, nil); code != http.StatusCreated {
		t.Fatalf("claim = %d: %s", code, body)
	}
	if code, body := authJSON(t, f.ts, http.MethodDelete, claim, f.contributor, nil); code != http.StatusOK {
		t.Fatalf("release = %d: %s", code, body)
	}
	// the owner (a maintainer, hence also a contributor) can now take it.
	resp, _ := doJSON(t, f.ts, http.MethodPost, claim, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("claim after release = %d, want 201", resp.StatusCode)
	}
	// releasing twice is 404, not 200: there is no claim of yours to release, and saying OK would
	// tell the caller they withdrew something that somebody else now holds.
	if code, body := authJSON(t, f.ts, http.MethodDelete, claim, f.contributor, nil); code != http.StatusNotFound {
		t.Fatalf("releasing a claim you no longer hold = %d, want 404: %s", code, body)
	}
}
