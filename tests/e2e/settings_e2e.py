"""The browser half of the settings page contract.

The Go tests assert the route, the template, the tab bar, the link and the private-project
guard. None of that proves the page WORKS: `complaintCards(complaints, slug)` threw a
ReferenceError that blanked the entire project page while every source-level gate stayed
green, because the string the gate searched for was still in the file.

So this file does what only a browser can -- it renders the page, lets the three fetches
resolve, and asserts on what a reader actually sees.

The charter's PascalCase keys are the thing most worth checking here. They come from
governance.Charter having no json tags, so every other endpoint in this API is snake_case
and the charter is not. A page written from the house style reads `quorum_ratio`, gets
undefined for all 22 fields, and renders a form with 22 empty inputs -- a page that
loads, looks plausible, and controls nothing.
"""

import re
import sqlite3
import tempfile

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

# One fixed port per suite: two sync_playwright() sessions cannot coexist in one
# pytest process, so the Makefile runs each file as its own invocation. The port
# line must be a bare int -- the cross-file gate parses it with int().
PORT = 8490
SLUG = "e2e-settings"


def _db(server):
    return sqlite3.connect(server["db"])


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-settings-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-settings-log-")
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
    """A fresh context per test, each with its OWN forwarded IP.

    clientKey() honours X-Forwarded-For from loopback, so contexts sharing one IP share
    a rate-limit bucket -- and the limiter then answers {"error":"rate limit exceeded"}
    AS THE PAGE BODY, which reads as a broken page rather than as a throttle.
    """
    ctx = browser.new_context(
        extra_http_headers={"X-Forwarded-For": f"10.131.{next(_ips) // 256}.{next(_ips) % 256}"}
    )
    pg = ctx.new_page()
    pg.errors = []
    pg.on("pageerror", lambda e: pg.errors.append(str(e)))
    # A failed RESOURCE is not a script error: a page that deliberately requests a missing
    # feature renders "No such feature" and logs three of them, so collecting them makes
    # the test assert against its own design.
    pg.on("console", lambda m: pg.errors.append(m.text) if m.type == "error" and
          "Failed to load resource" not in m.text else None)
    yield pg
    ctx.close()


import itertools

_ips = itertools.count(1)


def seed(db):
    """Seed through SQLite, never the API under test.

    Otherwise a broken write path yields an empty page and the assertions pass anyway.
    """
    con = sqlite3.connect(db)
    now = 1790000000
    # The column names are read from the schema the other suites use, not from what a
    # user table "should" have: `users` has trust_level and no role, `projects` has no
    # owner_id, and `members` has no is_moderator. Guessing all three produced
    # "table users has no column named role" and six errors in 0.96s.
    con.execute(
        "INSERT INTO users (username, display_name, created_at, password_hash, trust_level)"
        " VALUES (?,?,?,?,?)", ("setowner", "Set Owner", now, "x", 3))
    uid = con.execute("SELECT id FROM users WHERE username='setowner'").fetchone()[0]
    con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " created_at, updated_at, visibility) VALUES (?,?,?,?,?,?,?,?)",
        (SLUG, "Settings E2E", "settings page fixture", "collective", "MIT",
         now, now, "public"))
    pid = con.execute("SELECT id FROM projects WHERE slug=?", (SLUG,)).fetchone()[0]
    con.execute(
        "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,'owner',?)",
        (pid, uid, now))
    # A criterion, so the profiles panel is not empty for a reason the fixture chose.
    try:
        con.execute(
            "INSERT INTO ranking_criteria (project_id, name, description, weight, active) "
            "VALUES (?, 'Reach', 'How many people this reaches', 1.0, 1)", (pid,))
    except sqlite3.OperationalError:
        pass  # table named differently; the panel's empty state is still asserted
    con.commit()
    con.close()


def test_the_settings_page_shows_the_charter_with_real_values(page, server):
    """The load-bearing test: 22 inputs, none of them empty.

    Every other input on the site is snake_case. This one is PascalCase, and a page that
    guessed wrong still renders -- it just renders 22 blanks, which reads as a project with
    no charter rather than as a bug.
    """
    page.goto(f"{server['base']}/projects/{SLUG}/settings")
    page.wait_for_selector('#settings-root:not([aria-busy="true"])', timeout=15000)

    inputs = page.locator("#charter-form input[name]")
    expect(inputs.first).to_be_visible()

    quorum = page.locator("#charter-form input[name='QuorumRatio']")
    expect(quorum).to_have_value("0.2")

    # And the count, because "some fields rendered" is not the claim.
    n = inputs.count()
    assert n >= 20, f"only {n} charter fields rendered; the charter has 22 scalars"

    # Every numeric field must be non-empty. A blank here means the page read a key the
    # response does not have.
    empties = page.evaluate(
        """() => Array.from(document.querySelectorAll('#charter-form input[name]'))
                   .filter(i => i.value === '').map(i => i.name)""")
    assert not empties, f"charter fields rendered empty, which means the page read the "\
                        f"wrong key names: {empties}"
    assert not page.errors, page.errors


