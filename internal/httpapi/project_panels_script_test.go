package httpapi

// §4.10's panels as the BROWSER will see them (phase3-spec §4.3).
//
// The API tests in project_surfaces_test.go prove the endpoints answer. These
// prove the page asks them and shows what they returned, which is a different
// question and the one that has broken before: `finder.html` shipped unregistered
// and every page test passed against markup that was never rendered, and
// `finder.js` called show() without render() and the page looked loaded with an
// empty category picker.
//
// Asserted on project.js (the script that hydrates the page), not on
// project.html, because project.html is a nine-line mount point. The existing
// TestProjectPageHasTheForms states that reasoning: a test against the
// server-rendered HTML of a client-rendered page can only ever produce a false
// alarm.

import (
	"strings"
	"testing"
)

// The template must still load the script at all. If someone drops the tag,
// every assertion below passes vacuously on an empty page.
func TestTheProjectPageLoadsTheScriptThatDrawsThePanels(t *testing.T) {
	ts := newTestServer(t)
	slug, _ := makeProject(t, ts, "panels-template")
	body := htmlBody(t, ts, "/projects/"+slug)
	if !strings.Contains(body, "/assets/js/project.js") {
		t.Fatal("the project page does not load project.js, so it renders nothing at all")
	}
}

// The page must actually CALL both endpoints. A panel that is rendered but never
// fetched draws its empty state forever, and an empty state is exactly what a
// project with no data looks like.
func TestTheScriptFetchesBothPanels(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/project.js")
	for _, want := range []string{"/capabilities", "/field-reports"} {
		if !strings.Contains(js, want) {
			t.Errorf("project.js never fetches %s, so its panel always renders empty", want)
		}
	}
	// And they must be part of the Promise.all, not fire-and-forget promises the
	// render can race: a fetch started after render() is a panel that arrives
	// after the page is done.
	if !strings.Contains(js, "capabilities: res[3]") ||
		!strings.Contains(js, "fieldReports: res[4]") {
		t.Error("the two panel fetches are not threaded into render(); they will " +
			"arrive after the page has already painted")
	}
}

// The three states of a capability claim must be visibly different. This is the
// script half of the API half: a handler can return `state: "disputed"` and the
// page can still render it identically to a confirmed claim, and no API test
// sees that.
func TestTheScriptDistinguishesAssertedConfirmedAndDisputed(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/project.js")

	// The three states, by the branch that produces each. Asserted as the
	// branch conditions rather than as the strings "confirmed"/"disputed"/
	// "asserted" appearing in the file: the badge LABEL is free to change wording,
	// but the three must stay three. A first draft asserted the quoted literals
	// and failed on two counts -- the confirmed/disputed branches exist and were
	// fine, and the third branch's label had been written as a bare "asserted".
	// Pinning the label would have made this a change-detector.
	for _, want := range []string{"c.state === 'disputed'", "c.state === 'confirmed'"} {
		if !strings.Contains(js, want) {
			t.Errorf("project.js does not branch on %s; three states would render "+
				"identically", want)
		}
	}
	// Three distinct badges, not two: a claim with one vote is a different state
	// from one nobody has voted on, even though both are unconfirmed. If the third
	// state were ever dropped, an unconfirmed claim would read as settled.
	for _, b := range []string{"badge-amber", "badge-green", "badge-slate\">asserted"} {
		if !strings.Contains(js, b) {
			t.Errorf("the third claim state has no marker of its own (%s); it will "+
				"render as if it were settled", b)
		}
	}

	// An unasserted capability must read as "no claim", NOT as its value. If this
	// rendered `unknown` for a missing row, the distinction the capability matrix
	// exists for would be visible only in the JSON.
	if !strings.Contains(js, "!c.asserted") {
		t.Error("project.js does not branch on the `asserted` flag at all")
	}
	if !strings.Contains(js, "cap-unknown") {
		t.Error("the unasserted row has no class of its own, so it renders like a value")
	}
	// And it must NOT be worded as field-report vocabulary. "not reported" on a
	// capability row reads as "someone reported that it is unknown" -- the other
	// state -- while field reports sit one screen below using that word properly.
	//
	// Scoped to RETURNED STRINGS, not to the function's source. The phrase is
	// spelled out in the comment beside the branch that avoids it, and a test
	// that greps the source has to be deleted by the act of documenting the
	// decision. So: strip comment lines first, then look. A comment may mention
	// the phrase; nothing the browser renders may contain it.
	capFn := js[strings.Index(js, "function capabilityValue"):strings.Index(js, "function capabilitiesPanel")]
	var rendered []string
	for _, line := range strings.Split(capFn, "\n") {
		tl := strings.TrimSpace(line)
		if strings.HasPrefix(tl, "//") {
			continue
		}
		rendered = append(rendered, tl)
	}
	code := strings.Join(rendered, "\n")
	if strings.Contains(code, "not reported") {
		t.Error("the capability panel RENDERS \"not reported\"; on this row that reads " +
			"as a field report of ignorance, not as the absence of a claim")
	}
	if !strings.Contains(code, "no claim yet") {
		t.Error("the unasserted row does not say what is true of it")
	}
}

