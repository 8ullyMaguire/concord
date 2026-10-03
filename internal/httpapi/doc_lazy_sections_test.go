package httpapi

import (
	"strings"
	"testing"
)

// Regression tests for the lazy-section document reader, from the site-wide e2e
// sweep of 2026-10-02 (20-Areas/concord-site-e2e-verification.md).
//
// The reader only splits a document into sections when it is large, and the
// split originally matched H2 only. So every one of these defects was
// unreachable: the corpus had no document that both exceeded the size threshold
// and was headed entirely with H1, which is why the branch had never rendered
// before it was exercised with a 229 KB all-H1 document. They are pinned here as
// source-level assertions because that is the layer where they live, and
// because asserting on generated markup would only pass for the fixtures the
// test happens to build.
//
// Each test names the defect so a failure says which one came back.

// src is the documents.js asset, or the test fails.
func documentsJS(t *testing.T) string {
	t.Helper()
	return mustAsset(t, "/assets/js/documents.js")
}

// lazySections is the portion of the reader that splits and lazily renders a
// large document: from the split-level markup through the end of wire().
func lazySections(t *testing.T) string {
	t.Helper()
	src := documentsJS(t)
	// Anchored on the function, not on the string "data-split-level". The
	// small-document path also declares that attribute, and it comes first in
	// the file, so anchoring on it started the slice ABOVE the lazy path's
	// placeholder code and excluded it from every assertion -- which is how
	// TestPlaceholderHeightIsCapped passed against an uncapped height.
	i := strings.Index(src, "function splitSections(")
	if i < 0 {
		t.Fatal("documents.js no longer has splitSections(); a large document is " +
			"no longer split into lazily-rendered sections")
	}
	// To the end of the file: the placeholder markup, the click handler and the
	// renderer call are all below this point.
	return src[i:]
}

// The 200 KB document that exposed this, as a set of properties rather than a
// fixture: all headings at level 1, and long enough to clear the split
// threshold. A document of this shape produced a dead contents list, sections
// stacked inside the 200px navigation column, and every contents entry landing
// on the wrong section.
func TestLargeDocumentSplitsOnH1NotJustH2(t *testing.T) {
	src := lazySections(t)

	// splitSections takes the level to split on, and the markup publishes it so
	// the renderer and the contents list agree on one number.
	if !strings.Contains(src, "splitLevel") {
		t.Error("the split level is no longer computed and published; the renderer " +
			"and the contents list will each fall back to their own default and " +
			"disagree on which headings get ids")
	}

	// The renderer must be told the level, or it defaults to 2 and puts no ids
	// on an all-H1 document -- which is what left the contents list with zero
	// working links.
	//
	// Both render paths need it, and the count is what says so: the eager first
	// section and the lazily-rendered ones. Asserting only that the string
	// "idLevel:" appears passed when it appeared in exactly one of the two, which
	// left every lazily-rendered section without ids -- and so without a working
	// contents entry, which is the exact symptom this test exists to catch.
	if n := strings.Count(src, "idLevel:"); n < 2 {
		t.Errorf("the split level is passed to the renderer %d time(s); both the "+
			"eager section and the lazily-rendered ones need it, or a lazily-"+
			"rendered section gets no heading ids and its contents entry does "+
			"not resolve", n)
	}
}

// The sections were emitted as siblings of the contents nav inside a two-column
// grid, so the browser placed them into successive grid cells: measured on a
// 229 KB document, section 1 was 33314px tall inside the 200px nav column and
// section 2 sat beside it in the article column at the same scroll offset.
// Every placeholder shared a top with its neighbour.
func TestLargeDocumentSectionsAreWrappedInOneGridCell(t *testing.T) {
	src := lazySections(t)

	if !strings.Contains(src, "doc-sections") {
		t.Error("the sections are not wrapped in a .doc-sections element; as " +
			"siblings of the contents nav they are placed side by side by the " +
			"two-column grid instead of stacking down the page")
	}
	if !strings.Contains(src, "'<div class=\"doc-sections\">' + first + placeholder") {
		t.Error("the wrapper does not enclose both the eager first section and " +
			"the placeholders; whichever is left outside becomes a grid cell of " +
			"its own")
	}

	// The eager article has to be inside the wrapper too, or it takes the first
	// grid cell and the nav takes the second.
	if !strings.Contains(src, ".doc-sections > .markdown-body") {
		t.Error("nothing selects the eager first section through the wrapper; the " +
			"contents link for section 0 has no target inside the article column")
	}
}

