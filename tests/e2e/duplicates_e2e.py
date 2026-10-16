"""The browser half of §6.1's duplicate-detection panel.

§6.1: "Duplicate detection at filing time uses semantic similarity BEFORE submit,
and also searches *other projects*."

The endpoint shipped complete -- cross-project scope, embedding coverage, per-model
thresholds -- and with no caller at all. `grep -rin similar` over assets/js,
templates/ and tests/ returned the route registration and nothing else, so the
filing-time half of §6.1 did not exist: a filer was refused AFTER pressing submit,
which is the wrong end of the form.

Nothing in the Go layer can prove a panel works. A ReferenceError parses perfectly
and blanks the page, so the Go tests assert the source (the fetch exists, the link
comes from the response's url field, the title is escaped) and this file asserts
what a filer actually sees.

Two things are worth more than the happy path here:

  * the LINK the panel renders must open. The endpoint used to hand out
    `/api/v1/features/222`, a 404, for every match it returned -- a wrong URL that
    nothing could notice, because nothing followed one. This follows one.
  * the panel must ask about the right KIND. A hardcoded 'complaint' renders a
    plausible list on the feature form and means nothing.
"""

import itertools
import re
import sqlite3
import tempfile

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

# One fixed port per suite; the cross-file gate parses this line with int().
PORT = 8491
SLUG = "e2e-duplicates"
BASE = f"http://127.0.0.1:{PORT}"
EXISTING_TITLE = "The exporter silently drops the final row"

_ips = itertools.count(1)


def _db(server):
    return sqlite3.connect(server["db"])


def _member_token(db, username):
    """Register over HTTP (so the token is real) and make them a member by SQL.

    There is no members WRITE route -- server.go registers GET .../members and
    nothing else -- so the membership has to come from the database, which is what
    board_e2e.py documents at length.
    """
    reg = harness.api(BASE, "POST", "/api/v1/auth/register", {
        "username": username, "password": "correct-horse-battery", "display_name": username,
    })
    assert reg, "register returned nothing"
    token = reg.get("token")
    assert token, f"register returned no token: {reg}"
    uid = reg.get("user", {}).get("id") or reg.get("user_id") or reg.get("id")
    assert uid, f"register returned no user id: {reg}"

    con = sqlite3.connect(db)
    try:
        con.execute(
            "INSERT INTO members (project_id, user_id, role, joined_at)"
            " SELECT id, ?, 'maintainer', 1790000000 FROM projects"
            " WHERE id NOT IN (SELECT project_id FROM members WHERE user_id = ?)",
            (uid, uid))
        con.commit()
    finally:
        con.close()
    return token


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-dups-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-dups-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    seed(db)

    # The complaint the panel is supposed to find, filed ONCE per session over the
    # API rather than by SQL -- deliberately, and the reason is narrow: it needs an
    # EMBEDDING, and only the write path produces one. A SQL-seeded row would make
    # the panel answer "nothing similar" for a reason the fixture chose.
    #
    # Once, not per test: the second filing of an identical title is answered 409 by
    # the very feature under test, and the first version of this fixture filed it in
    # the per-test fixture -- so the first test passed and the other four errored in
    # setup with "HTTP Error 409", which reads as a broken suite rather than as the
    # duplicate detection working.
    tok = _member_token(db, "dup-seeder")
    harness.api(BASE, "POST", f"/api/v1/projects/{SLUG}/complaints", {
        "project_id": 1, "title": EXISTING_TITLE,
        "body": "every export loses exactly one row", "severity": 4,
    }, token=tok)

    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


@pytest.fixture()
def page(browser, server):
    """A SIGNED-IN page, because the filing forms do not render otherwise.

    The complaint and feature forms are inside `forms(p)`, which returns "Sign in
    to take part" for an anonymous visitor -- so an anonymous version of this suite
    would assert on markup that is not on the page, which reads as a broken panel.
    """
    ctx = browser.new_context(
        extra_http_headers={"X-Forwarded-For": f"10.94.{next(_ips) // 256}.{next(_ips) % 256}"}
    )
    pg = ctx.new_page()
    pg.errors = []
    pg.on("pageerror", lambda e: pg.errors.append("PAGEERROR: " + str(e)))
    pg.on("console", lambda m: pg.errors.append("CONSOLE: " + m.text)
          if m.type == "error" and "Failed to load resource" not in m.text else None)
    pg.goto(BASE + "/")

    token = _member_token(server["db"], f"dup-filer-{next(_ips)}")
    pg.evaluate("t => window.localStorage.setItem('concord.token', t)", token)
    return pg


def seed(db):
    """The project and its owner, through SQLite, before the server sees traffic."""
    now = 1790000000
    con = sqlite3.connect(db)
    con.execute(
        "INSERT INTO users (username, display_name, created_at, password_hash, trust_level)"
        " VALUES (?,?,?,?,?)", ("dupowner", "Dup Owner", now, "x", 3))
    uid = con.execute("SELECT id FROM users WHERE username='dupowner'").fetchone()[0]
    con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " created_at, updated_at, visibility) VALUES (?,?,?,?,?,?,?,?)",
        (SLUG, "Duplicates E2E", "duplicate panel fixture", "collective", "MIT",
         now, now, "public"))
    pid = con.execute("SELECT id FROM projects WHERE slug=?", (SLUG,)).fetchone()[0]
    con.execute(
        "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,'owner',?)",
        (pid, uid, now))
    con.commit()
    con.close()


