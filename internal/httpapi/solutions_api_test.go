package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// Solutions at the API boundary (§6.3-6.4).
//
// The store tests prove the scoring; these prove the workflow a solution actually
// travels: that a feature with no solutions answers with an empty board rather
// than a 404, that the board is readable without a token, that coverage claims
// and their contests go through, and that the two arenas cannot be crossed.

const solProject = "solwork"

// solutionFixture creates a project with one validated complaint and a feature
// traced to it, and returns the server, the feature id, and a second user's token.
//
// The feature is filed through the API so the fixture cannot accidentally create
// a state the product forbids -- §6.2 requires a feature to trace back to a
// validated complaint, and §5.2 stops the author judging their own solution, so a
// single-user fixture cannot express the ordinary case at all.
func solutionFixture(t *testing.T) (*httptest.Server, *store.DB, int64) {
	t.Helper()
	ts, st := newTestServerWithStore(t)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"`+solProject+`","name":"SolWork","description":"d"}`)

	// The complaint endpoint reads project_id and author_id from the body, which
	// is this API's existing shape rather than a requirement solutions invented.
	resp, body := postJSON(t, ts, "/api/v1/projects/"+solProject+"/complaints", `{
		"project_id":1,
		"title":"Export silently drops the last row",
		"body":"reproducible on 100% of exports",
		"severity":5,"frequency":3.0}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("complaint: %d %v", resp.StatusCode, body)
	}
	complaintID := bodyInt(t, body, "id")
	if resp, vbody := postJSON(t, ts, "/api/v1/projects/"+solProject+"/complaints/"+
		fmt.Sprint(complaintID)+"/validate", `{}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("validate: %d %v", resp.StatusCode, vbody)
	}

	resp, body = postJSON(t, ts, "/api/v1/projects/"+solProject+"/features", fmt.Sprintf(`{
		"project_id":1,
		"author_id":1,
		"title":"Export must be lossless",
		"body":"every row survives",
		"linked_complaints":[%d]}`, complaintID))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("feature: %d %v", resp.StatusCode, body)
	}
	return ts, st, bodyInt(t, body, "id")
}

// fileSolution posts one solution and returns its id.
func fileSolution(t *testing.T, ts *httptest.Server, featureID int64, title, typ, extra string, token ...string) int64 {
	t.Helper()
	payload := fmt.Sprintf(`{"title":%q,"type":%q%s}`, title, typ, extra)
	path := fmt.Sprintf("/api/v1/projects/%s/features/%d/solutions", solProject, featureID)
	// The token is required, not optional-in-practice: the harness's default
	// identity is the feature's author, and §5.2 refuses the feature's author
	// filing a solution. Naming it here keeps each test's actors explicit.
	var hdr []string
	if len(token) > 0 {
		hdr = []string{"Authorization", "Bearer " + token[0]}
	}
	resp, body := postJSON(t, ts, path, payload, hdr...)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("file solution %q: %d %v", title, resp.StatusCode, body)
	}
	sol, _ := body["solution"].(map[string]any)
	if sol == nil {
		t.Fatalf("no solution object in %v", body)
	}
	return bodyInt(t, sol, "id")
}

func TestAFeatureWithNoSolutionsHasAnEmptyBoard(t *testing.T) {
	// The normal state of a new feature. A 404 here reads as breakage, and a
	// client cannot tell "nobody has proposed one yet" from "this endpoint is
	// broken".
	ts, _, featureID := solutionFixture(t)
	resp, body := getJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/solutions", solProject, featureID))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200 (body=%v)", resp.StatusCode, body)
	}
	sols, ok := body["solutions"].([]any)
	if !ok {
		t.Fatalf("no solutions array in %v: a client cannot render an empty board", body)
	}
	if len(sols) != 0 {
		t.Errorf("board lists %d solutions for a feature nobody proposed for", len(sols))
	}
}

func TestTheSolutionBoardIsReadableWithoutAToken(t *testing.T) {
	// Reads unauthenticated, matching §6.1's duplicate panel and §2.5's vote log.
	// A ranking nobody can read is not a ranking anybody trusts, and a board that
	// demands a token is a board that does not get looked at.
	ts, _, featureID := solutionFixture(t)
	author := registerOn(t, ts, "solauthoranon")
	fileSolution(t, ts, featureID, "Rewrite the exporter", "build-new", "", author)

	// X-No-Auth is the harness's explicit anonymous opt-out.
	resp, body := getJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/solutions", solProject, featureID),
		"X-No-Auth", "1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("anonymous read: %d %v", resp.StatusCode, body)
	}
	sols, _ := body["solutions"].([]any)
	if len(sols) != 1 {
		t.Errorf("anonymous read returned %d solutions, want 1", len(sols))
	}
}

func TestFilingASolutionRequiresAToken(t *testing.T) {
	// The write side is the opposite: filing is attributed, so it needs an actor.
	ts, _, featureID := solutionFixture(t)
	resp, body := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/solutions", solProject, featureID),
		`{"title":"anonymous","type":"build-new"}`, "X-No-Auth", "1")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous filing: status %d, want 401 (body=%v)", resp.StatusCode, body)
	}
}

func TestTheFeatureAuthorCannotFileASolutionForIt(t *testing.T) {
	// §5.2, refused at write time with a message that says why, because a filer
	// who gets a bare 403 cannot tell which rule they hit.
	ts, _, featureID := solutionFixture(t)
	resp, body := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/solutions", solProject, featureID),
		`{"title":"mine","type":"build-new"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403 (body=%v)", resp.StatusCode, body)
	}
	detail, _ := body["detail"].(string)
	if detail == "" {
		t.Error("the refusal names no reason: a bare 403 is indistinguishable from a bug")
	}
}

