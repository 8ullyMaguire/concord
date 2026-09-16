package httpapi

import (
	"io"
	"net/http"
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

// TestProjectPage renders the detail shell for any slug (data hydrates client-side).
func TestProjectPage(t *testing.T) {
	ts := newTestServer(t)
	code, body := getBody(t, ts.URL+"/projects/nonexistent")
	if code != http.StatusOK {
		t.Fatalf("expected 200 (shell renders client-side), got %d", code)
	}
	for _, want := range []string{"<!DOCTYPE html>", "project-detail", "/assets/js/project.js"} {
		if !strings.Contains(body, want) {
			t.Errorf("project page missing %q", want)
		}
	}
}

// TestBoardPage renders the board shell for any slug.
func TestBoardPage(t *testing.T) {
	ts := newTestServer(t)
	code, body := getBody(t, ts.URL+"/projects/nonexistent/board")
	if code != http.StatusOK {
		t.Fatalf("expected 200 (shell renders client-side), got %d", code)
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
