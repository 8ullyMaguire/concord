package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// TestLinkedComplaintsAreReadable guards the endpoint that closes a write-only
// field.
//
// feature_complaints was write-only for the life of the schema: CreateFeature
// accepted linked_complaints, stored the ids, and no route read them back. So a
// feature's pain evidence existed in the database and in no response, and the
// feature page's complaints section rendered nothing.
//
// It says "No such feature" rather than "not implemented" because the section WAS
// implemented -- in feature.js, reading f.linked_complaints off the feature object,
// which is never populated on a read. The code looked finished and the page was
// empty, which is worse than an absent section.
func TestLinkedComplaintsAreReadable(t *testing.T) {
	ts := newTestServer(t)
	seedPrivateProject(t, ts)

	code, body := authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/privgate/features/1/complaints", anonymousMarker, nil)
	if code != http.StatusNotFound {
		t.Fatalf("private project's linked complaints returned %d to an anonymous caller: %s",
			code, body)
	}
}

// TestTheOwnerCanReadALinkedComplaint is the control.
//
// Without it the test above passes for the wrong reason: if the endpoint answered
// 404 because the feature has no complaints, or because the route does not exist,
// then the 404 proves nothing about visibility. This asserts the same URL returns
// 200 WITH the complaint for the owner.
func TestTheOwnerCanReadALinkedComplaint(t *testing.T) {
	ts := newTestServer(t)
	owner := seedPrivateProject(t, ts)

	code, body := authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/privgate/features/1/complaints", owner, nil)
	if code != http.StatusOK {
		t.Fatalf("the owner cannot read their own linked complaints: %d %s", code, body)
	}
	if !strings.Contains(string(body), "Export drops the last row") {
		t.Fatalf("the complaint is linked in the DB but missing from the response: %s", body)
	}
}

// TestLinkedComplaintsCannotBeReadThroughAnotherProjectsURL is the second check.
//
// requireProjectID covers the caller's access to the project in the PATH. A public
// project in the path plus a feature belonging to a private one must still fail --
// and this was the exact gap that let handleGetFeature's ownership check survive
// mutation testing unnoticed.
func TestLinkedComplaintsCannotBeReadThroughAnotherProjectsURL(t *testing.T) {
	ts := newTestServer(t)
	owner := seedPrivateProject(t, ts)

	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "pubgate", "name": "Public", "description": "d"}); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create public project: %d %s", code, body)
	}

	path := "/api/v1/projects/pubgate/features/1/complaints"
	code, body := authJSON(t, ts, http.MethodGet, path, owner, nil)
	if code == http.StatusOK && strings.Contains(string(body), "Export drops the last row") {
		t.Errorf("complaints of a PRIVATE feature were served under a PUBLIC project's "+
			"URL: %s -> %d %s", path, code, body)
	}
}