func TestAnIntegrateExternalSolutionIsRefusedWithoutACatalogLink(t *testing.T) {
	// §6.3's "integrate-external (a catalog link)". The message has to be a 400
	// with the reason, not a schema error, so the filer knows what to add.
	ts, _, featureID := solutionFixture(t)
	registerOn(t, ts, "solauthor1")

	resp, body := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/solutions", solProject, featureID),
		`{"title":"Use their library","type":"integrate-external"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (body=%v)", resp.StatusCode, body)
	}
	detail, _ := body["detail"].(string)
	if !contains(detail, "catalog link") {
		t.Errorf("detail = %q, want it to name the missing catalog link", detail)
	}
}

func TestCoverageClaimsAndContestsGoThroughTheAPI(t *testing.T) {
	// §6.4: "Coverage claims are challengeable. A reviewer can contest a claim,
	// and the claim must stand to count."
	ts, st, featureID := solutionFixture(t)
	registerOn(t, ts, "solauthor1")
	author := registerOn(t, ts, "solauthor2")
	reviewer := registerOn(t, ts, "solreviewer")

	solID := fileSolution(t, ts, featureID, "Rewrite the exporter", "build-new", "", author)

	// Find a complaint the feature links, which §6.2 guarantees.
	compID := linkedComplaintOf(t, st, featureID)

	// The author claims it.
	resp, body := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/solutions/%d/coverage", solProject, featureID, solID),
		fmt.Sprintf(`{"complaint_id":%d,"claim":"resolves"}`, compID), "Authorization", "Bearer "+author)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("claim: %d %v", resp.StatusCode, body)
	}
	cov, _ := body["coverage"].(map[string]any)
	if got, _ := cov["coverage"].(float64); got <= 0 {
		t.Errorf("coverage after a resolves claim = %v, want > 0", cov["coverage"])
	}

	// A reviewer contests it, with a reason.
	resp, body = postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/solutions/%d/coverage/%d/contest",
			solProject, featureID, solID, compID),
		`{"reason":"the failure mode is a filter, not the writer"}`,
		"Authorization", "Bearer "+reviewer)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("contest: %d %v", resp.StatusCode, body)
	}
	cov, _ = body["coverage"].(map[string]any)
	if got, _ := cov["contested"].(float64); got != 1 {
		t.Errorf("contested = %v after contesting, want 1", cov["contested"])
	}
	if after, _ := cov["coverage"].(float64); after >= 1 {
		t.Errorf("coverage = %v after contesting the only claim: §6.4 says a "+
			"contested claim must not count", after)
	}
}

func TestAContestWithoutAReasonIsRefused(t *testing.T) {
	// A contest with no reason is indistinguishable from a deletion, and the
	// claimant deserves to know what to answer.
	ts, st, featureID := solutionFixture(t)
	registerOn(t, ts, "solauthor1")
	author := registerOn(t, ts, "solauthor2")
	reviewer := registerOn(t, ts, "solreviewer2")
	solID := fileSolution(t, ts, featureID, "Rewrite it", "build-new", "", author)
	compID := linkedComplaintOf(t, st, featureID)

	postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/solutions/%d/coverage", solProject, featureID, solID),
		fmt.Sprintf(`{"complaint_id":%d,"claim":"resolves"}`, compID),
		"Authorization", "Bearer "+author)

	resp, body := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/solutions/%d/coverage/%d/contest",
			solProject, featureID, solID, compID),
		`{"reason":"  "}`, "Authorization", "Bearer "+reviewer)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400 (body=%v)", resp.StatusCode, body)
	}
}

func TestCoverageCannotBeClaimedOnAnotherFeaturesComplaint(t *testing.T) {
	// Otherwise the denominator is chosen by whoever files the claim.
	ts, _, featureID := solutionFixture(t)
	registerOn(t, ts, "solauthor1")
	author := registerOn(t, ts, "solauthor2")

	// A complaint in the project that this feature does not link.
	resp, body := postJSON(t, ts, "/api/v1/projects/"+solProject+"/complaints", `{
		"project_id":1,
		"title":"An unrelated problem entirely",
		"body":"nothing to do with export",
		"severity":5,"frequency":5.0}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("complaint: %d %v", resp.StatusCode, body)
	}
	other := bodyInt(t, body, "id")

	solID := fileSolution(t, ts, featureID, "Rewrite it", "build-new", "", author)
	resp, body = postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/solutions/%d/coverage", solProject, featureID, solID),
		fmt.Sprintf(`{"complaint_id":%d,"claim":"resolves"}`, other), "Authorization", "Bearer "+author)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400: a solution cannot claim a complaint its "+
			"feature does not link (body=%v)", resp.StatusCode, body)
	}
}