// data-section is an index into the section list, so entry n names section n.
// Section 0 is the eager article and the placeholders are numbered 1..n-1, so
// entry n is placeholder n. Reading it as n+1 put every entry one section too
// far: clicking the entry labelled "Heading 2" scrolled to "Heading 3", and the
// last entry asked for a placeholder that does not exist and fell back to the
// document header.
func TestContentsEntryNTargetsPlaceholderN(t *testing.T) {
	src := lazySections(t)

	// Matched on the whole selector expression rather than on "idx", because
	// the placeholder-markup line also builds a data-section-body attribute
	// from an index and would otherwise satisfy this.
	if !strings.Contains(src, `box.querySelector('[data-section-body="' + idx + '"]')`) {
		t.Error("the contents click handler no longer looks up placeholder idx for " +
			"entry idx; an off-by-one here silently sends every entry to the " +
			"wrong section, and there is no error to notice it by")
	}
	// The n+1 form is the specific regression. Guard it by name so a later
	// refactor cannot reintroduce it as an innocuous-looking change.
	if strings.Contains(src, "(idx + 1) + '\"") {
		t.Error("the contents click handler is back to mapping entry idx to " +
			"placeholder idx+1, which is the off-by-one measured in the e2e sweep")
	}
}

// A placeholder is shorter than the section it stands for: it reserves
// min-height, while the rendered content is ~14200px against a 4000px
// reservation. So a target's position is not final until everything above it
// renders. Scrolling to an unrendered target landed the reader short by exactly
// the growth of the sections above it (measured 1743px).
//
// Two earlier attempts failed here and both were removed rather than left in
// place: re-scrolling on a frame budget, and re-scrolling on a render event.
// Each was a guess at when the IntersectionObservers would fire, and each left
// residual offsets that were exact multiples of one section's growth.
func TestContentsClickRendersPathToTarget(t *testing.T) {
	src := lazySections(t)

	if !strings.Contains(src, "<= idx") {
		t.Error("the contents click no longer renders every section from the top " +
			"down to the target; the target's position is not final until then, " +
			"and the reader lands short by the growth of the sections above it")
	}

	// One scrollIntoView, and no correction loop. A loop here is the shape that
	// failed twice; if one comes back it needs a measurement that justifies it.
	if strings.Count(src, "scrollIntoView") > 1 {
		t.Errorf("the contents handler scrolls %d times; a second scroll is a "+
			"correction loop, which did not settle against observer-driven "+
			"layout changes (see the comment above renderPendingSection)",
			strings.Count(src, "scrollIntoView"))
	}
}

// A clicked section must be filled, not left as an empty band. Scrolling to an
// unrendered placeholder shows the reader whitespace and reads as a broken
// link.
func TestContentsClickRendersTheTargetItScrollsTo(t *testing.T) {
	src := documentsJS(t)
	i := strings.Index(src, "function renderPendingSection(")
	if i < 0 {
		t.Fatal("documents.js has no renderPendingSection(); a clicked section " +
			"would be scrolled to while still empty")
	}
	// The helper's own body, up to the next function. Checked for the three
	// markers it must clear, not just that it renders: leaving any one in place
	// means the observer renders it again and the height changes twice, which is
	// the reflow this whole path exists to avoid.
	body := src[i:]
	if j := strings.Index(body, "\n  function "); j > 0 {
		body = body[:j]
	}
	for _, want := range []string{
		"ConcordMarkdown.render",           // fills it
		"removeAttribute('data-markdown')", // the observer will not do it twice
		"classList.remove('is-pending')",   // and it stops counting as pending
		"removeAttribute('style')",         // drops the reserved height
	} {
		if !strings.Contains(body, want) {
			t.Errorf("renderPendingSection does not %s", want)
		}
	}

	// The body being correct is not enough -- it has to be CALLED from the
	// contents path. An earlier version of this test asserted only on the body
	// and so passed with the call deleted, which is the same defect: the reader
	// is left looking at an empty band.
	//
	// Counted as "declaration plus two calls". Three call sites matter: the
	// IntersectionObserver path and the contents click, which is the one that
	// fills a section the reader explicitly asked for. Getting this count wrong
	// is how the test below passed against a deleted call, so the expected
	// number is spelled out rather than left as an off-by-one.
	const wantRefs = 3 // the function declaration, the observer, the contents click
	if n := strings.Count(src, "renderPendingSection("); n != wantRefs {
		t.Errorf("found %d reference(s) to renderPendingSection, want %d "+
			"(the declaration plus the IntersectionObserver path plus the "+
			"contents click); dropping either call leaves a section empty "+
			"while the reader is looking at it", n, wantRefs)
	}
}

// The reserved height is what makes the scrollbar roughly right before the
// sections render. It is an estimate, so it must stay bounded: uncapped, a
// 229 KB section reserved 60 KB of blank page.
func TestPlaceholderHeightIsCapped(t *testing.T) {
	src := lazySections(t)
	// The trailing comma matters: "Math.min(400000" contains "Math.min(4000",
	// so an uncapped height satisfied this assertion until it was checked
	// against a comma.
	if !strings.Contains(src, "Math.min(4000,") {
		t.Error("the placeholder's reserved height is no longer capped at 4000px; " +
			"an uncapped estimate reserves a blank page the size of the document")
	}
}