def open_project(page, server):
    page.goto(f"{server['base']}/projects/{SLUG}")
    page.wait_for_selector('#project-detail:not([aria-busy="true"])', timeout=15000)


def test_the_panel_names_the_existing_complaint_and_links_to_it(page, server):
    open_project(page, server)

    form = page.locator("#complaint-form")
    form.locator("input[name='title']").fill(EXISTING_TITLE)

    panel = form.locator("[data-dup-panel]")
    # The strong-duplicate wording, not merely "some text appeared": at a score of
    # ~1.0 the panel says so, and that sentence is the feature.
    expect(panel).to_contain_text("looks like an existing complaint", timeout=15000)

    link = panel.locator("a")
    expect(link.first).to_be_visible()
    expect(link.first).to_contain_text(EXISTING_TITLE)
    href = link.first.get_attribute("href")
    assert href and href.startswith("/projects/"), (
        f"the panel's link is not a page route: {href!r}. The API used to hand out "
        f"/api/v1/features/222, which is a 404.")
    assert re.search(r"/complaints/\d+$", href), href

    # And it opens. This is the assertion that would have caught the 404, and no
    # Go test can make it: it needs a browser to follow a link.
    page.goto(server["base"] + href)
    expect(page.locator("body")).to_contain_text(EXISTING_TITLE)
    assert not page.errors, page.errors


def test_the_feature_form_asks_about_features_not_complaints(page, server):
    """The kind must come from the form, not from a constant.

    Filling the FEATURE form with the existing COMPLAINT's title must not produce a
    complaint match. A hardcoded 'complaint' would render a confident, correct-looking
    answer on the wrong corpus -- which is the same shape as every other defect this
    project has recorded: a page that reports the truth about something it failed to
    load.
    """
    open_project(page, server)

    form = page.locator("#feature-form")
    form.locator("input[name='title']").fill(EXISTING_TITLE)

    panel = form.locator("[data-dup-panel]")
    expect(panel).not_to_be_empty(timeout=15000)
    text = panel.inner_text()
    assert "feature" in text or "indexed" in text, (
        f"the feature form's panel says something that does not mention features: {text!r}")
    assert EXISTING_TITLE not in text, (
        f"the feature form matched a COMPLAINT: the kind is hardcoded. {text!r}")
    assert not page.errors, page.errors


def test_the_panel_makes_no_claim_before_a_title_is_worth_matching(page, server):
    """Three characters is not a title, and "nothing similar" would be a lie.

    Asserted on the REQUEST, not only on the text: the panel must not have asked.
    """
    open_project(page, server)

    asked = []
    page.on("request", lambda r: asked.append(r.url) if "/api/v1/similar/" in r.url else None)

    form = page.locator("#complaint-form")
    form.locator("input[name='title']").fill("abc")
    # BOUNDED (justified): this asserts the ABSENCE of a request and of a claim.
    # There is no state to wait for -- a request that should never arrive has no
    # completion to synchronise on -- so a bounded wait is the only way to give the
    # unwanted thing time to appear.
    #
    # The comment was BELOW the call until now, and the e2e-sync gate reported the sleep
    # as idle because that is where its justification rule looks. The gate was right about
    # the file and wrong about the reason: the reasoning existed and was invisible to the
    # only reader that checks it.
    page.wait_for_timeout(1200)

    assert not asked, f"the panel queried the server for a 3-character title: {asked}"
    assert form.locator("[data-dup-panel]").inner_text().strip() == "", (
        "the panel rendered a claim about a three-character title")
    assert not page.errors, page.errors


def test_the_panel_survives_a_title_that_changes_while_it_is_looking(page, server):
    """Two lookups in flight must not land out of order.

    The panel is debounced, so typing one title, pausing, then extending it leaves
    two requests open; without the sequence guard the first answer can arrive last
    and describe a title the filer has moved on from. Asserted on the END state: the
    panel must describe what is in the box, whoever asked first.
    """
    open_project(page, server)

    form = page.locator("#complaint-form")
    title = form.locator("input[name='title']")
    panel = form.locator("[data-dup-panel]")

    title.fill(EXISTING_TITLE)
    expect(panel).to_contain_text("looks like an existing complaint", timeout=15000)

    title.fill(EXISTING_TITLE + ", on the nightly build")
    expect(panel).not_to_be_empty(timeout=15000)
    expect(panel).to_contain_text("looks like an existing complaint", timeout=15000)
    assert not page.errors, page.errors


def test_no_console_errors_on_the_project_page_with_the_panel_present(page, server):
    open_project(page, server)
    page.locator("#complaint-form input[name='title']").fill(EXISTING_TITLE)
    expect(page.locator("#complaint-form [data-dup-panel]")).not_to_be_empty(timeout=15000)
    assert not page.errors, page.errors
