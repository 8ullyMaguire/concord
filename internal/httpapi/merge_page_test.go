package httpapi

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

// The merge page's gates, in the shape the other panels use
// (docs/specs/merge-gate-spec.md §5):
//
//   - the page exists, renders the slug, loads its script and mounts the root the
//     script needs -- and does not render at all for a project the caller cannot
//     see, because a 200 on a private project's slug confirms it exists;
//   - the script keeps the decisions that make the page honest. Each required
//     string below is a rule, not an implementation detail.
//
// What these cannot do is prove the page RENDERS correctly from real data -- that is
// tests/e2e/merge_e2e.py, which seeds merge requests and confirmations in sqlite and
// reads the DOM.
func TestTheMergePageIsRegisteredAndMounted(t *testing.T) {
	ts := newTestServer(t)
	createTestProject(t, ts, "merge-page")

	code, body := getBody(t, ts.URL+"/projects/merge-page/merge")
	if code != http.StatusOK {
		t.Fatalf("GET /projects/merge-page/merge = %d, want 200", code)
	}
	for _, want := range []string{
		"merge-page",          // the slug, so the page knows which project it is
		`id="merge-root"`,     // the mount point merge.js writes into
		"/assets/js/merge.js", // the script, or the page is a static shell
		"merge-notice",        // the live region every refusal is reported in
		"ready to land",       // §5.7's own question, in the page's words
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the merge page does not contain %q, so the page cannot do what "+
				"docs/specs/merge-gate-spec.md §5 says it does", want)
		}
	}
}

func TestMergePageHidesAPrivateProjectFromAnonymousCallers(t *testing.T) {
	ts := newTestServer(t)
	owner := registerAndLogin(t, ts, "merge-owner")
	createTestProjectWithAuth(t, ts, owner, "merge-secret")
	setVisibility(t, ts, owner, "merge-secret", "private")

	code, body := authJSON(t, ts, http.MethodGet, "/projects/merge-secret/merge", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("a private project's merge page returned %d to an anonymous caller; "+
			"the shell itself leaks the project's existence: %s", code, body)
	}
	if strings.Contains(string(body), "merge-secret") {
		t.Error("the 404 body still names the private slug, which is the one thing a " +
			"miss must not confirm")
	}
}

// TestTheMergeScriptKeepsItsRules is a source gate, and its limits are worth
// stating: it proves a rule is present, never that it works. Every rule here has a
// behavioural counterpart in the browser suite.
func TestTheMergeScriptKeepsItsRules(t *testing.T) {
	raw, err := os.ReadFile("assets/js/merge.js")
	if err != nil {
		t.Fatalf("read merge.js: %v", err)
	}
	src := string(raw)

	cases := []struct{ needle, why string }{
		// The gate is the SERVER's verdict, read and rendered. `needed`,
		// `reviewer_ok` and `requires_quorum` are what make the row explain itself;
		// the count shown is the counted confirmations, not the rows.
		{"g.needed", "the shortfall is the server's number, not one this page worked out"},
		{"g.reviewer_ok", "a reviewer's sign-off is a separate requirement and must show"},
		{"g.requires_quorum", "a charter that dropped the quorum reads differently from " +
			"one nobody has confirmed, and the page must not conflate them"},
		{"g.eligible", "the threshold is measured against a population, and a reader " +
			"cannot judge the shortfall without knowing it"},
		{"g.approvals", "the confirmations given, as counted by the server"},
		// The server's refusal, verbatim: it names whether another confirmation or a
		// reviewer is missing, and a page that replaced it with "failed" would throw
		// away the only explanation the gate produces.
		{"err.message", "a refusal is reported in the server's own words"},
		{"esc(", "every server-supplied string is escaped before it reaches innerHTML"},
		{"esc(mr.title)", "the title is user text and reaches the DOM"},
		{"apiAuth(", "every read and write carries the session token"},
		// The four endpoints, by name.
		{"'/merge'", "the list and the create form"},
		// The endpoint VALUES, adjacent to the attributes that trigger them: the
		// attribute names alone would satisfy a looser needle with the route gone.
		{"'approve'", "confirmation"},
		{"'execute'", "landing the change"},
		{"'reject'", "refusing it"},
		{"'/merge/'", "the writes hang off the collection they belong to"},
		{"signedIn", "a signed-out visitor must not be offered controls that can only " +
			"fail, which is not a policy question"},
		{"feature_id", "a merge request must name the feature it lands"},
	}
	for _, c := range cases {
		if !strings.Contains(src, c.needle) {
			t.Errorf("merge.js no longer contains %q: %s", c.needle, c.why)
		}
	}

	// The rule this page must NOT break: no client-side quorum arithmetic. The
	// threshold is max(merge_quorum_min, ceil(merge_quorum_ratio * eligible)) and
	// both numbers are configurable per project, so a page that recomputed it would
	// disagree with the server the first time a charter changed.
	for _, forbidden := range []string{"0.25", "Math.ceil", "merge_quorum"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("merge.js contains %q: the quorum is the server's arithmetic "+
				"(§6.3), and a second implementation is the one that gets it wrong",
				forbidden)
		}
	}
}
