package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Criteria API tests (spec extension, 2026-09-29). Each names a specific way
// the endpoints could be wrong in a way the store tests cannot see.

// mkCriterionAPI takes the project slug because the route parameter is a slug;
// the numeric id goes in the body, as it does for every other Concord endpoint.
func mkCriterionAPI(t *testing.T, ts *httptest.Server, projSlug string, projID int64, name string) int64 {
	t.Helper()
	resp, body := doJSON(t, ts, "POST", criteriaPath(projSlug), map[string]any{
		"project_id": projID, "slug": name, "name": name,
		"description": "how " + name + " reads", "direction": "higher_is_better",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create criterion %s: %d body=%v", name, resp.StatusCode, body)
	}
	return int64(body["id"].(float64))
}

// A criterion must be creatable over the API and immediately listable, or the
// feature is unreachable by anything but direct SQL.
func TestCriterionCreateAndList(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "crit-api")
	id := mkCriterionAPI(t, ts, slug, projID, "design")

	resp := getWith(t, ts, criteriaPath(slug))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list criteria: %d", resp.StatusCode)
	}
	found := false
	for _, c := range decodeList(t, ts, criteriaPath(slug)) {
		if int64(c["id"].(float64)) == id {
			found = true
			if c["slug"] != "design" {
				t.Errorf("slug = %v", c["slug"])
			}
			if c["direction"] != "higher_is_better" {
				t.Errorf("direction = %v", c["direction"])
			}
		}
	}
	if !found {
		t.Error("created criterion not in the list")
	}
}

// An empty project must return [] not null, or every client needs a nil check
// before it can render anything.
func TestListCriteriaEmptyIsArray(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "crit-empty")
	_ = projID
	resp := getWith(t, ts, criteriaPath(slug))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d", resp.StatusCode)
	}
	_, body := doJSON(t, ts, "GET", criteriaPath(slug), nil)
	if body["items"] == nil {
		t.Errorf("empty criteria list = %v, want an empty array", body)
	}
}

// A bad direction must be a 400 with a message naming the valid values, not a
// 500 and not a silent default.
func TestCriterionRejectsBadDirectionOverAPI(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "crit-baddir")
	resp, body := doJSON(t, ts, "POST", criteriaPath(slug), map[string]any{
		"project_id": projID, "slug": "x", "name": "X", "direction": "sideways",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad direction status = %d body=%v, want 400", resp.StatusCode, body)
	}
}

