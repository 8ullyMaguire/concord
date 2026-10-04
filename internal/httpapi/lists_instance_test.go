package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestAnInstanceWideListIsReadableThroughTheAPI is the API-level half of the
// instance-wide gap, and the one that makes the handler guards load-bearing.
//
// The store tests passed while both guards were mutated out, because the store is not
// where reachability is decided: a list's project is checked in the HANDLER. So the
// guard that makes an instance-wide list readable had no test at all until this one.
//
// Every assertion below is about a path that used to 404 or 500:
//
//	GET .../projects/{slug}/lists           -> must INCLUDE the instance-wide list
//	GET .../projects/{slug}/lists/{id}      -> must be 200, was 500 then 404
//	POST .../projects/{slug}/lists/{id}/entries/vote
//	                                         -> was 404: no honest weight exists
func TestAnInstanceWideListIsReadableThroughTheAPI(t *testing.T) {
	ts := newTestServer(t)
	owner := seedListForPage(t, ts)

	// A second project, so "readable from a project's URL" is a real claim rather than
	// the one project the fixture happens to own.
	other := registerAndLogin(t, ts, "lists-other-owner")
	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", other,
		map[string]any{"slug": "lists-other", "name": "Other", "description": "d"}); code >= 300 {
		t.Fatalf("create second project: %d %s", code, body)
	}

	// Create it through the API with the scope ASKING for, which is the whole point:
	// the field is not optional, because the path's project is also the access check.
	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/lists-other/lists",
		other, map[string]any{"slug": "shared", "title": "Shared", "description": "d",
			"instance_wide": true})
	if code != http.StatusCreated {
		t.Fatalf("create instance-wide list: %d %s", code, body)
	}
	// And WITHOUT the field it is project-scoped, because defaulting to global would
	// let any project member mint a list every project shares.
	// Distinct names: this ran `code, body =` and clobbered the instance-wide
	// response the assertions below decode, so they were reading the SCOPED list.
	// Two failures that both pointed at the wrong row, from one aliased variable.
	scopedCode, scopedBody := authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/lists-other/lists", other,
		map[string]any{"slug": "scoped", "title": "Scoped", "description": "d"})
	if scopedCode != http.StatusCreated {
		t.Fatalf("create scoped list: %d %s", scopedCode, scopedBody)
	}
	var scopedCheck struct {
		ProjectID *int64 `json:"project_id"`
	}
	if err := json.Unmarshal(scopedBody, &scopedCheck); err != nil {
		t.Fatalf("decode scoped: %v", err)
	}
	if scopedCheck.ProjectID == nil {
		t.Error("a list created WITHOUT instance_wide came back with no project; the " +
			"default must stay project-scoped")
	}
	var created struct {
		ID        int64  `json:"id"`
		ProjectID *int64 `json:"project_id"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if created.ProjectID != nil {
		t.Errorf("the API returned project_id %v for a list created without one; the "+
			"page reads this key to decide whether to label the list instance-wide",
			*created.ProjectID)
	}

	// VISIBLE FROM A PROJECT'S INDEX. GetListsByProject filtered `project_id=?`, which
	// never matches NULL, so this list was in no index at all.
	code, body = authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/lists-other/lists", anonymousMarker, nil)
	if code != http.StatusOK {
		t.Fatalf("list index: %d %s", code, body)
	}
	var index []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		ProjectID *int64 `json:"project_id"`
	}
	if err := json.Unmarshal(body, &index); err != nil {
		t.Fatalf("decode index: %v", err)
	}
	found := false
	for _, l := range index {
		if l.ID == created.ID {
			found = true
			if l.ProjectID != nil {
				t.Error("the index reports a project for an instance-wide list")
			}
			if l.Name != "Shared" {
				t.Errorf("name came back as %q; the COLUMN is lists.title and the JSON "+
					"key is name, because GetList selects title into json:\"name\"",
					l.Name)
			}
		}
	}
	if !found {
		t.Errorf("the instance-wide list is absent from the index, which returned "+
			"%d lists; 16 makes instance-wide the scope every project shares",
			len(index))
	}

	// AND FROM THE INSTANCE ENDPOINT, which exists because the project-scoped index
	// cannot be the only way to find one.
	code, body = authJSON(t, ts, http.MethodGet, "/api/v1/lists", anonymousMarker, nil)
	if code != http.StatusOK {
		t.Fatalf("instance list index: %d %s", code, body)
	}

	// AND THE DETAIL ROUTE SERVES IT. This was a 500 (int64 scan) and then a 404
	// (l.ProjectID != projectID), and now must be 200.
	code, body = authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/lists-other/lists/"+itoa64(created.ID), anonymousMarker, nil)
	if code != http.StatusOK {
		t.Fatalf("instance-wide list detail: %d %s -- a NULL project_id must be "+
			"readable, not a scan error or a 404", code, body)
	}

	// AND THE PAGE RENDERS IT, which is the whole reason any of this matters.
	pc, pbody := getBody(t, ts.URL+"/projects/lists-other/lists/"+itoa64(created.ID))
	if pc != http.StatusOK {
		t.Fatalf("page for an instance-wide list returned %d", pc)
	}
	if !strings.Contains(string(pbody), "list-root") {
		t.Error("the list page did not mount its root element")
	}

	// AND ITS ENTRIES. This is the ONLY route that calls requireReadableList, so the
	// nil branch inside it was dead code until now: mutating it away left the whole
	// suite green, because the detail route kept its own copy of the decision.
	// Removing the branch makes this 404.
	code, body = authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/lists-other/lists/"+itoa64(created.ID)+"/entries", other,
		map[string]any{"url": "https://example.invalid/x", "title": "An entry",
			"description": "d"})
	if code != http.StatusCreated {
		t.Fatalf("add an entry to an instance-wide list: %d %s", code, body)
	}
	code, body = authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/lists-other/lists/"+itoa64(created.ID)+"/entries",
		anonymousMarker, nil)
	if code != http.StatusOK {
		t.Fatalf("read the entries of an instance-wide list: %d %s -- requireReadableList "+
			"is the only route that resolves one, and its nil branch is what allows it",
			code, body)
	}
	var entries []struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		t.Fatalf("decode entries: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("got %d entries, want the 1 just created", len(entries))
	}

	// AND IT CANNOT BE VOTED ON THROUGH A PROJECT URL. The weight formula needs a
	// project, so an instance-wide list's entries have no honest weight: crediting a
	// comparison with influence from an unrelated project would be worse than
	// refusing it. handleVoteListEntry checks `list.ProjectID == projectID` for exactly
	// this, and the check had no test until now -- removing it was a clean build and a
	// green suite.
	code, body = authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/lists-other/lists/"+itoa64(created.ID)+"/entries/vote", other,
		map[string]any{"entry_a": entries[0].ID, "entry_b": entries[0].ID + 1,
			"outcome": "a"})
	if code == http.StatusOK {
		t.Errorf("an instance-wide list accepted a comparison through a project URL "+
			"(%s); the weight formula needs a project and there is none", body)
	}

	_ = owner
}

func itoa64(i int64) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
