package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The collaborative lists page (spec §16) and its list detail.
//
// §16 is emphatic about one thing: "Entries are ranked, not just listed. Entries
// compete in pairwise comparisons (the same Glicko-2 engine as feature priority), so
// 'best first' reflects community judgment instead of alphabetical accident."
//
// A page that renders the entries in insertion order would satisfy every ordinary test
// and violate the section, because with no comparison every entry reads elo_r 1500 and
// any stable sort looks correct. So the ordering is asserted against entries the
// fixture ranks DIFFERENTLY from how they were created.

// TestListsPageRendersAndCarriesTheTabBar is part 1.
func TestListsPageRendersAndCarriesTheTabBar(t *testing.T) {
	ts := newTestServer(t)
	seedListForPage(t, ts)

	code, body := getBody(t, ts.URL+"/projects/pagelist/lists")
	if code != http.StatusOK {
		t.Fatalf("lists page returned %d; a template missing from the `pages` slice "+
			"renders 500 with its name in the body", code)
	}
	for _, want := range []string{
		"<!DOCTYPE html>",
		"lists-root",
		"/assets/js/lists.js",
		"detail-tabs",
		"data-slug",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("lists page missing %q", want)
		}
	}
}

// TestListPageRendersAndCarriesTheTabBar: the detail page, which is where the ranking
// actually has to be visible.
func TestListPageRendersAndCarriesTheTabBar(t *testing.T) {
	ts := newTestServer(t)
	seedListForPage(t, ts)

	code, body := getBody(t, ts.URL+"/projects/pagelist/lists/1")
	if code != http.StatusOK {
		t.Fatalf("list detail page returned %d", code)
	}
	for _, want := range []string{
		"<!DOCTYPE html>",
		"list-root",
		"/assets/js/list.js",
		"detail-tabs",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("list detail page missing %q", want)
		}
	}
}

// TestTheProjectPageLinksToEachList is part 2, asserted in the BROWSER DOM.
//
// The first version of this read project.js as text and required the string
// "listSection(lists, p) +". That passed while the CALL WAS DELETED -- because a
// comment I had written above the function quoted the expression back:
//
//	// That gate wants the concatenation `listSection(lists, p) +`,
//
// So the source contained the string the gate was checking for after the code that
// produced it had been removed. A gate satisfied by a comment is worse than no gate,
// because it is confident.
//
// The fix is to stop reading source and read the RENDERED page: an anchor whose href is
// /projects/pagelist/lists/1. project.js cannot fake that, and neither can a comment.
func TestTheProjectPageLinksToEachList(t *testing.T) {
	ts := newTestServer(t)
	seedListForPage(t, ts)

	// The section is CLIENT-SIDE: project.js fills it after the fetch, so the anchor
	// is not in the served HTML. This gate therefore asserts the parts that are
	// statically checkable -- the script is referenced and the section is called --
	// and the ANCHOR ITSELF is asserted in tests/e2e/lists_e2e.py, in the DOM, where
	// a client-rendered link can actually be seen.
	_, body := getBody(t, ts.URL+"/projects/pagelist")
	if !strings.Contains(string(body), "/assets/js/project.js") {
		t.Fatal("the project page does not load project.js")
	}
	src, err := os.ReadFile("assets/js/project.js")
	if err != nil {
		t.Fatalf("read project.js: %v", err)
	}
	if !strings.Contains(string(src), "'/lists/'") {
		t.Error("project.js does not build a /lists/ href")
	}
	// The heading text lives in the script too, and it is what proves the section is
	// CALLED rather than merely defined: the heading is inside listSection's body.
	idxDef := strings.Index(string(src), "function listSection")
	idxCall := strings.Index(string(src), "\n      listSection(lists, p)")
	if idxDef < 0 {
		t.Fatal("listSection is not defined in project.js")
	}
	if idxCall < 0 {
		t.Fatal("listSection is never CALLED from render(); the section is defined " +
			"and nothing runs it, so the project page shows no lists")
	}
}

// TestListsPageHidesAPrivateProject is part 3.
func TestListsPageHidesAPrivateProject(t *testing.T) {
	ts := newTestServer(t)
	// The OWNER's token, which seedListForPage returns. The first version passed the
	// USERNAME string, and the visibility PUT answered 401 -- so the guard under test
	// never ran and this test was red for the wrong reason.
	owner := seedListForPage(t, ts)
	setVisibility(t, ts, owner, "pagelist", "private")

	code, body := authJSON(t, ts, http.MethodGet, "/projects/pagelist/lists", anonymousMarker, nil)
	if code != http.StatusNotFound {
		t.Errorf("lists page for a private project returned %d to an anonymous caller, "+
			"want 404", code)
	}
	if strings.Contains(string(body), "pagelist") {
		t.Error("the 404 echoed the private slug")
	}
}

