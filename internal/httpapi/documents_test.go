package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Project documents at the HTTP layer (spec extension, 2026-09-30).
//
// The store tests prove the rules hold. These prove the wiring does: that the
// routes are reachable, that the roles are enforced, and that a body larger
// than the default limit is accepted for a document but that nothing else is
// loosened to achieve it.

// documentProject creates a project owned by the harness's default user and
// returns its slug. The default user is its maintainer, so it can write
// documents; other accounts are set up explicitly where that matters.
func documentProject(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	resp, body := postJSON(t, ts, "/api/v1/projects",
		`{"slug":"tessera","name":"Tessera","description":"d","license":"MIT"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: status %d body=%v", resp.StatusCode, body)
	}
	return "tessera"
}

func TestDocuments_put_get_and_list(t *testing.T) {
	ts := newTestServer(t)
	slug := documentProject(t, ts)

	resp, doc := doJSON(t, ts, "PUT", "/api/v1/projects/"+slug+"/documents", map[string]any{
		"kind": "readme", "slug": "readme", "title": "Tessera", "body": "# Tessera",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put readme: status %d body=%v", resp.StatusCode, doc)
	}
	if doc["kind"] != "readme" {
		t.Errorf("kind = %v, want readme", doc["kind"])
	}
	docID := int(doc["id"].(float64))

	// A second, different kind: a project has both a README and a spec.
	resp, _ = doJSON(t, ts, "PUT", "/api/v1/projects/"+slug+"/documents", map[string]any{
		"kind": "spec", "slug": "architecture", "title": "Architecture", "body": "identity",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put spec: status %d", resp.StatusCode)
	}

	resp, list := doJSON(t, ts, "GET", "/api/v1/projects/"+slug+"/documents", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: status %d body=%v", resp.StatusCode, list)
	}
	items, _ := list["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("listed %d documents, want 2: %v", len(items), list)
	}

	// The kind filter must not leak the other kind.
	resp, filtered := doJSON(t, ts, "GET", "/api/v1/projects/"+slug+"/documents?kind=spec", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list spec: status %d", resp.StatusCode)
	}
	items, _ = filtered["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("kind=spec returned %d documents, want 1", len(items))
	}

	resp, got := doJSON(t, ts, "GET",
		fmt.Sprintf("/api/v1/projects/%s/documents/%d", slug, docID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get by id: status %d body=%v", resp.StatusCode, got)
	}
	if got["title"] != "Tessera" {
		t.Errorf("title = %v, want Tessera", got["title"])
	}
}

func TestDocuments_put_increments_revision(t *testing.T) {
	ts := newTestServer(t)
	slug := documentProject(t, ts)

	_, first := doJSON(t, ts, "PUT", "/api/v1/projects/"+slug+"/documents", map[string]any{
		"kind": "readme", "slug": "readme", "body": "one",
	})
	_, second := doJSON(t, ts, "PUT", "/api/v1/projects/"+slug+"/documents", map[string]any{
		"kind": "readme", "slug": "readme", "body": "two",
	})
	if first["id"] != second["id"] {
		t.Errorf("identity changed across an update: %v then %v", first["id"], second["id"])
	}
	if second["revision"] != float64(2) {
		t.Errorf("revision = %v, want 2", second["revision"])
	}
}

func TestDocuments_rejects_unknown_kind(t *testing.T) {
	ts := newTestServer(t)
	slug := documentProject(t, ts)

	resp, body := doJSON(t, ts, "PUT", "/api/v1/projects/"+slug+"/documents", map[string]any{
		"kind": "blog-post", "slug": "x", "body": "b",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown kind: status %d, want 400 (body=%v)", resp.StatusCode, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "kind must be one of") {
		t.Errorf("error should name the accepted kinds, got %q", msg)
	}
}

func TestDocuments_search_requires_a_query(t *testing.T) {
	ts := newTestServer(t)
	slug := documentProject(t, ts)
	doJSON(t, ts, "PUT", "/api/v1/projects/"+slug+"/documents", map[string]any{
		"kind": "spec", "slug": "a", "body": "a distinctive phrase",
	})

	// q= is required: returning the whole corpus when the box is empty is a
	// way to publish a project's documents by accident.
	resp, body := doJSON(t, ts, "GET", "/api/v1/projects/"+slug+"/documents/search", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("search without q: status %d, want 400 (body=%v)", resp.StatusCode, body)
	}

	resp, hits := doJSON(t, ts, "GET",
		"/api/v1/projects/"+slug+"/documents/search?q=distinctive", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search: status %d body=%v", resp.StatusCode, hits)
	}
	items, _ := hits["items"].([]any)
	if len(items) != 1 {
		t.Errorf("search returned %d hits, want 1: %v", len(items), hits)
	}

	// A non-matching query returns nothing, not everything.
	resp, none := doJSON(t, ts, "GET",
		"/api/v1/projects/"+slug+"/documents/search?q=absentterm", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search absent: status %d", resp.StatusCode)
	}
	items, _ = none["items"].([]any)
	if len(items) != 0 {
		t.Errorf("non-matching search returned %d hits, want 0", len(items))
	}
}

func TestDocuments_search_rejects_a_malformed_fts_query(t *testing.T) {
	ts := newTestServer(t)
	slug := documentProject(t, ts)

	// An unbalanced quote is an FTS5 syntax error. It is the caller's mistake
	// and must be a 400, not a 500.
	resp, body := doJSON(t, ts, "GET",
		"/api/v1/projects/"+slug+`/documents/search?q="unbalanced`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed FTS query: status %d, want 400 (body=%v)", resp.StatusCode, body)
	}
}

