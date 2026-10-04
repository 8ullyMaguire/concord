package httpapi

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryPageIsReachableByALink guards the failure that made Finder, Scout and
// the audit log invisible on the deployed site while every test stayed green.
//
// The mechanism: a page is a route plus a template. Nothing requires anything to
// LINK to it, so an unreachable page is a perfectly ordinary state for the code
// to be in. Nothing failed because nothing could fail -- the same shape as the
// defects recorded in docs/KNOWN-ISSUES.md.
//
// Measured on the live instance before this test existed: the site nav held four
// links (/, /search, /projects, /login). /finder and /scout were linked from
// nothing at all, and the project header omitted /audit. Every one of them
// answered 200 to a direct request. They were simply unreachable by clicking.
//
// The page list is written by hand on purpose. Deriving it from the router would
// let a route satisfy its own check, which is the tautology this suite exists to
// avoid.

// navPages must be reachable from the site header on every page.
//
// Finder and Scout are the two entry points that make the product usable at all:
// Scout answers "should I build this?", Finder guides a filing. A visitor who
// cannot see them has no way to file a complaint.
var navPages = []string{"/finder", "/scout"}

// projectPages must be reachable from the project detail header.
//
// The header is built in JavaScript (assets/js/project.js), so its links are
// STRING CONCATENATIONS. Worth stating because the first attempt at this test
// grepped the templates for href="/board" and concluded five working pages were
// orphaned; they were all linked and the grep simply could not see the
// concatenation. Anything that inspects this must match a SUFFIX, not an href.
var projectPages = []string{
	"/board", "/consensus", "/documents", "/audit", "/rank", "/ranking",
}

