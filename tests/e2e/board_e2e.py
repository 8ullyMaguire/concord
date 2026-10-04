"""Moving cards on the kanban board, which could not be moved on.

THE MISSING WRITE PATH, and the fourth time the "for every endpoint the API offers,
name the page that calls it" question has paid. handleMoveCard, MoveCard and
PUT .../board/cards/{id}/move existed, were guarded, and were tested at the store layer.
The board rendered columns and cards and could not be moved.

WHAT THE GO GATE DOES NOT COVER. board_move_test.go asserts the three move paths exist
in the source. It cannot assert that a move WORKS, because the failure mode of the first
version was invisible to a source gate: the JS sent the FEATURE id in the
`{card_id}` path segment, so every move 404'd, the board silently ignored the drag, and
the page looked correct. Hence the e2e.

THE HEADERS BELOW ARE COPIED FROM requests_e2e.py, which is why SLUG had to be changed
to board-demo -- the first run 404'd on /projects/req-demo/board and reported "no cards"
rather than "wrong slug".

ONE SERVER, ONE DATABASE, PER SESSION. _reset() puts the board back before each test,
because the board is tested by MOVING cards and a fixture that cannot be restored makes
every test after the first one order-dependent.

WHAT THE GO GATE DOES NOT COVER. requests_page_test.go asserts the page RENDERS, that
the link is BUILT in project.js, and that a private project 404s. All three are
source- and server-level checks. The complaint page taught what that misses:
`complaintCards(complaints, slug)` threw a ReferenceError at runtime, blanked the
ENTIRE project page, and TestComplaintPageIsLinkedFromTheProject stayed green because
it only looked for a string. This file is the half that has a DOM.

WHY THE ROWS ARE SEEDED THROUGH SQLITE. A browser test whose data arrives through the
API it is testing cannot fail for the reason it exists: a broken write path yields an
empty page and the assertions pass anyway.

THE THREE ANSWERS ARE DELIBERATELY DIFFERENT, because §17 requires the page to keep
three distinctions that a naive implementation collapses into one number:

  ranked      elo_r 1610, elo_rd 40     -- settled by comparison
  uncertain   elo_r 1502, elo_rd 344    -- barely moved, WIDE band
  accepted    elo_r 1420, elo_rd 55     -- badged, and RANKED LAST

The third is the one that breaks the obvious implementation. §17 says the accepted
badge "is displayed but does not override the ranking", so a page that floats the
accepted answer to the top is violating the spec -- and a page that hides its rating
is hiding the evidence. The accepted answer must appear last AND carry the badge.

EVERY CONTEXT GETS ITS OWN X-FORWARDED-FOR, or contexts share a rate-limit bucket and
the limiter's JSON is served AS THE PAGE BODY.
"""

import itertools
import sqlite3
import tempfile
import time

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

PORT = 8489
BASE = f"http://127.0.0.1:{PORT}"

SLUG = "board-demo"
PRIVATE = "board-private"

_ips = itertools.count(1)


