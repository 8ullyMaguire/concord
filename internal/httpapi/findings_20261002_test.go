package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression tests for the HTTP findings in the site-wide e2e sweep of
// 2026-10-02 (20-Areas/concord-site-e2e-verification.md).
//
// Each test names the finding it pins so a future failure says which one came
// back, rather than asserting a bare status code.

// get fetches a path and returns the status and body.
func get(t *testing.T, ts *httptest.Server, path string) (int, string) {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return resp.StatusCode, string(body)
}

// mustAsset fetches a served asset, failing the test if it is not reachable.
func mustAsset(t *testing.T, path string) string {
	t.Helper()
	ts := newTestServer(t)
	code, body := get(t, ts, path)
	if code != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, code)
	}
	return body
}

// F3: every browser 404 was a raw JSON body with not one link out of it, so a
// person who mistyped a project slug was served {"error": "not found"} as a web
// page.
//
// The status was already correct and must stay 404: answering a slug the caller
// cannot see with 403 would confirm the slug exists, which is the deliberate
// anti-enumeration decision these handlers make. These assert the
// representation, not a loosened status.
func TestPageMissRendersHTMLNotJSON(t *testing.T) {
	ts := newTestServer(t)

	for _, path := range []string{
		"/projects/no-such-project-xyz",
		"/projects/no-such-project-xyz/board",
		"/projects/no-such-project-xyz/documents",
		"/projects/no-such-project-xyz/rank",
		"/projects/no-such-project-xyz/ranking",
		"/nope/nope",
	} {
		code, body := get(t, ts, path)

		if code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, code)
		}
		if strings.Contains(body, `"error"`) {
			t.Errorf("%s: body is still the API error shape:\n%s", path, body)
		}
		if !strings.Contains(body, "<html") {
			t.Errorf("%s: body is not an HTML document:\n%s", path, body)
		}
		// A dead end is the defect. There must be a way onward.
		if !strings.Contains(body, `href="/projects"`) {
			t.Errorf("%s: 404 page has no link onward:\n%s", path, body)
		}
	}
}

// F3: the API must keep answering in JSON. Handing an HTML page to a machine
// consumer is the worse of the two mistakes.
func TestAPIMissStillReturnsJSON(t *testing.T) {
	ts := newTestServer(t)

	for _, path := range []string{
		"/api/v1/projects/no-such-project-xyz",
		"/api/v1/nope",
		"/api/v1/projects/no-such-project-xyz/features",
	} {
		code, body := get(t, ts, path)

		if code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, code)
		}
		if !strings.Contains(body, `"error"`) {
			t.Errorf("%s: API 404 is not JSON-shaped:\n%s", path, body)
		}
		if strings.Contains(body, "<html") {
			t.Errorf("%s: an API client got an HTML page:\n%s", path, body)
		}
	}
}

// F3: the 404 body must not distinguish "no such project" from "not yours".
// Either difference is an enumeration oracle.
func TestNotFoundPageDoesNotLeakExistence(t *testing.T) {
	ts := newTestServer(t)

	for _, path := range []string{"/projects/no-such-project-xyz", "/nope/nope"} {
		_, body := get(t, ts, path)
		lower := strings.ToLower(body)

		for _, leak := range []string{
			"does not exist", "forbidden", "private", "permission",
			"unauthorized", "sign in to see",
		} {
			if strings.Contains(lower, leak) {
				t.Errorf("%s: 404 body contains %q, which distinguishes absent "+
					"from forbidden:\n%s", path, leak, body)
			}
		}
	}
}

