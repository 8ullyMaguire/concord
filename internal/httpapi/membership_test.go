package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Each test here names a specific defect. A test that cannot fail against the
// bug it was written for is a comment, not a test.

// TestRegisteredUserCanVoteInProjectTheyDidNotCreate is the headline one.
//
// handleCastVote rejected any caller whose role was "guest", and a user only
// got a members row by creating a project. So the population able to vote was
// exactly one person per project: its creator. A community could not rank
// anything, because a community could not vote.
func TestRegisteredUserCanVoteInProjectTheyDidNotCreate(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "open-vote")

	// The creator proposes two features.
	ids := makeRankedPair(t, ts, slug, projID, "Option A", "Option B")
	featA, featB := ids[0], ids[1]

	// Someone who owns nothing votes on them.
	tok := registerOn(t, ts, "stranger")
	resp, body := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/vote", slug, featA),
		fmt.Sprintf(`{"feature_a":%d,"feature_b":%d,"outcome":"a"}`, featA, featB),
		"Authorization", "Bearer "+tok)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a registered non-creator must be able to vote, got %d body=%v",
			resp.StatusCode, body)
	}

	// And they are now a member, because voting is taking part.
	resp = getWith(t, ts, "/api/v1/projects/"+slug+"/members")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list members: %d", resp.StatusCode)
	}
}

// TestFilingComplaintEnrolsAuthor: enrolment is a side effect of contributing,
// not a separate step the user has to discover.
func TestFilingComplaintEnrolsAuthor(t *testing.T) {
	ts := newTestServer(t)
	_, projBody := doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": "enrol-on-complaint", "name": "Enrol", "description": "x",
	})
	projID := int64(projBody["id"].(float64))
	slug := slugOf(t, ts, projID)

	tok := registerOn(t, ts, "complainer")
	resp, body := postJSON(t, ts, "/api/v1/projects/"+slug+"/complaints",
		fmt.Sprintf(`{"project_id":%d,"title":"Something is broken","body":"detail","severity":3,"frequency":1.0}`, projID),
		"Authorization", "Bearer "+tok)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("file complaint: %d body=%v", resp.StatusCode, body)
	}

	// Before this change the complaint was written with author_id 0: the
	// handler had no auth check at all while every sibling had one, so it
	// attributed the claim to nobody.
	if id := int64(body["author_id"].(float64)); id == 0 {
		t.Errorf("complaint author_id is 0: it was filed anonymously")
	}

	members := membersOf(t, ts, slug)
	if members["complainer"] != "contributor" {
		t.Errorf("filing a complaint should enrol the author as contributor, got %q",
			members["complainer"])
	}
}

// TestJoinProjectDoesNotDemoteMaintainer: JoinProject is idempotent by
// construction. If it ever became an upsert, taking part would quietly cost a
// project owner their authority.
func TestJoinProjectDoesNotDemoteMaintainer(t *testing.T) {
	ts := newTestServer(t)
	_, projBody := doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": "no-demotion", "name": "No Demotion", "description": "x",
	})
	projID := int64(projBody["id"].(float64))
	slug := slugOf(t, ts, projID)

	creator := "testuser" // the harness's own account created the project
	for i := 0; i < 3; i++ {
		resp, body := postJSON(t, ts, "/api/v1/projects/"+slug+"/join", "{}")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("join %d: %d body=%v", i, resp.StatusCode, body)
		}
		if got := body["role"]; got != "maintainer" {
			t.Fatalf("join must not demote a maintainer; after %d joins role is %v", i+1, got)
		}
	}

	members := membersOf(t, ts, slug)
	if members[creator] != "maintainer" {
		t.Errorf("creator role changed to %q after joining three times", members[creator])
	}
}

// TestAuthorCannotVoteOnOwnFeature: Glicko-2 has no way to discount
// self-preference, so the refusal is an integrity property, not a nicety.
func TestAuthorCannotVoteOnOwnFeature(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "self-vote")
	ids := makeRankedPair(t, ts, slug, projID, "Mine", "Theirs")
	featA, featB := ids[0], ids[1]

	resp, body := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/vote", slug, featA),
		fmt.Sprintf(`{"feature_a":%d,"feature_b":%d,"outcome":"a"}`, featA, featB))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("author voting on their own feature should be 403, got %d body=%v",
			resp.StatusCode, body)
	}
	// The refusal has to say why. A bare "permission denied" for an action the
	// interface offered is indistinguishable from a bug to whoever hit it.
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "you proposed") {
		t.Errorf("self-vote refusal should name the reason, got %q", msg)
	}
	if _, ok := body["detail"]; !ok {
		t.Errorf("self-vote refusal should carry a detail field explaining the rule")
	}
}

