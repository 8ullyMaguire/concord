package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// TestTheStrategicWeightWritePathIsReachable.
//
// §6.2: "theme_weight comes from themes ... changed only by consensus (Normal tier)
// ... Nobody can pin an item to the top by hand. Steering happens through public
// inputs: pain, pairwise votes, and ratified themes."
//
// The mechanism had a store function with tests (ProposeStrategicWeight), a LIST
// route, a CONSENT route and a THEMES route -- and no way to open a proposal. Every
// read worked, the store refused out-of-range weights correctly, the whole suite was
// green, and the feature could not be used. This is the §3.1 shape at the API level:
// a writer with no route is a write that cannot happen.
//
// The feature row is inserted with SQL rather than through CreateFeature, which
// requires at least one validated complaint (features.go) and would make this test
// about the complaint pipeline instead of about the route.
func TestTheStrategicWeightWritePathIsReachable(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	createTestProject(t, ts, "strategy-route")

	var pid, uid int64
	if err := st.QueryRow(`SELECT id FROM projects WHERE slug='strategy-route'`).Scan(&pid); err != nil {
		t.Fatalf("project id: %v", err)
	}
	if err := st.QueryRow(`SELECT id FROM users WHERE username='testuser'`).Scan(&uid); err != nil {
		t.Fatalf("owner id: %v", err)
	}
	res, err := st.ExecContext(t.Context(),
		`INSERT INTO features (project_id, author_id, title, body, effort, status,
		 strategic_weight, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		pid, uid, "Wire the dial", "body", "M", "draft", 1.0, 1790000000.0, 1790000000.0)
	if err != nil {
		t.Fatalf("insert feature: %v", err)
	}
	featureID, _ := res.LastInsertId()
	// THE PATH CARRIES A SLUG, though the parameter is named {project_id}:
	// requireProjectID looks the value up with GetProject, which resolves a SLUG.
	// Passing the numeric id answered 404 "not found" -- indistinguishable from a
	// missing project, which is what makes it worth writing down. Every other
	// project-scoped route in the API behaves the same way, and the settings page
	// reaches /charter the same way.
	path := "/api/v1/projects/strategy-route/strategy/weight-proposals"

	// Anonymous: refused, and refused as an auth failure rather than a 500.
	if code, _ := doJSONAnon(t, ts, "POST", path, map[string]any{
		"feature_id": featureID, "weight": 2.0, "rationale": "security hardening",
	}); code.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous propose: %d, want 401", code.StatusCode)
	}

	// The owner (the default token) opens one.
	resp, body := doJSON(t, ts, "POST", path, map[string]any{
		"feature_id": featureID, "weight": 2.0, "rationale": "security hardening",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("propose: %d %v", resp.StatusCode, body)
	}
	prop, _ := body["proposal"].(map[string]any)
	if prop == nil {
		t.Fatalf("no proposal in the response: %v", body)
	}
	// The state the mechanism is about: the weight is PROPOSED, not applied. A 201
	// that had already moved the dial would be the hand-steering §6.2 forbids.
	if got, want := prop["weight_before"], 1.0; got != want {
		t.Errorf("weight_before = %v, want %v", got, want)
	}
	if got, want := prop["weight_after"], 2.0; got != want {
		t.Errorf("weight_after = %v, want %v", got, want)
	}
	if prop["status"] != "pending" {
		t.Errorf("status = %v, want pending", prop["status"])
	}
	msg, _ := body["message"].(string)
	if !strings.Contains(msg, "ratified by consensus") {
		t.Errorf("the 201 message does not say the change is proposed rather than "+
			"applied: %q", msg)
	}

	var live float64
	if err := st.QueryRow(`SELECT strategic_weight FROM features WHERE id=?`, featureID).Scan(&live); err != nil {
		t.Fatalf("read weight: %v", err)
	}
	if live != 1.0 {
		t.Errorf("the feature's weight is %v after proposing 2.0: proposing changed it", live)
	}

	// It is in the list the page reads.
	_, list := doJSON(t, ts, "GET", path, nil)
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("the proposal is not in the list: %v", list)
	}
	// The proposer cannot consent to their own proposal (§5.1), and that arrives as a
	// refusal naming the rule rather than a silent no-op. A page that showed "consent
	// recorded" here would be reporting a change to a count that did not move.
	first, _ := items[0].(map[string]any)
	id := int64(first["id"].(float64))
	resp2, body2 := doJSON(t, ts, "POST", path+"/"+itoa(id)+"/consent", map[string]any{})
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("self-consent: %d, want 403 (body=%v)", resp2.StatusCode, body2)
	}
	if e, _ := body2["error"].(string); !strings.Contains(e, "own proposal") {
		t.Errorf("the refusal does not name the rule it is enforcing: %q", e)
	}

	// One pending proposal per feature, enforced by a partial unique index and
	// translated into 409 rather than surfacing as a driver error.
	resp4, body4 := doJSON(t, ts, "POST", path, map[string]any{
		"feature_id": featureID, "weight": 3.0, "rationale": "second try",
	})
	if resp4.StatusCode != http.StatusConflict {
		t.Errorf("a second pending proposal: %d, want 409 (body=%v)", resp4.StatusCode, body4)
	}

	// A weight outside the store's bounds is a 400 that names the range, not a 500
	// and not a silent clamp: a clamped weight would look like a refusal.
	resp3, body3 := doJSON(t, ts, "POST", path, map[string]any{
		"feature_id": featureID, "weight": 99.0, "rationale": "too much",
	})
	if resp3.StatusCode != http.StatusBadRequest {
		t.Errorf("out-of-range weight: %d, want 400 (body=%v)", resp3.StatusCode, body3)
	}
	if e, _ := body3["error"].(string); !strings.Contains(e, "between") {
		t.Errorf("the refusal does not name the range: %q", e)
	}
}
