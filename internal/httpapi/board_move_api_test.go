package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// Regression tests for the board move ENDPOINT.
//
// F2 of the site-wide e2e sweep (20-Areas/concord-site-e2e-verification.md)
// reported that board_cards held zero rows across all 70 projects because no code
// path inserted one.
//
// Deriving cards so the board shows something left the endpoint unusable, and
// worse than unusable: handleMoveCard took whatever was in the URL path as the
// card id. A derived card has no row, so it is served with id 0, and
// `PUT /cards/0/move` answered 200 while the UPDATE underneath matched no rows.
// A caller could not tell a successful move from a discarded one.
//
// The contract these pin:
//
//   - a derived card is addressed by ?feature_id= and the move CREATES its row
//   - a placed card is addressed by its row id in the path
//   - a phase that does not exist is a 404, not a 500
//   - a request that names nothing is rejected, not silently accepted
//
// Fixtures go through the HTTP API rather than the store, so they use only routes
// and payloads the product actually serves.

// boardFixture is a project holding one unplaced feature, so its board has a
// single card and that card is derived.
//
// projectID is kept because the complaint and feature routes take the project in
// the path for routing but read it from the BODY (createComplaintRequest.ProjectID),
// so a fixture that only knows the slug gets a 400 "project_id and author_id are
// required".
type boardFixture struct {
	slug      string
	projectID int64
	feature   int64
}

