// Frontend baseline requirements, adopted 2026-10-02.
//
// Three of these were verified absent before being written, and each is a
// baseline accessibility requirement rather than a refinement:
//
//	focus indicators -- five :focus rules existed and all five were on inputs.
//	  Buttons, links, nav items and kanban cards had none.
//	prefers-reduced-motion -- @media was used for layout only, and
//	  .loading-spinner animated indefinitely regardless of the user's
//	  preference.
//	skeleton loading -- every page used a centred blocking spinner.
//
// The assertions are on the stylesheet source because the failures are
// invisible to a unit test: a missing focus ring is only observable by someone
// tabbing through the page, and an unrespected reduced-motion preference is
// only observable by someone who set it.
package httpapi

import (
	"strings"
	"testing"
)

// TestStylesheetGivesEveryInteractiveElementAFocusIndicator guards the gap that
// measured five input-only :focus rules.
func TestStylesheetGivesEveryInteractiveElementAFocusIndicator(t *testing.T) {
	ts := newTestServer(t)
	css := assetBody(t, ts, "/assets/css/style.css")

	if !strings.Contains(css, ":focus-visible") {
		t.Fatal("no :focus-visible rule anywhere; a keyboard user tabbing through " +
			"a page gets no indication of where they are")
	}

	// The elements that had nothing. Each must appear in a :focus-visible
	// selector list, not merely exist somewhere in the file.
	for _, sel := range []string{
		"a:focus-visible", "button:focus-visible",
		"[tabindex]:focus-visible", "summary:focus-visible",
	} {
		if !strings.Contains(css, sel) {
			t.Errorf("no :focus-visible rule for %s", sel)
		}
	}

	// A focus ring must never be removed without a replacement. The three
	// pre-existing sites are inputs that swap the outline for a box-shadow, so
	// they are correct -- but a fourth one on a button would not be.
	//
	// Scanned per rule block, splitting on "{" / "}" boundaries. A plain text
	// search for "outline: none" also matches the explanatory comment in the
	// :focus-visible section, which is prose and not a declaration -- that
	// produced a false positive on the first run of this test.
	for _, block := range strings.Split(css, "{") {
		if !strings.Contains(block, "outline: none") {
			continue
		}
		// Strip comments first: the explanatory comment in the :focus-visible
		// section mentions "outline: none" in prose and was matched by an
		// earlier version of this check.
		if at := strings.Index(block, "/*"); at >= 0 {
			if closeIdx := strings.Index(block[at:], "*/"); closeIdx >= 0 {
				block = block[:at] + block[at+closeIdx+2:]
			}
		}
		// Only the declarations matter, not the selector -- a selector like
		// `.input:focus` contains "focus" but says nothing about whether a ring
		// is drawn, so testing for "outline" anywhere in the block matched the
		// selector and passed rules that draw nothing.
		decls := block
		if nl := strings.Index(decls, "\n"); nl >= 0 {
			decls = decls[nl+1:]
		}
		hasReplacement := strings.Contains(decls, "box-shadow") ||
			strings.Contains(decls, "border-color") ||
			strings.Contains(decls, "background")
		if !hasReplacement {
			t.Errorf("a rule removes the focus outline and draws no replacement "+
				"declaration: %q", strings.TrimSpace(strings.Split(decls, "\n")[0]))
		}
	}
}

