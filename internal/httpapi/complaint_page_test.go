package httpapi

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

// TestComplaintPageRendersAndCarriesTheTabBar is the three-part contract from
// internal/httpapi/feature_page_test.go, applied to the next page.
//
// Same three properties, and the same reason each one is asserted separately: the
// feature page took three iterations to satisfy them, each iteration a different
// one of the three failing silently. Written as a template for the remaining seven
// pages rather than rediscovered per page.
func TestComplaintPageRendersAndCarriesTheTabBar(t *testing.T) {
	ts := newTestServer(t)
	seedFeatureForPage(t, ts)

	code, body := getBody(t, ts.URL+"/projects/pagefeat/complaints/1")
	if code != http.StatusOK {
		t.Fatalf("complaint page returned %d; an unregistered template renders 500 "+
			"with the template name in it", code)
	}
	for _, want := range []string{
		"<!DOCTYPE html>",
		"complaint-root",
		"/assets/js/complaint.js",
		"detail-tabs",
		"data-slug",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("complaint page missing %q", want)
		}
	}
	if strings.Contains(body, "loading-spinner") {
		t.Error("complaint page renders a centred spinner instead of the skeleton loader")
	}
}

// TestComplaintPageIsLinkedFromTheProject asserts reachability at the layer that
// BUILDS the link, for the reason spelled out on the feature page's twin: the
// complaint cards are rendered client-side, so the served HTML would contain
// "/complaints/" through the script's own source and the assertion would keep
// passing if the link were deleted from the renderer.
func TestComplaintPageIsLinkedFromTheProject(t *testing.T) {
	src, err := os.ReadFile("assets/js/project.js")
	if err != nil {
		t.Fatalf("read project.js: %v", err)
	}
	js := string(src)
	// complaintCards takes the slug as a PARAMETER (complaintCards(list, slug)),
	// unlike the feature cards which close over the project object, so the
	// expression to match is esc(slug) and not esc(p.slug).
	if !strings.Contains(js, "esc(slug) + '/complaints/'") {
		t.Error("project.js builds no link to a complaint page, so complaint detail is " +
			"reachable only by typing a URL")
	}
}

// TestComplaintPageHidesAPrivateProject is the third property: the shell renders for
// logged-out visitors, so a 200 confirms the slug exists and puts it in the title.
func TestComplaintPageHidesAPrivateProject(t *testing.T) {
	ts := newTestServer(t)
	owner := seedFeatureForPage(t, ts)
	setVisibility(t, ts, owner, "pagefeat", "private")

	code, body := authJSON(t, ts, http.MethodGet, "/projects/pagefeat/complaints/1", anonymousMarker, nil)
	if code != http.StatusNotFound {
		t.Fatalf("a private project's complaint page returned %d: %s", code, body)
	}
	if strings.Contains(string(body), "pagefeat") {
		t.Error("the 404 body names the private project's slug")
	}
}

// TestAComplaintsFeaturesAreReadable pins the new endpoint's visibility.
func TestAComplaintsFeaturesAreReadable(t *testing.T) {
	ts := newTestServer(t)
	owner := seedFeatureForPage(t, ts)

	// The owner sees the linked feature.
	code, body := authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/pagefeat/complaints/1/features", owner, nil)
	if code != http.StatusOK {
		t.Fatalf("the owner cannot read a complaint's linked features: %d %s", code, body)
	}
	if !strings.Contains(string(body), "Export must be lossless") {
		t.Fatalf("the feature is linked in the DB but missing from the response: %s", body)
	}
}

// TestAComplaintsFeaturesAreHiddenOnAPrivateProject is the control for the test
// above: a 200 for the owner must not mean a 200 for everyone.
func TestAComplaintsFeaturesAreHiddenOnAPrivateProject(t *testing.T) {
	ts := newTestServer(t)
	owner := seedFeatureForPage(t, ts)
	setVisibility(t, ts, owner, "pagefeat", "private")

	code, body := authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/pagefeat/complaints/1/features", anonymousMarker, nil)
	if code == http.StatusOK {
		t.Errorf("a private complaint's linked features were served to an anonymous "+
			"caller: %s", body)
	}
}

// TestAComplaintsFeaturesCannotBeReadThroughAnotherProjectsURL covers the second
// check. requireProjectID passes on the project NAMED IN THE PATH; without the
// ownership check a public project in the path serves a private complaint's
// features. That is the gap that let handleGetFeature's ownership check survive the
// entire suite.
func TestAComplaintsFeaturesCannotBeReadThroughAnotherProjectsURL(t *testing.T) {
	ts := newTestServer(t)
	owner := seedFeatureForPage(t, ts)

	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "pubgate", "name": "Public", "description": "d"}); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create public project: %d %s", code, body)
	}

	path := "/api/v1/projects/pubgate/complaints/1/features"
	code, body := authJSON(t, ts, http.MethodGet, path, owner, nil)
	if code == http.StatusOK && strings.Contains(string(body), "Export must be lossless") {
		t.Errorf("features of a PRIVATE complaint were served under a PUBLIC project's "+
			"URL: %s -> %d %s", path, code, body)
	}
}

// TestABogusComplaintIdRendersTheShell keeps the shell rendering: the page fetches
// client-side, so a wrong id is the client's message to report, not the server's.
func TestABogusComplaintIdRendersTheShell(t *testing.T) {
	ts := newTestServer(t)
	seedFeatureForPage(t, ts)

	code, _ := getBody(t, ts.URL+"/projects/pagefeat/complaints/999999")
	if code != http.StatusOK {
		t.Errorf("a bad complaint id returned %d; the shell should render and let the "+
			"client say the complaint is missing", code)
	}
}