func TestAVoteInTheSolutionArenaReordersTheBoard(t *testing.T) {
	// The end-to-end path a voter actually takes: read the board, judge a pair,
	// see the new order. If the vote is recorded but the board does not move,
	// ranking is decorative.
	ts, st, featureID := solutionFixture(t)
	registerOn(t, ts, "solauthor1")
	author := registerOn(t, ts, "solauthor2")
	voter := registerOn(t, ts, "solvoter1")

	rewrite := fileSolution(t, ts, featureID, "Rewrite the exporter", "build-new", "", author)
	workaround := fileSolution(t, ts, featureID, "Document the workaround", "config-or-docs-only", "", author)

	_ = st
	// Before: both at the prior, so the order is arbitrary but both are present.
	_, body := getJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/solutions", solProject, featureID))
	if n := len(body["solutions"].([]any)); n != 2 {
		t.Fatalf("board has %d solutions before voting, want 2", n)
	}

	resp, body := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/solutions/vote", solProject, featureID),
		fmt.Sprintf(`{"solution_a":%d,"solution_b":%d,"outcome":"a"}`, rewrite, workaround),
		"Authorization", "Bearer "+voter)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("vote: %d %v", resp.StatusCode, body)
	}
	sols, _ := body["solutions"].([]any)
	if len(sols) != 2 {
		t.Fatalf("the vote response carries %d solutions, want 2", len(sols))
	}
	first, _ := sols[0].(map[string]any)
	second, _ := sols[1].(map[string]any)
	if bodyInt(t, first, "solution_id") != rewrite {
		t.Errorf("after voting for the rewrite, the board leads with solution %v "+
			"(winner %d, loser %v): the vote did not move the ranking",
			first["solution_id"], rewrite, second["solution_id"])
	}
	fScore, _ := first["score"].(float64)
	sScore, _ := second["score"].(float64)
	if fScore <= sScore {
		t.Errorf("winner score %.1f, loser %.1f: a win must score higher", fScore, sScore)
	}
	_ = author
}

func TestTheAuthorCannotVoteOnTheirOwnSolution(t *testing.T) {
	// §5.2 at the API boundary, with the reason in the response.
	ts, _, featureID := solutionFixture(t)
	registerOn(t, ts, "solauthor1")
	author := registerOn(t, ts, "solauthor2")
	a := fileSolution(t, ts, featureID, "Mine A", "build-new", "", author)
	mineB := fileSolution(t, ts, featureID, "Mine B", "build-new", "", author)

	resp, body := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/solutions/vote", solProject, featureID),
		fmt.Sprintf(`{"solution_a":%d,"solution_b":%d,"outcome":"a"}`, a, mineB),
		"Authorization", "Bearer "+author)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403 (body=%v)", resp.StatusCode, body)
	}
	detail, _ := body["detail"].(string)
	if detail == "" {
		t.Error("the refusal names no reason")
	}
}