// TestStylesheetRespectsReducedMotion: an indefinitely spinning element is the
// classic vestibular trigger.
func TestStylesheetRespectsReducedMotion(t *testing.T) {
	ts := newTestServer(t)
	css := assetBody(t, ts, "/assets/css/style.css")

	if !strings.Contains(css, "prefers-reduced-motion") {
		t.Fatal("no prefers-reduced-motion block; .loading-spinner animates " +
			"indefinitely regardless of the user's setting")
	}
	// The block must actually neutralise animation and transitions, not merely
	// mention the query. Scan from the @media opening brace to the brace that
	// closes the whole rule -- a single "}" here would stop at the first nested
	// declaration block and miss the rules that follow it.
	start := strings.Index(css, "prefers-reduced-motion")
	if start < 0 {
		t.Fatal("no prefers-reduced-motion block")
	}
	// Bound the slice to what actually remains: an earlier version hardcoded
	// 2400 and panicked on a shorter tail, which reads as a crash rather than
	// as a failed assertion.
	body := css[start:]
	if len(body) > 2049 {
		window := body[:2049]
		if end := strings.LastIndex(window, "}"); end > 0 {
			body = window[:end+1]
		}
	}
	if !strings.Contains(body, "animation-duration") {
		t.Error("the block does not shorten animation-duration")
	}
	if !strings.Contains(body, "transition-duration") {
		t.Error("the block does not shorten transition-duration")
	}
	// scroll-behavior: smooth is set on html and is a motion trigger too.
	if !strings.Contains(body, "scroll-behavior") {
		t.Error("the block leaves `scroll-behavior: smooth` in place, which is a " +
			"motion effect regardless of the animation/transition rules")
	}
	// And the spinner must remain visible as a static cue rather than vanish:
	// it carries role="status" and an aria-label, so a reader still needs to see
	// something while the fetch is in flight.
	if !strings.Contains(body, "loading-spinner") {
		t.Error("the block does not mention .loading-spinner; stopping the " +
			"animation without a static replacement leaves an empty box where the " +
			"loading announcement was")
	}
}