// Anonymous callers must not create criteria. Anyone could otherwise mint a
// dimension and, by weighting it, steer every ranking in the project.
func TestCriterionCreateRequiresAuth(t *testing.T) {
	// Build the project on a server that has an actor, then hit the criteria
	// endpoint on one that does not. Calling makeProject on the no-actor server
	// fails at 401 on the project itself, which tests the wrong thing.
	authed := newTestServer(t)
	slug, projID := makeProject(t, authed, "crit-anon")

	anon := newTestServerNoActor(t)
	resp, body := doJSON(t, anon, "POST", criteriaPath(slug), map[string]any{
		"project_id": projID, "slug": "x", "name": "X",
	})
	if resp.StatusCode == http.StatusCreated {
		t.Errorf("anonymous caller created a criterion: %v", body)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// A duplicate slug is a client error, not a 500.
func TestCriterionDuplicateSlugIsBadRequest(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "crit-dup")
	_ = mkCriterionAPI(t, ts, slug, projID, "design")
	resp, _ := doJSON(t, ts, "POST", criteriaPath(slug), map[string]any{
		"project_id": projID, "slug": "design", "name": "Design again",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("duplicate status = %d, want 400", resp.StatusCode)
	}
}

// The headline capability: one feature leads on design, another on speed, and
// the composite endpoint reports a different leader for each weighting.
func TestCompositeRankRespondsToWeights(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "crit-weight")
	feats := makeRankedPair(t, ts, slug, projID, "Alpha", "Beta")
	a, b := feats[0], feats[1]

	design := mkCriterionAPI(t, ts, slug, projID, "design")
	speed := mkCriterionAPI(t, ts, slug, projID, "speed")

	// A beats B on design; B beats A on speed. Two distinct pairs is not
	// possible with one voter, so use a second voter for the second comparison.
	tok2 := registerOn(t, ts, "second-voter")
	if resp, body := postJSON(t, ts,
		fmt.Sprintf("%s/%d/vote", criteriaPath(slug), design),
		fmt.Sprintf(`{"project_id":%d,"feature_a":%d,"feature_b":%d,"outcome":"a"}`, projID, a, b),
		"Authorization", "Bearer "+tok2); resp.StatusCode != http.StatusOK {
		t.Fatalf("design vote: %d body=%v", resp.StatusCode, body)
	}
	if resp, body := postJSON(t, ts,
		fmt.Sprintf("%s/%d/vote", criteriaPath(slug), speed),
		fmt.Sprintf(`{"project_id":%d,"feature_a":%d,"feature_b":%d,"outcome":"b"}`, projID, a, b),
		"Authorization", "Bearer "+tok2); resp.StatusCode != http.StatusOK {
		t.Fatalf("speed vote: %d body=%v", resp.StatusCode, body)
	}

	designHeavy := postComposite(t, ts, slug, map[string]float64{
		fmt.Sprint(design): 9, fmt.Sprint(speed): 1,
	})
	speedHeavy := postComposite(t, ts, slug, map[string]float64{
		fmt.Sprint(design): 1, fmt.Sprint(speed): 9,
	})
	if designHeavy == nil || speedHeavy == nil {
		t.Fatal("composite returned no results")
	}
	// Slices are not comparable in Go, so compare the leaders by value.
	leader := func(rows []map[string]any) int64 {
		return int64(rows[0]["feature_id"].(float64))
	}
	firstDesign, firstSpeed := leader(designHeavy), leader(speedHeavy)
	if firstDesign == firstSpeed {
		t.Errorf("weights did not change the leader (both %d); "+
			"the weighting interface is decorative", firstDesign)
	}
	if firstDesign != a && firstDesign != b {
		t.Fatalf("unexpected leader %d", firstDesign)
	}
	if firstSpeed != a && firstSpeed != b {
		t.Fatalf("unexpected leader %d", firstSpeed)
	}
}

// Every result must carry the breakdown that produced it. A ranking nobody can
// interrogate is an opinion.
func TestCompositeResultCarriesExplanation(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "crit-explain")
	feats := makeRankedPair(t, ts, slug, projID, "Alpha", "Beta")
	c := mkCriterionAPI(t, ts, slug, projID, "design")

	tok2 := registerOn(t, ts, "voter2")
	resp, body := postJSON(t, ts,
		fmt.Sprintf("%s/%d/vote", criteriaPath(slug), c),
		fmt.Sprintf(`{"project_id":%d,"feature_a":%d,"feature_b":%d,"outcome":"a"}`,
			projID, feats[0], feats[1]),
		"Authorization", "Bearer "+tok2)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("vote: %d body=%v", resp.StatusCode, body)
	}

	results := postComposite(t, ts, slug, nil)
	if len(results) == 0 {
		t.Fatal("no results")
	}
	r := results[0]
	for _, field := range []string{"feature_id", "score", "rank", "contributions", "confidence"} {
		if _, ok := r[field]; !ok {
			t.Errorf("result missing %q: %v", field, r)
		}
	}
	contribs, _ := r["contributions"].([]any)
	if len(contribs) == 0 {
		t.Fatal("contributions empty; a result must explain itself")
	}
	c0 := contribs[0].(map[string]any)
	for _, field := range []string{"criterion_id", "slug", "weight", "raw", "rd", "normalised", "score"} {
		if _, ok := c0[field]; !ok {
			t.Errorf("contribution missing %q: %v", field, c0)
		}
	}
}

// A project with no criteria must not be presented as having a ranking.
func TestCompositeWithNoCriteriaIsEmpty(t *testing.T) {
	ts := newTestServer(t)
	slug, _ := makeProject(t, ts, "crit-none")
	results := postComposite(t, ts, slug, nil)
	if len(results) != 0 {
		t.Errorf("got %d results with no criteria", len(results))
	}
}

