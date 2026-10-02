// The aria-busy contract, checked by running the page scripts.
//
// Three earlier attempts at this were static and all three were wrong:
//
//  1. Count aria-busy clears per file. Too coarse -- documents.js had three
//     clears, so deleting the one in the error path still left three and the
//     count passed. Mutation M8 went MISSED.
//  2. Walk .catch blocks by brace balance and require each that writes the
//     mount to clear. Flagged rank.js's vote-submission catch, which runs long
//     after the initial load and has nothing to clear.
//  3. Identify the initial load positionally, via the entry block and the
//     loader named on it. Still flagged the same catch: three attempts at
//     inferring control flow from source text, three wrong answers.
//
// A leak is a runtime property, so it is checked at runtime: stub fetch to
// reject, run the script, read the attribute off the mount. No inference.
//
// The bug this exists for was found by hand in a browser with the API blocked:
// /projects/concord/documents rendered its error state with aria-busy still
// "true" and zero skeletons on screen.
//
// Requires node. Skipped with a clear message if absent.
package httpapi

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runPageScriptFailure loads a real page's script into a jsdom-free harness:
// a minimal fake window/document with the page's mount point, fetch stubbed to
// reject, and a timer. It returns the mount's aria-busy afterwards.
const harness = `
const fs = require('fs');
const path = process.argv[2];
// How fetch fails: 'network' rejects, 'http404' resolves 404, 'http401'
// resolves 401. Each drives a different branch of a page's catch, and only the
// network case reached the branch project.js originally cleared in -- which is
// why mutation M9 (removing that clear) went undetected until these were split.
const mode = process.argv[5] || 'network';
function fakeFetch(url) {
  if (mode === 'http404') {
    return Promise.resolve({ ok: false, status: 404, json: () => Promise.reject(new Error('no json')), text: () => Promise.resolve('not found') });
  }
  if (mode === 'http401') {
    return Promise.resolve({ ok: false, status: 401, json: () => Promise.resolve({ error: 'unauthenticated' }), text: () => Promise.resolve('unauthenticated') });
  }
  return Promise.reject(new Error('stubbed network failure'));
}
const mount = process.argv[3];
// Substituted per case by the Go test. search.js decides whether to fetch at all
// by reading the query string, so an empty one makes it return before doing any
// work -- which showed up as HAS_CONTENT:no on the first run.
const pathname = 'PATHNAME_TOKEN';
const search = 'SEARCH_TOKEN';
// The initial aria-busy as the TEMPLATE renders it. Seeded from the real
// template so a page cannot pass by never setting the flag itself: project.js
// never sets aria-busy -- the template ships aria-busy="true" -- so with an
// unset mount, deleting every one of its three clears changed nothing and the
// test passed. That was mutation M9, missed three times before this.
const initialBusy = 'INITIAL_BUSY_TOKEN';

// --- minimal DOM ------------------------------------------------------------
function makeEl(id) {
  return {
    id, innerHTML: '', textContent: '', hidden: false, disabled: false,
    attributes: {},
    classList: { add() {}, remove() {}, contains() { return false; } },
    setAttribute(k, v) { this.attributes[k] = v; },
    getAttribute(k) { return this.attributes[k]; },
    removeAttribute(k) { delete this.attributes[k]; },
    addEventListener() {}, appendChild() {}, remove() {},
    querySelector() { return null; }, querySelectorAll() { return []; },
    insertAdjacentHTML() {},
    getBoundingClientRect() { return { width: 0, height: 0, top: 0, left: 0 }; },
    focus() {}, click() {}, contains() { return false; },
    style: {}, dataset: {},
  };
}
const els = {};
const mountEl = makeEl(mount);
els[mount] = mountEl;
// Reproduce the template's own aria-busy before the script runs.
if (initialBusy && initialBusy !== '(unset)') {
  mountEl.setAttribute('aria-busy', initialBusy);
}

const documentEl = makeEl('documentElement');
documentEl.style = {};

// DOMContentLoaded listeners. Every page script registers its work here, so the
// harness has to record and fire them -- without this the script registers and
// returns, the mount is never touched, and the aria-busy assertion is vacuous.
// That was the first run's result: ARIA_BUSY:(unset), HAS_CONTENT:no on 5 of 7.
const domListeners = [];
function onDOMContentLoaded(fn) { domListeners.push(fn); }

global.document = {
  title: 'test',
  documentElement: documentEl,
  body: makeEl('body'),
  head: makeEl('head'),
  getElementById: (id) => els[id] || null,
  querySelector: () => null,
  querySelectorAll: () => [],
  createElement: (t) => makeEl('created-' + t),
  createTextNode: () => ({}),
  addEventListener: (ev, fn) => { if (ev === 'DOMContentLoaded') domListeners.push(fn); },
  location: { href: 'http://127.0.0.1/', pathname: pathname, search: search, hash: '' },
};
global.window = {
  document: global.document,
  location: global.document.location,
  localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  addEventListener() {},
  matchMedia: () => ({ matches: false, addEventListener() {}, addListener() {} }),
  requestAnimationFrame: (f) => setTimeout(f, 0),
  setTimeout, clearTimeout, setInterval, clearInterval,
  fetch: (...a) => fakeFetch(a[0]),
  getComputedStyle: () => ({ getPropertyValue: () => '' }),
};
global.location = global.window.location;
global.localStorage = global.window.localStorage;
global.fetch = global.window.fetch;
global.getComputedStyle = global.window.getComputedStyle;
global.requestAnimationFrame = global.window.requestAnimationFrame;
global.XMLHttpRequest = function () { throw new Error('no XHR'); };
global.navigator = { userAgent: 'node' };

// skeleton.js defines window.ConcordSkeleton. Load it first, as the page does.
try {
  new Function(fs.readFileSync(path.replace(/[^/]+$/, 'skeleton.js'), 'utf8'))();
} catch (e) { /* reported below via SKELETON_HELPER */ }

// Every page script is an IIFE or defines functions then runs; evaluate it.
const src = fs.readFileSync(path, 'utf8');
try {
  new Function(src)();
} catch (e) {
  // A script that throws on load is itself a failure, but report the attribute
  // anyway so the assertion message is about aria-busy, which is the contract.
  console.log('LOAD_ERROR:' + e.message);
}

// Fire DOMContentLoaded, then let the rejected fetch chain drain through its
// microtasks and any zero-delay timers the script schedules.
setTimeout(() => {
  for (const fn of domListeners) {
    try { fn(); } catch (e) { console.log('LISTENER_ERROR:' + e.message); }
  }
}, 0);

setTimeout(() => {
  const v = mountEl.getAttribute('aria-busy');
  console.log('ARIA_BUSY:' + (v === undefined ? '(unset)' : v));
  console.log('HAS_CONTENT:' + (mountEl.innerHTML.length > 0 ? 'yes' : 'no'));
  // Did the page's own skeleton helper load? Every page guards its clear with
  // window.ConcordSkeleton guard, so if the harness did not supply one, a stuck
  // aria-busy is the harness's fault and not the page's.
  console.log('SKELETON_HELPER:' + (window.ConcordSkeleton ? 'present' : 'ABSENT'));
  process.exit(0);
}, 250);
`

