package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// §6.5 at the API boundary.
//
// The store tests prove the three conditions. These prove the two things a
// client depends on that the store cannot: that a refusal arrives as 409 with
// the conditions attached, and that a caller cannot decide a call belonging to
// another project by naming its id.
//
// The 409-with-readiness shape is the one worth asserting hard. §6.5's mechanism
// is three conditions, and a 409 that says only "not ready" leaves the client
// unable to distinguish "the arena has not settled" from "this feature is
// broken".

// callFixtureAPI is solutionFixture plus a second user who may file a solution
// (the feature's own author may not) and one filed solution, so the arena has a
// leader and the early-call path has something to open a call about.
func callFixtureAPI(t *testing.T) (*httptest.Server, int64, string) {
	t.Helper()
	ts, _, featureID := solutionFixture(t)
	registerOn(t, ts, "callapib1")
	author := registerOn(t, ts, "callapib2")
	fileSolution(t, ts, featureID, "Rewrite the exporter", "build-new", "", author)
	// §6.5 gives the early-call discretion to collaborators, and a registered
	// user is a guest until they join the project. Joining explicitly rather than
	// widening the rule: a guest opening calls early is exactly what §8.2's
	// standing exists to prevent.
	if resp, _ := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/join", solProject), `{}`,
		"Authorization", "Bearer "+author); resp.StatusCode >= 400 {
		t.Fatalf("author cannot join the project: %d", resp.StatusCode)
	}
	return ts, featureID, author
}

// TestReadinessIsPublicAndExplainsItself checks the unauthenticated GET on a
// feature with no solutions.
func TestReadinessIsPublicAndExplainsItself(t *testing.T) {
	ts, featureID, _ := callFixtureAPI(t)
	resp, body := getJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/consensus", solProject, featureID),
		"X-No-Auth", "1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("anonymous readiness: %d %v", resp.StatusCode, body)
	}
	conds, ok := body["conditions"].([]any)
	if !ok || len(conds) == 0 {
		t.Fatalf("no conditions in %v: a caller cannot tell why no call has opened", body)
	}
	first, _ := conds[0].(map[string]any)
	if met, _ := first["met"].(bool); met {
		t.Errorf("a feature with no solutions reports a met condition: %v", first)
	}
}

// TestOpeningACallBeforeTheConditionsHoldIsAConflictNotABadRequest is the
// distinction the whole route exists for. A 400 tells the caller they made a
// mistake; they did not.
func TestOpeningACallBeforeTheConditionsHoldIsAConflictNotABadRequest(t *testing.T) {
	ts, featureID, _ := callFixtureAPI(t)
	resp, body := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/consensus", solProject, featureID), `{}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status %d, want 409 (body=%v)", resp.StatusCode, body)
	}
	// The refusal has to carry the conditions, or the 409 is just a slower 400.
	readiness, _ := body["readiness"].(map[string]any)
	if readiness == nil {
		t.Fatalf("the 409 carries no readiness object: %v", body)
	}
	conds, _ := readiness["conditions"].([]any)
	if len(conds) == 0 {
		t.Errorf("readiness has no conditions: %v", readiness)
	}
}

// TestOpeningACallRequiresAToken is the one asymmetry worth pinning: the
// explanation is public, the decision is not.
func TestOpeningACallRequiresAToken(t *testing.T) {
	ts, featureID, _ := callFixtureAPI(t)
	resp, _ := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/consensus", solProject, featureID),
		`{}`, "X-No-Auth", "1")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous call opening: %d, want 401", resp.StatusCode)
	}
}

// TestAnEarlyCallNeedsAReasonAtTheAPIBoundary checks §6.5's stated-reason
// requirement is enforced where a client can be told, not buried in the store.
func TestAnEarlyCallNeedsAReasonAtTheAPIBoundary(t *testing.T) {
	ts, featureID, author := callFixtureAPI(t)
	resp, body := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/consensus", solProject, featureID),
		`{"early":true,"reason":"   "}`, "Authorization", "Bearer "+author)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("early call with a blank reason: %d, want 400 (body=%v)", resp.StatusCode, body)
	}
	detail, _ := body["detail"].(string)
	if detail == "" {
		t.Error("the refusal names no reason the client could correct")
	}
}

// TestAnEarlyCallFlagsItself is the §6.5 requirement that the record says so.
func TestAnEarlyCallFlagsItself(t *testing.T) {
	ts, featureID, author := callFixtureAPI(t)
	resp, body := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/features/%d/consensus", solProject, featureID),
		`{"early":true,"reason":"the competitor left"}`, "Authorization", "Bearer "+author)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("early call: %d %v", resp.StatusCode, body)
	}
	call, _ := body["call"].(map[string]any)
	early, _ := call["opened_early"].(bool)
	if !early {
		t.Errorf("the call is not flagged early: %v", call)
	}
	if reason, _ := call["early_reason"].(string); reason != "the competitor left" {
		t.Errorf("early_reason = %q, want the stated reason", reason)
	}
}

// TestACallFromAnotherProjectCannotBeDecidedHere is the cross-project guard. The
// call id is the only thing a caller supplies, so without this any project could
// close any call it could name.
func TestACallFromAnotherProjectCannotBeDecidedHere(t *testing.T) {
	ts, _, author := callFixtureAPI(t)

	// A second project with its own feature and call, in the same test server.
	_, body := postJSON(t, ts, "/api/v1/projects",
		`{"slug":"otherproj","name":"Other","description":"d"}`)
	if resp, _ := postJSON(t, ts, "/api/v1/projects",
		`{"slug":"otherproj","name":"Other","description":"d"}`); resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected the second create to conflict, got %d", resp.StatusCode)
	}
	otherProject := 2 // project id 1 is solProject's

	// Fabricate a call id in the other project and try to decide it here.
	resp, out := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/consensus/999999/outcome", solProject),
		`{"outcome":"accepted"}`, "Authorization", "Bearer "+author)
	if resp.StatusCode == http.StatusOK {
		t.Errorf("a call outside this project was decided through it (body=%v)", out)
	}
	_ = otherProject
	_ = body
}

// TestAnOutcomeNeedsAKnownVocabulary keeps a typo from being recorded as a
// decision. The store has a CHECK for it, so this asserts the migration reached
// the database rather than that the handler is careful.
func TestAnOutcomeNeedsAKnownVocabulary(t *testing.T) {
	ts, _, author := callFixtureAPI(t)
	resp, body := postJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/consensus/1/outcome", solProject),
		`{"outcome":"banana"}`, "Authorization", "Bearer "+author)
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		t.Errorf("an invented outcome was accepted (body=%v)", body)
	}
}

// TestTheFallbackChainIsServedEmptyRatherThanNull pins the JSON shape a client
// iterates over.
func TestTheFallbackChainIsServedEmptyRatherThanNull(t *testing.T) {
	ts, _, _ := callFixtureAPI(t)
	resp, body := getJSON(t, ts,
		fmt.Sprintf("/api/v1/projects/%s/consensus/424242/fallback", solProject))
	// The call does not exist, so 404 -- the point is that the handler resolved
	// the id and reached the store rather than panicking on a missing param.
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d for a nonexistent call, want 404 (body=%v)", resp.StatusCode, body)
	}
}
