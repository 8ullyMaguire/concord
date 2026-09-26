package httpapi

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// These tests assert what the *pages* reference, not what the API returns.
//
// The reason they exist: project.js once fetched
// /api/v1/projects/{p.id}/features — a numeric id in a route that takes a slug
// — so every request 404'd, the handler mapped the failure to [], and the page
// confidently displayed "No features yet" for a project that had three. The
// endpoints were all correct and all tested. Nothing checked the path the
// browser actually requests, so the bug was invisible to a green suite.
//
// Each assertion below is about a literal string in a file. That is
// deliberately low-tech: the failure mode being guarded is a typo in a URL,
// and the cheapest reliable guard against a typo in a URL is a string
// comparison against the route definition.

// TestRankPageReferencesTheRealRoutes: the rank page must load its script and
// must fetch by slug, never by id.
func TestRankPageReferencesTheRealRoutes(t *testing.T) {
	ts := newTestServer(t)
	slug, _ := makeProject(t, ts, "rank-page")

	body := htmlBody(t, ts, "/projects/"+slug+"/rank")
	if !strings.Contains(body, "/assets/js/rank.js") {
		t.Error("rank page does not load rank.js, so it can never fetch a pair")
	}

	// The script is served, and is the file the page asked for.
	res := getWith(t, ts, "/assets/js/rank.js")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("rank.js is referenced but not served: %d", res.StatusCode)
	}
	_ = res.Body.Close()

	js := assetBody(t, ts, "/assets/js/rank.js")
	// By slug. A `p.id` here is the exact bug that hid the features.
	if !strings.Contains(js, "encodeURIComponent(slug)") {
		t.Error("rank.js does not reference the slug; if it uses an id the request 404s")
	}
	if strings.Contains(js, "/api/v1/projects/' + pair") {
		t.Error("rank.js builds a project URL from a feature object rather than the slug")
	}
	if !strings.Contains(js, "/votes/next") {
		t.Error("rank.js never calls /votes/next, so no pair is ever offered")
	}
	if !strings.Contains(js, "/vote") {
		t.Error("rank.js never posts a vote")
	}
}

// TestRankPageOffersAllFiveOutcomes pins the UI to the store's contract.
//
// The outcome strings live in internal/ranking and the store rejects anything
// else with ErrInvalid. A rename there would leave this page silently 400ing,
// and a rename in the JS would do the same. Neither is a compile error.
func TestRankPageOffersAllFiveOutcomes(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/rank.js")
	for _, outcome := range []string{"'a'", "'b'", "'both'", "'neither'", "'skip'"} {
		if !strings.Contains(js, outcome) {
			t.Errorf("rank.js does not offer the outcome %s", outcome)
		}
	}
}

// TestRankPageSurfacesFailures: the page must not turn a failed request into a
// valid-looking empty state. That mapping is what produced the confident
// "No features yet" for a project that had three.
func TestRankPageSurfacesFailures(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/rank.js")
	if !strings.Contains(js, "errorState") {
		t.Error("rank.js has no error path, so a failed fetch renders as empty state")
	}
	if !strings.Contains(js, "Could not load") {
		t.Error("rank.js has no visible failure message for a failed fetch")
	}
	// The two failure modes worth distinguishing: no account, and a real error.
	if !strings.Contains(js, "401") || !strings.Contains(js, "403") {
		t.Error("rank.js does not distinguish 401 from other failures")
	}
}

// TestRankPageExplainsUncertainty: a rating is meaningless without its
// deviation, and this is the page that shows ratings.
func TestRankPageExplainsUncertainty(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/rank.js")
	if !strings.Contains(js, "elo_rd") {
		t.Error("rank.js shows a rating without its deviation")
	}
}

