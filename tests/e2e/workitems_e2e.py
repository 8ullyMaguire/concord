"""§4.5.7's work items, driven in a real browser.

Prerequisites:  make build
Run:            pytest tests/e2e/workitems_e2e.py -v

What this proves that no other layer does:

  - the Go API tests prove `GET /work-items` answers and that a claim 409s;
  - the store tests prove the partial unique index refuses a second live claim;
  - the reader-coverage gate proves `project.js` CONTAINS an anchor.

None of them proves the panel reaches the page. That gap has a history here
(`finder.html` shipped unregistered; `finder.js` called show() without render()),
and it is sharper for this feature than most: a work-item panel that renders no
cards looks exactly like a project with no work.

So these tests read the RENDERED DOM, and they assert the distinctions the schema
exists to make: a sized task vs an unsized one, a claimed task vs a free one, a
complaint needing validation vs a feature needing implementation, and a draft that
must NOT be offered as work somebody can pick up.

Each test gets its OWN project. The server is session-scoped, so a claim written by
one test is visible to every later one -- a stranger test sharing a project with the
claim test would find the button gone and conclude "the control is missing", which
would be a conclusion about the fixture rather than about the product.
"""

import base64
import hashlib
import sqlite3
import tempfile
import time

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

PORT = 8497
BASE = f"http://127.0.0.1:{PORT}"


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-workitems-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-workitems-e2e-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    seed(db)
    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


# The rate limiter buckets by client key at 100 requests/minute and honours
# X-Forwarded-For from loopback, so every test sharing 127.0.0.1 shares one bucket.
# Each test below opens a page that makes ~10 requests; without a per-test address
# the suite starts failing partway through with `{"error":"rate limit exceeded"}`
# rendered as the page body.
_xff_counter = {"n": 0}


@pytest.fixture()
def page(browser):
    _xff_counter["n"] += 1
    n = _xff_counter["n"]
    ip = f"10.44.{n // 200}.{n % 200}"
    ctx = browser.new_context(extra_http_headers={"X-Forwarded-For": ip})
    pg = ctx.new_page()
    # A TypeError in project.js renders an empty div, and every assertion below
    # would then fail with a message pointing at the data instead of the script.
    pg.errors = []
    pg.on("pageerror", lambda e: pg.errors.append(str(e)))
    pg.on("console", lambda m: pg.errors.append(m.text) if m.type == "error" else None)
    yield pg
    ctx.close()


def seed(db):
    """Four projects, one per test, all the same shape.

    The shape is the point: a project that has an open complaint, a sized ready
    feature, a CLAIMED ready feature, and a draft. Every distinction the panel has
    to draw is present in one project, so a panel that draws them all the same
    fails here rather than in front of a contributor.
    """
    con = sqlite3.connect(db)
    cur = con.cursor()
    now = time.time()

    def user(username):
        cur.execute(
            "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
            (username, username, now))
        return cur.lastrowid

    owner = user("wi-owner")
    member = user("wi-member")
    stranger = user("wi-stranger")

    def project(slug, with_claim=True):
        cur.execute(
            "INSERT INTO projects (slug, name, description, governance_model,"
            " license, visibility, created_at, updated_at)"
            " VALUES (?,?,?,'collective','MIT','public',?,?)",
            (slug, slug.replace("-", " ").title(), f"Fixture project {slug}", now, now))
        pid = cur.lastrowid
        for uid, role in ((owner, "owner"), (member, "contributor")):
            cur.execute(
                "INSERT INTO members (project_id, user_id, role, joined_at)"
                " VALUES (?,?,?,?)", (pid, uid, role, now))
        # The complaint needing validation.
        cur.execute(
            "INSERT INTO complaints (project_id, author_id, title, body, severity,"
            " frequency, strategic_multiplier, status, created_at, updated_at)"
            " VALUES (?,?,?,?,3,2.0,1.0,'open',?,?)",
            (pid, owner, "Export drops the last row", "one row short", now, now))
        # The sized, unclaimed ready feature: 60 minutes, S, mentor offered.
        cur.execute(
            "INSERT INTO features (project_id, author_id, title, body, effort, status,"
            " difficulty, time_budget_minutes, needs_mentor, created_at, updated_at)"
            " VALUES (?,?,?,?,'S','ready','S',60,1,?,?)",
            (pid, owner, "Make the export complete", "keep the last row", now, now))
        free_id = cur.lastrowid
        if with_claim:
            # A second ready feature, already claimed by the contributor, so the
            # panel has to distinguish "yours to take" from "somebody's".
            cur.execute(
                "INSERT INTO features (project_id, author_id, title, body, effort, status,"
                " created_at, updated_at)"
                " VALUES (?,?,?,?,'M','ready',?,?)",
                (pid, owner, "Speed up the importer", "it is slow", now, now))
            claimed_id = cur.lastrowid
            cur.execute(
                "INSERT INTO work_item_claims (feature_id, user_id, claimed_at)"
                " VALUES (?,?,?)", (claimed_id, member, now))
        # A draft, which is work nobody has agreed to do and must never be offered.
        cur.execute(
            "INSERT INTO features (project_id, author_id, title, body, effort, status,"
            " created_at, updated_at)"
            " VALUES (?,?,?,?,'M','draft',?,?)",
            (pid, owner, "Only a proposal", "not agreed yet", now, now))
        return pid, free_id

    project("workitems-read")
    project("workitems-claim")
    project("workitems-stranger")
    project("workitems-label")

    con.commit()
    con.close()