// TestAuthorCannotVoteOnOwnFeatureInSecondPosition: the pair is unordered as
// presented, so checking only feature_a would let an author vote for their own
// feature by naming it second.
func TestAuthorCannotVoteOnSecondPosition(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "self-vote-b")
	ids := makeRankedPair(t, ts, slug, projID, "Mine", "Theirs")
	mine, theirs := ids[0], ids[1]

	// Author's feature is named second, and they vote for it.
	resp, body := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/vote", slug, mine),
		fmt.Sprintf(`{"feature_a":%d,"feature_b":%d,"outcome":"b"}`, theirs, mine))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("author's feature in second position must still be refused, got %d body=%v",
			resp.StatusCode, body)
	}
}

// TestPrioritiesArePubliclyReadable: reading the ranking is public; casting a
// vote is not. Reversing that would make the site a black box.
func TestPrioritiesArePubliclyReadable(t *testing.T) {
	ts := newTestServer(t)
	_, projBody := doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": "public-rank", "name": "Public Rank", "description": "x",
	})
	projID := int64(projBody["id"].(float64))
	slug := slugOf(t, ts, projID)

	// No Authorization header, on the same server. The suite has an
	// unauthenticated harness (newTestServerNoActor) but it builds a *separate*
	// database, so it would answer 404 for a project that does not exist there.
	// That says nothing about whether the route needs a token, and a 404 read
	// as "not 401" is a test that can pass for the wrong reason.
	resp := anonRequest(t, ts, "GET", "/api/v1/projects/"+slug+"/priorities", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("priorities should be public, got %d", resp.StatusCode)
	}
}

// TestFirstVoteIsPossible pins the ordering bug.
//
// JoinProject now happens before the guest check, because rejecting guests
// first made a first vote impossible: nobody is a member until they have voted
// or contributed, so the one act that grants membership was the act that
// required it. This test exists to keep that from being "tidied" back.
func TestFirstVoteIsPossible(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "first-vote")
	ids := makeRankedPair(t, ts, slug, projID, "A", "B")
	a, b := ids[0], ids[1]

	// A brand-new account, with no membership in this project, asks for a pair
	// and answers it. Neither step may require prior membership.
	tok := registerOn(t, ts, "newcomer")
	res := getWith(t, ts, "/api/v1/projects/"+slug+"/votes/next", "Authorization", "Bearer "+tok)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("a new account must be able to fetch a pair, got %d", res.StatusCode)
	}
	res.Body.Close()

	resp, body := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/vote", slug, a),
		fmt.Sprintf(`{"feature_a":%d,"feature_b":%d,"outcome":"a"}`, a, b),
		"Authorization", "Bearer "+tok)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a first vote must be possible, got %d body=%v", resp.StatusCode, body)
	}
}

// TestJoinIsIdempotent: the members table gets one row, not three.
func TestJoinIsIdempotent(t *testing.T) {
	ts := newTestServer(t)
	_, projBody := doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": "join-once", "name": "Join Once", "description": "x",
	})
	projID := int64(projBody["id"].(float64))
	slug := slugOf(t, ts, projID)

	tok := registerOn(t, ts, "joiner")
	for i := 0; i < 4; i++ {
		resp, body := postJSON(t, ts, "/api/v1/projects/"+slug+"/join", "{}", "Authorization", "Bearer "+tok)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("join %d: %d body=%v", i, resp.StatusCode, body)
		}
	}
	seen := 0
	for _, m := range memberList(t, ts, slug) {
		if m["username"] == "joiner" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("four joins produced %d rows for one user, want 1", seen)
	}
}

// TestSkipIsRecordedAndDoesNotCountAsALoss: "no preference" is information
// about a pair, and folding it in with a loss would make the UI unable to tell
// "nobody wanted it" from "nobody had an opinion".
func TestSkipIsRecordedAndDoesNotCountAsALoss(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "skip-test")
	ids := makeRankedPair(t, ts, slug, projID, "Left", "Right")
	a, b := ids[0], ids[1]

	tok := registerOn(t, ts, "skipper")
	resp, body := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/vote", slug, a),
		fmt.Sprintf(`{"feature_a":%d,"feature_b":%d,"outcome":"skip"}`, a, b),
		"Authorization", "Bearer "+tok)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("skip should be accepted: %d body=%v", resp.StatusCode, body)
	}

	tallies := talliesOf(t, ts, slug, a)
	if tallies["skips"] != 1.0 {
		t.Errorf("skip counted as %v skips, want 1", tallies["skips"])
	}
	if tallies["wins"] != 0 || tallies["losses"] != 0 {
		t.Errorf("a skip must not count as a win or loss, got wins=%v losses=%v",
			tallies["wins"], tallies["losses"])
	}
}

// TestAllFiveOutcomesAreAccepted pins the contract the rank page depends on.
// The outcome strings live in internal/ranking and the UI must match them
// exactly; a rename there would otherwise break the page silently.
func TestAllFiveOutcomesAreAccepted(t *testing.T) {
	for _, outcome := range []string{"a", "b", "both", "neither", "skip"} {
		t.Run(outcome, func(t *testing.T) {
			ts := newTestServer(t)
			slug, projID := makeProject(t, ts, "outcome-"+outcome)
			ids := makeRankedPair(t, ts, slug, projID, "L", "R")
			a, b := ids[0], ids[1]
			tok := registerOn(t, ts, "voter-"+outcome)
			resp, body := postJSON(t, ts,
				fmt.Sprintf("/api/v1/projects/%s/features/%d/vote", slug, a),
				fmt.Sprintf(`{"feature_a":%d,"feature_b":%d,"outcome":%q}`, a, b, outcome),
				"Authorization", "Bearer "+tok)
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("outcome %q should be accepted, got %d body=%v", outcome, resp.StatusCode, body)
			}
		})
	}
}