// TestRankingPageUsesTheForgeOrder: the ranking page must present the order the
// API returns. Re-sorting by rating client-side would discard the pain and
// strategic weight the score already folds in, and show an order the forge
// never concluded.
func TestRankingPageUsesTheForgeOrder(t *testing.T) {
	ts := newTestServer(t)
	slug, _ := makeProject(t, ts, "ranking-page")

	body := htmlBody(t, ts, "/projects/"+slug+"/ranking")
	if !strings.Contains(body, "/assets/js/ranking.js") {
		t.Error("ranking page does not load ranking.js")
	}
	res := getWith(t, ts, "/assets/js/ranking.js")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ranking.js is referenced but not served: %d", res.StatusCode)
	}
	_ = res.Body.Close()

	js := assetBody(t, ts, "/assets/js/ranking.js")
	if !strings.Contains(js, "/priorities") {
		t.Error("ranking.js does not read /priorities")
	}
	if !strings.Contains(js, "/tallies") {
		t.Error("ranking.js does not read participation counts, so a rating is shown with no way to judge it")
	}
	// A client-side sort by rating would silently drop pain and weight.
	if strings.Contains(js, ".sort(") {
		t.Error("ranking.js re-sorts client-side; the API order already includes pain and strategic weight")
	}
	// Both the score components and the uncertainty must be visible.
	for _, field := range []string{"pain_score", "strategic_weight", "priority_score", "elo_rd"} {
		if !strings.Contains(js, field) {
			t.Errorf("ranking.js does not display %s", field)
		}
	}
}

// TestRankingPagesLinkToEachOther: the loop has two halves, and a page that
// cannot reach the other is a dead end.
func TestRankingPagesLinkToEachOther(t *testing.T) {
	ts := newTestServer(t)
	slug, _ := makeProject(t, ts, "linked-pages")

	rank := htmlBody(t, ts, "/projects/"+slug+"/rank")
	if !strings.Contains(rank, "/projects/"+slug+"/ranking") {
		t.Error("the rank page offers no way to see the ranking")
	}
	ranking := htmlBody(t, ts, "/projects/"+slug+"/ranking")
	if !strings.Contains(ranking, "/projects/"+slug+"/rank") {
		t.Error("the ranking page offers no way to cast more votes")
	}
}

// TestProjectPageHasTheForms: a site that ranks proposals but cannot accept any
// is a ranking of nothing. Filing is a form, not an API call.
func TestProjectPageHasTheForms(t *testing.T) {
	ts := newTestServer(t)
	slug, _ := makeProject(t, ts, "forms-page")
	// The forms are built by project.js, not by the template, so the assertion
	// belongs on the script. Checking the server-rendered HTML would have
	// failed against a correctly working page, which is a test that can only
	// ever produce a false alarm — and therefore one that gets deleted.
	//
	// The template's only job here is to host the script.
	body := htmlBody(t, ts, "/projects/"+slug)
	if !strings.Contains(body, "/assets/js/project.js") {
		t.Fatal("the project page does not load project.js, so it renders nothing at all")
	}
	js := assetBody(t, ts, "/assets/js/project.js")
	for _, want := range []string{"complaint-form", "feature-form"} {
		if !strings.Contains(js, want) {
			t.Errorf("project.js does not render a %s", want)
		}
	}
	if !strings.Contains(js, "/complaints") {
		t.Error("project.js cannot file a complaint")
	}
	if !strings.Contains(js, "/features") {
		t.Error("project.js cannot propose a feature")
	}
	// A complaint cannot be created without a validated complaint behind a
	// feature, so the feature form must be able to reference one.
	if !strings.Contains(js, "validate") {
		t.Error("project.js cannot validate a complaint, so no feature can ever be ranked")
	}
	// The project page must also count the complaints, or a visitor cannot tell
	// whether anything has been filed.
	if !strings.Contains(js, "complaints") {
		t.Error("project.js never reads the complaint list")
	}
}

// assetBody returns a static asset's text. htmlBody already does exactly
// this — it is named for pages but nothing about it is HTML-specific — so
// there is one helper rather than two.
func assetBody(t *testing.T, ts *httptest.Server, path string) string {
	t.Helper()
	return htmlBody(t, ts, path)
}

