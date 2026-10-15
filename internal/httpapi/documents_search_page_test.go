package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestTheDocumentsPageHasASearchBoxWiredToTheSearchEndpoint.
//
// `GET /projects/{slug}/documents/search` is a real FTS5 index -- bm25-ranked, with
// the hyphen fix -- and it had NO CALLER. documents.js fetched the whole list and
// filtered it in the browser, and the template had no input at all, so 1,087
// documents on the live instance were searchable with curl and not from the site.
//
// Source inspection, which is the weaker half and is worth saying so: it proves the
// strings exist, never that the code runs. The browser half is
// tests/e2e/documents_e2e.py.
func TestTheDocumentsPageHasASearchBoxWiredToTheSearchEndpoint(t *testing.T) {
	ts := newTestServer(t)
	slug := documentProject(t, ts)

	page := htmlBody(t, ts, "/projects/"+slug+"/documents")
	// The box is in the TEMPLATE and above the mount, because the mount's innerHTML
	// is replaced wholesale on every render -- a control inside it is destroyed the
	// moment the page loads, and the page would then have a search box that vanishes.
	for _, want := range []string{
		`id="doc-search"`,
		`id="doc-search-input"`,
		`name="q"`,
		`for="doc-search-input"`,
		`data-search-status`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("documents.html does not contain %q", want)
		}
	}
	// A real form with a method, so it works before the script does.
	if !strings.Contains(page, `method="get"`) {
		t.Error("the search form has no method: without one a browser defaults to GET " +
			"anyway, but the intent is unstated and a later edit could change it")
	}
	if strings.Contains(page, `id="doc-search"`) &&
		strings.Index(page, `id="project-documents"`) < strings.Index(page, `id="doc-search"`) {
		t.Error("the search box is INSIDE the documents mount, which is replaced on " +
			"every render")
	}

	js := assetBody(t, ts, "/assets/js/documents.js")
	for _, want := range []string{
		"/documents/search?q=",
		"encodeURIComponent(q)",
		// The malformed-query path: a 400 is the caller's mistake and the page has to
		// say so, because an empty list is a claim about the corpus.
		"res.status === 400",
		// The stale-response guard: a slow answer to "consensus" must not land after
		// "consensus vote".
		"if (mine !== seq) return;",
		// The deep link.
		"get('q')",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("documents.js does not contain %q", want)
		}
	}
}

// TestTheDocumentsSearchAnswersABareArray.
//
// The page parses the search response, so the SHAPE is part of the contract, and the
// shape is not what it looks like from inside this package: doJSON wraps a bare array
// under "items" (so `hits["items"]` reads correctly in documents_test.go and is a
// helper artifact, not the wire), while the plan for this page guessed
// `{results: [...]}`. Both are wrong, in opposite directions, and neither is testable
// except against the raw body.
//
// This is the gate that would have caught the page parsing a wrapper that does not
// exist -- the §3.1 defect one level down: a field nobody sends, read by a page that
// looks fine.
func TestTheDocumentsSearchAnswersABareArray(t *testing.T) {
	ts := newTestServer(t)
	slug := documentProject(t, ts)

	// A distinctive word, so a hit means the index was searched rather than the
	// corpus returned.
	if resp, body := doJSON(t, ts, "PUT", "/api/v1/projects/"+slug+"/documents", map[string]any{
		"kind": "spec", "slug": "wire-shape", "title": "Wire shape",
		"body": "the quorumrule word appears here and nowhere else",
	}); resp.StatusCode != 200 {
		t.Fatalf("put document: %d %v", resp.StatusCode, body)
	}

	raw := getRawBody(t, ts, "/api/v1/projects/"+slug+"/documents/search?q=quorumrule")
	var hits []map[string]any
	if err := json.Unmarshal([]byte(raw), &hits); err != nil {
		t.Fatalf("the search endpoint does not answer a bare JSON array, and documents.js "+
			"parses it as one (%v):\n%s", err, firstBytes([]byte(raw), 400))
	}
	if len(hits) != 1 {
		t.Fatalf("search for a unique word returned %d hits: %s", len(hits), raw)
	}
	// The keys the page reads. A hit without `kind` cannot link to its own kind's
	// page, and a hit without `id` cannot be selected.
	for _, key := range []string{"id", "kind", "slug", "title"} {
		if _, ok := hits[0][key]; !ok {
			t.Errorf("a search hit carries no %q; documents.js reads it", key)
		}
	}
	// And no wrapper: the two shapes that were guessed instead.
	for _, absent := range []string{`"results"`, `"items"`} {
		if strings.Contains(raw, absent) {
			t.Errorf("the response carries %s, so it is not the bare array the page parses", absent)
		}
	}
}
