package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTheTaxonomyPageRendersAndKeepsItsRouteRegistered.
//
// Part one of the three-part contract for a page (see the skill): it renders, and it
// renders as the template rather than as the error that names a missing one.
// Registering the route without adding the template to server.go's `pages` slice
// answers 500 with the template name in the body, which is deliberate -- a blank page
// would be a worse report -- so the absence of "taxonomy.html" in the body is the
// evidence that the slice entry exists.
func TestTheTaxonomyPageRendersAndKeepsItsRouteRegistered(t *testing.T) {
	ts := newTestServer(t)

	// Anonymous on purpose: §4.3's reads are public and the page is a shell. If this
	// ever needs a token, every assertion below still passes for a signed-in caller
	// and the page has silently become unreachable to everyone else.
	body := htmlBody(t, ts, "/taxonomy")
	if !strings.Contains(body, "<html") {
		t.Fatalf("GET /taxonomy did not render a page (%d bytes)", len(body))
	}
	if strings.Contains(body, "taxonomy.html") {
		t.Errorf("the body names the template, which is what render() does when the "+
		"template is NOT in the pages slice:\n%s", firstBytes([]byte(body), 400))
	}
	for _, want := range []string{"taxonomy-root", `aria-busy="true"`, "/assets/js/taxonomy.js"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not contain %q", want)
		}
	}
}

// TestSomethingLinksToTheTaxonomyPage asserts part two directly.
//
// TestEveryRegisteredPageHasAnInboundLink covers this too, one page at a time, and
// this is not redundant with it: that gate has to be edited to include a page, so it
// is the edit that protects the page. This one fails if the nav link disappears
// while the gate's list still mentions the path.
func TestSomethingLinksToTheTaxonomyPage(t *testing.T) {
	ts := newTestServer(t)
	body := htmlBody(t, ts, "/")
	if !strings.Contains(body, `href="/taxonomy"`) {
		t.Error("nothing on the home page links to /taxonomy: the page is reachable " +
			"only by typing the URL, which is how Finder, Scout, the consensus page " +
			"and the audit log were all invisible with every test green")
	}
}

// TestTheTaxonomyPageReadsTheEndpointsItExistsFor.
//
// Asserted on the JS SOURCE, which is the weaker half of the pair and worth stating
// as such: source inspection proves a string exists, never that the code runs -- a
// ReferenceError parses perfectly, and the token-key typo survived four files that
// way. The other half is tests/e2e/taxonomy_e2e.py, which loads the page in a browser
// and drives a real proposal and consent.
func TestTheTaxonomyPageReadsTheEndpointsItExistsFor(t *testing.T) {
	ts := newTestServer(t)
	src := assetBody(t, ts, "/assets/js/taxonomy.js")

	for _, want := range []string{
		// Both reads, and the suggested flag on the tag one: §4.3 requires
		// machine-inferred tags to be LABELLED, and the default read drops them, so a
		// page that used the default would show a taxonomy that disagrees with what
		// tag search finds.
		"/api/v1/taxonomy/tags?include_suggested=true",
		"/api/v1/taxonomy/proposals",
		// The consent write, and the 202 it will usually get.
		"/consent",
		"res.status === 202",
		// The two counts the page renders, from the response rather than from a
		// number of its own.
		"p.consent_count",
		"p.eligible_count",
		// The rule beside the control. A consent control with no statement of what
		// consenting does is a button that appears to do nothing.
		"The proposer cannot consent to their own",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("taxonomy.js does not contain %q", want)
		}
	}

	// The actions it offers must be the actions the store accepts, or the form is a
	// 400 waiting to happen. Sourced from store's switch, checked here by name.
	for _, action := range []string{"create", "rename", "alias", "merge", "reparent", "delete"} {
		if !strings.Contains(src, `id: "`+action+`"`) {
			t.Errorf("taxonomy.js does not offer the %q action, which the store accepts", action)
		}
	}

	// And it must not invent a project scope: this page is instance-wide, and a
	// project_id scraped from a URL would silently scope a global change to whichever
	// project the visitor arrived from.
	if strings.Contains(src, "project_id: slug") || strings.Contains(src, "project_id:slug") {
		t.Error("taxonomy.js scopes a proposal to a project it read from the URL; the " +
			"page is instance-wide")
	}
}

// TestTheProposalListCarriesTheCountsThePageRenders.
//
// The page shows "N of M eligible taggers have consented". Both numbers come from the
// list endpoint, which carried NEITHER until this page needed them -- the only place
// the count appeared was the 202 message after consenting, so "how many more does
// this need" could be learned only by acting.
//
// Asserted against a real response and in its exact spelling: the surrounding API is
// snake_case, and the point of this test is the WIRE, not the struct. The store-level
// agreement between these numbers and what RatifyTaxonomyProposal divides by lives in
// TestTheCountsThePageShowsAreTheCountsTheServerUses.
func TestTheProposalListCarriesTheCountsThePageRenders(t *testing.T) {
	ts := newTestServer(t)
	htmlBody(t, ts, "/taxonomy") // ensure the route and template are live

	// A proposal to look at. Anonymous proposing is refused, so this registers.
	_, reg := postJSON(t, ts, "/api/v1/auth/register",
		`{"username":"taxcounts","password":"correct horse battery"}`)
	tok, _ := reg["token"].(string)
	if tok == "" {
		t.Fatalf("register returned no token: %v", reg)
	}
	joinSomeProject(t, ts, tok)

	resp, body := postJSON(t, ts, "/api/v1/taxonomy/proposals",
		`{"action":"create","target_tag":"topic:counts-on-the-wire","rationale":"why"}`,
		"Authorization", "Bearer "+tok)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("propose: %d %v", resp.StatusCode, body)
	}

	// The raw body, not doJSON: it unmarshals into map[string]any, and a bare
	// ARRAY fails to unmarshal into one -- so a list endpoint read through it cannot
	// be asserted against its own shape. That is recorded in docs/REMAINING.md
	// §11.3 and it is why this test reads text and decodes it here.
	raw := getRawBody(t, ts, "/api/v1/taxonomy/proposals")
	var list []map[string]any
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("the list endpoint does not answer a JSON array: %v\n%s", err, firstBytes([]byte(raw), 300))
	}
	var found map[string]any
	for _, p := range list {
		if p["target_tag"] == "topic:counts-on-the-wire" {
			found = p
		}
	}
	if found == nil {
		t.Fatalf("the proposal just created is not in the list: %s", firstBytes([]byte(raw), 400))
	}
	for _, key := range []string{"consent_count", "eligible_count"} {
		v, present := found[key]
		if !present {
			t.Errorf("the proposal carries no %q; the page renders it and would show "+
				"undefined", key)
			continue
		}
		n, ok := v.(float64)
		if !ok {
			t.Errorf("%s is %T, not a number", key, v)
			continue
		}
		if n < 0 {
			t.Errorf("%s is negative (%v)", key, n)
		}
	}
	// The spelling, both ways: the keys must be snake_case AND the Go field names
	// must be absent. A whitelist test that only forbids is half a whitelist test.
	for _, banned := range []string{"ConsentCount", "EligibleCount"} {
		if strings.Contains(raw, banned) {
			t.Errorf("the response carries the Go field name %q; the page reads "+
				"snake_case", banned)
		}
	}
}

// getRawBody fetches a path and returns the body as text.
//
// Needed for LIST endpoints specifically: every shared JSON helper in this package
// decodes into map[string]any, and a bare array does not fit one.
func getRawBody(t *testing.T, ts *httptest.Server, path string) string {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, resp.StatusCode, firstBytes(b, 200))
	}
	return string(b)
}
