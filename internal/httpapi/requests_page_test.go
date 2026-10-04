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

// The §17 request board page, written to the same three-part contract as the feature
// and complaint pages: it renders, something links to it, and it hides a private
// project.
//
// §17 is the substance here and it is unusually specific, so the page has to carry
// all of it rather than a generic list of answers:
//
//   - an answer is a PROJECT REFERENCE with a fit rationale, not an opinion
//   - one answer per project per request
//   - answers compete in pairwise comparisons on a Glicko rating
//   - the author may mark an accepted answer, DISPLAYED BUT NOT OVERRIDING THE RANK
//   - self-promotion is transparent: an answer by the proposer's own project carries
//     an affiliation label
//
// The last one is the one a naive page gets wrong, so it is asserted here rather than
// left to review.

// TestRequestsPageRendersAndCarriesTheTabBar is part 1 of the contract.
func TestRequestsPageRendersAndCarriesTheTabBar(t *testing.T) {
	ts := newTestServer(t)
	seedRequestForPage(t, ts)

	code, body := getBody(t, ts.URL+"/projects/pagereq/requests/1")
	if code != http.StatusOK {
		t.Fatalf("requests page returned %d; a page that is not registered in the "+
			"`pages` slice renders 500 with the template name in it", code)
	}

	for _, want := range []string{
		"<!DOCTYPE html>",
		"request-root", // the JS mount point
		"/assets/js/requests.js",
		"detail-tabs", // the tab bar, from base.html
		"data-slug",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("requests page missing %q", want)
		}
	}

	if strings.Contains(body, "loading-spinner") {
		t.Error("requests page renders a centred spinner instead of the skeleton loader")
	}
}

// TestRequestsPageIsLinkedFromTheProject is part 2: reachability, asserted where the
// link is BUILT.
//
// Asserted on project.js rather than the server HTML on purpose. The request list is
// rendered client-side, so a server-HTML assertion would pass because the script's own
// source contains the path, and would keep passing if the link were deleted from the
// renderer. That gate kept Finder, Scout, consensus, audit, the feature page and the
// complaint page invisible with every test green.
func TestRequestsPageIsLinkedFromTheProject(t *testing.T) {
	src, err := os.ReadFile("assets/js/project.js")
	if err != nil {
		t.Fatalf("read project.js: %v", err)
	}
	// Built from BOTH the slug and the request id, so a hardcoded path cannot satisfy
	// it.
	//
	// Asserting the halves rather than one literal is deliberate. The first version
	// wanted the substring "/requests/${", which only appeared because the source had
	// been contorted into a pseudo-template-literal that is not one -- and a gate that
	// rewards a strange way of writing the code keeps the strangeness long after the
	// reason is forgotten. Assert the code as it should read.
	//
	// The first two are what make this more than a string check: the section has to
	// be DEFINED and CALLED. Both are needed, and neither alone is enough:
	//
	//   - asserting only the call passes with no function, because the call is a
	//     string in a template
	//   - asserting only "requestSection(requests, p)" passes when the call is
	//     DELETED, because the DEFINITION has that exact signature.
	//
	// Mutation proved the second one: removing the call site
	// `      requestSection(requests, p) +` from render() left the suite green,
	// because the only remaining occurrence was `function requestSection(requests, p)`
	// -- the declaration. So the call is asserted WITH its surrounding concatenation,
	// which a declaration cannot produce.
	for _, want := range []string{
		"function requestSection",
		"requestSection(requests, p) +",
		"'/projects/' + encodeURIComponent(p.slug) + '/requests/'",
		"encodeURIComponent(r.id)",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("project.js does not build a request detail link from slug and "+
				"id: missing %q", want)
		}
	}
}

// TestRequestsPageHidesAPrivateProject is part 3.
//
// The shell renders for logged-out visitors, so a 200 confirms the slug exists and
// puts it in the title bar. A private project's page must 404 and must not echo the
// slug into the error page either.
func TestRequestsPageHidesAPrivateProject(t *testing.T) {
	ts := newTestServer(t)
	// The OWNER's token, which seedRequestForPage returns. Registering a second user
	// gave a 404 from the visibility PUT -- that account does not own pagereq -- and
	// the guard under test never ran. The helper's return value existed for exactly
	// this and the first version ignored it.
	owner := seedRequestForPage(t, ts)
	// setVisibility, not `visibility` on create: the create handler does not read
	// that field, so the first version of this fixture created a PUBLIC project named
	// "privateq" and the guard never fired. The test was green about the wrong thing.
	setVisibility(t, ts, owner, "pagereq", "private")

	// anonymousMarker, not the owner's token. Read as the owner this always passed,
	// which is the mistake worth naming: a guard test that reads WITH the token
	// proves nothing about what a logged-out visitor sees.
	code, body := authJSON(t, ts, http.MethodGet, "/projects/pagereq/requests/1",
		anonymousMarker, nil)
	if code != http.StatusNotFound {
		t.Errorf("requests page for a private project returned %d, want 404", code)
	}
	if strings.Contains(string(body), "pagereq") {
		t.Error("the 404 echoed the private slug, confirming the project exists")
	}
}