// TestSessionIsASingleSharedInstance guards a bug the browser found and no
// test would have.
//
// ConcordSession was exported as a bare constructor with no shared instance,
// so every caller built its own copy: auth.js stored a token into one object,
// session.js painted the header from another, and project.js asked a third.
// None of that raises an error — it appears as a header reading "Sign in" to a
// visitor who is signed in, and forms that render as though nobody were
// present. The failure is only visible in a browser, which is exactly why the
// scripts need an assertion about their shared surface.
func TestSessionIsASingleSharedInstance(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/session.js")

	if !strings.Contains(js, "ConcordSession.instance") {
		t.Error("session.js exports no shared instance, so each caller gets a private copy")
	}
	if strings.Contains(js, "= ConcordSession;") && !strings.Contains(js, "ConcordSession.instance =") {
		t.Error("session.js exports the constructor without a shared accessor")
	}
	// Every other script must go through the shared accessor, never the
	// constructor, or the copies drift apart again.
	for _, script := range []string{"auth.js", "project.js", "rank.js", "ranking.js"} {
		body := assetBody(t, ts, "/assets/js/"+script)
		if strings.Contains(body, "new window.ConcordSession(") ||
			strings.Contains(body, "new ConcordSession(") {
			t.Errorf("%s constructs its own ConcordSession instead of using the shared one", script)
		}
	}
	// The storage keys live in one file. Duplicated literals are how the two
	// halves stop agreeing about whether anyone is signed in.
	auth := assetBody(t, ts, "/assets/js/auth.js")
	if !strings.Contains(js, "TOKEN_KEY = 'concord.token'") {
		t.Error("session.js no longer defines the canonical token key")
	}
	// auth.js may still write the key, but must refresh the shared instance so
	// the header is correct without a reload.
	if !strings.Contains(auth, "ConcordSession.instance().refresh()") {
		t.Error("auth.js does not refresh the shared session after storing the token")
	}
}

// TestNoFunctionIsStringifiedIntoMarkup catches a bug the browser found and a
// green suite would not have.
//
// project.js built the complaints section with `+ complaintCards +` instead of
// `+ complaintCards(complaints) +`. String concatenation coerces a function to
// its source text, so the page rendered its own JavaScript as visible copy,
// intermingled with the real complaint cards. Every request succeeded, every
// status was 200, and the test asserting "the page mentions Complaints" passed
// — the word was there, in a JavaScript function body, where nobody would look.
//
// The cheap general guard: a script that builds HTML must not concatenate a
// bare function reference into a string.
func TestNoFunctionIsStringifiedIntoMarkup(t *testing.T) {
	ts := newTestServer(t)
	for _, script := range []string{"project.js", "rank.js", "ranking.js", "board.js"} {
		body := assetBody(t, ts, "/assets/js/"+script)
		// Only *function declarations* are of interest. A local string variable
		// holding pre-built markup is concatenated on purpose, and flagging it
		// would make this test cry wolf — the first version of it did exactly
		// that on `var featureCards = (...).map(...)`, which is correct code.
		for _, m := range regexp.MustCompile(`function\s+(\w+)\s*\(`).FindAllStringSubmatch(body, -1) {
			fn := m[1]
			if strings.Contains(body, "+ "+fn+" +") {
				t.Errorf("%s concatenates the function %s without calling it: "+
					"a function is being stringified into the markup instead of rendered",
					script, fn)
			}
		}
	}
}

// TestProjectPageRendersComplaintsTheServerActuallyHas: the complaints section
// must be populated from the API, not merely present in the script.
func TestProjectPageRendersComplaintsTheServerActuallyHas(t *testing.T) {
	ts := newTestServer(t)
	slug, projID := makeProject(t, ts, "complaint-render")
	makeRankedPair(t, ts, slug, projID, "A fix", "Another fix")

	js := assetBody(t, ts, "/assets/js/project.js")
	// The render function must actually invoke the card builder, and must be
	// handed the complaint list it fetched.
	if !strings.Contains(js, "complaintCards(complaints)") {
		t.Error("render does not call complaintCards with the complaint list")
	}
	if !strings.Contains(js, "render(p, features, complaints)") {
		t.Error("render is not given the complaints it fetched")
	}
	if !strings.Contains(js, "/complaints") {
		t.Error("project.js never reads the complaint list")
	}
	// The form's select needs the complaint ids, so the list must be available
	// at render time.
	if !strings.Contains(js, "__complaints") {
		t.Error("the feature form's complaint picker has no list to choose from")
	}
}
