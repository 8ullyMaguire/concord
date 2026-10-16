"""End-to-end tests for the merge queue page (spec §5.7, §6.3).

Prerequisites:  make build
Run:            pytest tests/e2e/merge_e2e.py -v

Why this suite exists separately from `internal/httpapi/merge_gate_test.go`:

  - the Go tests prove the refusals: no confirmations is refused, an outsider is
    refused, a self-confirmation is refused, another project's request is refused;
  - `internal/store/merge_gate_store_test.go` proves the arithmetic behind them;
  - NEITHER proves a browser gets them together, which is the whole point of the
    page: the gate has to be READABLE before it can be satisfied. A page that
    renders every row "met" and hides the shortfall passes every source gate.

The fixture seeds members, contributions and merge requests DIRECTLY in sqlite,
and the confirmations too. A browser test whose data comes from the endpoints under
test cannot fail for the reason it exists.

The claims worth a browser here:

  1. each row reports the gate it must meet, in the server's numbers -- the
     shortfall is visible before anybody presses anything;
  2. pressing Merge on a short request is REFUSED, and the notice carries the
     server's own sentence naming what is missing;
  3. confirming twice, once by a reviewer, lands the change -- and the row then
     reads `merged`. A gate that refuses everything passes test 2 alone;
  4. the title is user text and renders as text.
"""

import base64
import hashlib
import sqlite3
import tempfile
import time

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

PORT = 8427
BASE = f"http://127.0.0.1:{PORT}"

# Same per-test bucket trick as the other suites: the limiter counts 100 requests a
# minute per client key, and clientKey() honours X-Forwarded-For from loopback.
_xff_counter = {"n": 0}

HOSTILE_TITLE = '<img src=x onerror="window.__xss=1">'


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-merge-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-merge-e2e-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    seed(db)
    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


def _context(browser, token=None):
    """A browser context, optionally signed in with both credentials.

    The cookie opens the PAGE (a navigation cannot carry a token); the localStorage
    token is what the page's own fetches present. They are different credentials on
    purpose, so a signed-in browser needs both.
    """
    _xff_counter["n"] += 1
    n = _xff_counter["n"]
    ctx = browser.new_context(
        extra_http_headers={"X-Forwarded-For": f"10.66.{n // 200}.{n % 200}"})
    if token:
        ctx.add_init_script("localStorage.setItem('concord.token', %r);" % token)
        ctx.add_cookies([{"name": "concord_nav", "value": token, "url": BASE + "/"}])
    return ctx


def _signed_in(browser, db, username):
    ctx = _context(browser, _mint_token(db, username))
    pg = ctx.new_page()
    errors = []
    pg.on("pageerror", lambda e: errors.append(str(e)))
    pg.on("console", lambda m: errors.append(m.text) if m.type == "error" else None)
    pg.errors = errors
    return ctx, pg


@pytest.fixture()
def owner_page(browser, server):
    ctx, pg = _signed_in(browser, server["db"], "merge-owner")
    yield pg
    ctx.close()


@pytest.fixture()
def anon_page(browser, server):
    ctx = _context(browser, None)
    pg = ctx.new_page()
    yield pg
    ctx.close()


def _mint_token(db, username):
    """A token row for `username`, the way the login handler would write one.

    Two details are the server's, not invented here: api_tokens.token_hash is base64
    RawStd of the sha256 digest (not hex, whatever 0001_init.sql's comment says), and
    ResolveToken rejects anything that does not start with `clt_`.
    """
    raw = "clt_merge-" + username
    con = sqlite3.connect(db)
    uid = con.execute("SELECT id FROM users WHERE username = ?", (username,)).fetchone()[0]
    digest = hashlib.sha256(raw.encode()).digest()
    con.execute(
        "INSERT OR REPLACE INTO api_tokens (token_hash, user_id, created_at, last_used_at)"
        " VALUES (?,?,?,?)",
        (base64.b64encode(digest).decode().rstrip("="), uid, time.time(), time.time()))
    con.commit()
    con.close()
    return raw