// An empty body must work: "rank with the default weights".
func TestCompositeAcceptsEmptyBody(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "crit-emptybody")
	_ = mkCriterionAPI(t, ts, slug, projID, "design")
	resp, body := postJSON(t, ts, compositePath(slug), ``)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("empty body status = %d body=%v, want 200", resp.StatusCode, body)
	}
}

// A non-numeric criterion id in the weight map is a client error, not a
// silently dropped weight that quietly changes the ranking.
func TestCompositeRejectsNonNumericWeightKey(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "crit-badkey")
	_ = mkCriterionAPI(t, ts, slug, projID, "design")
	resp, body := postJSON(t, ts, compositePath(slug),
		`{"weights":{"design":1}}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("non-numeric key status = %d body=%v, want 400", resp.StatusCode, body)
	}
}

// Deactivating must take a criterion out of the ranking without losing votes.
func TestCriterionDeactivateRemovesFromRank(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "crit-deact")
	feats := makeRankedPair(t, ts, slug, projID, "Alpha", "Beta")
	speed := mkCriterionAPI(t, ts, slug, projID, "speed")

	tok2 := registerOn(t, ts, "voter3")
	if resp, body := postJSON(t, ts,
		fmt.Sprintf("%s/%d/vote", criteriaPath(slug), speed),
		fmt.Sprintf(`{"project_id":%d,"feature_a":%d,"feature_b":%d,"outcome":"a"}`,
			projID, feats[0], feats[1]),
		"Authorization", "Bearer "+tok2); resp.StatusCode != http.StatusOK {
		t.Fatalf("vote: %d body=%v", resp.StatusCode, body)
	}

	before := postComposite(t, ts, slug, nil)
	if len(before) == 0 {
		t.Fatal("no results before deactivation")
	}
	sawSpeed := false
	for _, r := range before {
		for _, c := range r["contributions"].([]any) {
			if int64(c.(map[string]any)["criterion_id"].(float64)) == speed {
				sawSpeed = true
			}
		}
	}
	if !sawSpeed {
		t.Fatal("speed criterion did not appear in the ranking before deactivation")
	}

	resp, body := doJSON(t, ts, "PUT",
		fmt.Sprintf("%s/%d/active", criteriaPath(slug), speed),
		map[string]any{"active": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deactivate: %d body=%v", resp.StatusCode, body)
	}

	after := postComposite(t, ts, slug, nil)
	for _, r := range after {
		for _, c := range r["contributions"].([]any) {
			if int64(c.(map[string]any)["criterion_id"].(float64)) == speed {
				t.Error("deactivated criterion still contributed")
			}
		}
	}
}

// A saved profile must return the ranking it produces, and a name in the
// request must be usable in place of an explicit weight vector.
func TestCriteriaProfileAppliesToComposite(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "crit-profile")
	design := mkCriterionAPI(t, ts, slug, projID, "design")
	speed := mkCriterionAPI(t, ts, slug, projID, "speed")

	resp, body := doJSON(t, ts, "POST", profilesPath(slug),
		map[string]any{
			"project_id": projID, "slug": "edge", "name": "Edge deployment",
			"weights": map[string]float64{
				fmt.Sprint(design): 1, fmt.Sprint(speed): 3,
			},
		})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("save profile: %d body=%v", resp.StatusCode, body)
	}

	profiles := decodeList(t, ts, profilesPath(slug))
	if len(profiles) != 1 || profiles[0]["slug"] != "edge" {
		t.Fatalf("profiles = %v", profiles)
	}

	// Naming the profile must be accepted wherever an explicit map is.
	resp, body = postJSON(t, ts, compositePath(slug),
		`{"profile":"edge"}`)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("profile ranking status = %d body=%v", resp.StatusCode, body)
	}
}

// An unknown profile is a 404, not a silent fall back to defaults — a caller
// who asked for a saved weighting and got the default one has been misled.
func TestCompositeUnknownProfileIsNotFound(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "crit-noprof")
	_ = mkCriterionAPI(t, ts, slug, projID, "design")
	resp, body := postJSON(t, ts, compositePath(slug),
		`{"profile":"does-not-exist"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown profile status = %d body=%v, want 404", resp.StatusCode, body)
	}
}

