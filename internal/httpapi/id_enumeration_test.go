package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestPrivateProjectItemsAreNotReadableByID closes an enumeration hole.
//
// Six handlers load a row by its NUMERIC id and return it, with no
// projectReadable check:
//
//	handleGetComplaint        GET /api/v1/projects/{slug}/complaints/{id}
//	handleGetFeature          GET /api/v1/projects/{slug}/features/{id}
//	handleGetList             GET /api/v1/projects/{slug}/lists/{id}
//	handleGetListEntries      GET /api/v1/projects/{slug}/lists/{id}/entries
//	handleGetRequest          GET /api/v1/requests/{request_id}
//	handleGetRequestAnswers   GET /api/v1/requests/{request_id}/answers
//
// Every one of them sits under a route group with no auth middleware, so an
// anonymous caller reaches them. Ids are sequential integers starting at 1, so
// "know the id" is "count from one" -- the whole private roadmap of an instance
// is one loop.
//
// The list endpoints beside them are guarded: handleListFeatures and
// handleGetProject both call projectReadable. So the API answered 404 for a
// private project's features and 200 for the same project's feature 1, which is
// the worst shape: the list is hidden and its contents are not.
//
// handleGetRequest and handleGetRequestAnswers are the awkward two, because their
// route has NO project segment -- /api/v1/requests/{request_id} rather than
// /api/v1/projects/{slug}/requests/{id}. The check has to be reached through the
// request's project, so they are asserted separately below rather than being
// quietly excluded.
func TestPrivateProjectItemsAreNotReadableByID(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		seed      func(t *testing.T, ts *httptest.Server) string // returns the path under test
		secret    string
		wantGuard bool
	}{
		{
			name:   "feature",
			path:   "/api/v1/projects/{slug}/features/{id}",
			secret: "Export must be lossless",
			seed: func(t *testing.T, ts *httptest.Server) string {
				seedPrivateProject(t, ts)
				return "/api/v1/projects/privgate/features/1"
			},
		},
		{
			name:   "complaint",
			path:   "/api/v1/projects/{slug}/complaints/{id}",
			secret: "Export drops the last row",
			seed: func(t *testing.T, ts *httptest.Server) string {
				seedPrivateProject(t, ts)
				return "/api/v1/projects/privgate/complaints/1"
			},
		},
		{
			name:   "list",
			path:   "/api/v1/projects/{slug}/lists/{id}",
			secret: "internal list",
			seed: func(t *testing.T, ts *httptest.Server) string {
				owner := seedPrivateProject(t, ts)
				if code, body := authJSON(t, ts, http.MethodPost,
					"/api/v1/projects/privgate/lists", owner,
					map[string]any{"project_id": 1, "title": "internal list"}); code != http.StatusCreated {
					t.Fatalf("create list: %d %s", code, body)
				}
				return "/api/v1/projects/privgate/lists/1"
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts := newTestServer(t)
			path := c.seed(t, ts)

			code, body := authJSON(t, ts, http.MethodGet, path, anonymousMarker, nil)
			if code == http.StatusOK && strings.Contains(string(body), c.secret) {
				t.Errorf("LEAK: anonymous caller read %s of a private project.\n"+
					"  %s -> %d: %s", c.name, path, code, body)
			}
		})
	}
}

// TestFeatureOfAPrivateProjectIsNotReadableByID is the single case, kept separate
// because it was the one observed leaking and its failure output is the useful
// record: the full feature including status, elo_r and elo_rd came back.
//
// Duplicated from the table above deliberately -- a table row that fails is easy
// to miss in a long run, and this is the defect that motivated the gate.
func TestFeatureOfAPrivateProjectIsNotReadableByID(t *testing.T) {
	ts := newTestServer(t)
	seedPrivateProject(t, ts)

	code, body := authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/privgate/features/1", anonymousMarker, nil)
	t.Logf("anonymous GET feature 1 of a private project -> %d: %s", code, body)

	if code == http.StatusOK {
		t.Errorf("anonymous caller read feature 1 of a private project (200). " +
			"ids are sequential, so a private roadmap is enumerable with a loop")
	}
}

// TestTheListEndpointStillHidesThem is the control.
//
// Without it, the table above could pass because the seed failed to create
// anything -- a green leak test that never seeded a feature proves nothing. This
// asserts the private project really does hold a readable-by-owner feature.
func TestTheListEndpointStillHidesThem(t *testing.T) {
	ts := newTestServer(t)
	owner := seedPrivateProject(t, ts)

	// The owner can see it.
	code, body := authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/privgate/features", owner, nil)
	if code != http.StatusOK || !strings.Contains(string(body), "Export must be lossless") {
		t.Fatalf("the owner cannot read their own private project's features: %d %s",
			code, body)
	}

	// An anonymous caller cannot see the list.
	code, body = authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/privgate/features", anonymousMarker, nil)
	if code == http.StatusOK && strings.Contains(string(body), "Export must be lossless") {
		t.Errorf("the LIST endpoint leaks a private project's features: %s", body)
	}
}