// F4: the documents table of contents was unreachable. It was built only when a
// document exceeded BIG_DOC_BYTES (200 KB) and the largest document in the
// database is 55 KB, so no document ever got a nav -- the 36 KB frontend spec
// has 22 headings and rendered with none.
//
// The fix separated the two questions the threshold conflated: a TOC depends on
// how many headings a document has, lazy rendering depends on how many bytes it
// has. A mid-sized document now gets a TOC and still renders whole.
func TestDocumentsTOCIsNotGatedOnSize(t *testing.T) {

	// documents.js must contain the size threshold AND a heading-count path, or
	// the TOC is still gated on bytes alone.
	js := mustAsset(t, "/assets/js/documents.js")

	if !strings.Contains(js, "BIG_DOC_BYTES") {
		t.Error("documents.js no longer mentions BIG_DOC_BYTES; lazy rendering " +
			"threshold was removed entirely rather than separated from the TOC")
	}
	if !strings.Contains(js, "doc-toc") {
		t.Error("documents.js builds no TOC at all")
	}
	// The TOC must be read back from the rendered headings, not recomputed from
	// a parallel count. Counting independently is what produced a TOC with six
	// links to ids that did not exist and duplicate ids for every heading after
	// the first block.
	if !strings.Contains(js, "buildTOCFromDOM") {
		t.Error("documents.js does not build the TOC from the rendered headings; " +
			"an independently computed list can drift from the ids it links to")
	}
	if strings.Contains(js, "sectionsForToc") {
		t.Error("documents.js still derives the TOC from a parallel section count")
	}
}

// F5: the voting surface advertised nothing about keyboard shortcuts and had no
// key handler, while the frontend spec promised arrow-key voting. Every outcome is
// a button, so this was a missing shortcut rather than an unreachable action.
func TestRankPageAdvertisesKeyboardShortcuts(t *testing.T) {
	js := mustAsset(t, "/assets/js/rank.js")

	if !strings.Contains(js, "ArrowLeft") || !strings.Contains(js, "ArrowRight") {
		t.Error("rank.js binds no arrow keys; the spec promises " +
			"left/right/up/down for A/B/both/neither")
	}
	// A held key must not fire repeatedly, which would spend a voter's whole
	// queue on one keypress.
	if !strings.Contains(js, "ev.repeat") {
		t.Error("rank.js does not guard against key repeat")
	}
	// Arrows in a text field still mean caret movement.
	if !strings.Contains(js, "isTypingContext") {
		t.Error("rank.js does not skip keys typed into a field")
	}
	// A key must reach the same code path as a click, or they can diverge.
	if !strings.Contains(js, "keydown") {
		t.Error("rank.js registers no keydown listener")
	}
	if !strings.Contains(js, "rank-keys") {
		t.Error("rank.js renders no keyboard hint; a shortcut nobody can discover " +
			"is not a shortcut")
	}
}

// F7: POST /api/v1/auth/logout existed from the start but nothing in the
// interface called it, so the only way to end a session was clearing localStorage
// by hand. Sign out must revoke server-side too, not just forget the token
// locally -- otherwise a working credential stays live in the database.
func TestSignOutRevokesTheTokenServerSide(t *testing.T) {

	js := mustAsset(t, "/assets/js/session.js")
	if !strings.Contains(js, "/api/v1/auth/logout") {
		t.Error("session.js never calls the logout endpoint")
	}
	// clear() alone would satisfy the grep above and leave the token live.
	if !strings.Contains(js, "authHeaders") {
		t.Error("sign out does not send the token, so nothing is revoked")
	}
	if !strings.Contains(js, "s.clear()") {
		t.Error("sign out never clears the local token")
	}
	// The account link pointed at '#account', a fragment that exists nowhere in
	// the document. Matched as an assignment, not as a bare substring: the fix
	// explains the old value in a comment, so a substring check fails on its own
	// explanation and would push someone to delete the reason it was fixed.
	if strings.Contains(js, "link.href = '#account'") {
		t.Error("the account link still points at the dead #account fragment")
	}
}

// F6: a long inline code span set its own width and overflowed the document at
// 375px -- 601-647px wide, clipped because the page does not scroll sideways.
func TestInlineCodeCanWrap(t *testing.T) {
	css := mustAsset(t, "/assets/css/style.css")

	idx := strings.Index(css, ".markdown-body code")
	if idx < 0 {
		t.Fatal("no .markdown-body code rule")
	}
	// Take the rule up to its closing brace.
	end := strings.Index(css[idx:], "}")
	if end < 0 {
		t.Fatal("malformed .markdown-body code rule")
	}
	rule := css[idx : idx+end]

	if !strings.Contains(rule, "overflow-wrap") {
		t.Errorf(".markdown-body code has no overflow-wrap; a long token "+
			"overflows at phone widths:\n%s", rule)
	}
}