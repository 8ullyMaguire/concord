"""The cookie notice, guest interactions, and folding (docs/specs/guest-interactions-spec.md).

Three claims, one file:

  1. A visitor who is not signed in can interact -- file a complaint, comment --
     without creating an account.
  2. A cookie notice is shown, and interactions are refused until it is accepted.
  3. What a visitor produced as a guest becomes their account's when they register.

The notice is the reason this file exists separately from the Go tests: the Go suite
proves the SERVER refuses a write with no consent (that is the gate), and only a
browser can show that a visitor is TOLD why. So each test here drives the page and
reads what the page says.

The consent cookie is normally added to every browser context by conftest.py, on the
grounds that a real visitor accepts once. These tests clear it, because they are the
ones about the notice.
"""

import sqlite3
import tempfile
import time

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

PORT = 8494
BASE = f"http://127.0.0.1:{PORT}"

SLUG = "guest-demo"


def seed(db):
    """A project anyone can file against, and a maintainer to own it.

    Written in SQL rather than over HTTP because a fixture that has to register an
    account is a fixture that shares the 100-per-minute limiter with every other
    request in this file, and a 429 in setup reads as a broken page.
    """
    con = sqlite3.connect(db)
    cur = con.cursor()
    now = time.time()
    owner = cur.execute(
        "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
        ("guest-demo-owner", "Demo Owner", now)).lastrowid
    pid = cur.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " visibility, created_at, updated_at)"
        " VALUES (?,?,?,'collective','AGPL-3.0','public',?,?)",
        (SLUG, "Guest Demo", "Where a visitor files without an account", now, now)).lastrowid
    cur.execute(
        "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,'owner',?)",
        (pid, owner, now))
    # A guest is a users row with is_guest = 1 and no credential, exactly as
    # store.CreateGuest writes one: the page must render it as "guest", not as a
    # synthetic handle.
    cur.execute(
        "INSERT INTO users (username, display_name, created_at, is_guest)"
        " VALUES ('guest-abcd1234','guest',?,1)", (now,))
    con.commit()
    con.close()
    return pid


@pytest.fixture(scope="module")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-guest-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-guest-e2e-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    seed(db)
    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="module")
def pid(server):
    """The seeded project's id, read the way the page reads it."""
    return harness.api(server["base"], "GET", f"/api/v1/projects/{SLUG}")["id"]


@pytest.fixture(scope="module")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


@pytest.fixture()
def visitor(browser):
    """A first-time visitor: no cookie notice accepted.

    Its own rate-limit identity, like every other fixture in this directory, so a
    page load and the reads that follow do not share the 100-per-minute bucket with
    the direct API calls this file also makes.
    """
    ctx = browser.new_context(extra_http_headers={"X-Forwarded-For": "10.77.0.9"})
    ctx.clear_cookies()  # the conftest accepts the notice for everyone else
    pg = ctx.new_page()
    pg.errors = []
    pg.on("pageerror", lambda e: pg.errors.append("PAGEERROR: " + str(e)))
    pg.on("console", lambda m: pg.errors.append("CONSOLE: " + m.text)
          if m.type == "error" else None)
    yield pg
    ctx.close()


def test_the_notice_is_shown_and_disappears_when_accepted(server, visitor):
    """Claim 2, from the visitor's side.

    Both halves matter: the notice is visible to a first-time visitor, and accepting
    it makes it go away. A notice that cannot be dismissed is the state where a
    visitor can never interact, which no Go test would notice.
    """
    visitor.goto(BASE + "/")
    notice = visitor.locator("#cookie-notice")
    expect(notice).to_be_visible()
    expect(notice).to_contain_text("Interactions are off until you accept")
    expect(visitor.locator("body")).to_have_attribute("data-consent", "required")

    visitor.locator("#cookie-accept").click()
    expect(notice).to_have_count(0)
    expect(visitor.locator("body")).to_have_attribute("data-consent", "accepted")
    # The choice survives a navigation, which is the point of storing it.
    visitor.goto(BASE + "/projects")
    expect(visitor.locator("#cookie-notice")).to_have_count(0)
    expect(visitor.locator("body")).to_have_attribute("data-consent", "accepted")


def test_an_interaction_is_refused_until_the_notice_is_accepted(server, visitor, pid):
    """The gate, from the browser.

    The client refuses the request before it is sent, and says so in the same words
    the server would. The refusal has to be VISIBLE: the failure this guards against
    is a button that appears to do nothing.
    """
    visitor.goto(f"{BASE}/projects/{SLUG}")
    # Ask the page's own script to file a complaint while consent is missing: the
    # wrapper in consent.js must refuse it locally, without a network round trip.
    sent = visitor.evaluate("""(pid) => {
        return fetch('/api/v1/projects/' + pid + '/complaints', {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({project_id: pid, title: 'from the notice test'})
        }).then(r => r.status);
    }""", pid)
    assert sent == 403, f"a refused write answered {sent}"
    expect(visitor.locator("#concord-message")).to_contain_text(
        "cookies must be accepted before interacting")