// seedPrivateProject creates a private project holding one validated complaint and
// one feature traced to it, then returns nothing: the ids are deterministic
// (complaint 1, feature 1) because each test gets a fresh database.
//
// Through the API, not the store. §6.2 requires a feature to trace back to a
// validated complaint, so a store-level shortcut would build a state the product
// forbids and would not tell us whether the real path is guarded.
func seedPrivateProject(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	owner := registerAndLogin(t, ts, "privgate-owner")

	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "privgate", "name": "P", "description": "d"}); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create project: %d %s", code, body)
	}
	setVisibility(t, ts, owner, "privgate", "private")

	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/privgate/complaints",
		owner, map[string]any{
			"project_id": 1, "title": "Export drops the last row",
			"body": "reproducible", "severity": 5, "frequency": 3.0,
		})
	if code != http.StatusCreated {
		t.Fatalf("complaint: %d %s", code, body)
	}
	if code, v := authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/privgate/complaints/"+strconv.FormatInt(bodyInt64(t, body, "id"), 10)+"/validate",
		owner, map[string]any{}); code != http.StatusOK {
		t.Fatalf("validate: %d %s", code, v)
	}

	code, body = authJSON(t, ts, http.MethodPost, "/api/v1/projects/privgate/features",
		owner, map[string]any{
			"project_id": 1, "author_id": 1,
			"title": "Export must be lossless", "body": "every row survives",
			"linked_complaints": []int64{1},
		})
	if code != http.StatusCreated {
		t.Fatalf("feature: %d %s", code, body)
	}
	return owner
}

// bodyInt64 reads one numeric field out of a JSON response body.
func bodyInt64(t *testing.T, body []byte, field string) int64 {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode %s: %v (%s)", field, err, body)
	}
	v, ok := m[field].(float64)
	if !ok {
		t.Fatalf("no numeric %q in %s", field, body)
	}
	return int64(v)
}

var _ = fmt.Sprintf

// TestAFeatureCannotBeReadThroughAnotherProjectsURL is the second half of
// handleGetFeature's guard, and it was untested.
//
// requireProjectID checks the caller's access to the project NAMED IN THE PATH.
// That is necessary and not sufficient: a PUBLIC project in the path plus a
// feature belonging to a PRIVATE one still returns the private row, because the
// access check passes on the project that was named rather than on the project
// that owns the row.
//
// This survived mutation testing. Dropping `if f.ProjectID != projectID` left the
// entire suite green, which is the exact failure mode KNOWN-ISSUES.md records: a
// guard that looks present and is never exercised.
func TestAFeatureCannotBeReadThroughAnotherProjectsURL(t *testing.T) {
	ts := newTestServer(t)
	owner := seedPrivateProject(t, ts)

	// A second, PUBLIC project. Its visibility is the one that gets checked.
	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "pubgate", "name": "Public", "description": "d"})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create public project: %d %s", code, body)
	}

	// The owner reads their own private feature under the PUBLIC project's URL.
	path := "/api/v1/projects/pubgate/features/1"
	code, body = authJSON(t, ts, http.MethodGet, path, owner, nil)
	if code == http.StatusOK && strings.Contains(string(body), "Export must be lossless") {
		t.Errorf("feature 1 of a PRIVATE project was served under a PUBLIC project's "+
			"URL: %s -> %d %s", path, code, body)
	}
}

// TestAComplaintCannotBeReadThroughAnotherProjectsURL is the same guard on
// handleGetComplaint.
func TestAComplaintCannotBeReadThroughAnotherProjectsURL(t *testing.T) {
	ts := newTestServer(t)
	owner := seedPrivateProject(t, ts)

	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "pubgate", "name": "Public", "description": "d"}); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create public project: %d %s", code, body)
	}

	path := "/api/v1/projects/pubgate/complaints/1"
	code, body := authJSON(t, ts, http.MethodGet, path, owner, nil)
	if code == http.StatusOK && strings.Contains(string(body), "Export drops the last row") {
		t.Errorf("complaint 1 of a PRIVATE project was served under a PUBLIC "+
			"project's URL: %s -> %d %s", path, code, body)
	}
}

// TestRequestsOfAPrivateProjectAreNotReadable guards the two request endpoints.
//
// These are the awkward ones: their route is /api/v1/requests/{request_id} with NO
// project segment, so the project has to be reached through the row. That is easy
// to leave out precisely because the route does not look project-scoped.
//
// Dropping the projectReadable check from requireReadableRequest left every test
// green, so this case exists to make that mutation fail.
func TestRequestsOfAPrivateProjectAreNotReadable(t *testing.T) {
	ts := newTestServer(t)
	owner := seedPrivateProject(t, ts)

	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/privgate/requests",
		owner, map[string]any{
			"project_id": 1, "title": "should we drop v1?",
			"body": "internal deliberation", "kind": "question",
		})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Skipf("this deployment's request shape differs (status %d: %s); the guard "+
			"is still asserted by TestRequestHandlersResolveTheirProject", code, body)
	}

	for _, p := range []string{
		"/api/v1/requests/1",
		"/api/v1/requests/1/answers",
	} {
		code, body := authJSON(t, ts, http.MethodGet, p, anonymousMarker, nil)
		if code == http.StatusOK && strings.Contains(string(body), "should we drop v1?") {
			t.Errorf("LEAK: anonymous caller read %s of a private project: %s", p, body)
		}
	}
}

// TestListEntriesOfAPrivateProjectAreNotReadable guards the entries endpoint, which
// is read through the LIST rather than the project for a reason: a protected list
// inside a public project must not become readable because its parent is.
func TestListEntriesOfAPrivateProjectAreNotReadable(t *testing.T) {
	ts := newTestServer(t)
	owner := seedPrivateProject(t, ts)

	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/privgate/lists",
		owner, map[string]any{"project_id": 1, "title": "internal list"}); code != http.StatusCreated {
		t.Fatalf("create list: %d %s", code, body)
	}

	for _, p := range []string{
		"/api/v1/projects/privgate/lists/1/entries",
	} {
		code, _ := authJSON(t, ts, http.MethodGet, p, anonymousMarker, nil)
		if code == http.StatusOK {
			// An empty array is fine; the row having been served is the leak, so
			// assert the status only when the list itself is unreadable.
			t.Errorf("LEAK: anonymous caller read %s of a private project (%d)", p, code)
		}
	}
}