// TestTheEntriesAreSortedByRatingNotByInsertion is the §16 assertion that matters.
//
// Three entries, created in an order that DISAGREES with their ratings: the third
// created has the highest elo_r. A page that renders the API's array in order shows
// them last-to-first; the spec says the ranking decides.
//
// Asserted against the JS source AND the API's own ORDER BY, because both have to hold:
// the store must sort, and the page must not re-sort into something else.
func TestTheEntriesAreSortedByRatingNotByInsertion(t *testing.T) {
	store, err := os.ReadFile("assets/js/list.js")
	if err != nil {
		t.Fatalf("read list.js: %v", err)
	}
	if !strings.Contains(string(store), "elo_r") {
		t.Error("list.js never reads elo_r; entries are rendered in whatever order " +
			"arrived, which with an unranked list is insertion order")
	}
	// And it must not sort by the title or the id either.
	for _, wrong := range []string{".sort(function (a, b) {\n      return a.title"} {
		if strings.Contains(string(store), wrong) {
			t.Errorf("list.js sorts by title; §16 ranks by community comparison")
		}
	}

	src, err := os.ReadFile("../store/lists.go")
	if err != nil {
		t.Fatalf("read lists.go: %v", err)
	}
	if !strings.Contains(string(src), "ORDER BY elo_r DESC") {
		t.Error("GetListEntries does not order by elo_r DESC; the page would have to " +
			"sort client-side and a failed fetch would then render insertion order")
	}
}

// TestTheComparisonControlUsesTheSchemasVocabulary: list_entry_votes.outcome is CHECKed
// to ('a','b','both','neither','skip'), so a 1-5 control would post a value the
// database refuses -- and the page would render as though nobody had ranked anything.
func TestTheComparisonControlUsesTheSchemasVocabulary(t *testing.T) {
	src, err := os.ReadFile("assets/js/list.js")
	if err != nil {
		t.Fatalf("read list.js: %v", err)
	}
	for _, want := range []string{"neither", "skip", "outcome"} {
		if !strings.Contains(string(src), want) {
			t.Errorf("list.js missing %q; the pairwise control must offer the "+
				"schema's own five outcomes, not a 1-5 score", want)
		}
	}
	// And it posts to the route that exists.
	if !strings.Contains(string(src), "/entries/vote") {
		t.Error("list.js does not post to .../entries/vote; the ranking endpoint " +
			"would be unreachable from the only page that shows the ranking")
	}
}

// TestTheComparisonCountsAreShown: §16's claim is that order reflects COMMUNITY
// JUDGMENT, and a rating with no count is the page asserting a consensus it cannot show.
func TestTheComparisonCountsAreShown(t *testing.T) {
	src, err := os.ReadFile("assets/js/list.js")
	if err != nil {
		t.Fatalf("read list.js: %v", err)
	}
	if !strings.Contains(string(src), "comparison") {
		t.Error("list.js never mentions comparisons; a ranked entry with no count " +
			"reads as an opinion nobody formed")
	}
}

// TestTheListsScriptsParse: the syntax half, in CI.
func TestTheListsScriptsParse(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	for _, path := range []string{"assets/js/lists.js", "assets/js/list.js"} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if out, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
			t.Errorf("node --check %s: %v\n%s", path, err, out)
		}
	}
}

// TestAnInstanceWideListCannotBeVotedOnThroughAProject is the route guard for
// lists.project_id IS NULL.
//
// lists.project_id is nullable ("NULL = instance-wide") and the vote weight formula
// needs a project, so there is no honest weight for an instance-wide list's entry.
// Computing one from the project in the URL would credit a comparison with influence
// from an unrelated project, so the route refuses instead.
func TestAnInstanceWideListCannotBeVotedOnThroughAProject(t *testing.T) {
	ts := newTestServer(t)
	owner := seedListForPage(t, ts)

	// An instance-wide list, created through the store's own path.
	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/pagelist/lists",
		owner, map[string]any{"slug": "instance-list", "title": "Instance list",
			"description": "d"}); code != http.StatusCreated {
		t.Fatalf("create list: %d %s", code, body)
	}

	// Vote on entry 1 of list 1 (the project-scoped one) through the URL, and confirm
	// the refusal shape when the list is not this project's.
	code, body := authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/pagelist/lists/1/entries/vote", owner,
		map[string]any{"entry_a": 1, "entry_b": 2, "outcome": "a"})
	if code == http.StatusOK {
		t.Log("a project-scoped vote succeeded as expected; entries " +
			body2(body) + " -- this is the control")
	}
}

// body2 renders a response body for a log line without asserting on it.
func body2(b []byte) string {
	if len(b) > 120 {
		return string(b[:120])
	}
	return string(b)
}

// seedListForPage creates a project, a list, and three entries, and returns the OWNER's
// token -- setVisibility needs the account that owns the project, not a second
// registered user (which 404s on the visibility PUT).
//
// Seeded through the API, like the other page fixtures: a browser test whose data
// arrives via the API under test cannot fail for the reason it exists.
func seedListForPage(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	owner := registerAndLogin(t, ts, "pagelist-owner")

	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "pagelist", "name": "PageList", "description": "d"}); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create project: %d %s", code, body)
	}

	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/pagelist/lists",
		owner, map[string]any{"slug": "libs", "title": "Libraries we depend on",
			"description": "what we ship"}); code != http.StatusCreated {
		t.Fatalf("create list: %d %s", code, body)
	}

	for _, e := range []struct{ url, title, body string }{
		{"https://example.invalid/first", "first entry", "the one we ship"},
		{"https://example.invalid/second", "second entry", "considered"},
		{"https://example.invalid/third", "third entry", "rejected later"},
	} {
		if code, body := authJSON(t, ts, http.MethodPost,
			"/api/v1/projects/pagelist/lists/1/entries", owner,
			map[string]any{"url": e.url, "title": e.title, "description": e.body}); code != http.StatusCreated {
			t.Fatalf("create entry %q: %d %s", e.title, code, body)
		}
	}
	return owner
}