// A profile naming a criterion from another project must be refused.
func TestCriteriaProfileRejectsForeignCriterion(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "crit-foreign")
	resp, body := doJSON(t, ts, "POST", profilesPath(slug),
		map[string]any{
			"project_id": projID, "slug": "bad", "name": "Bad",
			"weights": map[string]float64{"999": 1},
		})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("foreign criterion status = %d body=%v, want 400", resp.StatusCode, body)
	}
}

// A vote for a feature outside the project must be refused, not silently
// recorded against a pool it does not belong to.
func TestCriterionVoteRejectsForeignFeatureOverAPI(t *testing.T) {
	ts := newTestServer(t)
	slugA, projA := makeProject(t, ts, "crit-scope-a")
	slugB, projB := makeProject(t, ts, "crit-scope-b")
	featsA := makeRankedPair(t, ts, slugA, projA, "A1", "A2")
	featsB := makeRankedPair(t, ts, slugB, projB, "B1", "B2")

	c := mkCriterionAPI(t, ts, slugA, projA, "design")
	tok2 := registerOn(t, ts, "cross-voter")
	resp, body := postJSON(t, ts,
		fmt.Sprintf("%s/%d/vote", criteriaPath(slugA), c),
		fmt.Sprintf(`{"project_id":%d,"feature_a":%d,"feature_b":%d,"outcome":"a"}`,
			projA, featsA[0], featsB[0]),
		"Authorization", "Bearer "+tok2)
	if resp.StatusCode == http.StatusOK {
		t.Errorf("cross-project vote accepted: %v", body)
	}
}

// A re-vote on the same pair must be refused, so one opinion cannot be counted
// twice.
func TestCriterionVoteTwiceIsRejected(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "crit-twice")
	feats := makeRankedPair(t, ts, slug, projID, "A1", "A2")
	c := mkCriterionAPI(t, ts, slug, projID, "design")
	tok2 := registerOn(t, ts, "twice-voter")
	path := fmt.Sprintf("%s/%d/vote", criteriaPath(slug), c)
	payload := fmt.Sprintf(`{"project_id":%d,"feature_a":%d,"feature_b":%d,"outcome":"a"}`,
		projID, feats[0], feats[1])
	if resp, body := postJSON(t, ts, path, payload,
		"Authorization", "Bearer "+tok2); resp.StatusCode != http.StatusOK {
		t.Fatalf("first vote: %d body=%v", resp.StatusCode, body)
	}
	resp, body := postJSON(t, ts, path, payload, "Authorization", "Bearer "+tok2)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("second vote status = %d body=%v, want 400", resp.StatusCode, body)
	}
}

// Path helpers.
//
// The route parameter is named {project_id} but requireProjectID resolves it
// through GetProject(slug), so the path segment is the project's SLUG. Passing
// the numeric id produced "project \"1\" not found" on every call.
func criteriaPath(slug string) string {
	return "/api/v1/projects/" + slug + "/criteria"
}

func profilesPath(slug string) string {
	return "/api/v1/projects/" + slug + "/criteria-profiles"
}

func compositePath(slug string) string {
	return "/api/v1/projects/" + slug + "/rank/composite"
}

// decodeList reads a JSON array response into maps, matching how the other
// tests in this package decode bodies (json.NewDecoder over resp.Body).
// decodeList reads the array a list endpoint returns, via the package's own
// doJSON, which already wraps a bare array under "items". Reading resp.Body
// here instead produced "read on closed response body", because getWith had
// already consumed and closed it.
func decodeList(t *testing.T, ts *httptest.Server, path string) []map[string]any {
	t.Helper()
	_, body := doJSON(t, ts, "GET", path, nil)
	items, _ := body["items"].([]any)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// postComposite posts a composite ranking request and returns the result rows.
func postComposite(t *testing.T, ts *httptest.Server, slug string, weights map[string]float64) []map[string]any {
	t.Helper()
	body := "{}"
	if weights != nil {
		m := map[string]any{"weights": weights}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		body = string(b)
	}
	resp, decoded := postJSON(t, ts, compositePath(slug), body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("composite: %d body=%v", resp.StatusCode, decoded)
	}
	raw, _ := decoded["results"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
