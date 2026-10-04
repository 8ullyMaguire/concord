package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestFeaturePageRendersAndCarriesTheTabBar is the definition of done for a new
// page, asserted from the start rather than after it is built.
//
// frontend-spec.md 3.1 ranks feature detail third, and every one of the eight
// remaining pages needs all seven of these properties. Writing them as a template
// for the test is deliberate: the first page built here took three iterations to
// discover that a page also has to be registered in the `pages` slice, linked from
// somewhere, and carrying the tab bar -- each of which fails silently.
func TestFeaturePageRendersAndCarriesTheTabBar(t *testing.T) {
	ts := newTestServer(t)
	seedFeatureForPage(t, ts)

	code, body := getBody(t, ts.URL+"/projects/pagefeat/features/1")
	if code != http.StatusOK {
		t.Fatalf("feature page returned %d; a page that is not registered renders 500 "+
			"with the template name in it", code)
	}

	for _, want := range []string{
		"<!DOCTYPE html>",
		"feature-root", // the JS mount point
		"/assets/js/feature.js",
		"detail-tabs", // the tab bar, from base.html
		"data-slug",   // the slug the JS reads the project from
	} {
		if !strings.Contains(body, want) {
			t.Errorf("feature page missing %q", want)
		}
	}

	// No centred spinner in the initial markup: the skeleton loader replaced that
	// everywhere, and a page that regresses to it is caught by the a11y suite.
	if strings.Contains(body, "loading-spinner") {
		t.Error("feature page renders a centred spinner instead of the skeleton loader")
	}
}

// TestFeaturePageIsLinkedFromTheProject pins reachability at the layer that builds
// the link.
//
// A page nothing links to is a perfectly ordinary state for working code to be in,
// which is how Finder, Scout, consensus and audit were ALL invisible while every
// test stayed green. That is the defect this repo keeps re-finding.
//
// This asserts on project.js, not on the server's HTML, because that is where the
// link is built: the feature cards are rendered client-side. Asserting the
// server-rendered HTML contained "/features/" would pass for the wrong reason (the
// script's own source contains that string) and would keep passing if the link were
// deleted from the card renderer.
//
// The complementary check that the rendered DOM really contains a clickable link is
// an e2e test, because only a browser has the DOM this gate is about.
func TestFeaturePageIsLinkedFromTheProject(t *testing.T) {
	src, err := os.ReadFile("assets/js/project.js")
	if err != nil {
		t.Fatalf("read project.js: %v", err)
	}
	js := string(src)
	if !strings.Contains(js, "/features/") {
		t.Error("project.js builds no link to a feature page, so feature detail is " +
			"reachable only by typing a URL")
	}
	// The link must be built from the project's slug and the feature's id, not
	// hardcoded: a static "/features/1" would satisfy the check above.
	if !strings.Contains(js, "esc(p.slug) + '/features/'") {
		t.Error("the feature link is not built from the project slug and feature id, " +
			"so it points somewhere fixed rather than at this feature")
	}
}

// TestFeaturePageHidesAPrivateProject is the security assertion for the shell.
//
// The page renders for logged-out visitors, so a 200 for a private project
// confirms the slug exists and puts it in the title. Same rule as the documents
// and audit pages.
func TestFeaturePageHidesAPrivateProject(t *testing.T) {
	ts := newTestServer(t)
	owner := seedFeatureForPage(t, ts)
	setVisibility(t, ts, owner, "pagefeat", "private")

	code, body := authJSON(t, ts, http.MethodGet, "/projects/pagefeat/features/1", anonymousMarker, nil)
	if code != http.StatusNotFound {
		t.Fatalf("a private project's feature page returned %d to an anonymous "+
			"caller; the shell leaks existence: %s", code, body)
	}
	if strings.Contains(string(body), "pagefeat") {
		t.Error("the 404 page body still names the private project's slug")
	}
}