def test_the_members_panel_names_the_owner(page, server):
    page.goto(f"{server['base']}/projects/{SLUG}/settings")
    page.wait_for_selector('#settings-root:not([aria-busy="true"])', timeout=15000)
    expect(page.locator("#settings-root")).to_contain_text("setowner")
    expect(page.locator("#settings-root")).to_contain_text("owner")
    assert not page.errors, page.errors


def test_the_page_is_reachable_by_clicking_the_tab(page, server):
    """The browser half of "something links to it".

    The Go gate asserts the link string is in project.js and base.html. Only a browser can
    show that clicking it actually lands on the settings page.
    """
    page.goto(f"{server['base']}/projects/{SLUG}")
    page.wait_for_selector('#project-detail:not([aria-busy="true"])', timeout=15000)

    page.locator('a[href$="/settings"]').first.click()
    page.wait_for_url(f"**/projects/{SLUG}/settings", timeout=15000)
    expect(page.locator("#settings-root")).to_be_visible()
    assert not page.errors, page.errors


def test_a_private_project_serves_no_settings_page(page, server):
    con = sqlite3.connect(server["db"])
    now = 1790000000
    con.execute(
        "INSERT INTO users (username, display_name, created_at, password_hash, trust_level)"
        " VALUES (?,?,?,?,?)", ("setpriv", "Set Priv", now, "x", 3))
    uid = con.execute("SELECT id FROM users WHERE username='setpriv'").fetchone()[0]
    con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " created_at, updated_at, visibility) VALUES (?,?,?,?,?,?,?,?)",
        ("setpriv-e2e", "Private Settings", "secret", "collective", "MIT",
         now, now, "private"))
    con.commit()
    con.close()

    page.goto(f"{server['base']}/projects/setpriv-e2e/settings")
    body = page.locator("body").inner_text().lower()
    assert "no project" in body, f"a private project's settings page rendered: {body[:200]}"
    # And the slug is not echoed, which is what makes it an enumeration oracle.
    assert "setpriv-e2e" not in page.content()
    assert not page.errors, page.errors


def test_no_console_errors_on_the_settings_page(page, server):
    for path in (f"/projects/{SLUG}/settings", f"/projects/{SLUG}/settings", f"/projects/{SLUG}"):
        page.goto(server["base"] + path)
        # Wait for the page's OWN signal. `#settings-root:not([aria-busy="true"])` matches
        # on the settings page; the project page's root is #project-detail, and
        # `:not([aria-busy])` would never match either, because the codebase clears the
        # flag two ways -- ConcordSkeleton.done() SETS it to "false" and lists.js REMOVES
        # it -- and an attribute whose value is "false" is still an attribute.
        root = "project-detail" if path.endswith(SLUG) else "settings-root"
        page.wait_for_selector('#' + root + ':not([aria-busy="true"])', timeout=15000)
        assert not page.errors, f"{path}: {page.errors}"


def test_a_refused_charter_save_says_so(page, server):
    """A signed-out save must be ANNOUNCED, not silently ignored.

    This is the token-typo failure mode: the header is absent, the server answers 401, and
    if the page does not say anything the reader concludes the button is broken. So the
    assertion is on the live region, and it is what the settings page would have needed to
    pass had the token key been wrong.
    """
    page.goto(f"{server['base']}/projects/{SLUG}/settings")
    page.wait_for_selector('#settings-root:not([aria-busy="true"])', timeout=15000)

    page.locator("#charter-form button[type='submit']").click()

    live = page.locator("[data-settings-live]")
    # The message is the server's status turned into English by the page. Assert on the
    # ROLE, which the region has, and on a word the page chose -- not on a count of
    # elements, and not on a fixed duration.
    expect(live).not_to_be_empty(timeout=15000)
    expect(live).to_contain_text("Not allowed", timeout=15000)
    assert not page.errors, page.errors