def _mint_token(db, username):
    """A token row for `username`, the way the login handler would write one.

    The fixture seeds users with sqlite and no password, so there is no login to
    perform. Two details are the server's, and both cost a debugging round when
    guessed wrong: the digest is base64 RawStd of sha256 (NOT hex, whatever the
    0001 comment says), and ResolveToken rejects a value that does not start with
    `clt_` before it looks anything up.
    """
    raw = "clt_workitems-" + username
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


def _page_as(browser, server, slug, username, ip):
    """A browser context signed in as `username`, with both credentials.

    TWO are needed and neither is optional: the concord_nav cookie for the
    NAVIGATION (the page's HTML is fetched before any script runs, and the session
    lives in localStorage), and the token in localStorage for the page's OWN
    fetches (the cookie is deliberately not an API credential).
    """
    ctx = browser.new_context(extra_http_headers={"X-Forwarded-For": ip})
    pg = ctx.new_page()
    pg.errors = []
    pg.on("pageerror", lambda e: pg.errors.append(str(e)))
    if username:
        token = _mint_token(server["db"], username)
        pg.add_init_script("localStorage.setItem('concord.token', %r);" % token)
        ctx.add_cookies([{"name": "concord_nav", "value": token, "url": BASE + "/"}])
    pg.goto(f"{BASE}/projects/{slug}")
    # The panel is the last thing the page renders, so waiting for a work-item card
    # is waiting for the render to have finished rather than for a timer.
    pg.wait_for_selector('[data-work-item]', timeout=15000)
    return ctx, pg


def _item(pg, title):
    return pg.locator("[data-work-item]", has_text=title).first


