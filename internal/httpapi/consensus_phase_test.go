package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// §6.5 over HTTP: one open call per feature, and the phase follows the call.
//
// The store tests prove the rules; these prove the ROUTES carry them, because a rule
// enforced only where the store is called directly is a rule with a bypass -- and the
// second half (the feature's status moving) is invisible to curl unless something reads
// the feature back over the same API a page uses.

// phaseFixture creates a project owned by the harness actor with one feature that has a
// validated complaint, which is what a call requires.
func phaseFixture(t *testing.T, ts *httptest.Server, slug string) (projID, featID int64) {
	t.Helper()
	resp, projBody := doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": slug, "name": slug, "description": "x",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: %d", resp.StatusCode)
	}
	projID = int64(projBody["id"].(float64))

	resp, compBody := doJSON(t, ts, "POST", "/api/v1/projects/"+slug+"/complaints", map[string]any{
		"title": slug + " complaint", "body": "x", "severity": 1, "frequency": 0.5,
		"strategic_multiplier": 1.0, "project_id": projID,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create complaint: %d body=%v", resp.StatusCode, compBody)
	}
	compID := int64(compBody["id"].(float64))
	resp, _ = doJSON(t, ts, "POST", "/api/v1/projects/"+slug+"/complaints/"+itoa(compID)+"/validate", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("validate complaint: %d", resp.StatusCode)
	}

	resp, featBody := doJSON(t, ts, "POST", "/api/v1/projects/"+slug+"/features", map[string]any{
		"title": slug + " feature", "body": "x", "linked_complaints": []int64{compID},
		"project_id": projID,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create feature: %d body=%v", resp.StatusCode, featBody)
	}
	return projID, int64(featBody["id"].(float64))
}

func TestASecondConsensusCallOnTheSameFeatureAnswers409(t *testing.T) {
	ts := newTestServer(t)
	_, featID := phaseFixture(t, ts, "one-call-only")

	resp, body := doJSON(t, ts, "POST", "/api/v1/projects/one-call-only/consensus", map[string]any{
		"feature_id": featID, "title": "First", "description": "x",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first call: %d body=%v", resp.StatusCode, body)
	}

	resp, body = doJSON(t, ts, "POST", "/api/v1/projects/one-call-only/consensus", map[string]any{
		"feature_id": featID, "title": "Second", "description": "x",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second call: %d body=%v, want 409 -- an open decision is not a queue of decisions",
			resp.StatusCode, body)
	}
	if msg, _ := body["error"].(string); msg == "" {
		t.Error("409 with no error message: the reader cannot tell what to do")
	}
}

func TestOpeningACallMovesTheFeatureSeenOverTheAPI(t *testing.T) {
	ts := newTestServer(t)
	_, featID := phaseFixture(t, ts, "phase-follows-call")

	resp, body := doJSON(t, ts, "POST", "/api/v1/projects/phase-follows-call/consensus", map[string]any{
		"feature_id": featID, "title": "Decide", "description": "x",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open call: %d body=%v", resp.StatusCode, body)
	}

	resp, read := doJSON(t, ts, "GET", "/api/v1/projects/phase-follows-call/features/"+itoa(featID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("read feature: %d", resp.StatusCode)
	}
	if got, _ := read["status"].(string); got != "consensus" {
		t.Errorf("feature status over the API = %q, want consensus -- the board and the record must name the same phase", got)
	}
}