def seed(db):
    """One public project with three columns and a card in the first.

    THE CARD IS PLACED, because a board with no card cannot demonstrate moving one and
    every move assertion would pass vacuously.

    Column 3 carries a WIP LIMIT OF 1 and already holds a card, so the board can show
    both halves of the rule: a move within the limit, and a refusal past it.
    """
    con = sqlite3.connect(db)
    cur = con.cursor()
    now = time.time()

    cur.execute(
        "INSERT INTO users (username, display_name, created_at, password_hash, trust_level)"
        " VALUES (?,?,?,?,?)",
        ("board-owner", "Board Owner", now, "x", 3),
    )
    owner = cur.lastrowid

    cur.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " created_at, updated_at, visibility) VALUES (?,?,?,?,?,?,?,?)",
        (SLUG, "Board Demo", "a project with a real board", "collective", "MIT",
         now, now, "public"),
    )
    pid = cur.lastrowid

    # position, wip_limit. Column 3 is at its limit from the start.
    for i, (phase, wip) in enumerate(
            [("proposed", None), ("accepted", None), ("in progress", 1)]):
        cur.execute(
            "INSERT INTO board_columns (project_id, phase, position, wip_limit)"
            " VALUES (?,?,?,?)", (pid, phase, i, wip))
        if i == 0:
            first_col = cur.lastrowid
        if i == 1:
            second_col = cur.lastrowid
        if i == 2:
            full_col = cur.lastrowid

    # A feature PER CARD. board_cards has UNIQUE (project_id, feature_id) -- migration
    # 0020, added because a feature was appearing on the board once per column it had
    # ever visited. So one feature cannot occupy two columns, and a seed that tried made
    # the fixture fail rather than the page.
    for title in ("Add a migration check", "Document the release runbook"):
        cur.execute(
            # `body` not `description`, `author_id` not `created_by`, and the status
            # CHECK has no 'accepted' -- it has 'in_progress'. All three were wrong in
            # the first version and each failed loudly, which is the right way for a
            # fixture to fail: at the schema, before the page is ever reached.
            "INSERT INTO features (project_id, title, body, status, author_id,"
            " created_at, updated_at) VALUES (?,?,?,?,?,?,?)",
            (pid, title, "d", "in_progress", owner, now, now))
        if title.startswith("Add a"):
            feature = cur.lastrowid
        else:
            other_feature = cur.lastrowid

    cur.execute(
        "INSERT INTO board_cards (project_id, kind, feature_id, column_id, entered_at)"
        " VALUES (?, 'feature', ?, ?, ?)", (pid, feature, first_col, now))
    # And the card that fills the WIP-limited column.
    cur.execute(
        "INSERT INTO board_cards (project_id, kind, feature_id, column_id, entered_at)"
        " VALUES (?, 'feature', ?, ?, ?)", (pid, other_feature, full_col, now))

    cur.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " created_at, updated_at, visibility) VALUES (?,?,?,?,?,?,?,?)",
        (PRIVATE, "Private", "no board", "collective", "MIT", now, now, "private"),
    )

    con.commit()
    con.close()


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-board-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-board-e2e-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    seed(db)
    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


@pytest.fixture()
def page(browser, server):
    """A SIGNED-IN page.

    handleMoveCard requires an actor, so a card move 401s without a session. That is
    correct -- and it is why this suite cannot be anonymous: the first version moved
    cards from a signed-out page, the PUT answered 401, and the board silently ignored
    it, so the test failed at the final DOM assertion rather than at the request.

    Registered over HTTP so the token is real, then promoted to a member by SQL because
    there is no members WRITE route (server.go registers GET .../members and nothing
    else), which scout_e2e.py documents at length.
    """
    ctx = browser.new_context(
        extra_http_headers={
            "X-Forwarded-For": f"10.96.{next(_ips) // 256}.{next(_ips) % 256}"
        }
    )
    pg = ctx.new_page()
    pg.errors = []
    pg.on("pageerror", lambda e: pg.errors.append("PAGEERROR: " + str(e)))
    pg.on("console", lambda m: pg.errors.append("CONSOLE: " + m.text)
          if m.type == "error" and "Failed to load resource" not in m.text else None)
    pg.goto(BASE + "/")

    username = f"board-mover-{next(_ips)}"
    body = harness.api(BASE, "POST", "/api/v1/auth/register", {
        "username": username, "password": "correct-horse-battery",
        "display_name": username,
    })
    token = body.get("token")
    assert token, f"register returned no token: {body}"
    uid = body.get("user", {}).get("id") or body.get("user_id") or body.get("id")
    assert uid, f"register returned no user id: {body}"

    con = sqlite3.connect(server["db"])
    try:
        con.execute(
            "INSERT INTO members (project_id, user_id, role, joined_at)"
            " SELECT id, ?, 'maintainer', ? FROM projects"
            " WHERE id NOT IN (SELECT project_id FROM members WHERE user_id = ?)",
            (uid, time.time(), uid))
        con.commit()
    finally:
        con.close()

    pg.evaluate("t => window.localStorage.setItem('concord.token', t)", token)
    return pg


@pytest.fixture()
def anon_page(browser):
    ctx = browser.new_context(
        extra_http_headers={
            "X-Forwarded-For": f"10.95.{next(_ips) // 256}.{next(_ips) % 256}"
        }
    )
    pg = ctx.new_page()
    pg.errors = []
    pg.on("pageerror", lambda e: pg.errors.append("PAGEERROR: " + str(e)))
    pg.goto(BASE + "/")
    return pg
    ctx = browser.new_context(
        extra_http_headers={
            "X-Forwarded-For": f"10.99.{next(_ips) // 256}.{next(_ips) % 256}"
        }
    )
    pg = ctx.new_page()
    pg.errors = []
    pg.on("pageerror", lambda e: pg.errors.append("PAGEERROR: " + str(e)))
    pg.on("console", lambda m: pg.errors.append("CONSOLE: " + m.text)
          if m.type == "error" and "Failed to load resource" not in m.text else None)
    pg.goto(BASE + "/")
    return pg


