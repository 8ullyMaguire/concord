package httpapi

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

// The criteria page's two gates, in the shape the other panels use
// (docs/specs/criteria-panel-spec.md §5):
//
//   - the page exists, renders the slug, loads its script and mounts the root the
//     script needs -- and does not render at all for a project the caller cannot
//     see, because a 200 on a private project's slug confirms it exists;
//   - the script keeps the decisions that make the page honest. Each required
//     string below is a rule, not an implementation detail: the unrated branch, the
//     five outcomes the schema allows, the two composite flags, and the four write
//     paths. Removing any of them changes what a reader is told.
//
// What these cannot do is prove the page RENDERS correctly from real data -- that
// is tests/e2e/criteria_e2e.py, which seeds ratings in sqlite and reads the DOM.
func TestTheCriteriaPageIsRegisteredAndMounted(t *testing.T) {
	ts := newTestServer(t)
	createTestProject(t, ts, "crit-page")

	code, body := getBody(t, ts.URL+"/projects/crit-page/criteria")
	if code != http.StatusOK {
		t.Fatalf("GET /projects/crit-page/criteria = %d, want 200", code)
	}
	for _, want := range []string{
		"crit-page",              // the slug, so the page knows which project it is
		`id="criteria-root"`,     // the mount point criteria.js writes into
		"/assets/js/criteria.js", // the script, or the page is a static shell
		"criteria-notice",        // the live region every refusal is reported in
		"comparison table",       // the spec's own words for what this is
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the criteria page does not contain %q, so the page cannot do "+
				"what docs/specs/criteria-panel-spec.md §3 says it does", want)
		}
	}
}

func TestCriteriaPageHidesAPrivateProjectFromAnonymousCallers(t *testing.T) {
	ts := newTestServer(t)
	owner := registerAndLogin(t, ts, "crit-owner")
	createTestProjectWithAuth(t, ts, owner, "crit-secret")
	setVisibility(t, ts, owner, "crit-secret", "private")

	code, body := authJSON(t, ts, http.MethodGet, "/projects/crit-secret/criteria", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("a private project's criteria page returned %d to an anonymous caller; "+
			"the shell itself leaks the project's existence: %s", code, body)
	}
	if strings.Contains(string(body), "crit-secret") {
		t.Error("the 404 body still names the private slug, which is the one thing a " +
			"miss must not confirm")
	}
}

// TestTheCriteriaScriptKeepsItsRules is a source gate, and its limits are worth
// stating: it proves a rule is present, never that it works. Every rule here has a
// behavioural counterpart in the browser suite.
func TestTheCriteriaScriptKeepsItsRules(t *testing.T) {
	raw, err := os.ReadFile("assets/js/criteria.js")
	if err != nil {
		t.Fatalf("read criteria.js: %v", err)
	}
	src := string(raw)

	cases := []struct{ needle, why string }{
		// The CELL, not the footnote: "unrated" also appears in the prose under the
		// table, so a needle of `>unrated<` stayed green with every cell rendering
		// 0.50 -- the mutation run is what showed that. The class is on the two
		// returns that render a cell and nowhere else.
		// Both the class and the text: the mutation run showed each one alone can be
		// satisfied by the other surviving -- replacing the TEXT with 0.50 left the
		// class in place, and the footnote's "<em>unrated</em>" satisfied a bare
		// `>unrated<` needle, which is exactly the state this gate exists to forbid.
		{"crit-cell crit-unrated", "a criterion with no votes must render as a cell " +
			"state, not as the normalised midpoint of an empty distribution"},
		{"unrated</td>", "the cell must READ as unrated, not merely carry the class"},
		{"games", "the cell has to know how many comparisons are behind it, because " +
			"that is the difference between a measurement and a starting value"},
		// The five VALUES, not the labels: the label for skip is also the word
		// "skip", so a needle of `'skip'` survived renaming the value to 'skipped'.
		{"v: 'a'", "the schema allows five outcomes and the select must offer them"},
		{"v: 'b'", "the schema allows five outcomes and the select must offer them"},
		{"v: 'both'", "both is recorded and moves no rating, which the copy says"},
		{"v: 'neither'", "neither is recorded and moves no rating"},
		{"v: 'skip'", "abstention is data, and the value must be the one the schema " +
			"accepts -- a label is not the wire format"},
		{"include_inactive", "retiring a criterion must be visible in the query it " +
			"changes, or the flag is a lie"},
		{"require_min_rd", "an empty dimension must be excludable"},
		{"/rank/composite", "the table comes from the composite endpoint, whose " +
			"contributions are the cells"},
		{"contributions", "the per-criterion breakdown is what lets a reader argue " +
			"with the order"},
		{"/criteria-profiles", "saved weightings are reachable from this page"},
		{"'/criteria'", "creating a criterion"},
		{"'/active'", "retiring one"},
		{"/vote", "the comparison is the only thing that fills the pools"},
		{"esc(", "every server-supplied string is escaped before it reaches innerHTML"},
		{"apiAuth(", "every read carries the session token"},
		{"confidence", "a provisional order must be distinguishable from a settled one"},
	}
	for _, c := range cases {
		if !strings.Contains(src, c.needle) {
			t.Errorf("criteria.js no longer contains %q: %s", c.needle, c.why)
		}
	}

	// The rule this page must NOT break: no client-side ranking. Sorting the
	// results here would discard the weights and the normalisation and produce an
	// order the forge never concluded, which is the mistake ranking.js documents.
	if strings.Contains(src, ".sort(") {
		t.Error("criteria.js sorts something locally; the order and the scores come " +
			"from POST /rank/composite, and re-sorting discards the weighting that " +
			"produced them")
	}
}