// assetCorpus concatenates every template and JS asset, which is where every
// href in the application is written.
func assetCorpus(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, d := range []string{"templates", filepath.Join("assets", "js")} {
		entries, err := os.ReadDir(d)
		if err != nil {
			t.Fatalf("read %s: %v", d, err)
		}
		for _, e := range entries {
			if e.IsDir() || (!strings.HasSuffix(e.Name(), ".html") && !strings.HasSuffix(e.Name(), ".js")) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(d, e.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			b.Write(data)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// TestNavLinksEveryTopLevelPage checks the site header offers Finder and Scout.
func TestNavLinksEveryTopLevelPage(t *testing.T) {
	base, err := os.ReadFile(filepath.Join("templates", "base.html"))
	if err != nil {
		t.Fatalf("read base.html: %v", err)
	}
	header := string(base)
	// Only the nav element. The footer and the <link rel=alternate> feed entry
	// are not navigation, and the brand link is not a page list.
	start := strings.Index(header, `class="site-nav"`)
	if start < 0 {
		t.Fatal("base.html has no site-nav, so this test cannot tell what is navigable")
	}
	end := strings.Index(header[start:], "</nav>")
	if end < 0 {
		t.Fatal("site-nav is never closed")
	}
	nav := header[start : start+end]

	for _, want := range navPages {
		if !strings.Contains(nav, `href="`+want+`"`) {
			t.Errorf("site nav does not link %s: the page answers 200 but no user can reach it", want)
		}
	}
}

// TestEveryRegisteredPageHasAnInboundLink is the general form. Each page route is
// fetched for real and every href in the rendered response is collected, then each
// page must be the target of at least one of them.
//
// Collected across ALL pages rather than per-page, because navigation is the
// property of the site, not of any single document.
func TestEveryRegisteredPageHasAnInboundLink(t *testing.T) {
	ts := newTestServer(t)
	createTestProject(t, ts, "nav-project")

	pages := []string{
		"/", "/finder", "/scout", "/projects", "/search", "/login", "/register",
		"/projects/nav-project",
		"/projects/nav-project/board",
		"/projects/nav-project/consensus",
		"/projects/nav-project/documents",
		"/projects/nav-project/audit",
		"/projects/nav-project/rank",
		"/projects/nav-project/ranking",
	}

	// Entry points. Nothing links to the home page or the auth pages because they
	// are reached by typing a URL, and nothing should link to a sign-in form.
	selfLinked := map[string]bool{"/": true, "/login": true, "/register": true}

	corpus := assetCorpus(t)
	status := map[string]int{}

	for _, p := range pages {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		status[p] = resp.StatusCode

		// A page that does not render cannot be navigated to, so report it here
		// rather than letting a missing link be the only symptom.
		if resp.StatusCode != http.StatusOK {
			t.Errorf("page %s returned %d, so it is not usable regardless of links", p, resp.StatusCode)
			continue
		}
		corpus += "\n" + string(body)
	}

	for _, p := range pages {
		if selfLinked[p] {
			continue
		}
		if !hasLinkTo(corpus, p) {
			t.Errorf("page %s renders %d but nothing links to it", p, status[p])
		}
	}
}

// normaliseCorpus rewrites every spelling of a project link into one canonical
// form, so link checking compares PATHS instead of enumerating source syntax.
//
// Three spellings exist for the same link:
//
//	href="/projects/{{.Slug}}/board"                    template
//	href="/projects/' + esc(p.slug) + '/board"           JavaScript concatenation
//	href="/projects/nav-project/board"                  rendered HTML
//
// The first version of this check enumerated those spellings as literal needles
// and reported four working pages as unlinked, because the concatenation form did
// not match. Enumerating syntax is how that goes wrong: the next spelling of the
// same link fails the check for a reason that has nothing to do with the site.
//
// So the JS glue is collapsed to {{.Slug}} first, and everything afterwards is one
// string comparison.
func normaliseCorpus(corpus string) string {
	c := corpus
	// The JavaScript builds the link as:
	//   '<a class="detail-tab" href="/projects/' + esc(p.slug) + '/board">Board</a>'
	// i.e. the slug expression is `' + esc(p.slug) + '` and the suffix that follows
	// it begins with a single quote, not a double one. Replacing the expression with
	// a bare {{.Slug}} therefore yields exactly href="/projects/{{.Slug}}/board".
	//
	// Ordering matters and getting it wrong is silent: an earlier version replaced
	// `'+esc(p.slug)+'"` FIRST, which never occurs (the char after the closing
	// quote is `'`), so the narrower rule simply did not apply and the wider one
	// then produced {{.Slug}}" -- an extra quote that no href pattern matches. The
	// test then reported four working, correctly linked pages as unlinked.
	//
	// One rule, no quote variants. Each additional spelling tried here is a chance
	// to break the links that DO work.
	c = strings.ReplaceAll(c, `' + esc(p.slug) + '`, "{{.Slug}}")
	c = strings.ReplaceAll(c, `' + slug + '`, "{{.Slug}}")
	c = strings.ReplaceAll(c, `' + p.slug + '`, "{{.Slug}}")
	// Whitespace inside a tag is not significant for this comparison.
	c = strings.ReplaceAll(c, "\n", "")
	c = strings.ReplaceAll(c, "\r", "")
	c = strings.ReplaceAll(c, "\t", "")
	return c
}

// hasLinkTo reports whether the corpus contains an href resolving to page.
func hasLinkTo(corpus, page string) bool {
	c := normaliseCorpus(corpus)
	slug := "/nav-project"

	i := strings.Index(page, slug)
	if i < 0 {
		// A top-level page: the literal href is the whole check.
		return strings.Contains(c, `href="`+page+`"`) || strings.Contains(c, `href='`+page+`'`)
	}
	prefix, suffix := page[:i], page[i+len(slug):]

	// Rendered HTML with the concrete slug.
	if strings.Contains(c, `href="`+prefix+slug+suffix+`"`) {
		return true
	}
	// Template or normalised JavaScript.
	if strings.Contains(c, `href="`+prefix+"/{{.Slug}}"+suffix+`"`) {
		return true
	}
	// A bare JS fragment that lost its quoting during concatenation.
	return suffix != "" && strings.Contains(c, prefix+"/{{.Slug}}"+suffix)
}

// TestSubPagesCarryTheTabBar is the second half of the fix, and the reason the
// first attempt did not work.
//
// The tab bar was first built in project.js. That put it on the project overview
// only, because every sub-page is a separate template loading its own bundle -- so
// audit, consensus, board, rank, ranking and documents each rendered with no way
// back to any sibling. Six pages, six dead ends.
//
// It now lives in templates/base.html and is driven by the request path, so it
// appears on all seven.
func TestSubPagesCarryTheTabBar(t *testing.T) {
	ts := newTestServer(t)
	createTestProject(t, ts, "tab-project")

	for _, p := range []string{
		"/projects/tab-project",
		"/projects/tab-project/consensus",
		"/projects/tab-project/board",
		"/projects/tab-project/documents",
		"/projects/tab-project/audit",
		"/projects/tab-project/rank",
		"/projects/tab-project/ranking",
	} {
		_, body := getBody(t, ts.URL+p)
		if !strings.Contains(body, "detail-tabs") {
			t.Errorf("%s renders without the project tab bar, so it is a dead end", p)
		}
		// Every sibling must be reachable from here, not just the bar.
		for _, suffix := range projectPages {
			if !strings.Contains(normaliseCorpus(body), "/{{.Slug}}"+suffix) &&
				!strings.Contains(body, "/tab-project"+suffix) {
				t.Errorf("%s does not link a sibling %s page", p, suffix)
			}
		}
	}
}

// TestProjectTabsAreNotOnNonProjectPages keeps the layout's path-driven nav from
// appearing where it makes no sense. /projects with no slug is not a project page,
// and rendering six links built from an empty slug would produce /projects//board.
func TestProjectTabsAreNotOnNonProjectPages(t *testing.T) {
	ts := newTestServer(t)
	for _, p := range []string{"/", "/projects", "/finder", "/scout", "/search"} {
		_, body := getBody(t, ts.URL+p)
		if strings.Contains(body, "detail-tabs") {
			t.Errorf("%s renders project tabs it has no project for", p)
		}
		if strings.Contains(body, "/projects//") {
			t.Errorf("%s renders a malformed project link (empty slug)", p)
		}
	}
}

// TestProjectTabMarksTheCurrentPage checks the active state is real.
//
// The layout reads the third path segment, so each sub-page must mark exactly its
// own tab. An is-active class that never changes would render six identical pages
// with one tab permanently lit, which reads as working and tells the reader
// nothing.
func TestProjectTabMarksTheCurrentPage(t *testing.T) {
	ts := newTestServer(t)
	createTestProject(t, ts, "mark-project")

	cases := []struct{ path, want string }{
		{"/projects/mark-project", "Overview"},
		{"/projects/mark-project/audit", "Audit"},
		{"/projects/mark-project/consensus", "Consensus"},
		{"/projects/mark-project/board", "Board"},
		{"/projects/mark-project/ranking", "Ranking"},
	}
	for _, c := range cases {
		_, body := getBody(t, ts.URL+c.path)
		// Find the anchor carrying is-active and read its label.
		//
		// The label comes AFTER the href in this markup, so the first version of
		// this assertion read backwards from the class to the opening >, compared
		// against ">"+label+"<", and failed on all five cases while the page was
		// rendering exactly the right tab. A test that reports correct behaviour as
		// broken is worse than no test: it trains you to ignore it.
		//
		// Assert on the anchor's own text instead, which is what "this tab is
		// marked" actually means.
		anchor := activeAnchor(body)
		if anchor == "" {
			t.Errorf("%s marks no tab active", c.path)
			continue
		}
		if !strings.Contains(anchor, ">"+c.want+"<") {
			t.Errorf("%s marks %q active, expected %q", c.path, tabLabel(anchor), c.want)
		}
	}
}

// activeAnchor returns the first <a ...> element carrying the is-active class.
func activeAnchor(body string) string {
	i := strings.Index(body, "is-active")
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(body[:i], "<a ")
	if start < 0 {
		return ""
	}
	// The closing tag is INCLUDED, so tabLabel can find it. Excluding it left
	// tabLabel with no "</a>" to search for and every label came back empty.
	end := strings.Index(body[start:], "</a>")
	if end < 0 {
		return body[start:]
	}
	return body[start : start+end+len("</a>")]
}

// tabLabel extracts the visible text of an anchor element.
//
// The closing tag's own ">" is the LAST ">" in the fragment, so slicing at it
// leaves an empty range and every label came back blank -- which is how this
// reported five correct pages as wrong. Find the end of the opening tag, not the
// end of the element.
func tabLabel(anchor string) string {
	i := strings.Index(anchor, ">")
	if i < 0 {
		return ""
	}
	rest := anchor[i+1:]
	j := strings.Index(rest, "</a>")
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:j])
}

// TestProjectHeaderLinksEverySubPage pins the full set, so a page added later
// without a link fails here instead of shipping invisible.
func TestProjectHeaderLinksEverySubPage(t *testing.T) {
	corpus := assetCorpus(t)
	for _, suffix := range projectPages {
		linked := false
		for _, q := range []string{"'", `"`} {
			if strings.Contains(corpus, q+suffix) || strings.Contains(corpus, `href="`+suffix+`"`) {
				linked = true
			}
		}
		if !linked {
			t.Errorf("nothing links to a project's %s page", suffix)
		}
	}
}

// TestProjectTabsPathClassification pins which paths get project tabs.
//
// projectTabs is the only thing standing between a malformed URL and six links
// built from a wrong slug, so each shape is asserted directly rather than through
// a rendered page.
//
// Both halves of the guard are load-bearing, and each needs its own case.
//
// The first version of this table had no two-segment non-project path, so a
// mutation dropping `parts[0] != "projects"` SURVIVED: every case was either
// shorter than two segments (rejected by the length check) or began with
// /projects. Adding "/finder/x" killed it. Redundant-looking conditions need
// cases that isolate them, or a mutation of one is invisible.
func TestProjectTabsPathClassification(t *testing.T) {
	cases := []struct {
		path     string
		wantTabs bool
		slug     string
		sub      string
	}{
		{"", false, "", ""},
		{"/", false, "", ""},
		{"/finder", false, "", ""},
		{"/scout", false, "", ""},
		{"/search", false, "", ""},
		{"/login", false, "", ""},
		{"/projects", false, "", ""},
		{"/atom.xml", false, "", ""},
		{"/x", false, "", ""},
		{"/projects/", false, "", ""},
		// A two-segment path that is NOT /projects/<slug>. Without the
		// parts[0] comparison this returns slug="finder", and the layout would
		// render six links under /projects/finder/board for the Finder page.
		{"/finder/x", false, "", ""},
		{"/search/x", false, "", ""},
		{"/atom.xml/x", false, "", ""},
		{"/projects/x", true, "x", ""},
		{"/projects/x/audit", true, "x", "audit"},
		{"/projects/a/b/c", true, "a", "b"},
	}
	for _, c := range cases {
		got := projectTabs(c.path)
		if c.wantTabs != (got != nil) {
			t.Errorf("projectTabs(%q) tabs=%v, want %v (%v)", c.path, got != nil, c.wantTabs, got)
			continue
		}
		if got != nil && (got["Slug"] != c.slug || got["Sub"] != c.sub) {
			t.Errorf("projectTabs(%q) = slug %q sub %q, want %q/%q",
				c.path, got["Slug"], got["Sub"], c.slug, c.sub)
		}
	}
}

// TestLayoutRendersEachPieceOnce catches a whole-document duplication.
//
// While moving the tab bar block, an index-based edit wrote the middle of
// base.html out twice. Every rendered project page then carried FOURTEEN tabs
// instead of seven -- and every navigation test still passed, because each one
// asks whether a link EXISTS, not how many times.
//
// So the assertions here are on COUNTS, not presence. A layout that renders a
// region twice is broken in a way no presence check can see, and duplicating a
// <nav> also duplicates every landmark for anyone using a screen reader.
func TestLayoutRendersEachPieceOnce(t *testing.T) {
	ts := newTestServer(t)
	createTestProject(t, ts, "once-project")

	_, body := getBody(t, ts.URL+"/projects/once-project/audit")

	if n := strings.Count(body, "<html"); n != 1 {
		t.Errorf("page contains %d <html> elements, want 1", n)
	}
	if n := strings.Count(body, "</html>"); n != 1 {
		t.Errorf("page contains %d </html> elements, want 1", n)
	}
	if n := strings.Count(body, `class="detail-tabs"`); n != 1 {
		t.Errorf("page renders the project tab bar %d times, want 1", n)
	}
	if n := strings.Count(body, `class="site-nav"`); n != 1 {
		t.Errorf("page renders the site nav %d times, want 1", n)
	}
	if n := strings.Count(body, "<main"); n != 1 {
		t.Errorf("page contains %d <main> elements, want 1", n)
	}
	// Each tab exactly once: seven sections, no repeats.
	//
	// Counted with the trailing quote so the CONTAINER (`class="detail-tabs"`) is
	// not counted as an eighth tab. Counting `class="detail-tab` reported 8 and
	// looked like a real duplication -- which is a useful reminder that a count
	// assertion needs its own boundary, or it fails on a correct page.
	//
	// The ACTIVE tab renders class="detail-tab is-active" and the rest render
	// class="detail-tab", so counting either form alone is always short by one --
	// 6 or 8 depending which, and both look like a real defect.
	n := strings.Count(body, `class="detail-tab"`) +
		strings.Count(body, `class="detail-tab is-active"`)
	if n != 7 {
		t.Errorf("page renders %d tabs, want 7", n)
	}
	// Exactly one lit tab. Two lit tabs means the active state is decorative.
	if n := strings.Count(body, "is-active"); n != 1 {
		t.Errorf("page marks %d tabs active, want exactly 1", n)
	}
}

// TestErrorPagesNeverEchoTheSlugFromThePath is the security half of making the
// layout path-driven.
//
// Path-driven chrome means the URL decides what a page renders. On a 404 the URL
// is exactly the thing that must not be confirmed: the slug is either unknown or
// forbidden, and a page echoing it back is enough to enumerate the instance.
//
// This was a real regression, not a hypothetical: switching notFoundPage to
// pageFor made every 404 under /projects/<slug> render seven links naming a
// private project. TestDocumentsPageHidesAPrivateProjectFromAnonymousCallers
// caught it -- the documents test, not a navigation test, which is why the
// general orphan check here never would have.
//
// Asserted on the 404 body, not on the status, because the status was always
// right.
func TestErrorPagesNeverEchoTheSlugFromThePath(t *testing.T) {
	ts := newTestServer(t)
	owner := registerAndLogin(t, ts, "leak-owner")
	createTestProjectWithAuth(t, ts, owner, "leak-secret")
	setVisibility(t, ts, owner, "leak-secret", "private")

	for _, p := range []string{
		"/projects/leak-secret",
		"/projects/leak-secret/board",
		"/projects/leak-secret/documents",
		"/projects/leak-secret/audit",
		"/projects/leak-secret/consensus",
		"/projects/leak-secret/rank",
		"/projects/leak-secret/ranking",
	} {
		_, body := authJSON(t, ts, http.MethodGet, p, "", nil)
		if strings.Contains(string(body), "leak-secret") {
			t.Errorf("%s returned a page naming a private project's slug", p)
		}
		if strings.Contains(string(body), "detail-tabs") {
			t.Errorf("%s renders project tabs for a project the caller may not read", p)
		}
	}
}

// TestProjectTabsAreKeyboardReachable keeps the tab bar inside the existing
// focus-ring baseline.
//
// .detail-tab is a plain <a>, so it is covered by the bare `a:focus-visible`
// selector today. That coverage is implicit -- nothing names the class -- so a
// restyle that swaps .detail-tab for a styled <div> or <span> would lose the
// keyboard affordance without failing anything here.
//
// Asserted on the rendered markup being real links, which is the property that
// makes them focusable at all.
func TestProjectTabsAreKeyboardReachable(t *testing.T) {
	ts := newTestServer(t)
	createTestProject(t, ts, "kbd-project")

	_, body := getBody(t, ts.URL+"/projects/kbd-project/audit")

	// Every tab is an <a href>, not a click-handled element.
	n := strings.Count(body, `<a class="detail-tab`)
	if n == 0 {
		t.Fatal("no tab anchors found")
	}
	if n != strings.Count(body, `<a class="detail-tab is-active"`)+
		strings.Count(body, `<a class="detail-tab"`) {
		t.Error("a tab is not an anchor, so it cannot be reached by keyboard")
	}
	// Each has an href, so Enter activates it.
	for _, suffix := range projectPages {
		if !strings.Contains(body, `href="/projects/kbd-project`+suffix+`"`) {
			t.Errorf("tab for %s has no href", suffix)
		}
	}

	// And the stylesheet keeps a focus indicator on plain anchors, which is what
	// makes the tabs visible to a keyboard user.
	css, err := os.ReadFile(filepath.Join("assets", "css", "style.css"))
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	if !strings.Contains(string(css), "a:focus-visible") {
		t.Error("no a:focus-visible rule; the tab bar would be invisible to a keyboard user")
	}
}