// A disputed claim must not be rendered as settled. Asserted as: the disputed
// branch exists AND is not the same markup as the confirmed branch, because a
// refactor that made both render `c.value` with no marker would still satisfy a
// presence check on the word "disputed".
func TestTheScriptShowsADisputeRatherThanPresentingTheClaimAsSettled(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/project.js")

	iDisputed := strings.Index(js, "c.state === 'disputed'")
	iConfirmed := strings.Index(js, "c.state === 'confirmed'")
	if iDisputed < 0 || iConfirmed < 0 {
		t.Fatal("project.js is missing one of the two claim states")
	}
	disputedBranch := js[iDisputed:iConfirmed]
	if !strings.Contains(disputedBranch, "badge-amber") {
		t.Error("the disputed branch renders no distinct badge; a contested claim " +
			"and a confirmed one must not look the same")
	}
	// The original value is still shown beside the dispute, because the store
	// keeps it and rewriting it to "no" would be a second, wrong claim.
	if !strings.Contains(disputedBranch, "contested it") {
		t.Error("the disputed badge does not say the claim is contested")
	}
}

// The rate and its denominator are one unit. A regression that drops
// `sample_size` from the string would leave the panel saying a bare percentage,
// which is the misleading form §4.7's wording exists to prevent.
func TestTheScriptPrintsTheRateWithItsSampleSize(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/project.js")
	if !strings.Contains(js, "data.sample_size") {
		t.Error("project.js never reads sample_size, so it will print a bare " +
			"percentage with no denominator")
	}
	if !strings.Contains(js, "data.outcome_rate != null") {
		t.Error("project.js does not guard the rate on null, so a project with no " +
			"reports will print 0% instead of nothing")
	}
	if strings.Contains(js, "outcome_rate != null) {") &&
		strings.Contains(js, "0 + '%' of") {
		t.Error("the rate reads as if 0% were a valid value")
	}
}

// A failing panel fetch must not look like a project with no data. Both would be
// an empty state, and an endpoint that 500s would pass for a real answer.
func TestTheScriptDistinguishesAFailedPanelFromAnEmptyOne(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/project.js")

	if !strings.Contains(js, "panelError") {
		t.Fatal("project.js has no failure rendering for the panels")
	}
	if !strings.Contains(js, "panel-error") {
		t.Error("the failure state carries no distinct class, so it renders the same " +
			"as a genuine empty state")
	}
	// Both panel builders must call it on a missing payload.
	if strings.Count(js, "if (!data) {") < 2 {
		t.Error("only one panel guards a missing payload; the other would render a " +
			"TypeError or an empty state for a failed fetch")
	}
}

