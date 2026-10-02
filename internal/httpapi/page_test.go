package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func getBody(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(body)
}

// TestIndexPage verifies the homepage renders the premise-2 theme.
func TestIndexPage(t *testing.T) {
	ts := newTestServer(t)
	code, body := getBody(t, ts.URL+"/")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	for _, want := range []string{
		"<!DOCTYPE html>",
		"The forge for",
		"decisions",
		"hero-title",
		"What Concord does",
		"stats-strip",
		"The flip",
		"site-header",
		"/assets/css/style.css",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("homepage missing %q", want)
		}
	}
}

// TestProjectsPage verifies the projects listing page renders the theme.
func TestProjectsPage(t *testing.T) {
	ts := newTestServer(t)
	code, body := getBody(t, ts.URL+"/projects")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	for _, want := range []string{"<!DOCTYPE html>", "page-title", "projects-grid", "site-header"} {
		if !strings.Contains(body, want) {
			t.Errorf("projects page missing %q", want)
		}
	}
}

// TestSearchPage verifies the search page renders the theme + search box.
func TestSearchPage(t *testing.T) {
	ts := newTestServer(t)
	code, body := getBody(t, ts.URL+"/search")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	for _, want := range []string{"<!DOCTYPE html>", "search-box-input", "search-results", "Type a query"} {
		if !strings.Contains(body, want) {
			t.Errorf("search page missing %q", want)
		}
	}
}

// TestProjectPage renders the detail shell for a real project (data hydrates
// client-side).
//
// This used to request /projects/nonexistent and assert a 200, on the theory
// that the shell renders before the data arrives. That was wrong in a way worth
// recording: the page answered 200 for any slug at all, including ones that do
// not exist, so the response code confirmed nothing and a visitor could not tell
// a typo from a project that is not there. The page now resolves the project
// and refuses what the caller may not see, so the test creates a real project.
func TestProjectPage(t *testing.T) {
	ts := newTestServer(t)
	createTestProject(t, ts, "page-project")

	code, body := getBody(t, ts.URL+"/projects/page-project")
	if code != http.StatusOK {
		t.Fatalf("expected 200 for a real project, got %d", code)
	}
	for _, want := range []string{"<!DOCTYPE html>", "project-detail", "/assets/js/project.js"} {
		if !strings.Contains(body, want) {
			t.Errorf("project page missing %q", want)
		}
	}
}

// TestProjectPageUnknownSlugIs404 pins the fix above: a slug that does not exist
// must not render a page. 404 rather than 403, for the reason requireProjectID
// gives: the code must not vary with whether the project exists.
func TestProjectPageUnknownSlugIs404(t *testing.T) {
	ts := newTestServer(t)
	code, _ := getBody(t, ts.URL+"/projects/nonexistent")
	if code != http.StatusNotFound {
		t.Errorf("unknown slug: expected 404, got %d", code)
	}
}

// TestBoardPage renders the board shell for a real project.
func TestBoardPage(t *testing.T) {
	ts := newTestServer(t)
	createTestProject(t, ts, "board-project")

	code, body := getBody(t, ts.URL+"/projects/board-project/board")
	if code != http.StatusOK {
		t.Fatalf("expected 200 for a real project, got %d", code)
	}
	if !strings.Contains(body, "kanban-board") {
		t.Error("board page missing kanban-board container")
	}
}

// TestSecurityHeadersOnPages verifies the hardening middleware covers web pages.
func TestSecurityHeadersOnPages(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Error("missing Content-Security-Policy on web page")
	}
	if resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Error("expected X-Frame-Options DENY on web page")
	}
}

// createTestProject makes a real project through the API, so page tests can
// exercise a slug that actually exists instead of asserting on a shell.
func createTestProject(t *testing.T, ts *httptest.Server, slug string) {
	t.Helper()
	body := map[string]any{
		"slug": slug, "name": slug, "description": "page test project",
	}
	resp, err := ts.Client().Post(ts.URL+"/api/v1/projects", "application/json",
		bytes.NewReader(mustJSON(t, body)))
	if err != nil {
		t.Fatalf("create project %s: %v", slug, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project %s: status %d", slug, resp.StatusCode)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
