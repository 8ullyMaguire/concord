package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// TestIndexPage verifies the homepage renders with embedded templates.
func TestIndexPage(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := make([]byte, 10000)
	n, _ := resp.Body.Read(body)
	content := string(body[:n])
	if !strings.Contains(content, "<!DOCTYPE html>") {
		t.Error("homepage should contain DOCTYPE declaration")
	}
	if !strings.Contains(content, "<html") {
		t.Error("homepage should contain html tag")
	}
}

// TestProjectsPage verifies the projects listing page renders.
func TestProjectsPage(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/projects")
	if err != nil {
		t.Fatalf("GET /projects: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := make([]byte, 10000)
	n, _ := resp.Body.Read(body)
	content := string(body[:n])
	if !strings.Contains(content, "<!DOCTYPE html>") {
		t.Error("projects page should contain DOCTYPE")
	}
}

// TestSearchPage verifies the search page renders.
func TestSearchPage(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/search")
	if err != nil {
		t.Fatalf("GET /search: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := make([]byte, 10000)
	n, _ := resp.Body.Read(body)
	content := string(body[:n])
	if !strings.Contains(content, "<!DOCTYPE html>") {
		t.Error("search page should contain DOCTYPE")
	}
}

// TestProjectPage renders for any slug (SPA-style, data loaded client-side).
func TestProjectPage(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/projects/nonexistent")
	if err != nil {
		t.Fatalf("GET /projects/nonexistent: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (SPA renders client-side), got %d", resp.StatusCode)
	}
	body := make([]byte, 10000)
	n, _ := resp.Body.Read(body)
	content := string(body[:n])
	if !strings.Contains(content, "<!DOCTYPE html>") {
		t.Error("project page should contain DOCTYPE")
	}
}

// TestBoardPage renders for any slug.
func TestBoardPage(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/projects/nonexistent/board")
	if err != nil {
		t.Fatalf("GET /projects/nonexistent/board: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (SPA renders client-side), got %d", resp.StatusCode)
	}
}