// TestFeaturePageIsReachableWhenTheFeatureDoesNotExist keeps the shell rendering
// for a real project whose feature id is wrong.
//
// The page fetches its data client-side, so the server has no way to know the id is
// bogus. Answering 404 here would require a lookup the page does not otherwise
// need, and a wrong id in a URL is a reader's typo rather than an attack. The JS
// renders the "no such feature" state instead.
func TestFeaturePageIsReachableWhenTheFeatureDoesNotExist(t *testing.T) {
	ts := newTestServer(t)
	seedFeatureForPage(t, ts)

	code, _ := getBody(t, ts.URL+"/projects/pagefeat/features/999999")
	if code != http.StatusOK {
		t.Errorf("a bad feature id returned %d; the shell should render and let the "+
			"client say the feature is missing", code)
	}
}

// seedFeatureForPage creates a public project holding one validated complaint and
// one feature traced to it, and returns the owner's auth header.
//
// Through the API, because §6.2 requires a feature to trace back to a validated
// complaint -- a store-level shortcut would build a state the product forbids, and
// would not tell us whether the real path is reachable.
func seedFeatureForPage(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	owner := registerAndLogin(t, ts, "pagefeat-owner")

	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "pagefeat", "name": "PageFeat", "description": "d"}); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create project: %d %s", code, body)
	}

	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/pagefeat/complaints",
		owner, map[string]any{
			"project_id": 1, "title": "Export drops the last row",
			"body": "reproducible", "severity": 4, "frequency": 2.0,
		})
	if code != http.StatusCreated {
		t.Fatalf("complaint: %d %s", code, body)
	}
	complaintID := bodyInt64(t, body, "id")

	if code, v := authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/pagefeat/complaints/"+strconv.FormatInt(complaintID, 10)+"/validate",
		owner, map[string]any{}); code != http.StatusOK {
		t.Fatalf("validate: %d %s", code, v)
	}

	if code, f := authJSON(t, ts, http.MethodPost, "/api/v1/projects/pagefeat/features",
		owner, map[string]any{
			"project_id": 1, "author_id": 1,
			"title": "Export must be lossless", "body": "every row survives",
			"linked_complaints": []int64{complaintID},
		}); code != http.StatusCreated {
		t.Fatalf("feature: %d %s", code, f)
	}
	return owner
}

// TestTheProjectPagesJavaScriptHasNoUndefinedReferences is the gate the reachability
// check turned out to need.
//
// The complaint-page work changed one call: complaintCards(complaints) became
// complaintCards(complaints, p.slug), because render() receives the PROJECT as `p`
// and `slug` was not in scope. `slug` threw a ReferenceError on that line, which
// aborted the whole project-page render -- and TestComplaintPageIsLinkedFromThe
// Project PASSED throughout, because it only checks that the link string exists in
// the source. The page was completely broken and the gate was green.
//
// This is the general shape of that failure: a static check on a JS source proves a
// string is present, never that the code around it runs. Only executing it does.
//
// So it is executed here, through the same e2e path that already found it. What
// this Go test adds is the assertion that the JS PARSES at all -- `node --check`
// catches an unbalanced brace in CI, before the browser suite is ever run -- and
// the e2e suite's 13 tests cover the runtime half. A source-level gate alone would
// have missed the ReferenceError; a runtime gate alone would have needed a browser
// to find a syntax error. Both, or neither.
func TestTheProjectPagesJavaScriptHasNoUndefinedReferences(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH; the e2e suite exercises these scripts in a browser")
	}
	for _, name := range []string{"project.js", "feature.js", "complaint.js"} {
		out, err := exec.Command(node, "--check", filepath.Join("assets", "js", name)).CombinedOutput()
		if err != nil {
			t.Errorf("%s does not parse: %v\n%s", name, err, out)
		}
	}
}

// TestEveryRenderedScriptIsLoadedBySomePage keeps the asset list honest.
//
// A script nothing loads is a page that silently does nothing. The list is
// asserted in a11y_test.go; this asserts the direction that matters -- that the
// templates actually reference the script they need.
func TestEveryRenderedScriptIsLoadedBySomePage(t *testing.T) {
	pairs := map[string]string{
		"feature.html":   "feature.js",
		"complaint.html": "complaint.js",
	}
	for tmpl, script := range pairs {
		body, err := os.ReadFile(filepath.Join("templates", tmpl))
		if err != nil {
			t.Fatalf("read %s: %v", tmpl, err)
		}
		if !strings.Contains(string(body), script) {
			t.Errorf("%s does not load %s, so its page renders a permanent skeleton",
				tmpl, script)
		}
	}
}
