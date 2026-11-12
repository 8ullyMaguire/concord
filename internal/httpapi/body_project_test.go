package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// The project in the PATH and the project in the BODY are the same fact, and the two
// create routes asked for both.
//
// Nothing caught it because every test sent the body field -- the API had always required
// it -- so the requirement was invisible from inside the suite and obvious the first time
// a live probe sent only the URL a reader would copy. These tests exist so the next
// refactor cannot reintroduce the requirement silently.

func TestAFeatureIsFiledFromThePathAlone(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	owner := registerAndLogin(t, ts, "pathonly-owner")
	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "pathonly", "name": "pathonly", "description": "d"})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create project: %d %s", code, body)
	}
	pid := bodyInt64(t, body, "id")
	cid := fileComplaint(t, ts, owner, "pathonly", pid, "the export drops the last row")
	if err := st.ValidateComplaint(t.Context(), cid); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}

	// NO project_id in the body: the path names it, and the path is what the route
	// authorised the caller against.
	code, body = authJSON(t, ts, http.MethodPost, "/api/v1/projects/pathonly/features", owner,
		map[string]any{"title": "Make the export complete", "body": "rows missing",
			"effort": "M", "linked_complaints": []int64{cid}})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("filing from the path alone: %d %s", code, body)
	}
	if got := bodyInt64(t, body, "project_id"); got != pid {
		t.Fatalf("stored under project %d, want %d", got, pid)
	}
}

func TestAComplaintIsFiledFromThePathAlone(t *testing.T) {
	ts, _ := newTestServerWithStore(t)
	owner := registerAndLogin(t, ts, "pathonly-c-owner")
	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "pathonly-c", "name": "pathonly-c", "description": "d"})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create project: %d %s", code, body)
	}
	pid := bodyInt64(t, body, "id")

	code, body = authJSON(t, ts, http.MethodPost, "/api/v1/projects/pathonly-c/complaints", owner,
		map[string]any{"title": "the importer writes duplicates", "body": "repro",
			"severity": 3, "frequency": 0.8})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("filing from the path alone: %d %s", code, body)
	}
	if got := bodyInt64(t, body, "project_id"); got != pid {
		t.Fatalf("stored under project %d, want %d", got, pid)
	}
}

// A body id that names a DIFFERENT project is refused, not silently overruled: the caller
// has said two things and one of them is wrong, and picking the path for them hides it.
func TestABodyProjectThatContradictsThePathIsRefused(t *testing.T) {
	ts, _ := newTestServerWithStore(t)
	owner := registerAndLogin(t, ts, "pathonly-m-owner")
	for _, slug := range []string{"pathonly-m", "pathonly-other"} {
		code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
			map[string]any{"slug": slug, "name": slug, "description": "d"})
		if code != http.StatusCreated && code != http.StatusOK {
			t.Fatalf("create %s: %d %s", slug, code, body)
		}
	}
	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/pathonly-m/complaints", owner,
		map[string]any{"project_id": 999999, "title": "mismatch", "body": "b",
			"severity": 2, "frequency": 0.5})
	if code != http.StatusBadRequest {
		t.Fatalf("a contradicting body id: %d %s", code, body)
	}
	if !strings.Contains(string(body), "does not match the project in the path") {
		t.Fatalf("the refusal does not say what disagreed: %s", body)
	}
}

// The intake-mode route answers `intake_mode` and used to accept only `mode`, so echoing
// the response back earned "must be one of [complaint_first idea_first both]" -- a
// refusal that reads like a validation bug to a caller who sent one of those three.
func TestIntakeModeAcceptsTheFieldNameTheResponseUses(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	owner := registerAndLogin(t, ts, "pathonly-i-owner")
	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "pathonly-i", "name": "pathonly-i", "description": "d"})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create project: %d %s", code, body)
	}
	// The OWNER of this project, not the fixture's seeded owner: the role check is
	// per-project, and a token minted for somebody else's project answers 403 -- which is
	// what the first version of this test did, and it looked like the field name failing.
	ownerTok := "Bearer " + tokenFor(t, db, "pathonly-i-owner")

	code, body = authJSON(t, ts, http.MethodPut, "/api/v1/projects/pathonly-i/intake-mode",
		ownerTok, map[string]any{"intake_mode": "both"})
	if code != http.StatusOK {
		t.Fatalf("intake_mode as a request field: %d %s", code, body)
	}
	if !strings.Contains(string(body), "both") {
		t.Fatalf("the answer does not report the mode it stored: %s", body)
	}
}