def seed(db):
    """One project, four merge requests, and the collaborators whose confirmations
    count.

    Shapes, and why each exists:

      MR1 "Short of quorum"   no confirmations at all: 0 of 2, no reviewer
      MR2 "Needs a reviewer"  two contributor confirmations: quorum met, no reviewer
      MR3 "Ready to land"     a contributor + a reviewer: the gate passes
      MR4 "Already merged"    status merged, closed
      MR5 <img ...>           a title that is markup

    eligible collaborators: three (two contributors, one reviewer), each with a
    project document -- route (1) of qualifyingContributionsSQL. needed is
    min(3, max(2, ceil(0.75))) = 2.
    """
    con = sqlite3.connect(db)
    cur = con.cursor()
    now = time.time()

    def user(username, display=None):
        cur.execute("INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
                    (username, display or username, now))
        return cur.lastrowid

    def document(pid, uid, slug):
        cur.execute(
            "INSERT INTO project_documents (project_id, kind, slug, title, body,"
            " author_id, created_at, updated_at) VALUES (?,?,?,?,'',?,?,?)",
            (pid, "readme", slug, slug, uid, now, now))

    owner = user("merge-owner")
    author = user("merge-author")
    contributor = user("merge-contrib")
    reviewer = user("merge-reviewer")

    cur.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " visibility, created_at, updated_at)"
        " VALUES (?,?,?,'collective','MIT','public',?,?)",
        ("merge-e2e", "Merge E2E", "Fixture project", now, now))
    pid = cur.lastrowid
    for uid, role in ((owner, "owner"), (author, "contributor"), (contributor, "contributor"),
                      (reviewer, "reviewer")):
        cur.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                    (pid, uid, role, now))
    # Only the three who are NOT the maintainer have a qualifying contribution, so
    # the eligible population is exactly the people the gate measures against.
    document(pid, contributor, "contrib-notes")
    document(pid, reviewer, "reviewer-notes")
    document(pid, author, "author-notes")

    def feature(title):
        cur.execute(
            "INSERT INTO features (project_id, author_id, title, body, effort, status,"
            " created_at, updated_at) VALUES (?,?,?,'','M','in_progress',?,?)",
            (pid, owner, title, now, now))
        return cur.lastrowid

    def merge_request(title, status="open", approvals=(), closed=None):
        cur.execute(
            "INSERT INTO merge_requests (project_id, feature_id, author_id, title,"
            " external_ref, status, opened_at, closed_at) VALUES (?,?,?,?,?,?,?,?)",
            (pid, feature(title), author, title, "refs/heads/fixture", status, now, closed))
        mr = cur.lastrowid
        for uid in approvals:
            cur.execute(
                "INSERT INTO merge_approvals (mr_id, user_id, weight, created_at)"
                " VALUES (?,?,1.0,?)", (mr, uid, now))
        return mr

    merge_request("Short of quorum")
    merge_request("Needs a reviewer", approvals=(contributor,))
    merge_request("Ready to land", approvals=(contributor, reviewer))
    merge_request("Already merged", status="merged", approvals=(contributor, reviewer),
                  closed=now)
    merge_request(HOSTILE_TITLE)

    con.commit()
    con.close()


def open_merge(page):
    """Load the page and wait for the queue to hydrate.

    The wait is on the table rather than on load: the shell paints instantly and
    fills in after two fetches, so a bare goto would race them.
    """
    page.goto(f"{BASE}/projects/merge-e2e/merge")
    page.wait_for_selector("#merge-table tbody tr", timeout=15000)
    return page.locator("#merge-root")


def row_for(page, title):
    return page.locator(".merge-row", has_text=title).first


def test_the_page_shows_a_row_per_request_with_the_gate_it_must_meet(owner_page):
    open_merge(owner_page)
    rows = owner_page.locator("#merge-table tbody tr")
    expect(rows).to_have_count(5)

    short = row_for(owner_page, "Short of quorum")
    expect(short.locator(".merge-gate")).to_contain_text("0 of")
    expect(short.locator(".merge-gate")).to_contain_text("short")
    expect(short.locator(".merge-gate")).to_contain_text("a reviewer has not signed off")

    ready = row_for(owner_page, "Ready to land")
    expect(ready.locator(".merge-gate")).to_contain_text("met")
    expect(ready.locator(".merge-gate")).to_contain_text("a reviewer has signed off")

    expect(row_for(owner_page, "Already merged").locator(".merge-status")).to_have_text("merged")


def test_pressing_merge_on_a_short_request_is_refused_in_the_servers_words(owner_page):
    """The refusal is the gate explaining itself, so the sentence has to survive all
    the way to the notice: "gate not met" alone would leave a reader guessing whether
    they need another confirmation or a reviewer."""
    open_merge(owner_page)
    row = row_for(owner_page, "Short of quorum")
    row.locator("[data-execute]").click()
    expect(owner_page.locator("#merge-notice")).to_contain_text("confirmations")
    # And nothing landed: the status is still open after the reload that follows.
    expect(row_for(owner_page, "Short of quorum").locator(".merge-status")).to_have_text("open")