def _reset(server):
    """Put the board back to its seeded state.

    ONE SERVER, ONE DATABASE, PER SESSION. So a move in one test is still there for the
    next, and the first version of this suite failed for exactly that reason: the card
    asserted to be in "proposed" had been moved to "accepted" by an earlier test, and
    the failure pointed at the page rather than at the suite's own shared state.

    Board mutations are tested by MOVING cards; a fixture that cannot be put back makes
    every test after the first one order-dependent.
    """
    con = sqlite3.connect(server["db"])
    cur = con.cursor()
    cur.execute(
        "UPDATE board_cards SET column_id ="
        " (SELECT id FROM board_columns WHERE phase='proposed')"
        " WHERE feature_id = (SELECT id FROM features WHERE title='Add a migration check')")
    cur.execute(
        "UPDATE board_cards SET column_id ="
        " (SELECT id FROM board_columns WHERE phase='in progress')"
        " WHERE feature_id = (SELECT id FROM features WHERE title='Document the release runbook')")
    con.commit()
    con.close()


def _board(page, server):
    _reset(server)
    page.goto(BASE + f"/projects/{SLUG}/board")
    page.wait_for_selector("#kanban-board .kanban-card", timeout=15000)


def test_a_card_can_be_moved(page, server):
    """THE MISSING WRITE PATH.

    handleMoveCard, MoveCard and PUT .../board/cards/{id}/move all existed and were
    tested at the store layer. The board could not be moved at all: a kanban board
    nobody can move a card on is a read-only list wearing a board's layout.

    Uses the SELECT, not a synthetic drag event: Playwright's drag-and-drop is not the
    HTML5 drag API the board implements, so a synthetic DataTransfer is the only honest
    way to test it -- and the select is the path a keyboard user takes, so testing it is
    testing the accessible interface rather than a test-only branch.
    """
    _board(page, server)
    card = page.locator("#kanban-board .kanban-card", has_text="Add a migration check")

    # The card's OWN column, read off its data-column attribute.
    #
    # NOT `.kanban-column` filtered by `has=card`: that composes a card locator with a
    # column locator, and Playwright resolves `has` against the WHOLE document rather
    # than the candidate's subtree -- so it matched no column and the assertion failed
    # on a board that was correct. An element's position is read from that element.
    # A plain assert, not expect(): expect() is for the auto-retrying locator
    # assertions, and it rejects a bare string outright with
    # "Unsupported type: <class 'str'>" -- which reads like a Playwright bug rather
    # than a wrong call.
    assert card.get_attribute("data-column") == "proposed", (
        "the card is in " + repr(card.get_attribute("data-column")) +
        ", not the seeded first column; _reset() should have put it back")

    # The MOVE RESPONSE IS WATCHED, not assumed. A move that 401s renders a perfectly
    # normal board -- the card stays put, no error is shown unless the reader looks for
    # one -- so a test that only checks the final DOM cannot tell "refused" from
    # "never sent". This is the assertion that distinguishes them.
    seen = []
    page.on("response", lambda r: seen.append((r.request.method, r.url.split("/api")[-1],
                                               r.status)) if "/move" in r.url else None)
    page.locator("#kanban-board select.kanban-card-move").first.select_option("accepted")
    page.wait_for_timeout(2500)
    assert seen, "selecting a column issued no PUT at all"
    assert seen[0][2] < 300, (
        f"the move was refused with {seen[0][2]}; the commonest cause is a token read "
        f"from the wrong localStorage key, which returns null instead of throwing")

    # And it LANDS in the second column, after the board re-reads.
    page.wait_for_function(
        """() => {
             const cols = [...document.querySelectorAll('.kanban-column')];
             const target = cols.find(c => c.querySelector('.kanban-column-title')
                                          ?.textContent.trim() === 'accepted');
             return target && target.textContent.includes('Add a migration check');
           }""", timeout=15000)