// Authored text goes through esc(). The project page renders user-written
// capability evidence, report caveats and advice; a panel that interpolates one
// without escaping is an XSS hole, and there is an existing test in this
// codebase for exactly that mistake on another page.
func TestTheScriptEscapesAuthoredTextInThePanels(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/project.js")

	// Every interpolation inside the two panel builders must be wrapped. Read the
	// panel section and look for a raw `+ <identifier>.` in a string.
	start := strings.Index(js, "function capabilitiesPanel")
	end := strings.Index(js, "function healthBar")
	if start < 0 || end < 0 || end < start {
		t.Fatal("cannot locate the panel section in project.js")
	}
	section := js[start:end]

	// Fields that are authored, free text: evidence, caveats, advice, body.
	for _, field := range []string{"c.evidence", "r.caveats", "r.advice", "resp.body"} {
		if !strings.Contains(section, "esc("+field+")") {
			t.Errorf("the panel renders %s without esc(); it is authored text", field)
		}
	}
	if strings.Contains(section, "'<p class=\"card-text\">' + r.caveats") {
		t.Error("caveats are concatenated raw into markup")
	}
}

// The panel must be bounded, and must SAY what it left out.
func TestTheScriptAnnouncesOmittedFieldReports(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/project.js")
	if !strings.Contains(js, "data.omitted") {
		t.Error("project.js never reads the omitted count, so a bounded list reads " +
			"as if it were the whole truth")
	}
	if !strings.Contains(js, "not shown") {
		t.Error("the omitted count is not stated to the reader")
	}
}

// The CSS the panels rely on must exist. A missing .cap-unknown means
// "not reported" renders in the same style as a value, and the distinction the
// panel exists to show becomes invisible.
func TestThePanelStylesAreDefined(t *testing.T) {
	ts := newTestServer(t)
	css := assetBody(t, ts, "/assets/css/style.css")
	for _, want := range []string{".cap-table", ".cap-unknown", ".cap-value", ".fr-summary", ".panel-error"} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css does not define %s", want)
		}
	}
}

// Every state a panel can be in carries its root element.
//
// This exists because marking both the section-head and its wrapper made
// `.panel-capabilities` match two elements, and Playwright's strict mode then
// failed all eleven browser tests with an ambiguity error whose message named
// the panels as if they were missing.
//
// Asserted per STATE, by requiring the root to be on the `return` line that
// produces that state. Three earlier versions of this test were all wrong:
// counting class occurrences ("3 vs 1" is just what a three-outcome function
// looks like), counting `return` statements ("13 paths" swept up the returns
// inside each `.map()` callback), and searching for a class inside the state
// ("fr-card" lives in a callback, forty lines from its own return, so the
// window never found the root). Keying on the return line is the one that
// matches the rule: every exit path must open the root it promises.
func TestEachPanelStateCarriesItsRootElement(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/project.js")

	// One entry per exit path that must carry a root, keyed on a fragment of the
	// `return` line itself. The populated capability path is
	// `return '<div class="panel-capabilities">' + head +` and its table is on
	// the next line, so the marker is the head expression rather than the table
	// tag -- the table string lives in a different line entirely.
	states := []struct{ marker, cls string }{
		{"No capability matrix yet", "panel-capabilities"},
		{`'<div class="panel-capabilities">' + head +`, "panel-capabilities"},
		{"No field reports yet", "panel-field-reports"},
		{"summary + env + omitted", "panel-field-reports"},
	}

	for _, st := range states {
		// Find the return line carrying this state's marker.
		var root bool
		seen := false
		for _, line := range strings.Split(js, "\n") {
			if !strings.Contains(line, "return ") || !strings.Contains(line, st.marker) {
				continue
			}
			seen = true
			root = strings.Contains(line, st.cls)
			break
		}
		switch {
		case !seen:
			t.Errorf("no return in project.js produces the %q state; this test "+
				"cannot check what replaced it", st.marker)
		case !root:
			t.Errorf("the %q state returns markup without %s; a browser locator "+
				"for .%s will not match it", st.marker, st.cls, st.cls)
		}
	}

	// The failed state gets its root from panelError, so panelError must name
	// which panel failed -- otherwise a failure in both is indistinguishable in
	// the DOM and a test can only assert "something broke".
	errFn := js[strings.Index(js, "function panelError"):]
	for _, cls := range []string{"panel-capabilities", "panel-field-reports"} {
		if !strings.Contains(errFn, cls) {
			t.Errorf("panelError does not emit %s, so a failed fetch of that panel "+
				"has no root element", cls)
		}
	}
}
