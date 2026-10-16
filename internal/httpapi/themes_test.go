package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// §6.2's themes at the edge: the write path had NO route in any revision. The store could
// list themes, the handler served them, and nothing could create one -- so `theme_weight`
// was always zero and the spec's third steering input did not exist at this layer either.

func TestTheThemeWritePathIsReachableAndAppliesNothing(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	createTestProject(t, ts, "theme-route")
	slug := "theme-route"
	base := "/api/v1/projects/" + slug
	if _, err := st.ExecContext(t.Context(),
		`INSERT INTO tags (project_id, name) VALUES (NULL, 'security-hardening')`); err != nil {
		t.Fatalf("insert tag: %v", err)
	}

	// Anonymous: refused as an auth failure, not a 500.
	if code, _ := doJSONAnon(t, ts, "POST", base+"/strategy/themes", map[string]any{
		"theme_tag": "security-hardening", "weight": 2.0, "rationale": "safety floor",
	}); code.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous propose: %d, want 401", code.StatusCode)
	}

	resp, body := doJSON(t, ts, "POST", base+"/strategy/themes", map[string]any{
		"theme_tag": "security-hardening", "theme_namespace": "topic",
		"weight": 2.0, "rationale": "safety floor",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("propose: %d %v", resp.StatusCode, body)
	}
	prop, _ := body["proposal"].(map[string]any)
	if prop == nil {
		t.Fatalf("no proposal in the response: %v", body)
	}
	if prop["status"] != "pending" {
		t.Errorf("status = %v, want pending", prop["status"])
	}
	// The message is the contract: a 201 that a caller could mistake for an applied change
	// is the hand-steering §6.2 forbids, one misreading later.
	if msg, _ := body["message"].(string); !strings.Contains(msg, "not applied") {
		t.Errorf("message = %q; it must say the change has not been applied", msg)
	}
	// And the state it is about: nothing is in force.
	if resp, themes := doJSON(t, ts, "GET", base+"/strategy/themes", nil); resp.StatusCode == http.StatusOK {
		// doJSON wraps a bare array under "items": this endpoint answers a JSON array (and
		// an empty array, not null -- a null here would be the shape defect the repo gates).
		items, _ := themes["items"].([]any)
		if items == nil {
			t.Errorf("GET themes answered %v; want an empty array", themes)
		} else if n := len(items); n != 0 {
			t.Errorf("live themes after a proposal: %d, want none", n)
		}
	}

	// A weight outside the declared range is refused BY NAME.
	if resp, bad := doJSON(t, ts, "POST", base+"/strategy/themes", map[string]any{
		"theme_tag": "security-hardening", "weight": 99.0, "rationale": "too much",
	}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("weight 99: %d, want 400", resp.StatusCode)
	} else if msg, _ := bad["error"].(string); !strings.Contains(msg, "-2") || !strings.Contains(msg, "3") {
		t.Errorf("the refusal does not name the range: %q", msg)
	}

	// A second pending proposal for the same tag is a conflict, not a second row.
	if resp, _ := doJSON(t, ts, "POST", base+"/strategy/themes", map[string]any{
		"theme_tag": "security-hardening", "weight": 1.0, "rationale": "rival idea",
	}); resp.StatusCode != http.StatusConflict {
		t.Errorf("a competing proposal: %d, want 409", resp.StatusCode)
	}

	// The proposals are readable, which is what the settings page's new panel reads.
	resp, listed := doJSON(t, ts, "GET", base+"/strategy/theme-proposals", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list proposals: %d", resp.StatusCode)
	}
	items, _ := listed["items"].([]any)
	if len(items) != 1 {
		t.Errorf("proposal list = %v, want one entry", listed["items"])
	}
}