// TestEveryPageShowsASkeletonNotABlockingSpinner: the loading pattern must be
// uniform, so the next page cannot invent its own.
func TestEveryPageShowsASkeletonNotABlockingSpinner(t *testing.T) {
	ts := newTestServer(t)

	// The skeleton helper exists and is served.
	js := assetBody(t, ts, "/assets/js/skeleton.js")
	for _, required := range []string{"ConcordSkeleton", "rows:", "article:", "board:", "done:"} {
		if !strings.Contains(js, required) {
			t.Errorf("skeleton.js does not define %q", required)
		}
	}

	// No template may ship a spinner in its initial markup. A spinner in a
	// template is on screen before any script runs, which is exactly the
	// blocking state being removed.
	// Only the pages that fetch client-side. login and register are excluded
	// deliberately: they are server-rendered forms with no mount point, no
	// spinner and no client fetch, so requiring a skeleton of them would be
	// asserting against a state that does not exist.
	// A real project slug: the project page and everything under it resolve the
	// slug and 404 on a missing one, and a 404 body contains neither aria-busy
	// nor a script tag -- so an invented slug makes every assertion here pass for
	// the wrong reason. The first version used "a-slug" and reported all eight
	// pages as failing.
	makeProject(t, ts, "a11y-probe")
	for page, url := range map[string]string{
		"index":     "/",
		"projects":  "/projects",
		"search":    "/search",
		"rank":      "/projects/a11y-probe/rank",
		"ranking":   "/projects/a11y-probe/ranking",
		"board":     "/projects/a11y-probe/board",
		"project":   "/projects/a11y-probe",
		"documents": "/projects/a11y-probe/documents",
		"audit":     "/projects/a11y-probe/audit",
	} {
		body := htmlBody(t, ts, url)
		if !strings.Contains(body, "<html") {
			t.Fatalf("%s did not render a page (%d bytes)", page, len(body))
		}
		if strings.Contains(body, "loading-spinner") {
			t.Errorf("%s.html still renders a centred spinner in its initial markup", page)
		}
		// search.html is the exception and must be: with no query it renders a
		// static "type a query to start" state and fetches nothing, so marking it
		// busy would be a lie. With a query it is busy.
		if page == "search" {
			if !strings.Contains(htmlBody(t, ts, "/search?q=kanban"), `aria-busy="true"`) {
				t.Error("search.html is not aria-busy while a query is being searched")
			}
			if !strings.Contains(body, `aria-busy="false"`) {
				t.Error("search.html marks itself busy with no query, while fetching nothing")
			}
		} else if !strings.Contains(body, `aria-busy="true"`) {
			t.Errorf("%s.html does not mark its mount point aria-busy", page)
		}
		if !strings.Contains(body, "/assets/js/skeleton.js") {
			t.Errorf("%s.html does not load skeleton.js", page)
		}
	}

	css := assetBody(t, ts, "/assets/css/style.css")
	// A filled button must carry a two-tone ring. TestStylesheetGivesEvery-
	// InteractiveElementAFocusIndicator passes with a plain indigo outline on
	// .btn-primary, because the declaration exists and is valid -- but .btn-primary
	// is itself indigo, so the ring lands on the background in the same colour and
	// is invisible. Found by tabbing through the deployed page: the computed
	// outline was 2px solid rgb(99,102,241) on an rgb(99,102,241) background.
	//
	// Asserted as: the primary button's focus rule must set a box-shadow, which is
	// the inner contrasting ring. A test cannot compute contrast without a layout
	// engine, so it asserts the mechanism that makes contrast possible.
	if !strings.Contains(css, ".btn:focus-visible") {
		t.Error("no .btn:focus-visible rule")
	}
	// Anchor on the dedicated rule, not the combined selector list that also
	// contains .btn:focus-visible and has no box-shadow. Searching for the bare
	// string finds the earlier occurrence first.
	// Anchor on .btn-primary:focus-visible, which appears only in the dedicated
	// rule. Two earlier anchors were wrong: the bare string and "\n.btn:..."
	// both match the general selector list higher up, which legitimately has no
	// box-shadow, so the check failed against correct CSS.
	at := strings.Index(css, ".btn-primary:focus-visible")
	if at < 0 {
		t.Error("no dedicated .btn focus rule naming the filled variants")
		return
	}
	// Window is 400 chars from the rule's opening brace: the selector list alone
	// is ~150 of them, so a tighter window stops before the declarations and the
	// check fails against correct CSS.
	brace := strings.Index(css[at:], "{")
	if brace < 0 {
		t.Error("the dedicated .btn:focus-visible rule has no body")
		return
	}
	btnBlock := css[at+brace:]
	if end := strings.Index(btnBlock, "}"); end > 0 {
		btnBlock = btnBlock[:end]
	}
	if !strings.Contains(btnBlock, "box-shadow") {
		t.Error(".btn:focus-visible sets no box-shadow. .btn-primary is indigo and " +
			"an indigo outline on an indigo fill is invisible: the declaration is " +
			"valid, so every static check passes while a keyboard user sees nothing.")
	}

	for _, required := range []string{".skeleton", ".skeleton-card", ".skeleton-lines", ".skeleton-row"} {
		if !strings.Contains(css, required) {
			t.Errorf("the stylesheet has no %s rule", required)
		}
	}
}

// TestAriaBusyIsClearedWhenContentArrives is the half that is easy to forget.
//
// aria-busy suppresses live-region updates inside the region it marks. A page
// that sets it and never clears it silently stops announcing its own results --
// and the error and empty paths are exactly where it gets skipped.
func TestAriaBusyIsClearedWhenContentArrives(t *testing.T) {
	ts := newTestServer(t)

	for _, script := range []string{"project.js", "board.js", "documents.js", "search.js", "projects.js"} {
		js := assetBody(t, ts, "/assets/js/"+script)
		if !strings.Contains(js, "aria-busy") && !strings.Contains(js, "ConcordSkeleton.done") {
			t.Errorf("%s never sets or clears aria-busy", script)
			continue
		}
		if !strings.Contains(js, "ConcordSkeleton.done") {
			t.Errorf("%s sets aria-busy but never calls ConcordSkeleton.done; the "+
				"region stays permanently busy and stops announcing results", script)
		}
	}

	js := assetBody(t, ts, "/assets/js/skeleton.js")
	if !strings.Contains(js, "aria-busy', 'false'") && !strings.Contains(js, `setAttribute('aria-busy', 'false')`) {
		t.Error("ConcordSkeleton.done does not set aria-busy to false")
	}
}