// initialBusyOf reads the aria-busy the template ships on its mount point, by
// fetching the page and looking at the attribute in the rendered HTML.
func initialBusyOf(t *testing.T, ts *httptest.Server, page, mount string) string {
	t.Helper()
	body := htmlBody(t, ts, page)
	// Find the tag opening the mount point and read its aria-busy.
	i := strings.Index(body, `id="`+mount+`"`)
	if i < 0 {
		t.Fatalf("page %s does not render #%s", page, mount)
	}
	// Walk back to the start of the tag.
	start := strings.LastIndex(body[:i], "<")
	tag := body[start:]
	if k := strings.IndexByte(tag, '>'); k >= 0 {
		tag = tag[:k]
	}
	if j := strings.Index(tag, `aria-busy="`); j >= 0 {
		rest := tag[j+len(`aria-busy="`):]
		if k := strings.IndexByte(rest, '"'); k >= 0 {
			return rest[:k]
		}
	}
	return "(unset)"
}

func TestPageScriptsClearAriaBusyWhenTheFetchFails(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping the runtime aria-busy check " +
			"(the static checks above still run)")
	}

	ts := newTestServer(t)
	// The project-scoped routes resolve a slug and 404 without one; a 404 body
	// has no mount point, so the test would read the initial busy state from the
	// error page.
	makeProject(t, ts, "a11y-probe")

	// Mount point per page script, and the URL the script derives its slug from.
	// slugFromPath() reads location.pathname, so the pathname is set per script.
	cases := []struct {
		script string
		mount  string
		path   string
		page   string
	}{
		{"documents.js", "project-documents", "/projects/probe/documents", "/projects/a11y-probe/documents"},
		{"projects.js", "projects-grid", "/projects", "/projects"},
		{"project.js", "project-detail", "/projects/probe", "/projects/a11y-probe"},
		{"board.js", "kanban-board", "/projects/probe/board", "/projects/a11y-probe/board"},
		{"search.js", "search-results", "/search?q=kanban", "/search?q=kanban"},
		{"rank.js", "rank-root", "/projects/probe/rank", "/projects/a11y-probe/rank"},
		{"ranking.js", "ranking-root", "/projects/probe/ranking", "/projects/a11y-probe/ranking"},
	}

	for _, tc := range cases {
		for _, mode := range []string{"network", "http404", "http401"} {
			t.Run(tc.script+"/"+mode, func(t *testing.T) {
				dir := t.TempDir()
				harnessPath := filepath.Join(dir, "harness.js")
				if err := os.WriteFile(harnessPath, []byte(harness), 0o644); err != nil {
					t.Fatal(err)
				}

				// The harness is written with placeholders for the pathname and
				// query string, substituted per case.
				h := strings.Replace(harness, "PATHNAME_TOKEN", tc.path, 1)
				h = strings.Replace(h, "SEARCH_TOKEN", "?q=kanban", 1)
				// The starting aria-busy is whatever the template ships, read from
				// the real template rather than assumed.
				h = strings.Replace(h, "INITIAL_BUSY_TOKEN", initialBusyOf(t, ts, tc.page, tc.mount), 1)
				if err := os.WriteFile(harnessPath, []byte(h), 0o644); err != nil {
					t.Fatal(err)
				}

				// Write the real script to disk for the harness to read. The harness
				// uses fs.readFileSync, which cannot open an http:// URL -- the first
				// run failed with ENOENT on the test server's own address.
				scriptFile := filepath.Join(dir, tc.script)
				if err := os.WriteFile(scriptFile, []byte(assetBody(t, ts, "/assets/js/"+tc.script)), 0o644); err != nil {
					t.Fatal(err)
				}
				// Load the real skeleton helper into the harness before the page
				// script runs, exactly as the browser does: the page templates put
				// skeleton.js first. Without it every `window.ConcordSkeleton &&`
				// guard short-circuits and the page cannot clear anything -- which
				// looked exactly like two real leaks on the first run of this test.
				skelFile := filepath.Join(dir, "skeleton.js")
				if err := os.WriteFile(skelFile, []byte(assetBody(t, ts, "/assets/js/skeleton.js")), 0o644); err != nil {
					t.Fatal(err)
				}
				out, err := exec.Command(node, harnessPath, scriptFile, tc.mount, tc.path, mode).
					CombinedOutput()
				if err != nil {
					t.Fatalf("harness failed for %s: %v\n%s", tc.script, err, out)
				}
				got := string(out)

				busy := "unset"
				for _, line := range strings.Split(got, "\n") {
					if strings.HasPrefix(line, "ARIA_BUSY:") {
						busy = strings.TrimPrefix(line, "ARIA_BUSY:")
					}
				}
				if strings.Contains(got, "LOAD_ERROR:") {
					t.Errorf("%s threw on load; cannot assert its aria-busy contract.\n%s",
						tc.script, got)
				}
				// The contract: after a failed fetch, the region is not busy. "unset"
				// also passes -- a page that never marks itself busy has nothing to
				// leak -- but it must still have rendered something, otherwise the
				// script did no work and the test is vacuous.
				if busy == "true" {
					t.Errorf("%s leaves #%s aria-busy=true after the fetch failed.\n"+
						"aria-busy suppresses live-region updates inside the region, so the "+
						"error it rendered is never announced to a screen reader.\n%s",
						tc.script, tc.mount, got)
				}
				if !strings.Contains(got, "HAS_CONTENT:yes") {
					t.Errorf("%s rendered nothing into #%s after a failed fetch; the "+
						"aria-busy assertion would be vacuous.\n%s",
						tc.script, tc.mount, got)
				}
			})
		}
	}
}