def test_a_guest_can_file_a_complaint_without_an_account(server, visitor, pid):
    """Claim 1: a visitor with no account can act, once they have accepted.

    The complaint is filed through the API exactly as the page's own script would
    (same wrapper, same identity minting), and then read back from the DATABASE: the
    point is that the row exists and belongs to a guest, not that a response said so.
    """
    visitor.goto(f"{BASE}/projects/{SLUG}")
    visitor.locator("#cookie-accept").click()
    expect(visitor.locator("body")).to_have_attribute("data-consent", "accepted")

    title = "the export drops the last row"
    status = visitor.evaluate("""([pid, title]) => {
        return fetch('/api/v1/projects/' + pid + '/complaints', {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({project_id: pid, title: title, body: 'b',
                                  severity: 3, frequency: 0.5})
        }).then(r => r.status);
    }""", [pid, title])
    assert status == 201, f"a guest's complaint was answered {status}"

    con = sqlite3.connect(server["db"])
    try:
        row = con.execute(
            "SELECT c.title, u.is_guest FROM complaints c JOIN users u ON u.id = c.author_id"
            " WHERE c.title = ?", (title,)).fetchone()
    finally:
        con.close()
    assert row is not None, "the guest's complaint is not in the database"
    assert row[1] == 1, "the complaint's author is not a guest identity"


def test_the_guest_identities_work_survives_registration(server, visitor, pid):
    """Claim 3: the guest's rows become the account's.

    Registering from the browser that holds the guest token must keep the work. The
    page is then reloaded, and the account's own complaint list is read from the
    database -- the identity is the same id, so the row never moved.
    """
    visitor.goto(f"{BASE}/projects/{SLUG}")
    visitor.locator("#cookie-accept").click()
    title = "filed while browsing anonymously"
    status = visitor.evaluate("""([pid, title]) => {
        return fetch('/api/v1/projects/' + pid + '/complaints', {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({project_id: pid, title: title, body: 'b',
                                  severity: 3, frequency: 0.5})
        }).then(r => r.status);
    }""", [pid, title])
    assert status == 201, f"a guest's complaint was answered {status}"

    guest_id = visitor.evaluate("() => JSON.parse(localStorage.getItem('concord.user')).id")

    # The REAL form, not a hand-made fetch: if the form does not present the guest's
    # token, the server has nothing to promote and the work is stranded -- which is
    # exactly the defect this test was written to find (auth.js did not send it).
    visitor.goto(BASE + "/register")
    visitor.locator("#reg-username").fill("guest-turned-account")
    visitor.locator("#reg-display").fill("Was A Guest")
    visitor.locator("#reg-password").fill("test-password-123")
    visitor.locator("#register-form button[type=submit]").click()
    visitor.wait_for_url(lambda u: "/register" not in u, timeout=10000)
    registered = visitor.evaluate(
        "() => JSON.parse(localStorage.getItem('concord.user'))")
    assert registered["id"] == guest_id, (
        "registration created a new identity: the guest's work would be stranded")
    assert registered["is_guest"] is False, "the promoted identity is still a guest"

    con = sqlite3.connect(server["db"])
    try:
        n = con.execute("SELECT COUNT(*) FROM complaints WHERE author_id = ?",
                        (guest_id,)).fetchone()[0]
        username = con.execute("SELECT username FROM users WHERE id = ?",
                               (guest_id,)).fetchone()[0]
    finally:
        con.close()
    assert n == 1, f"the promoted account owns {n} complaints; want the guest's 1"
    assert username == "guest-turned-account", f"the account is still named {username}"

# ------------------------------------------------------------------ the door shut
#
# A second instance, configured the way an operator shuts the door: the limit is 0.
# The claim is not "guests are refused" (the Go suite proves that on every guest
# write) but that a visitor is TOLD, in words, instead of watching a button do
# nothing -- which is the failure this whole pass exists to avoid.

PORT_SHUT = PORT + 1
BASE_SHUT = f"http://127.0.0.1:{PORT_SHUT}"


@pytest.fixture(scope="module")
def shut_server():
    dbdir = tempfile.mkdtemp(prefix="concord-guest-shut-")
    logdir = tempfile.mkdtemp(prefix="concord-guest-shut-log-")
    proc, db, base = harness.start_server(PORT_SHUT, dbdir, logdir,
                                          extra_env={"CONCORD_GUEST_MAU_LIMIT": "0"})
    seed(db)
    yield {"base": base, "db": db}
    harness.stop_server(proc)


@pytest.fixture()
def shut_visitor(browser, shut_server):
    ctx = browser.new_context(extra_http_headers={"X-Forwarded-For": "10.77.1.9"})
    ctx.clear_cookies()  # a first-time visitor, like `visitor` above
    pg = ctx.new_page()
    pg.goto(BASE_SHUT + "/")
    yield pg
    ctx.close()


def test_a_visitor_is_told_when_the_guest_door_is_shut(shut_server, shut_visitor):
    """The refusal is a sentence, not a dead button.

    The instance has its limit at 0, so no guest identity can be minted. The page has
    to say why and offer the way forward, because the alternative -- a form that
    silently does nothing -- is exactly what a visitor cannot debug.
    """
    shut_visitor.locator("#cookie-accept").click()
    expect(shut_visitor.locator("body")).to_have_attribute("data-consent", "accepted")
    # An ORDINARY interaction, not the mint endpoint: what a visitor meets is a form
    # that cannot find an identity, and the wrapper's job is to say so.
    status = shut_visitor.evaluate("""() => fetch('/api/v1/projects/1/complaints', {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({project_id: 1, title: 'with the door shut'})
    }).then(r => r.status)""")
    assert status == 403, f"a write was accepted with the door shut ({status})"
    expect(shut_visitor.locator("#concord-message")).to_contain_text(
        "grown past its guest limit")
    expect(shut_visitor.locator("#concord-message")).to_contain_text("Create an account")
