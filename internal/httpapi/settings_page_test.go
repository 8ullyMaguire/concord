package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The three-part contract for the settings page.
//
// Every other page in this repo needed three iterations to "complete" against this
// contract, and each iteration was one of the three:
//
//	1. it RENDERS  -- the route is registered AND the template name is in the pages slice
//	2. something LINKS to it -- from the project page body AND the shared tab bar
//	3. it HIDES a private project, with the same message as a nonexistent one
//
// Point 2 has two halves on purpose. The tab bar lives in base.html and the page button
// in project.js, so a gate checking only one stays green after every link from the other
// is deleted.

// seedSettingsProject creates a public project owned by a fresh harness user.
func seedSettingsProject(t *testing.T, ts *httptest.Server, slug string) {
	t.Helper()
	owner := registerAndLogin(t, ts, slug+"-owner")
	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": slug, "name": "Settings Project", "description": "d"})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create project %s: %d %s", slug, code, body)
	}
}

// statusOf is the raw status for a GET, since htmlBody returns only the body and the
// 404 assertion needs the code.
func statusOf(t *testing.T, ts *httptest.Server, path string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

func TestTheSettingsPageRendersAndCarriesTheTabBar(t *testing.T) {
	ts := newTestServer(t)
	seedSettingsProject(t, ts, "setpage")

	body := htmlBody(t, ts, "/projects/setpage/settings")
	// A route registered without the template in the `pages` slice renders 500 with the
	// template name in the body, so the 200 is only half the claim.
	if strings.Contains(body, "no such template") {
		t.Fatal("the route is registered but the template is not in the pages slice")
	}
	for _, want := range []string{`id="settings-root"`, "assets/js/settings.js"} {
		if !strings.Contains(body, want) {
			t.Errorf("the rendered page does not contain %q", want)
		}
	}
	if !strings.Contains(body, `href="/projects/setpage/settings"`) {
		t.Error("the settings tab is not in the tab bar")
	}
}

// Asserted on the JS source, not the rendered HTML: the button is built client-side, so
// the server HTML does not contain the path and an HTML assertion would be satisfied by
// the script's own source -- which is how a deleted link keeps passing.
func TestTheSettingsPageIsLinkedFromTheProjectPage(t *testing.T) {
	ts := newTestServer(t)

	js := assetBody(t, ts, "/assets/js/project.js")
	// Built from the slug AND concatenated, so a hardcoded path cannot satisfy it.
	if !strings.Contains(js, `esc(p.slug) + '/settings">`) {
		t.Error("project.js does not link to the settings page")
	}

	// The tab bar is a TEMPLATE, not a served asset, so it is read from source. assetCorpus
	// concatenates templates and JS, which is where every href in the app is written.
	if !strings.Contains(assetCorpus(t), `href="/projects/{{.Slug}}/settings"`) {
		t.Error("the shared tab bar does not link to the settings page")
	}
}

// The shell renders for logged-out visitors, so a 200 confirms the slug exists and puts
// it in the title bar -- enough to enumerate a private roadmap. The 404 must not echo it.
func TestTheSettingsPageHidesAPrivateProject(t *testing.T) {
	ts := newTestServer(t)
	owner := registerAndLogin(t, ts, "setpriv-owner")
	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "setpriv", "name": "Private", "description": "d"})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create private: %d %s", code, body)
	}
	// PUT, not POST: the route is registered as r.Put. Guessing POST here produced a 405
	// and a test failure that read like a visibility bug.
	if code, body = authJSON(t, ts, http.MethodPut, "/api/v1/projects/setpriv/visibility", owner,
		map[string]any{"visibility": "private"}); code != http.StatusOK {
		t.Fatalf("make private: %d %s", code, body)
	}

	if got := statusOf(t, ts, "/projects/setpriv/settings"); got != http.StatusNotFound {
		t.Errorf("a private project's settings page answered %d, want 404", got)
	}
	if strings.Contains(htmlBody(t, ts, "/projects/setpriv/settings"), "setpriv") {
		t.Error("the 404 echoes the slug, which is enough to confirm the project exists")
	}
}

// THE MEASURED WIRE SHAPE, pinned.
//
// governance.Charter carries NO json tags, so encoding/json marshals the Go FIELD NAMES
// and the endpoint answers PascalCase while every other endpoint in the API is
// snake_case. A page reading `quorum_ratio` gets undefined for all 22 fields and renders
// an empty charter -- so the assertion is on the keys the response actually has.
func TestTheCharterIsServedInPascalCaseNotSnakeCase(t *testing.T) {
	ts := newTestServer(t)
	seedSettingsProject(t, ts, "setcharter")

	code, raw := authJSON(t, ts, http.MethodGet, "/api/v1/projects/setcharter/charter", "", nil)
	if code != http.StatusOK {
		t.Fatalf("charter: status %d, body %s", code, raw)
	}
	got := string(raw)
	for _, want := range []string{`"QuorumRatio"`, `"ConsentRatio"`, `"WIPInProgress"`} {
		if !strings.Contains(got, want) {
			t.Errorf("charter response does not contain %s; a page built from the "+
				"snake_case house style renders every field empty", want)
		}
	}
	for _, wrong := range []string{`"quorum_ratio"`, `"consent_ratio"`} {
		if strings.Contains(got, wrong) {
			t.Errorf("charter response contains %s -- the keys changed and every caller "+
				"reading the documented names now reads undefined", wrong)
		}
	}
}

