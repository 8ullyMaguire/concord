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

// §354's extra link at the edge.
//
// `store.LinkComplaint` had a complete body and no caller in any revision, so a feature could
// only ever be linked to the complaints it was CREATED with: `CreateFeature` writes the join,
// and nothing else could. The write below is that caller, and the guards it needs are the ones
// no route-level check can supply -- the complaint arrives in the request BODY, so nothing above
// this layer knows which project it belongs to.
//
// Every test asserts the stored state as well as the answer: a handler that returns 404 and
// writes the row anyway passes an error-code assertion.

type linkFixture struct {
	ts     *httptest.Server
	st     *store.DB
	slug   string
	pid    int64
	featID int64
	linked int64 // validated, attached at creation
	spare  int64 // validated, unattached: the complaint the extra link exists for
	open   int64 // open, so linking it must be refused
}

func setupLinkRouteFixture(t *testing.T, slug string) linkFixture {
	t.Helper()
	ts, st := newTestServerWithStore(t)
	owner := registerAndLogin(t, ts, slug+"-owner")

	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": slug, "name": slug, "description": "the extra link"})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create project: %d %s", code, body)
	}
	pid := bodyInt64(t, body, "id")

	var fx linkFixture
	fx.ts, fx.st, fx.slug, fx.pid = ts, st, slug, pid
	fx.linked = fileComplaint(t, ts, owner, slug, pid, "the exporter drops the last row")
	fx.spare = fileComplaint(t, ts, owner, slug, pid, "the importer writes duplicates")
	fx.open = fileComplaint(t, ts, owner, slug, pid, "something smells off")

	for _, id := range []int64{fx.linked, fx.spare} {
		if err := st.ValidateComplaint(context.Background(), id); err != nil {
			t.Fatalf("ValidateComplaint(%d): %v", id, err)
		}
	}

	code, body = authJSON(t, ts, http.MethodPost, "/api/v1/projects/"+slug+"/features", owner,
		map[string]any{"project_id": pid, "title": "Make the export complete",
			"body": "rows are missing", "effort": "M",
			"linked_complaints": []int64{fx.linked}})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create feature: %d %s", code, body)
	}
	fx.featID = bodyInt64(t, body, "id")
	if fx.featID == 0 {
		t.Fatalf("feature created without an id: %s", body)
	}
	return fx
}

// decodedBody unmarshals an authJSON response. authJSON answers the raw bytes (its callers
// mostly read one field with bodyInt64 or match a substring), and this test needs the list it
// carries.
func decodedBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return m
}

// linkedComplaintIDs reads the join table's contents for a feature, so an assertion about the
// link is about the ROW and not only about the response body.
func linkedComplaintIDs(t *testing.T, st *store.DB, featureID int64) []int64 {
	t.Helper()
	cs, err := st.GetFeatureComplaints(context.Background(), featureID)
	if err != nil {
		t.Fatalf("GetFeatureComplaints: %v", err)
	}
	ids := make([]int64, 0, len(cs))
	for _, c := range cs {
		ids = append(ids, c.ID)
	}
	return ids
}

// extraLinkOwner is the token of the project's owner, minted through the store: the fixture
// registered the account and the password is the harness's, so this does not depend on the
// login handler.
func (fx linkFixture) ownerToken(t *testing.T) string {
	t.Helper()
	return "Bearer " + tokenFor(t, fx.st, fx.slug+"-owner")
}

func (fx linkFixture) path() string {
	return "/api/v1/projects/" + fx.slug + "/features/" + strconv.FormatInt(fx.featID, 10) +
		"/complaints"
}