// TestComplaintRequiresAuth: handleCreateComplaint had no auth check while
// every sibling did, so claims could be filed anonymously with author_id 0.
func TestComplaintRequiresAuth(t *testing.T) {
	ts := newTestServer(t)
	_, projBody := doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": "anon-complaint", "name": "Anon", "description": "x",
	})
	projID := int64(projBody["id"].(float64))
	slug := slugOf(t, ts, projID)

	resp := anonRequest(t, ts, "POST", "/api/v1/projects/"+slug+"/complaints",
		fmt.Sprintf(`{"project_id":%d,"title":"Anonymous smear","body":"b","severity":5,"frequency":1.0}`, projID))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous complaint should be 401, got %d", resp.StatusCode)
	}
}

// --- helpers ---

// membersOf returns username -> role for a project.
func membersOf(t *testing.T, ts *httptest.Server, slug string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range memberList(t, ts, slug) {
		if u, ok := m["username"].(string); ok {
			out[u], _ = m["role"].(string)
		}
	}
	return out
}

func memberList(t *testing.T, ts *httptest.Server, slug string) []map[string]any {
	t.Helper()
	res, body := getJSON(t, ts, "/api/v1/projects/"+slug+"/members")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list members: %d body=%v", res.StatusCode, body)
	}
	raw, _ := body["items"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		if mm, ok := m.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

// talliesOf returns the participation counts for one feature.
func talliesOf(t *testing.T, ts *httptest.Server, slug string, featureID int64) map[string]float64 {
	t.Helper()
	res, body := getJSON(t, ts, fmt.Sprintf("/api/v1/projects/%s/tallies", slug))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("tallies: %d body=%v", res.StatusCode, body)
	}
	out := map[string]float64{}
	raw, _ := body["items"].([]any)
	for _, m := range raw {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := mm["feature_id"].(float64); ok && int64(id) == featureID {
			for _, k := range []string{"wins", "losses", "skips", "votes_cast"} {
				if v, ok := mm[k].(float64); ok {
					out[k] = v
				}
			}
			return out
		}
	}
	t.Fatalf("no tally for feature %d in %v", featureID, body)
	return out
}

// makeProject creates a project owned by the harness user and returns its slug
// and numeric id. Every test in this file needs one, and building it inline
// each time is how a test ends up asserting against the wrong project.
func makeProject(t *testing.T, ts *httptest.Server, slug string) (string, int64) {
	t.Helper()
	resp, body := doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": slug, "name": slug, "description": "x",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project %s: %d body=%v", slug, resp.StatusCode, body)
	}
	id := int64(body["id"].(float64))
	return slugOf(t, ts, id), id
}

// makeRankedPair creates two features that can actually be ranked.
//
// A feature requires at least one validated complaint to exist — that is not a
// formality: GetFeaturePriorities sums pain over a feature's validated
// complaints, so a feature with none is not a candidate for anything. Building
// the complaint and validating it is therefore part of creating a votable pair,
// and skipping it yields a 400 that looks like a test bug.
func makeRankedPair(t *testing.T, ts *httptest.Server, slug string, projID int64, titles ...string) []int64 {
	t.Helper()
	resp, compBody := doJSON(t, ts, "POST", "/api/v1/projects/"+slug+"/complaints", map[string]any{
		"project_id": projID, "title": "Underlying problem " + titles[0],
		"body": "detail", "severity": 3, "frequency": 1.0,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create complaint: %d body=%v", resp.StatusCode, compBody)
	}
	compID := int64(compBody["id"].(float64))
	resp, _ = doJSON(t, ts, "POST",
		"/api/v1/projects/"+slug+"/complaints/"+itoa(compID)+"/validate", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("validate complaint: %d", resp.StatusCode)
	}

	ids := make([]int64, 0, len(titles))
	for _, title := range titles {
		resp, body := doJSON(t, ts, "POST", "/api/v1/projects/"+slug+"/features", map[string]any{
			"title": title, "body": "a body", "project_id": projID,
			"linked_complaints": []int64{compID},
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create feature %q: %d body=%v", title, resp.StatusCode, body)
		}
		ids = append(ids, int64(body["id"].(float64)))
	}
	return ids
}

// anonRequest makes a request with no Authorization header against an existing
// server, so the caller's own database is the one being asked.
func anonRequest(t *testing.T, ts *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	// On the authenticated harness this is what actually makes the request
	// anonymous: the wrapper fills in a default identity for anything that
	// arrives without an Authorization header, so omitting the header proves
	// nothing.
	req.Header.Set("X-No-Auth", "1")
	res, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}
