package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Helpers for driving the §4.3 taxonomy through the real HTTP API.
//
// These are in their own file because a fixture belongs to every taxonomy test,
// and it kept getting overwritten when it shared a file with unrelated tests.

// ratifyTaxonomy consents to a taxonomy proposal until it is ratified, returning
// whether it got there.
//
// It registers a fresh user per attempt because the proposer cannot consent to
// their own proposal (§5.1) and usernames are unique across the test database.
func ratifyTaxonomy(t *testing.T, ts *httptest.Server, proposalID int64, nameHint string) bool {
	t.Helper()
	nameHint = strings.NewReplacer("-", "", ":", "", "/", "").Replace(nameHint)
	path := "/api/v1/taxonomy/proposals/" + itoa(proposalID) + "/consent"
	for i := 0; i < 8; i++ {
		_, reg := postJSON(t, ts, "/api/v1/auth/register",
			`{"username":"rv`+nameHint+itoa(int64(i))+`","password":"correct horse battery"}`)
		tok, _ := reg["token"].(string)
		if tok == "" {
			// The username is taken, so this helper already ran for this hint.
			// Continuing with fewer voters would ratify below the quorum the
			// product requires, which is exactly what the tests must not do.
			continue
		}
		// Joining makes the voter a contributor, the population an
		// instance-wide taxonomy proposal is ratified by (§4.3). It joins an
		// EXISTING project rather than creating one: an instance-wide proposal
		// only needs contributor somewhere, and a fixture that adds a project
		// changes what any exact-count assertion elsewhere in the suite sees.
		joinSomeProject(t, ts, tok)
		r2, b2 := postJSON(t, ts, path, "{}", "Authorization", "Bearer "+tok)
		if r2.StatusCode != http.StatusOK {
			continue
		}
		p2, _ := b2["proposal"].(map[string]any)
		if p2 != nil && p2["status"] == "ratified" {
			return true
		}
	}
	return false
}

// joinSomeProject makes a token's user a contributor in a project that already
// exists, creating one only if the instance has none.
//
// The candidates are the slugs the rest of the suite already creates, so a
// fixture never adds a project of its own. Adding one is not cosmetic: several
// search tests assert exact project totals, and a helper-injected project made
// them fail for a reason that had nothing to do with search.
func joinSomeProject(t *testing.T, ts *httptest.Server, tok string) {
	t.Helper()
	for _, slug := range []string{
		"taggable", "governance-lab", "rust-indexer", "one", "two", "dup", "bare",
		"guarded", "forged", "novel", "mergeproj", "cycproj", "quorumproj",
	} {
		resp, _ := postJSON(t, ts, "/api/v1/projects/"+slug+"/join", "{}",
			"Authorization", "Bearer "+tok)
		if resp.StatusCode == http.StatusOK {
			return
		}
	}
	// No project exists at all. Creating one is then the only way to reach the
	// contributor population, and it cannot disturb a count that assumed projects
	// existed.
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"taggable","name":"Taggable","description":"d"}`)
	postJSON(t, ts, "/api/v1/projects/taggable/join", "{}", "Authorization", "Bearer "+tok)
}

// seedGlobalTag ratifies a tag in the global taxonomy the way the product
// requires: propose, then consent until quorum is met.
//
// Since revision 4 §4.3, PUT /projects/{slug}/tags resolves an EXISTING tag and
// fails otherwise -- creating vocabulary is a quorum-ratified taxonomy proposal,
// not a side effect of tagging one project. Tests that want to tag a project
// therefore have to create the tag first, which is what this does.
//
// It drives the HTTP API rather than inserting rows, so the permission path is
// exercised too: if taxonomy ratification stops working, these tests fail
// rather than passing against a hand-seeded database.
func seedGlobalTag(t *testing.T, ts *httptest.Server, tag string) {
	t.Helper()
	resp, body := postJSON(t, ts, "/api/v1/taxonomy/proposals", `{
		"action": "create", "target_tag": "`+tag+`",
		"rationale": "test fixture: this tag needs to exist"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("propose tag %q: status %d body=%v", tag, resp.StatusCode, body)
	}
	prop, _ := body["proposal"].(map[string]any)
	if prop == nil {
		t.Fatalf("no proposal in %v", body)
	}
	id := int64(prop["id"].(float64))
	if !ratifyTaxonomy(t, ts, id, "seed"+tag) {
		t.Fatalf("tag %q was never ratified after 8 consents", tag)
	}
}