// TestAThemeCannotBeRatifiedByItsOwnProposer is the gate that matters most: with the quorum
// capped to the eligible population, one consent ratifies, so "the proposer cannot consent"
// is the difference between a consensus route and a self-service dial.
func TestAThemeCannotBeRatifiedByItsOwnProposer(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	createTestProject(t, ts, "theme-self")
	if _, err := st.ExecContext(t.Context(),
		`INSERT INTO tags (project_id, name) VALUES (NULL, 'security-hardening')`); err != nil {
		t.Fatalf("insert tag: %v", err)
	}
	base := "/api/v1/projects/theme-self"
	resp, body := doJSON(t, ts, "POST", base+"/strategy/themes", map[string]any{
		"theme_tag": "security-hardening", "weight": 2.0, "rationale": "safety floor",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("propose: %d %v", resp.StatusCode, body)
	}
	prop, _ := body["proposal"].(map[string]any)
	id := int64(prop["id"].(float64))

	// The default token belongs to the proposer, so this is the self-consent path.
	resp, got := doJSON(t, ts, "POST", base+"/strategy/themes/"+itoa(id)+"/consent", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("the proposer consenting to their own theme: %d %v, want 403", resp.StatusCode, got)
	}
	if msg, _ := got["error"].(string); !strings.Contains(msg, "own proposal") {
		t.Errorf("the refusal does not say why: %q", msg)
	}
	// And nothing was applied by the attempt.
	if _, themes := doJSON(t, ts, "GET", base+"/strategy/themes", nil); themes["items"] != nil {
		if items, _ := themes["items"].([]any); len(items) != 0 {
			t.Errorf("live themes after a refused self-consent: %v, want none", items)
		}
	}
}

// TestTheFeatureTagRoutesCarryTheSteeringInput -- the write that makes the themes meaningful.
func TestTheFeatureTagRoutesCarryTheSteeringInput(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	createTestProject(t, ts, "feature-tags")
	if _, err := st.ExecContext(t.Context(),
		`INSERT INTO tags (project_id, name) VALUES (NULL, 'security-hardening')`); err != nil {
		t.Fatalf("insert tag: %v", err)
	}
	var pid, uid int64
	if err := st.QueryRow(`SELECT id FROM projects WHERE slug='feature-tags'`).Scan(&pid); err != nil {
		t.Fatalf("project id: %v", err)
	}
	if err := st.QueryRow(`SELECT id FROM users WHERE username='testuser'`).Scan(&uid); err != nil {
		t.Fatalf("owner id: %v", err)
	}
	res, err := st.ExecContext(t.Context(),
		`INSERT INTO features (project_id, author_id, title, body, effort, status,
		 strategic_weight, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		pid, uid, "Harden the login flow", "body", "M", "draft", 1.0, 1790000000.0, 1790000000.0)
	if err != nil {
		t.Fatalf("insert feature: %v", err)
	}
	featureID, _ := res.LastInsertId()
	path := "/api/v1/projects/feature-tags/features/" + itoa(featureID) + "/tags"

	// Anonymous cannot write a tag: it decides which ratified themes steer the feature.
	if resp, _ := doJSONAnon(t, ts, "POST", path, map[string]any{"tag": "security-hardening"}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous add: %d, want 401", resp.StatusCode)
	}
	// The read is open (the project is public) and starts empty.
	if resp, got := doJSON(t, ts, "GET", path, nil); resp.StatusCode != http.StatusOK ||
		len(got["tags"].([]any)) != 0 {
		t.Errorf("empty read: %d %v", resp.StatusCode, got)
	}
	// An unknown tag names the route that creates one.
	if resp, bad := doJSON(t, ts, "POST", path, map[string]any{"tag": "invented"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown tag: %d, want 400", resp.StatusCode)
	} else if msg, _ := bad["error"].(string); !strings.Contains(msg, "taxonomy/proposals") {
		t.Errorf("the refusal does not say how to fix it: %q", msg)
	}
	// The real thing.
	resp, got := doJSON(t, ts, "POST", path, map[string]any{"tag": "security-hardening"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add tag: %d %v", resp.StatusCode, got)
	}
	if tags, _ := got["tags"].([]any); len(tags) != 1 || tags[0] != "security-hardening" {
		t.Errorf("tags = %v, want [security-hardening]", got["tags"])
	}
	// Removal, and the empty answer afterwards.
	if resp, got := doJSON(t, ts, "POST", path+"/remove", map[string]any{"tag": "security-hardening"}); resp.StatusCode != http.StatusOK {
		t.Errorf("remove: %d %v", resp.StatusCode, got)
	} else if len(got["tags"].([]any)) != 0 {
		t.Errorf("tags after removal = %v, want none", got["tags"])
	}
}

// TestAFeatureFromAnotherProjectCannotBeTaggedThroughThisURL: feature ids are global, so
// the project half of the path has to be checked rather than assumed.
func TestAFeatureFromAnotherProjectCannotBeTaggedThroughThisURL(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	createTestProject(t, ts, "tags-one")
	createTestProject(t, ts, "tags-two")
	if _, err := st.ExecContext(t.Context(),
		`INSERT INTO tags (project_id, name) VALUES (NULL, 'security-hardening')`); err != nil {
		t.Fatalf("insert tag: %v", err)
	}
	var pid, uid int64
	if err := st.QueryRow(`SELECT id FROM projects WHERE slug='tags-one'`).Scan(&pid); err != nil {
		t.Fatalf("project id: %v", err)
	}
	if err := st.QueryRow(`SELECT id FROM users WHERE username='testuser'`).Scan(&uid); err != nil {
		t.Fatalf("owner id: %v", err)
	}
	res, err := st.ExecContext(t.Context(),
		`INSERT INTO features (project_id, author_id, title, body, effort, status,
		 strategic_weight, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		pid, uid, "Feature of project one", "body", "M", "draft", 1.0, 1790000000.0, 1790000000.0)
	if err != nil {
		t.Fatalf("insert feature: %v", err)
	}
	featureID, _ := res.LastInsertId()

	resp, _ := doJSON(t, ts, "POST",
		"/api/v1/projects/tags-two/features/"+itoa(featureID)+"/tags",
		map[string]any{"tag": "security-hardening"})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("tagging project one's feature through project two's URL: %d, want 404",
			resp.StatusCode)
	}
}
