"""The inbox in a browser (docs/specs/notification-spec.md).

Three claims that no Go test can make, because all three are about what a page does after the
server has answered:

  * The header badge is on the FIRST PAINT. It is server-rendered from pageFor, so the assertion
    is on the HTML the browser received before any script ran -- a client-side count would pass
    the Go tests and be absent here.
  * Reading the page marks nothing read. The card still says unread after a reload, because
    "mark read" is a control, not a side effect of looking.
  * Pressing "Mark read" clears the card's dot AND the header numeral in one action, without a
    full page load. The numeral is server-rendered, so the only correct behaviour after a
    client-side mark is a reload -- and this test holds the page to it.

THE NOTIFICATION IS SEEDED THROUGH THE REAL STORE PATH in the fixture: a feature in an arena,
removed by a second account. An INSERT would have tested the page against a row shape this
suite invented.
"""

import itertools
import sqlite3
import tempfile

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

PORT = 8428
BASE = f"http://127.0.0.1:{PORT}"

SLUG = "inbox-demo"
PASSWORD = "correct-horse-battery"
AUTHOR = "inbox-author"
MODERATOR = "inbox-moderator"

_ips = itertools.count(1)


def _register(username):
    reg = harness.api(BASE, "POST", "/api/v1/auth/register", {
        "username": username, "password": PASSWORD, "display_name": username}) or {}
    user = reg.get("user") or {}
    return reg["token"], user.get("id") or reg.get("user_id")


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-inbox-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-inbox-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    seed(db)
    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


def seed(db):
    """A project, its author, a moderator, and one feature removed from an arena."""
    now = 1790000000
    author_token, author_uid = _register(AUTHOR)
    moderator_token, moderator_uid = _register(MODERATOR)

    con = sqlite3.connect(db)
    try:
        pid = con.execute(
            "INSERT INTO projects (slug, name, description, governance_model, license,"
            " created_at, updated_at, visibility) VALUES (?,?,?,'collective','MIT',?,?,'public')",
            (SLUG, "Inbox Demo", "fixture", now, now)).lastrowid
        for uid, role in ((author_uid, "contributor"), (moderator_uid, "maintainer")):
            con.execute(
                "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (pid, uid, role, now))
        con.execute("UPDATE users SET trust_level = 1 WHERE id = ?", (moderator_uid,))

        fid = con.execute(
            "INSERT INTO features (project_id, author_id, title, body, effort, status,"
            " created_at, updated_at) VALUES (?,?,?,'a body worth removing','M','draft',?,?)",
            (pid, author_uid, "Make the exporter complete", now, now)).lastrowid
        arena = con.execute(
            "INSERT INTO arenas (type, project_id, feature_id, question, use_case, created_at)"
            " VALUES ('feature-priority',?,0,'Which idea should we build next?','fixture',?)",
            (pid, now)).lastrowid
        con.execute(
            "INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd, sigma, games,"
            " is_baseline, parent_entry_id, updated_at)"
            " VALUES (?,'feature',?,1500.0,350.0,0.06,0,0,NULL,?)", (arena, fid, now))
        # The decision, through the table the store writes: the fixture cannot call Go, and the
        # row shape is the migration's, so one INSERT here is honest as long as it is the shape
        # the store produces (kind, subject, copied title, reason, actor).
        con.execute(
            "INSERT INTO notifications (user_id, kind, project_id, subject_id, title, reason,"
            " actor_id, created_at, read_at) VALUES (?,?,?,?,?,?,?,?,NULL)",
            (author_uid, "feature_removed", pid, fid, "Make the exporter complete",
             "duplicate of #12", moderator_uid, now))
        con.commit()
    finally:
        con.close()

    AUTHOR_TOKEN[0] = author_token
    MODERATOR_TOKEN[0] = moderator_token


AUTHOR_TOKEN = [None]
MODERATOR_TOKEN = [None]


def _page(browser, token):
    ctx = browser.new_context(
        extra_http_headers={"X-Forwarded-For": f"10.96.{next(_ips) % 250}.{next(_ips) % 250}"})
    # The navigation cookie is what lets the SERVER know the reader on a page request; without
    # it the badge is absent no matter what the token says, which is the trap that made an
    # earlier suite conclude a feature was missing.
    ctx.add_cookies([{"name": "concord_nav", "value": token, "url": BASE + "/"}])
    pg = ctx.new_page()
    pg.errors = []
    pg.on("pageerror", lambda e: pg.errors.append("PAGEERROR: " + str(e)))
    pg.goto(BASE + "/")
    pg.evaluate("t => window.localStorage.setItem('concord.token', t)", token)
    return pg


def test_the_header_shows_the_unread_count_before_any_script_runs(server):
    """The numeral is in the HTML the server sent -- asserted on the raw response body."""
    import urllib.request
    req = urllib.request.Request(BASE + "/projects", headers={
        "Cookie": "concord_nav=" + (AUTHOR_TOKEN[0] or ""), "X-Forwarded-For": "10.95.1.1"})
    body = urllib.request.urlopen(req, timeout=15).read().decode()
    assert 'data-unread-count="1"' in body, "the first paint has no unread count"
    assert ">1</span>" in body, "the count is not rendered as a numeral"


def test_reading_the_page_marks_nothing_read(server, browser):
    pg = _page(browser, AUTHOR_TOKEN[0])
    pg.goto(BASE + "/notifications")
    expect(pg.locator("#notifications-root")).to_have_attribute("aria-busy", "false", timeout=15000)
    expect(pg.locator(".notif-card")).to_have_count(1)
    expect(pg.locator(".notif-card.notif-unread")).to_have_count(1)
    expect(pg.locator(".notif-title")).to_have_text("Make the exporter complete")

    # Reload: still unread. Loading the page is not reading it.
    pg.reload()
    expect(pg.locator("#notifications-root")).to_have_attribute("aria-busy", "false", timeout=15000)
    expect(pg.locator(".notif-card.notif-unread")).to_have_count(1)
    assert not pg.errors, pg.errors


def test_marking_read_clears_the_card_and_the_header(server, browser):
    pg = _page(browser, AUTHOR_TOKEN[0])
    pg.goto(BASE + "/notifications")
    expect(pg.locator(".notif-card")).to_have_count(1)

    pg.locator("[data-read]").first.click()
    # The card loses its unread class and its dot, and the toolbar's count goes away.
    expect(pg.locator(".notif-card.notif-unread")).to_have_count(0, timeout=15000)
    expect(pg.locator(".notif-dot")).to_have_count(0)
    expect(pg.locator("[data-unread]")).to_have_count(0)

    # The header numeral is server-rendered, so the honest check is a reload: it must be gone.
    pg.reload()
    expect(pg.locator(".notif-card")).to_have_count(1)
    expect(pg.locator(".notif-card.notif-unread")).to_have_count(0)
    expect(pg.locator("[data-unread-count]")).to_have_count(0)
    assert not pg.errors, pg.errors


def test_a_moderator_sees_nothing_of_another_readers_inbox(server, browser):
    pg = _page(browser, MODERATOR_TOKEN[0])
    pg.goto(BASE + "/notifications")
    expect(pg.locator("#notifications-root")).to_have_attribute("aria-busy", "false", timeout=15000)
    # The empty state is a statement about THIS reader's record, not about the feature.
    expect(pg.locator(".empty-state-title")).to_contain_text(
        "Nothing has been removed from your contributions")
    expect(pg.locator(".notif-card")).to_have_count(0)
    assert not pg.errors, pg.errors
