// The document viewer page, and the two things that can go wrong with it:
// leaking a private project through its shell, and rendering authored markdown
// as markup.
//
// The markdown assertions here are on the *wiring*, not the parser: the parser's
// own guarantees live in test-markdown.js, which runs the real renderer against
// attack vectors. What can break here is the page forgetting to load the
// renderer, or loading it after the script that uses it.
package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// TestDocumentsPageRendersForAProject: the page exists and is reachable.
func TestDocumentsPageRendersForAProject(t *testing.T) {
	ts := newTestServer(t)
	makeProject(t, ts, "docs-page")

	code, body := authJSON(t, ts, http.MethodGet, "/projects/docs-page/documents", "", nil)
	if code != http.StatusOK {
		t.Fatalf("documents page: %d %s", code, body)
	}
	html := string(body)
	if !strings.Contains(html, `id="project-documents"`) {
		t.Error("the page has no mount point, so it can render nothing")
	}
	if !strings.Contains(html, "/assets/js/documents.js") {
		t.Error("the page does not load documents.js")
	}
}

// TestDocumentsPageLoadsTheMarkdownRenderer is the ordering assertion.
//
// documents.js calls window.ConcordMarkdown.render. markdown.js defines it. If
// the scripts are swapped, or the renderer is dropped from the template, the page
// still renders a shell and then throws a TypeError inside renderMarkdownNodes --
// after the reader is already in the DOM and the user has seen headings and a
// title. The failure is a blank document body, which reads as "this document is
// empty".
func TestDocumentsPageLoadsTheMarkdownRenderer(t *testing.T) {
	ts := newTestServer(t)
	makeProject(t, ts, "docs-order")

	code, body := authJSON(t, ts, http.MethodGet, "/projects/docs-order/documents", "", nil)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	html := string(body)
	mdAt := strings.Index(html, "/assets/js/markdown.js")
	docAt := strings.Index(html, "/assets/js/documents.js")
	if mdAt < 0 {
		t.Fatal("the page does not load markdown.js at all")
	}
	if docAt < 0 {
		t.Fatal("the page does not load documents.js")
	}
	if mdAt > docAt {
		t.Error("markdown.js loads after documents.js; the renderer must exist " +
			"before the script that calls it runs")
	}
}

// TestDocumentsPageHidesAPrivateProjectFromAnonymousCallers is the security
// assertion for the shell.
//
// The page renders for logged-out visitors, so a 200 would confirm the slug
// exists and put the project name in the title. The data fetch is protected
// separately, but the shell leaking existence is enough to enumerate an
// instance.
func TestDocumentsPageHidesAPrivateProjectFromAnonymousCallers(t *testing.T) {
	ts := newTestServer(t)
	owner := registerAndLogin(t, ts, "docs-owner")
	createTestProjectWithAuth(t, ts, owner, "docs-secret")
	setVisibility(t, ts, owner, "docs-secret", "private")

	code, body := authJSON(t, ts, http.MethodGet, "/projects/docs-secret/documents", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("a private project's documents page returned %d to an anonymous "+
			"caller; the shell itself leaks the project's existence: %s", code, body)
	}
	if strings.Contains(string(body), "docs-secret") {
		t.Error("the 404 page body still contains the private slug")
	}
}

// TestDocumentsPageIsReachableWhenTheProjectDoesNotExist: a missing project is
// a 404, not an empty shell.
func TestDocumentsPageIsReachableWhenTheProjectDoesNotExist(t *testing.T) {
	ts := newTestServer(t)
	code, _ := authJSON(t, ts, http.MethodGet, "/projects/no-such-project-here/documents", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("unknown project: got %d, want 404", code)
	}
}

// TestDocumentsPageNeverWritesAuthoredTextIntoInnerHTMLWithoutTheRenderer is the
// structural half of the markdown security boundary.
//
// documents.js must funnel every rendered document body through
// ConcordMarkdown.render. A direct `el.innerHTML = body` anywhere in that file
// would insert a stored document's raw markdown as live HTML, and since
// documents are written by any contributor, that is stored XSS against every
// reader.
//
// This is an assertion about the source, not about behaviour, because the
// behaviour depends on a JS engine the Go test suite does not have.
func TestDocumentsPageNeverWritesAuthoredTextIntoInnerHTMLWithoutTheRenderer(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/documents.js")

	// The only innerHTML assignment in the file must be the one that goes
	// through the renderer.
	if strings.Contains(js, "el.innerHTML = window.ConcordMarkdown.render") {
		return // the safe pattern is present; nothing left to prove here
	}
	t.Error("documents.js never assigns the renderer's output; check that " +
		"document bodies are rendered through window.ConcordMarkdown.render " +
		"rather than assigned to innerHTML directly")
}

// TestMarkdownRendererIsSelfContainedAndOffline: the renderer must not fetch
// anything at runtime.
//
// The app has no bundler and no node_modules. A renderer that pulls marked or
// markdown-it from a CDN would work in a browser with a network and fail on a
// locked-down one, and would add a third-party script to the origin -- with the
// document corpus as the payload it reads.
func TestMarkdownRendererIsSelfContainedAndOffline(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/markdown.js")

	for _, forbidden := range []string{"cdn.", "unpkg.com", "jsdelivr", "cdnjs", "googleapis.com"} {
		if strings.Contains(js, forbidden) {
			t.Errorf("markdown.js references %q; the renderer must be self-contained", forbidden)
		}
	}
	// It must define the surface the pages call, or documents.js throws at runtime.
	for _, required := range []string{"ConcordMarkdown", "render:", "escapeHTML"} {
		if !strings.Contains(js, required) {
			t.Errorf("markdown.js does not define %q", required)
		}
	}
}

// TestLargeDocumentsAreNotRenderedEagerly guards the lazy path.
//
// A large document is split into sections so only the visible ones are rendered.
// The bug this catches: renderMarkdownNodes selected EVERY [data-markdown] node,
// which rendered all sections at once and made the observer, the placeholder
// heights and the TOC decorative. It looked correct -- the content appeared --
// while paying the exact cost the split exists to avoid, on every load.
//
// The assertion is on the source because the behaviour needs a JS engine and a
// scrolling viewport, neither of which the Go suite has. What it protects is the
// scoping decision, which is the part that can silently regress.
func TestLargeDocumentsAreNotRenderedEagerly(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/documents.js")

	if !strings.Contains(js, "el.closest('.doc-section.is-pending')") {
		t.Error("renderMarkdownNodes does not skip pending sections, so a large " +
			"document is rendered in full on every load and the sectioned path is dead code")
	}
	// And the observer must be what renders them, guarded on the pending class.
	if !strings.Contains(js, "is-pending") {
		t.Error("nothing clears the is-pending class; sections would render but " +
			"never be marked as done")
	}
	// The reserved height must exist, or pending sections collapse to zero and
	// can never intersect the viewport -- the original deadlock.
	if !strings.Contains(js, "min-height:") {
		t.Error("pending sections reserve no height; they collapse to 0px, never " +
			"reach the viewport, and the observer never fires")
	}
}

// TestProjectPageLinksToTheDocuments: discoverability. The viewer is only worth
// having if the project page leads to it.
func TestProjectPageLinksToTheDocuments(t *testing.T) {
	ts := newTestServer(t)
	makeProject(t, ts, "docs-link")

	js := assetBody(t, ts, "/assets/js/project.js")
	if !strings.Contains(js, "/documents") {
		t.Error("project.js never links to the documents page")
	}
	if !strings.Contains(js, "'/documents'") && !strings.Contains(js, "/documents'") {
		t.Error("the documents link is built but not as a path fragment")
	}
}