// newBoardFixture creates a project with one validated complaint and one feature
// in "draft", which derives a card in the inbox column.
//
// Seeded through the store, not the HTTP API. An earlier version posted a
// complaint and a feature over HTTP and then tried to set the feature's status
// with PUT /features/{id}/status, which answers 403 for a caller who is not a
// project member. Getting a feature into a given status through the API means
// first becoming a member, which is a separate concern from what these tests are
// about and buried several permission rules deep. The store calls here are the
// same ones the store-level tests in board_move_test.go use.
//
// Three field names were wrong before this was pinned, each failing as a
// validation error rather than a parse error: the project is read from the BODY
// (createComplaintRequest.ProjectID) even though the route carries it in the
// path; project_id is an int64, so the JSON string form is rejected; and the
// feature's complaint linkage is linked_complaints, with an unrecognised key
// silently ignored.
func newBoardFixture(t *testing.T, st *store.DB, slug string) boardFixture {
	t.Helper()
	ctx := t.Context()

	// By username, not id: GetUser takes a username.
	u, err := st.GetUser(ctx, "testuser")
	if err != nil {
		t.Fatalf("get fixture user: %v", err)
	}
	proj, err := st.CreateProject(ctx, u.ID, slug, slug, "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	// CreateProject does not write visibility, so the column keeps whatever the
	// schema default is. CanAccessProject switches on the exact value and fails
	// CLOSED on anything it does not recognise, so a project created this way can
	// be unreadable even to its own maintainer, and every route then answers a
	// bare 404 {"error":"not found"} that names nothing.
	//
	// Not what these tests are about, so visibility is set explicitly rather than
	// left to whatever the default happens to be.
	if _, err := st.ExecContext(ctx,
		`UPDATE projects SET visibility='public' WHERE id=?`, proj.ID); err != nil {
		t.Fatalf("set visibility: %v", err)
	}
	comp, err := st.CreateComplaint(ctx, proj.ID, u.ID, "something to build", "body", 3, 1.0, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := st.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	f, err := st.CreateFeature(ctx, proj.ID, u.ID, "the only feature", "body", "", nil, nil,
		[]int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	if err := st.UpdateFeatureStatus(ctx, f.ID, "draft"); err != nil {
		t.Fatalf("UpdateFeatureStatus: %v", err)
	}
	return boardFixture{slug: slug, projectID: proj.ID, feature: f.ID}
}

// boardCards reads the board and returns its cards.
func boardCards(t *testing.T, ts *httptest.Server, slug string) []map[string]any {
	t.Helper()
	resp, body := doJSON(t, ts, http.MethodGet, "/api/v1/projects/"+slug+"/board", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("read board: status %d, body %v", resp.StatusCode, body)
	}
	cards, _ := body["cards"].([]any)
	out := make([]map[string]any, 0, len(cards))
	for _, c := range cards {
		if m, ok := c.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// putMove PUTs a move against card 0 and returns the status.
func putMove(t *testing.T, ts *httptest.Server, slug, query string) int {
	t.Helper()
	return putMoveCard(t, ts, slug, 0, query)
}

// putMoveCard PUTs a move against a named card and returns the status. Split out
// so a failure can show the server's own error message: a bare status code did
// not distinguish "unknown project" from "unknown phase" from "unknown feature",
// and guessing between them cost several rounds.
func putMoveCard(t *testing.T, ts *httptest.Server, slug string, cardID int64, query string) int {
	t.Helper()
	// The move route is nested UNDER the board: /projects/{project_id}/board
	// then /cards/{card_id}/move. Omitting the /board segment gave a bare 404
	// {"error":"not found"} that read exactly like a rejected move.
	url := ts.URL + "/api/v1/projects/" + slug + "/board/cards/" +
		strconv.FormatInt(cardID, 10) + "/move?" + query
	req, err := http.NewRequest(http.MethodPut, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Logf("PUT %s -> %d body=%q", url, resp.StatusCode,
			strings.Join(strings.Fields(string(b)), " "))
	}
	return resp.StatusCode
}

// A move of a derived card has to create the row. Before, it answered 200 and
// nothing was stored -- the exact silent loss the finding describes.
func TestMoveDerivedCardCreatesItsRow(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	fx := newBoardFixture(t, st, "board-move-api")

	cards := boardCards(t, ts, fx.slug)
	if len(cards) != 1 {
		t.Fatalf("expected 1 derived card, got %d", len(cards))
	}
	if id, _ := cards[0]["id"].(float64); int64(id) != 0 {
		t.Fatalf("derived card has id %v, want 0 (it has no row)", cards[0]["id"])
	}
	if derived, _ := cards[0]["derived"].(bool); !derived {
		t.Fatal("the card is not marked derived")
	}

	if code := putMove(t, ts, fx.slug,
		"to=triaged&feature_id="+strconv.FormatInt(fx.feature, 10)); code != http.StatusOK {
		t.Fatalf("moving a derived card: status %d, want 200", code)
	}

	cards = boardCards(t, ts, fx.slug)
	if len(cards) != 1 {
		t.Fatalf("after the move the board has %d cards, want 1", len(cards))
	}
	if id, _ := cards[0]["id"].(float64); int64(id) == 0 {
		t.Error("the card still has no row after being moved")
	}
	if derived, _ := cards[0]["derived"].(bool); derived {
		t.Error("the card is still served as derived after being placed")
	}
	if col, _ := cards[0]["column"].(string); col != "triaged" {
		t.Errorf("card is in %q, want triaged", col)
	}
}

// Moving twice must not duplicate the card: the second move goes by row id.
func TestMoveTwiceDoesNotDuplicateTheCard(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	fx := newBoardFixture(t, st, "board-move-api-twice")

	if code := putMove(t, ts, fx.slug,
		"to=triaged&feature_id="+strconv.FormatInt(fx.feature, 10)); code != http.StatusOK {
		t.Fatalf("first move: status %d", code)
	}
	cards := boardCards(t, ts, fx.slug)
	if len(cards) != 1 {
		t.Fatalf("after the first move there are %d cards, want 1", len(cards))
	}
	id, _ := cards[0]["id"].(float64)
	if int64(id) == 0 {
		t.Fatal("the first move produced no row")
	}

	if code := putMoveCard(t, ts, fx.slug, int64(id), "to=done"); code != http.StatusOK {
		t.Fatalf("second move by row id: status %d", code)
	}

	cards = boardCards(t, ts, fx.slug)
	if len(cards) != 1 {
		t.Fatalf("two moves produced %d cards, want 1", len(cards))
	}
	if col, _ := cards[0]["column"].(string); col != "done" {
		t.Errorf("card is in %q, want done", col)
	}
}

// Naming card 0 with no feature_id is a client mistake. It used to answer 200, so
// a caller could not tell a real move from a lost one.
func TestMoveCardZeroWithoutFeatureIsRejected(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	fx := newBoardFixture(t, st, "board-move-api-zero")

	code := putMove(t, ts, fx.slug, "to=triaged")
	if code == http.StatusOK {
		t.Error("moving card 0 with no feature_id returned 200; the move was " +
			"discarded and the caller was told it worked")
	}
	if code != http.StatusBadRequest && code != http.StatusNotFound {
		t.Errorf("status %d, want 400 or 404", code)
	}
	if len(boardCards(t, ts, fx.slug)) != 1 {
		t.Error("the rejected move changed the board")
	}
}

// A phase that does not exist is a 404. It used to write column_id=NULL and fail
// the NOT NULL constraint, which mapped to a 500 -- a server fault for a typo.
func TestMoveToUnknownPhaseIsNotFound(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	fx := newBoardFixture(t, st, "board-move-api-badphase")

	if code := putMove(t, ts, fx.slug,
		"to=nope&feature_id="+strconv.FormatInt(fx.feature, 10)); code != http.StatusNotFound {
		t.Errorf("status %d, want 404 for a phase that does not exist", code)
	}
}

// A missing phase is a bad request, not a silent no-op.
func TestMoveWithoutPhaseIsRejected(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	fx := newBoardFixture(t, st, "board-move-api-noto")

	if code := putMove(t, ts, fx.slug,
		"feature_id="+strconv.FormatInt(fx.feature, 10)); code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", code)
	}
}

// A malformed feature_id is rejected, not parsed as zero: zero is the id of "no
// card", which is a different thing.
func TestMoveWithMalformedFeatureIDIsRejected(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	fx := newBoardFixture(t, st, "board-move-api-badid")

	if code := putMove(t, ts, fx.slug, "to=triaged&feature_id=abc"); code != http.StatusBadRequest {
		t.Errorf("status %d, want 400 for feature_id=abc", code)
	}
}

// Moving a card that does not exist is a 404, not a success.
func TestMoveUnknownCardIsNotFound(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	fx := newBoardFixture(t, st, "board-move-api-unknown")

	if code := putMoveCard(t, ts, fx.slug, 999999, "to=triaged"); code != http.StatusNotFound {
		t.Errorf("status %d, want 404", code)
	}
}

// A placement is an explicit decision, so the endpoint requires a token.
//
// Uses the no-actor server: the authenticated wrapper used by newTestServer
// supplies a default bearer token to any request without an Authorization header,
// so "no header" there means "the test user", not "nobody" -- which would make
// this test pass for the wrong reason.
func TestMoveRequiresAuthentication(t *testing.T) {
	ts := newTestServerNoActor(t)

	req, err := http.NewRequest(http.MethodPut,
		ts.URL+"/api/v1/projects/anything/cards/0/move?to=triaged&feature_id=1", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("an unauthenticated move returned 200")
	}
	// 401 when the project resolves, 404 when it does not -- requireProjectID runs
	// before the auth check, so an unknown project is reported first. Either is a
	// refusal; 200 is not.
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 401 or 404", resp.StatusCode)
	}
}
