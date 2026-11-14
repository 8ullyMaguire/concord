"""Intent-first creation: `/projects/new` (docs/plans/post-port-roadmap.md, step 3b).

The page's job is an ORDER: ask what the person is building, answer it from the catalogue, and only then offer the form. Two halves are
tested here, and the split is deliberate:

* the **wiring and the rendering** are tested with a stubbed `/api/v1/similar/project`, because the scratch database this suite runs on
  has no project embeddings — the embedder belongs to the host, and a suite that backfilled vectors would be testing ollama, not the page.
  A stub also lets the suite choose the answers that matter: several matches, and none.
* the **semantic answer itself** is proved against the live instance, not here: `text=a federated social platform in rust` answers
  `lemmy` at 0.682 (recorded in the roadmap). A test that stubbed the embedder and asserted a similarity would assert the stub.

The assertion worth the file is the empty one. An intent router that manufactures a "closest project" out of no matches teaches people to
trust a wrong suggestion, so the page must SAY "nothing is described that way" and link nothing.
"""

import itertools
import json
import tempfile

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

PORT = 8522
BASE = f"http://127.0.0.1:{PORT}"

NOW = 1790000000
DAY = 86400
_ips = itertools.count(1)

SIMILAR = "**/api/v1/similar/project?*"


def seed(db):
    con = harness.connect(db)
    try:
        con.execute(
            "INSERT INTO users (username, display_name, created_at) VALUES ('np-author','NP Author',?)", (NOW,)
        )
        for slug, name in (
            ("np-parquet", "Parquet Conversion Toolkit"),
            ("np-unrelated", "Something Else Entirely"),
        ):
            con.execute(
                "INSERT INTO projects (slug, name, description, governance_model, license, created_at, updated_at, visibility)"
                " VALUES (?,?,?,'collective','MIT',?,?,'public')",
                (slug, name, "a project the intent check may or may not name", NOW - 30 * DAY, NOW - 30 * DAY),
            )
        con.commit()
    finally:
        con.close()


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-np-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-np-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    seed(db)
    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


@pytest.fixture()
def page(browser):
    ctx = browser.new_context(extra_http_headers={"X-Forwarded-For": f"10.78.0.{next(_ips)}"})
    page = ctx.new_page()
    yield page
    ctx.close()


def answer(similar, *, available=True, has_duplicate=False):
    return {
        "available": available,
        "kind": "project",
        "model_id": "stub-embedder",
        "similar": similar,
        "has_duplicate": has_duplicate,
        "bands": {"strong": 0.9, "likely": 0.75, "weak": 0.5},
    }


def stub(page, body):
    """Answer every similarity request with `body`, and count them."""
    calls = []

    def handler(route):
        calls.append(route.request.url)
        route.fulfill(status=200, content_type="application/json", body=json.dumps(body))

    page.route(SIMILAR, handler)
    return calls


def test_the_create_page_asks_the_intent_before_the_name(page, server):
    """The order is the feature. Both fields exist; the intent field comes first in the document."""
    page.goto(f"{BASE}/projects/new")
    expect(page.locator("#intent")).to_be_visible()
    expect(page.locator("#create-form")).to_be_visible()
    # `evaluate` on the live DOM, because "comes first" is a claim about document order and no locator selector expresses it.
    assert page.evaluate(
        "(() => { const i = document.getElementById('intent'); const n = document.getElementById('name');"
        " return !!(i && n) && (i.compareDocumentPosition(n) & Node.DOCUMENT_POSITION_FOLLOWING) > 0; })()"
    )


def test_a_match_is_named_with_a_link(page, server):
    calls = stub(page, answer([
        {"kind": "project", "entity_id": 7, "score": 0.91, "title": "Parquet Conversion Toolkit",
         "status": "public", "url": "/projects/np-parquet"},
        {"kind": "project", "entity_id": 9, "score": 0.62, "title": "Something Else Entirely",
         "status": "public", "url": "/projects/np-unrelated"},
    ]))
    page.goto(f"{BASE}/projects/new")
    page.fill("#intent", "a tool that converts csv files to parquet")
    expect(page.locator("#intent-results")).to_contain_text("2 projects already match")
    expect(page.locator('#intent-results a[href="/projects/np-parquet"]')).to_have_count(1)
    # Exactly one request: the debounce must collapse keystrokes into one question, and a positive count here is what makes a
    # "same question is asked once" test meaningful at all (a listener that never fires also asks zero times).
    assert len(calls) == 1, calls
    assert "csv" in calls[0]


def test_no_match_is_said_out_loud_and_links_nothing(page, server):
    """The assertion this suite exists for: an empty answer must not become a suggestion."""
    stub(page, answer([]))
    page.goto(f"{BASE}/projects/new")
    page.fill("#intent", "an orbital mechanics solver for kerbal")
    results = page.locator("#intent-results")
    expect(results).to_contain_text("Nothing in the catalogue is described the way you put it")
    # No project links at all — neither a fabricated "closest" nor an empty list rendered as one.
    expect(results.locator('a[href^="/projects/"]')).to_have_count(0)
    # The search link IS offered: the honest next step when the wording, not the project, may be what differs.
    expect(results.locator('a[href="/search"]')).to_have_count(1)


def test_an_unavailable_index_is_not_reported_as_no_matches(page, server):
    """`available: false` and "nothing matched" are different facts, and the page must not merge them."""
    stub(page, {"available": False, "kind": "project", "similar": []})
    page.goto(f"{BASE}/projects/new")
    page.fill("#intent", "a tool that converts csv files to parquet")
    expect(page.locator("#intent-results")).to_contain_text("similarity index is not available")
    expect(page.locator("#intent-results")).not_to_contain_text("Nothing in the catalogue")


def test_a_short_intent_asks_nothing(page, server):
    """A three-character floor, because "a" matches every project and asking is a wasted request."""
    calls = stub(page, answer([]))
    page.goto(f"{BASE}/projects/new")
    page.fill("#intent", "ab")
    page.wait_for_timeout(600)
    assert calls == [], calls


def test_creating_without_an_account_says_so(page, server):
    """The form posts to the API's own route, so the refusal is the API's: 401, and the page turns it into a sentence."""
    stub(page, answer([]))
    page.goto(f"{BASE}/projects/new")
    page.fill("#name", "New Project")
    page.fill("#slug", "new-project")
    # The page's own `session.js` asks for a guest identity first (`POST /api/v1/guest`), so this is the interesting arm: a visitor WITH a
    # token and WITHOUT an account. It must be refused, and the refusal is the server's — measured 2026-11-14 as `201 Created` before the
    # `is_guest` check existed, which is a squatting vector the handler's own comment claimed to refuse.
    created = []
    page.on("response", lambda r: created.append((r.request.method, r.url, r.status))
            if r.url.endswith("/api/v1/projects") else None)
    page.click("#create-form button[type=submit]")
    expect(page.locator("#create-status")).to_contain_text("Sign in first")
    # And nothing was created: the status code is the assertion, because a 403 that still wrote a row would read as a refusal.
    assert created == [("POST", f"{BASE}/api/v1/projects", 403)], created
    assert page.url.endswith("/projects/new"), page.url