func TestAVoteCannotCrossIntoAnotherFeatureArena(t *testing.T) {
	// Two arenas rank against different competitors. A request naming a solution
	// from another feature must not spend a vote in this one.
	ts, st, featureID := solutionFixture(t)
	registerOn(t, ts, "solauthor1")
	voter := registerOn(t, ts, "solvoter2")

	author := registerOn(t, ts, "solauthor2")
	a := fileSolution(t, ts, featureID, "Mine A", "build-new", "", author)

	// A second feature in the same project, with its own solution.
	_, compBody := postJSON(t, ts, "/api/v1/projects/"+solProject+"/complaints", `{
		"project_id":1,
		"title":"Second problem","body":"x","severity":3,"frequency":1.0}`)
	compID := bodyInt(t, compBody, "id")
	if resp, vbody := postJSON(t, ts, "/api/v1/projects/"+solProject+"/complaints/"+
		fmt.Sprint(compID)+"/validate", `{}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("validate second complaint: %d %v", resp.StatusCode, vbody)
	}
	_, featBody := postJSON(t, ts, "/api/v1/projects/"+solProject+"/features", fmt.Sprintf(`{
		"project_id":1,"author_id":1,
		"title":"Second feature","body":"y","linked_complaints":[%d]}`, compID))
	otherFeature := bodyInt(t, featBody, "id")

	resp, body := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/solutions", solProject, otherFeature),
		`{"title":"Other feature solution","type":"build-new"}`, "Authorization", "Bearer "+author)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("file against the second feature: %d %v", resp.StatusCode, body)
	}
	otherSol, _ := body["solution"].(map[string]any)
	otherSolID := bodyInt(t, otherSol, "id")

	resp, body = postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/solutions/vote", solProject, featureID),
		fmt.Sprintf(`{"solution_a":%d,"solution_b":%d,"outcome":"a"}`, a, otherSolID),
		"Authorization", "Bearer "+voter)
	t.Logf("cross-arena vote: status %d body=%v", resp.StatusCode, body)

	// Assert the vote was NOT recorded, not merely that the status was not 200.
	// With the feature check removed the request still failed -- the foreign
	// solution has no entry in this arena -- so "status != 200" was satisfied by
	// an unrelated error and the test passed against the bug.
	var logged int
	if err := st.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM pairwise_votes WHERE outcome != 'skip'`).Scan(&logged); err != nil {
		t.Fatalf("count votes: %v", err)
	}
	if logged != 0 {
		t.Errorf("%d votes recorded for a cross-arena request: a solution from another "+
			"feature must not spend a vote in this one", logged)
	}
}

func TestTheSolutionBoardCarriesEveryColumnSection64Names(t *testing.T) {
	// §6.4: "rank, score, confidence, coverage, effort, risk, type, and a compact
	// pro/con digest". The ones the store computes must be present, or a client
	// has to re-derive a Glicko score in JavaScript.
	ts, _, featureID := solutionFixture(t)
	registerOn(t, ts, "solauthor1")
	registerOn(t, ts, "solauthor2")
	registerOn(t, ts, "solvoter3")
	author := registerOn(t, ts, "solauthor3")
	fileSolution(t, ts, featureID, "Rewrite the exporter", "build-new", "", author)

	resp, body := getJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/solutions", solProject, featureID))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d %v", resp.StatusCode, body)
	}
	sols, _ := body["solutions"].([]any)
	if len(sols) != 1 {
		t.Fatalf("got %d solutions, want 1", len(sols))
	}
	row, _ := sols[0].(map[string]any)
	for _, field := range []string{"solution_id", "score", "rating", "coverage", "kappa",
		"distinct_voters", "is_baseline"} {
		if _, ok := row[field]; !ok {
			t.Errorf("the board row has no %q: %v", field, row)
		}
	}
}

// linkedComplaintOf returns a complaint id the feature links. §6.2 guarantees at
// least one exists, so a failure here means the fixture is wrong, not the product.
func linkedComplaintOf(t *testing.T, st *store.DB, featureID int64) int64 {
	t.Helper()
	ids, err := st.LinkedComplaints(t.Context(), featureID)
	if err != nil {
		t.Fatalf("LinkedComplaints: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("the fixture's feature links no complaint: §6.2 requires one")
	}
	return ids[0]
}

// bodyInt reads a numeric field out of a decoded JSON body. Written here rather
// than reused because no existing helper does exactly this: the tests that need
// an id out of a response each do it inline.
func bodyInt(t *testing.T, body map[string]any, field string) int64 {
	t.Helper()
	v, ok := body[field].(float64)
	if !ok {
		t.Fatalf("no numeric %q in %v", field, body)
	}
	return int64(v)
}

// contains reports whether needle appears in haystack. strings.Contains exists;
// this is here only because the assertion reads better without the import in a
// file that otherwise needs nothing from strings.
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