def test_confirming_twice_including_a_reviewer_lands_the_change(browser, server):
    """The other half of a gate: it must open. Two different accounts confirm -- one
    of them a reviewer -- and the owner merges."""
    open_merge_ctx, owner = _signed_in(browser, server["db"], "merge-owner")
    contrib_ctx, contrib = _signed_in(browser, server["db"], "merge-contrib")
    review_ctx, reviewer = _signed_in(browser, server["db"], "merge-reviewer")
    try:
        # Two confirmations, one from a reviewer: MR3 is seeded with both already,
        # so the missing piece is the merge itself.
        open_merge(owner)
        row = row_for(owner, "Ready to land")
        expect(row.locator(".merge-gate")).to_contain_text("met")
        row.locator("[data-execute]").click()
        expect(owner.locator("#merge-notice")).to_contain_text("merged")
        expect(row_for(owner, "Ready to land").locator(".merge-status")).to_have_text("merged")

        # A contributor's confirmation moves the reported count on the row it
        # belongs to: "Short of quorum" goes from none to one, and the gate is still
        # short because a reviewer has not signed off. The count is the SERVER's --
        # approvals is the counted number, and this is what proves the page shows it
        # rather than counting rows it can see.
        open_merge(contrib)
        short = row_for(contrib, "Short of quorum")
        expect(short.locator(".merge-gate")).to_contain_text("0 of 2")
        short.locator("[data-approve]").click()
        expect(contrib.locator("#merge-notice")).to_contain_text("approved")
        expect(row_for(contrib, "Short of quorum").locator(".merge-gate")).to_contain_text(
            "1 of 2")

        # The reviewer's confirmation is the one the charter asks for, and it flips
        # the row to met without anybody recomputing anything client-side.
        open_merge(reviewer)
        expect(row_for(reviewer, "Short of quorum").locator(".merge-gate")).to_contain_text(
            "a reviewer has not signed off")
        row_for(reviewer, "Short of quorum").locator("[data-approve]").click()
        expect(reviewer.locator("#merge-notice")).to_contain_text("approved")
        met = row_for(reviewer, "Short of quorum").locator(".merge-gate")
        expect(met).to_contain_text("met")
        expect(met).to_contain_text("2 of 2")
        expect(met).to_contain_text("a reviewer has signed off")

        # And the gate that the two of them filled through the UI opens.
        open_merge(owner)
        row_for(owner, "Short of quorum").locator("[data-execute]").click()
        expect(owner.locator("#merge-notice")).to_contain_text("merged")
        expect(row_for(owner, "Short of quorum").locator(".merge-status")).to_have_text("merged")
    finally:
        for ctx in (open_merge_ctx, contrib_ctx, review_ctx):
            ctx.close()


def test_a_signed_out_visitor_reads_the_gate_and_is_offered_no_controls(browser, server):
    ctx = _context(browser, None)
    pg = ctx.new_page()
    try:
        open_merge(pg)
        expect(pg.locator("#merge-table tbody tr")).to_have_count(5)
        expect(pg.locator("[data-approve]")).to_have_count(0)
        expect(pg.locator("[data-execute]")).to_have_count(0)
        expect(pg.locator("#merge-root")).to_contain_text("sign in to confirm")
    finally:
        ctx.close()


def test_the_hostile_title_renders_as_text(owner_page):
    open_merge(owner_page)
    cell = row_for(owner_page, "img src=x").locator(".merge-title")
    expect(cell).to_contain_text("<img")
    # The payload never executed: a title that reached innerHTML unescaped would have
    # fired onerror and set the flag.
    assert owner_page.evaluate("window.__xss") is None


def test_opening_a_merge_request_from_the_page_adds_a_row(owner_page):
    open_merge(owner_page)
    owner_page.select_option("#merge-feature", index=0)
    owner_page.fill("#merge-title", "Land the exporter fix")
    owner_page.click("#merge-create-form button[type=submit]")
    expect(owner_page.locator("#merge-notice")).to_contain_text("open")
    row = row_for(owner_page, "Land the exporter fix")
    expect(row.locator(".merge-status")).to_have_text("open")
    expect(row.locator(".merge-gate")).to_contain_text("0 of")


def test_no_console_errors_on_the_merge_page(owner_page):
    open_merge(owner_page)
    # The state being asked for is "the page has stopped fetching", and that is what
    # networkidle means; a 300ms sleep is a guess at it that gets less reliable the busier
    # the machine is. This suite's own subject is a stray console error, so the wait has to
    # outlast the LAST request, not the first paint.
    owner_page.wait_for_load_state("networkidle")
    assert owner_page.errors == []