func TestTheExtraLinkAttachesAValidatedComplaint(t *testing.T) {
	fx := setupLinkRouteFixture(t, "extralink")
	owner := fx.ownerToken(t)

	if got := linkedComplaintIDs(t, fx.st, fx.featID); len(got) != 1 {
		t.Fatalf("fixture: linked complaints = %v, want just the created one", got)
	}

	code, body := authJSON(t, fx.ts, http.MethodPost, fx.path(), owner,
		map[string]any{"complaint_id": fx.spare})
	if code != http.StatusOK {
		t.Fatalf("attach: %d %s", code, body)
	}
	// The response carries the WHOLE list, because the page renders the section's count from
	// it: a response with only the new row would force the read-after-write this exists to
	// avoid.
	list, _ := decodedBody(t, body)["complaints"].([]any)
	if len(list) != 2 {
		t.Errorf("response lists %d complaints, want 2: %s", len(list), body)
	}

	got := linkedComplaintIDs(t, fx.st, fx.featID)
	if len(got) != 2 {
		t.Fatalf("linked complaints = %v, want 2", got)
	}
	found := false
	for _, id := range got {
		if id == fx.spare {
			found = true
		}
	}
	if !found {
		t.Errorf("complaint %d is not linked; linked = %v", fx.spare, got)
	}

	// And it is written down: an unlogged write is a change nobody can account for.
	var n int
	if err := fx.st.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE project_id=? AND action='link_complaint'
		 AND entity='feature' AND entity_id=?`, fx.pid, fx.featID).Scan(&n); err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if n != 1 {
		t.Errorf("audit rows for link_complaint = %d, want 1", n)
	}
}

func TestTheExtraLinkIsIdempotent(t *testing.T) {
	fx := setupLinkRouteFixture(t, "extralink-twice")
	owner := fx.ownerToken(t)

	for i := 0; i < 2; i++ {
		if code, body := authJSON(t, fx.ts, http.MethodPost, fx.path(), owner,
			map[string]any{"complaint_id": fx.spare}); code != http.StatusOK {
			t.Fatalf("attach call %d: %d %s", i+1, code, body)
		}
	}
	if got := linkedComplaintIDs(t, fx.st, fx.featID); len(got) != 2 {
		t.Errorf("linked complaints = %v, want 2 after attaching the same one twice", got)
	}
}

func TestTheExtraLinkNeedsAContributor(t *testing.T) {
	fx := setupLinkRouteFixture(t, "extralink-gate")
	payload := map[string]any{"complaint_id": fx.spare}

	// No credential: 401, so a nameless caller is told to sign in rather than to ask for a role.
	if code, body := authJSON(t, fx.ts, http.MethodPost, fx.path(), "", payload); code != http.StatusUnauthorized {
		t.Errorf("anonymous: got %d (%s), want 401", code, body)
	}

	// Signed in, not a member: 403 naming the role.
	if _, err := fx.st.CreateUser(context.Background(), "extralink-outsider", "Outsider"); err != nil {
		t.Fatalf("create outsider: %v", err)
	}
	outsider := "Bearer " + tokenFor(t, fx.st, "extralink-outsider")
	code, body := authJSON(t, fx.ts, http.MethodPost, fx.path(), outsider, payload)
	if code != http.StatusForbidden {
		t.Errorf("non-member: got %d (%s), want 403", code, body)
	}
	if !strings.Contains(string(body), "contributor") {
		t.Errorf("the refusal does not name the role it wanted: %s", body)
	}
	// Neither refusal linked anything.
	if got := linkedComplaintIDs(t, fx.st, fx.featID); len(got) != 1 {
		t.Errorf("linked complaints = %v after two refusals, want 1", got)
	}
}

func TestTheExtraLinkRefusesAComplaintFromAnotherProject(t *testing.T) {
	fx := setupLinkRouteFixture(t, "extralink-project")
	owner := fx.ownerToken(t)

	// A real second project with a real validated complaint, so the refusal can only be the
	// project rule and not "no such complaint".
	code, body := authJSON(t, fx.ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "extralink-other", "name": "other", "description": "elsewhere"})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create second project: %d %s", code, body)
	}
	otherPID := bodyInt64(t, body, "id")
	foreign := fileComplaint(t, fx.ts, owner, "extralink-other", otherPID, "the other one")
	if err := fx.st.ValidateComplaint(context.Background(), foreign); err != nil {
		t.Fatalf("ValidateComplaint(foreign): %v", err)
	}

	code, body = authJSON(t, fx.ts, http.MethodPost, fx.path(), owner,
		map[string]any{"complaint_id": foreign})
	if code != http.StatusNotFound {
		t.Fatalf("cross-project attach: got %d (%s), want 404", code, body)
	}
	// 404 and not 403: a 403 confirms the id is real, which is a statement about another
	// project's data.
	if got := linkedComplaintIDs(t, fx.st, fx.featID); len(got) != 1 {
		t.Errorf("linked complaints = %v after a refused attach, want 1", got)
	}
}

func TestTheExtraLinkRefusesAnOpenComplaint(t *testing.T) {
	fx := setupLinkRouteFixture(t, "extralink-open")
	owner := fx.ownerToken(t)

	code, body := authJSON(t, fx.ts, http.MethodPost, fx.path(), owner,
		map[string]any{"complaint_id": fx.open})
	if code != http.StatusBadRequest {
		t.Fatalf("attaching an open complaint: got %d (%s), want 400", code, body)
	}
	if got := linkedComplaintIDs(t, fx.st, fx.featID); len(got) != 1 {
		t.Errorf("linked complaints = %v after a refused attach, want 1", got)
	}
}

func TestTheExtraLinkNeedsAComplaintID(t *testing.T) {
	fx := setupLinkRouteFixture(t, "extralink-noid")
	owner := fx.ownerToken(t)

	// 400, not 404: an absent field is a malformed request, and answering "not found" would
	// send the reader looking for a complaint id they never sent.
	code, body := authJSON(t, fx.ts, http.MethodPost, fx.path(), owner, map[string]any{})
	if code != http.StatusBadRequest {
		t.Errorf("no complaint_id: got %d (%s), want 400", code, body)
	}
	if got := linkedComplaintIDs(t, fx.st, fx.featID); len(got) != 1 {
		t.Errorf("linked complaints = %v, want 1", got)
	}
}

func TestTheExtraLinkRefusesAFeatureFromAnotherProject(t *testing.T) {
	fx := setupLinkRouteFixture(t, "extralink-feature")
	owner := fx.ownerToken(t)

	code, body := authJSON(t, fx.ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "extralink-third", "name": "third", "description": "elsewhere"})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create second project: %d %s", code, body)
	}
	other := bodyInt64(t, body, "id")
	_ = other

	// The feature id addressed through ANOTHER project's URL, with a complaint that is
	// genuinely that other project's -- so the only guard that can refuse is the one that
	// proves the feature belongs to the project in the path.
	foreignComplaint := fileComplaint(t, fx.ts, owner, "extralink-third",
		bodyInt64(t, body, "id"), "the third one")
	if err := fx.st.ValidateComplaint(context.Background(), foreignComplaint); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	path := "/api/v1/projects/extralink-third/features/" +
		strconv.FormatInt(fx.featID, 10) + "/complaints"
	code, body = authJSON(t, fx.ts, http.MethodPost, path, owner,
		map[string]any{"complaint_id": foreignComplaint})
	if code != http.StatusNotFound {
		t.Fatalf("feature addressed through another project: got %d (%s), want 404", code, body)
	}
	if got := linkedComplaintIDs(t, fx.st, fx.featID); len(got) != 1 {
		t.Errorf("linked complaints = %v after a refused attach, want 1", got)
	}
}