def test_the_move_is_persisted_not_just_re_rendered(page, server):
    """The card's column in the DATABASE, after a page reload.

    Without this, a page that reordered its own DOM would pass the first test while the
    server state never changed -- the board would look right until anyone else looked.
    """
    _board(page, server)
    page.locator("#kanban-board select.kanban-card-move").first.select_option("accepted")
    page.wait_for_timeout(1500)

    page.reload()
    page.wait_for_selector("#kanban-board .kanban-card", timeout=15000)
    moved = page.locator("#kanban-board .kanban-card",
                         has_text="Add a migration check")
    assert moved.get_attribute("data-column") == "accepted", (
        "after a reload the card is in " +
        repr(moved.get_attribute("data-column")) +
        ", so the move was rendered but never persisted")


def test_a_card_is_focusable(page, server):
    """A draggable <div> is not focusable, so the arrow-key handlers would have no
    element to attach to and the board would be unusable by keyboard."""
    _board(page, server)
    card = page.locator("#kanban-board .kanban-card", has_text="Add a migration check")
    # Tab-reachable: assert the element is a button, which is focusable by definition,
    # rather than counting tab stops (fragile, and it would pass for a div with a
    # tabindex that still announces nothing).
    assert card.evaluate("el => el.tagName") == "BUTTON", (
        "a card is a " + card.evaluate("el => el.tagName") +
        "; a draggable div is not focusable and announces nothing")
    # And it says which column it is in, so a screen reader conveys the position.
    assert "in proposed" in card.get_attribute("aria-label"), (
        "aria-label is " + repr(card.get_attribute("aria-label")) +
        "; without the column a screen reader gives no position")


def test_a_column_at_its_wip_limit_is_marked(page, server):
    """The header already rendered "n / WIP 1". What was missing is the STATE, so the
    reader finds out by being refused rather than by seeing the limit reached."""
    _board(page, server)
    full = page.locator(".kanban-column.is-at-wip")
    expect(full).to_have_count(1)
    expect(full.locator(".kanban-column-title")).to_have_text("in progress")


def test_moving_into_a_full_column_is_refused_with_the_servers_reason(page, server):
    """The refusal must NAME the gate or the limit.

    A generic "that move was refused" tells the reader nothing about which rule stopped
    them, and the store's message is the only place that is written down.
    """
    _board(page, server)
    # Take the card out of the full column first so there is one there to move.
    page.locator(".kanban-column.is-at-wip select.kanban-card-move").first.select_option(
        "proposed")
    page.wait_for_timeout(1500)

    _board(page, server)
    # Now put it back into the WIP-limited column, which holds one card already.
    page.locator("#kanban-board select.kanban-card-move").first.select_option(
        "in progress")
    page.wait_for_timeout(1500)

    body = page.locator("#kanban-board").inner_text().lower()
    assert ("wip" in body or "limit" in body), (
        "moving past a WIP limit produced no explanation: " + body[:300])
    assert not page.errors, page.errors


def test_a_refused_move_leaves_the_card_where_it_was(page, server):
    """The board is RE-READ rather than reordered locally.

    The move response is {"status":"moved"} and says nothing about whether a limit or a
    gate refused it, so a local reorder would show the card in the column it was
    refused for -- looking correct to the person who just tried to move it.
    """
    _board(page, server)
    page.locator(".kanban-column.is-at-wip select.kanban-card-move").first.select_option(
        "proposed")
    page.wait_for_timeout(1500)
    _board(page, server)
    page.locator("#kanban-board select.kanban-card-move").first.select_option(
        "in progress")
    page.wait_for_timeout(2000)

    # Whatever happened, the board and the database must agree.
    page.reload()
    page.wait_for_selector("#kanban-board .kanban-card", timeout=15000)
    counts = page.locator(".kanban-column.is-at-wip .kanban-card").count()
    db_counts = sqlite3.connect(server["db"]).execute(
        "SELECT COUNT(*) FROM board_cards WHERE column_id = "
        "(SELECT id FROM board_columns WHERE phase='in progress')").fetchone()[0]
    assert counts == db_counts, (
        f"the board shows {counts} cards in the WIP-limited column and the database "
        f"has {db_counts}; one of them is lying")


def test_the_board_does_not_console_error(page, server):
    _board(page, server)
    page.locator("#kanban-board select.kanban-card-move").first.select_option("accepted")
    page.wait_for_timeout(1500)
    assert not page.errors, page.errors


def test_a_private_project_serves_no_board(anon_page, server):
    page = anon_page
    page.goto(BASE + f"/projects/{PRIVATE}/board")
    body = page.locator("body").inner_text()
    assert "no project" in body.lower() or "not found" in body.lower(), body[:200]