func TestDocuments_accepts_a_body_larger_than_the_default_limit(t *testing.T) {
	ts := newTestServer(t)
	slug := documentProject(t, ts)

	// 2 MB: past readJSON's 1 MB default, because a real specification is
	// larger than that and the whole point of the document store is to hold it.
	big := strings.Repeat("The identity of a tag is a concept, never a label.\n", 30000)
	if len(big) < 1<<20 {
		t.Fatalf("test body is only %d bytes, needs to exceed 1 MB", len(big))
	}

	resp, body := doJSON(t, ts, "PUT", "/api/v1/projects/"+slug+"/documents", map[string]any{
		"kind": "spec", "slug": "v03", "body": big,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("2 MB document: status %d, want 200 (body=%v)", resp.StatusCode, body)
	}
	if got, _ := body["body"].(string); len(got) != len(big) {
		t.Errorf("stored %d bytes, sent %d", len(got), len(big))
	}
}

func TestDocuments_writing_requires_a_role(t *testing.T) {
	ts := newTestServer(t)
	slug := documentProject(t, ts)

	// A second, unrelated account: authenticated, but with no role on this
	// project. A document states what a project is, so this must be refused.
	//
	// The header must be set explicitly. The harness supplies the default
	// identity to any request arriving without one, so a test that just omits
	// Authorization is testing the harness, not the handler.
	tok2 := registerOn(t, ts, "stranger")
	req, _ := http.NewRequest("PUT",
		ts.URL+"/api/v1/projects/"+slug+"/documents",
		strings.NewReader(`{"kind":"readme","slug":"readme","body":"b"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok2)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("put as stranger: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("an account with no role on the project was allowed to write a document")
	}
}

func TestDocuments_reading_is_open_to_any_signed_in_account(t *testing.T) {
	ts := newTestServer(t)
	slug := documentProject(t, ts)

	doJSON(t, ts, "PUT", "/api/v1/projects/"+slug+"/documents", map[string]any{
		"kind": "readme", "slug": "readme", "body": "public-facing prose",
	})

	// Reading is not gated: a specification nobody can read is not
	// documentation.
	tok2 := registerOn(t, ts, "reader")
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/projects/"+slug+"/documents", nil)
	req.Header.Set("Authorization", "Bearer "+tok2)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("get as reader: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("reading documents as a non-member: status %d, want 200", resp.StatusCode)
	}
}

func TestDocuments_delete_then_get_is_not_found(t *testing.T) {
	ts := newTestServer(t)
	slug := documentProject(t, ts)

	_, doc := doJSON(t, ts, "PUT", "/api/v1/projects/"+slug+"/documents", map[string]any{
		"kind": "adr", "slug": "0001", "body": "b",
	})
	docID := int(doc["id"].(float64))

	resp, body := doJSON(t, ts, "DELETE",
		fmt.Sprintf("/api/v1/projects/%s/documents/%d", slug, docID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: status %d body=%v", resp.StatusCode, body)
	}

	resp, _ = doJSON(t, ts, "GET",
		fmt.Sprintf("/api/v1/projects/%s/documents/%d", slug, docID), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("get after delete: status %d, want 404", resp.StatusCode)
	}
}

func TestDocuments_kinds_vocabulary_is_served(t *testing.T) {
	ts := newTestServer(t)
	slug := documentProject(t, ts)

	resp, body := getJSON(t, ts, "/api/v1/projects/"+slug+"/documents/kinds")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("kinds: status %d body=%v", resp.StatusCode, body)
	}
	kinds, _ := body["kinds"].([]any)
	want := []string{"readme", "spec", "plan", "wiki", "adr", "changelog"}
	if len(kinds) != len(want) {
		t.Fatalf("got %d kinds, want %d: %v", len(kinds), len(want), kinds)
	}
	for i, k := range want {
		if kinds[i] != k {
			t.Errorf("kind %d = %v, want %q", i, kinds[i], k)
		}
	}
}