// THE READ-SIDE CHECK, made permanent.
//
// Three endpoints had handlers, auth guards and store tests for their whole life and no
// reader in assets/js. A writer with no reader is not an unfinished feature, it is a
// silent hole. Asserted by NAME, so deleting one fetch turns it red -- a count assertion
// would pass whenever the count matched, whatever the endpoints were.
func TestEveryEndpointThisPageReadsIsActuallyCalledFromIt(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/settings.js")
	// The CALL, not the substring. Asserting on `"/criteria-profiles"` alone passed while
	// the fetch was deleted, because this file's own header comment -- the one explaining
	// the defect the page exists to close -- contains the same text twice.
	//
	// A gate satisfied by its own documentation is worse than no gate, because it is
	// confident. The trailing `")` is what only the call site has.
	for _, want := range []string{`+ "/criteria-profiles")`, `+ "/members")`, `+ "/charter")`,
		`+ "/tags/completeness")`, `+ "/strategy/weight-proposals")`, `+ "/strategy/themes")`} {
		if !strings.Contains(js, want) {
			t.Errorf("settings.js never fetches %s -- the endpoint has a handler and no "+
				"reader, which is the defect this page exists to close", want)
		}
	}
}

// The write path sends the token the server reads. `concord.token`, not
// `concord_token`: getItem returns null for a missing key rather than throwing, so the
// wrong key produces a header of "Bearer null" and a silent 401 -- the page renders, the
// button is there, and nothing happens.
func TestTheSettingsWriteSendsTheTokenTheServerReads(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/settings.js")
	if !strings.Contains(js, `localStorage.getItem("concord.token")`) {
		t.Error("settings.js does not read concord.token")
	}
	if strings.Contains(js, "concord_token") {
		t.Error("settings.js reads concord_token, which does not exist; every save 401s " +
			"silently")
	}
}

// The two panels added after the read-side gate above was already green.
//
// Both close the same hole one level down: /tags/completeness is the §4.3 completeness
// meter and /strategy/* is §6.2's consensus path, and neither had a reader. The gates
// here are about the SHAPE the page has to read, because both endpoints disagree with
// the house style in ways that render as blank rather than as an error.
func TestTheCompletenessMeterReadsTheKeysTheStoreReturns(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/settings.js")
	// The keys come from store.TagCompleteness's map, transcribed from the literal
	// rather than guessed: want_tags and want_namespaced are the threshold, and a page
	// that hardcoded 5 and 2 would keep claiming them after the store changed.
	for _, key := range []string{"want_tags", "want_namespaced", "suggested_pending", "namespaced"} {
		if !strings.Contains(js, key) {
			t.Errorf("settings.js does not read %q; the meter would show a percentage "+
				"with none of the numbers behind it", key)
		}
	}
	// The store spells them snake_case. A page reading c.wantTags renders "0 of
	// undefined" -- asserted on the READ (c.wantTags), not on the bare word, because
	// this file's comment above names the Go-side identifier and a substring check
	// would flag its own documentation. That is the trap the read-side gate below
	// already documents.
	if strings.Contains(js, "c.wantTags") || strings.Contains(js, "c.wantNamespaced") {
		t.Error("settings.js reads wantTags/wantNamespaced, which the endpoint does not send")
	}
}

// The strategy panel is the page's only writer besides the charter, and the two
// outcomes §6.2 distinguishes -- consent recorded but below quorum (202) and ratified
// (200) -- must not be collapsed. A page that said "ratified" for a 202 would report a
// weight change that has not happened, which is the hand-steering the mechanism exists
// to prevent.
func TestTheStrategyPanelDistinguishesBelowQuorumFromRatified(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/settings.js")
	if !strings.Contains(js, "r.status === 202") {
		t.Error("settings.js does not distinguish a 202 (consent recorded, below quorum) " +
			"from a 200 (ratified); the page would report a change that has not happened")
	}
	// The consent button posts to the consent route, built from the data attribute the
	// row carries. A row with a button that posts to the list route would 405.
	if !strings.Contains(js, `"/consent"`) {
		t.Error("settings.js never posts to the consent route")
	}
	// And the panel says the proposer cannot consent to their own proposal, because the
	// server refuses it with a 403 and the reader needs to know that is a rule.
	if !strings.Contains(js, "cannot consent to their own") {
		t.Error("settings.js does not state the self-consent rule it will be refused by")
	}
}