// TestRequestsPageScriptHasNoUndefinedReferences is the static half of defect 5.
//
// `complaintCards(complaints, p.slug)` threw a ReferenceError because `slug` was not
// in scope, and it aborted the ENTIRE project-page render -- while the reachability
// gate above stayed green, because it only checks that a string exists.
//
// node --check cannot catch this: a ReferenceError parses fine. So the identifiers
// each render function uses are asserted to be declared in the file at all.
func TestRequestsPageScriptHasNoUndefinedReferences(t *testing.T) {
	src, err := os.ReadFile("assets/js/requests.js")
	if err != nil {
		t.Fatalf("read requests.js: %v", err)
	}

	// The complaint page interpolated `${slug}` where the enclosing function's
	// parameter was `p`. Asserted directly, because a source gate that only looks
	// for the link string stayed green through exactly that bug.
	if strings.Contains(string(src), "${slug}") ||
		strings.Contains(string(src), "${project.slug}") {
		t.Errorf("requests.js interpolates a bare identifier that is not a parameter of " +
			"the enclosing function:\n  this is exactly how complaintCards(complaints, " +
			"slug) became a ReferenceError that blanked the whole project page")
	}
}

// TestRequestsPageScriptParses is the other half: a missing brace must fail in CI,
// before any browser runs.
func TestRequestsPageScriptParses(t *testing.T) {
	for _, script := range []string{
		"assets/js/requests.js", "assets/js/project.js",
	} {
		if _, err := os.Stat(script); err != nil {
			t.Errorf("%s: %v", script, err)
			continue
		}
		cmd := exec.Command("node", "--check", script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("node --check %s: %v\n%s", script, err, out)
		}
	}
}

// TestEveryRenderedScriptIsLoadedBySomePage keeps a new script from being dead weight.
func TestRequestsPageScriptIsLoadedBySomePage(t *testing.T) {
	// requests-detail.html, not requests.html: the two request pages shared one
	// filename until the INDEX page needed one, and whichever was written last won.
	// The template name is the ONLY thing distinguishing them at render time, so the
	// detail handler pointing at the index's old name is a 500 with a clear log and
	// a blank page for the reader.
	data, err := os.ReadFile(filepath.Join("templates", "requests-detail.html"))
	if err != nil {
		t.Fatalf("read requests-detail.html: %v", err)
	}
	if !strings.Contains(string(data), "/assets/js/requests.js") {
		t.Error("requests-detail.html does not load requests.js, so the page renders " +
			"empty without error -- the exact failure mode api_surface.py was written " +
			"to find")
	}
}

// seedRequestForPage creates a public project, a request in it, and one answer, so
// the page has the §17 shape: a question and a project's fit for it.
//
// Seeded through the API, like seedFeatureForPage, because the handler being tested
// is the create path too -- a fixture written straight to SQLite would let a broken
// POST pass here and fail on the page.
func seedRequestForPage(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	owner := registerAndLogin(t, ts, "pageq-author")

	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "pagereq", "name": "PageReq", "description": "d"}); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create project: %d %s", code, body)
	}

	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/pagereq/requests",
		owner, map[string]any{
			"project_id": 1,
			"title":      "Self-hosted Kanban with WIP limits",
			"body":       "Must run on a Pi. MIT or AGPL.",
		})
	if code != http.StatusCreated {
		t.Fatalf("create request: %d %s", code, body)
	}
	reqID := bodyInt64(t, body, "id")

	if code, a := authJSON(t, ts, http.MethodPost,
		"/api/v1/requests/"+strconv.FormatInt(reqID, 10)+"/answers", owner,
		map[string]any{"body": "It fits: small enough for a Pi, and MIT."}); code != http.StatusCreated {
		t.Fatalf("create answer: %d %s", code, a)
	}
	return owner
}