def test_the_panel_draws_the_distinctions(browser, server):
    """Anonymous, because the read is public: a work list behind a login cannot
    answer "should I sign up" for the person asking it."""
    ctx, pg = _page_as(browser, server, "workitems-read", None, "10.44.1.1")
    try:
        sized = _item(pg, "Make the export complete")
        expect(sized.locator(".badge", has_text="60 min")).to_be_visible()
        expect(sized.locator(".badge", has_text="difficulty S")).to_be_visible()
        expect(sized.locator(".badge", has_text="mentor offered")).to_be_visible()

        # The complaint is a work item of a DIFFERENT kind, and the panel must say so:
        # "needs validating" is a fifteen-minute job, "ready" is an implementation.
        complaint = _item(pg, "Export drops the last row")
        expect(complaint.locator(".badge", has_text="needs validating")).to_be_visible()

        # Somebody else's work is labelled and offers no control.
        claimed = _item(pg, "Speed up the importer")
        expect(claimed.locator("text=claimed by another contributor")).to_be_visible()
        expect(claimed.locator("[data-claim]")).to_have_count(0)

        # The draft is not work anybody can pick up. Scoped to the PANEL: the same title
        # correctly appears in the Features list above, so a page-wide "does not contain"
        # would fail on the feature list rather than on the panel.
        expect(pg.locator('[data-work-item]:has-text("Only a proposal")')).to_have_count(0)

        # Ordering: the complaint leads (it is the cheapest real contribution), and
        # the 60-minute feature precedes the unsized one.
        titles = pg.locator("[data-work-item] .card-title").all_inner_texts()
        assert titles[0] == "Export drops the last row", titles
        assert titles.index("Make the export complete") < titles.index("Speed up the importer"), titles

        # Signed out: the claim needs an account, so the panel offers a way IN rather than
        # a button whose only possible answer is 401.
        expect(sized.locator("[data-claim]")).to_have_count(0)
        expect(sized.locator('a[href="/login"]')).to_be_visible()
        assert not pg.errors, pg.errors
    finally:
        ctx.close()


def test_a_member_claims_a_feature_from_the_panel(browser, server):
    """The write half, and the reason the panel exists: a claim an API offers and no
    page exposes is a feature only its author can use."""
    ctx, pg = _page_as(browser, server, "workitems-claim", "wi-member", "10.44.2.1")
    try:
        item = _item(pg, "Make the export complete")
        btn = item.locator("[data-claim]")
        expect(btn).to_be_visible()
        btn.click()
        expect(btn).to_have_text("Claimed", timeout=10000)

        # The claim is in the database, read directly: the button's own text is the
        # page's claim about the write, and this is the write.
        con = sqlite3.connect(server["db"])
        live = con.execute(
            "SELECT COUNT(*) FROM work_item_claims wc JOIN features f ON f.id = wc.feature_id"
            " WHERE f.title = ? AND wc.released_at IS NULL",
            ("Make the export complete",)).fetchone()[0]
        con.close()
        assert live == 1, live
        assert not pg.errors, pg.errors
    finally:
        ctx.close()


def test_a_stranger_is_told_why_they_cannot_claim(browser, server):
    """Signed in, not a member. The refusal must be a sentence, not a dead button:
    a control that does nothing is indistinguishable from a broken page."""
    ctx, pg = _page_as(browser, server, "workitems-stranger", "wi-stranger", "10.44.3.1")
    try:
        btn = _item(pg, "Make the export complete").locator("[data-claim]")
        btn.click()
        expect(btn).to_have_text("Join the project to claim work", timeout=10000)
        # No JS exception: the 403 is an expected response, not a defect.
        assert not pg.errors, pg.errors
    finally:
        ctx.close()


def test_a_maintainer_sizes_a_task_and_the_panel_shows_it(browser, server):
    """The label form. Refused for everyone else by the server, so the panel renders
    it for everyone -- which is the house rule: a client-side role check is a claim
    the client cannot verify."""
    ctx, pg = _page_as(browser, server, "workitems-label", "wi-owner", "10.44.4.1")
    try:
        form = pg.locator('[data-work-item-form]').first
        form.locator('[name="minutes"]').fill("90")
        form.locator('[name="difficulty"]').select_option("M")
        form.locator('button[type="submit"]').click()
        expect(pg.locator("[data-work-item-status]").first).to_have_text("Saved", timeout=10000)

        # Reloaded, because the panel is rendered from the server's answer: if the
        # write had only updated the DOM, this would still read 60.
        pg.reload()
        pg.wait_for_selector("[data-work-item]", timeout=15000)
        sized = _item(pg, "Make the export complete")
        expect(sized.locator(".badge", has_text="90 min")).to_be_visible()
        expect(sized.locator(".badge", has_text="difficulty M")).to_be_visible()
        assert not pg.errors, pg.errors
    finally:
        ctx.close()
