"""§4.3's taxonomy page, in a browser.

The page is the first reader of GET /taxonomy/tags, GET /taxonomy/proposals and
POST /taxonomy/proposals/{id}/consent. All three had handlers, auth checks and store
tests; nothing in assets/js referenced any of them, and `taxonomy/proposals` was named
in docs/REMAINING.md §11.3 as an endpoint that answers `[]` and is read by nobody.

What only a browser can assert, and the reason this file exists beside the Go gates:

  * the 202 path. Consenting below quorum is the ORDINARY answer -- the server says so
    with a message rather than an error -- and the page must render it as information.
    A page that painted it red would teach people the control is broken.
  * the proposer's own card, which must say why its button is absent.
  * the "suggested" badge, which only appears if the page asks for suggested tags. The
    default read drops them, so a page written from the house style shows a taxonomy
    that disagrees with what tag search finds -- and looks completely fine.

None of those is reachable from Go: a ReferenceError parses cleanly (the token-key
typo lived in four files that way), so `node --check` proves nothing about whether the
page runs.
"""

import itertools
import sqlite3
import tempfile

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

# One fixed port per suite; the cross-file gate parses this line with int().
PORT = 8492
FODDER = "e2e-taxonomy-people"
BASE = f"http://127.0.0.1:{PORT}"

_ips = itertools.count(1)


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-tax-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-tax-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    seed(db)
    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


def seed(db):
    """The accounts that make a quorum countable, and a project for people to join.

    Five eligible taggers, so one consent is visibly below quorum and the 202 path is
    the one under test rather than a coincidence of the default charter.

    A register-over-HTTP user has no real password hash? No -- these five are SQL-only
    and never sign in; the browser users are registered over HTTP and promoted to
    contributors of FODDER, which is what makes them allowed to *propose* (an
    instance-wide proposal needs contributor somewhere) while leaving the five below
    untouched for the denominator arithmetic.

    FODDER exists for exactly one reason: joining a project is the only way to become
    a contributor, and joining the project under test would change the eligible count
    this suite reads.
    """
    now = 1790000000
    con = sqlite3.connect(db)
    try:
        for i in range(1, 6):
            con.execute(
                "INSERT INTO users (username, display_name, created_at, password_hash, trust_level)"
                " VALUES (?,?,?,?,?)",
                (f"taxmember{i}", f"Tax Member {i}", now, "x", 3))
        con.execute(
            "INSERT INTO projects (slug, name, description, governance_model, license,"
            " created_at, updated_at, visibility) VALUES (?,?,?,?,?,?,?,?)",
            (FODDER, "Taxonomy People", "accounts for the taxonomy suite", "collective",
             "MIT", now, now, "public"))
        pid = con.execute("SELECT id FROM projects WHERE slug=?", (FODDER,)).fetchone()[0]
        for row in con.execute("SELECT id FROM users WHERE username LIKE 'taxmember%'"):
            con.execute(
                "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?, 'contributor', ?)",
                (pid, row[0], now))
        con.commit()
    finally:
        con.close()


def _register(db, username, join_fodder=True):
    """Sign a browser user up for real, and make them a contributor by SQL.

    There is no members WRITE route, so membership has to come from the database --
    the same shape board_e2e.py documents. Without it the user can consent (that needs
    only an account) but cannot propose (that needs contributor somewhere), and the
    propose test would fail as a 403 that reads like a broken form.
    """
    reg = harness.api(BASE, "POST", "/api/v1/auth/register", {
        "username": username, "password": "correct-horse-battery", "display_name": username,
    })
    assert reg, "register returned nothing"
    token = reg.get("token")
    assert token, f"register returned no token: {reg}"
    user = reg.get("user", {})
    uid = user.get("id") or reg.get("user_id") or reg.get("id")
    assert uid, f"register returned no user id: {reg}"
    if join_fodder:
        con = sqlite3.connect(db)
        try:
            pid = con.execute("SELECT id FROM projects WHERE slug=?", (FODDER,)).fetchone()[0]
            con.execute(
                "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?, 'contributor', ?)",
                (pid, uid, 1790000000))
            con.commit()
        finally:
            con.close()
    return token, uid, user


def _page(browser, token=None, user=None, **kw):
    """A page, optionally signed in the way the SIGN-IN FORM signs in.

    Both keys, not just the token. auth.js sets `concord.token` AND `concord.user`,
    and session.js reads its user object from the second. A fixture that set only the
    token produced a page that was "signed in" for every gate and had no idea WHO, so
    the comparisons against a proposal's proposed_by were all false -- which is how the
    proposer's own card came to offer them a Consent button that always fails.
    """
    ctx = browser.new_context(
        extra_http_headers={"X-Forwarded-For": f"10.95.{next(_ips) // 256}.{next(_ips) % 256}"},
        **kw)
    pg = ctx.new_page()
    pg.errors = []
    pg.on("pageerror", lambda e: pg.errors.append("PAGEERROR: " + str(e)))
    pg.on("console", lambda m: pg.errors.append("CONSOLE: " + m.text)
          if m.type == "error" and "Failed to load resource" not in m.text else None)
    pg.goto(BASE + "/")
    if token:
        pg.evaluate("t => window.localStorage.setItem('concord.token', t)", token)
    if user:
        pg.evaluate("u => window.localStorage.setItem('concord.user', JSON.stringify(u))", user)
    return pg


def open_taxonomy(page, server):
    page.goto(f"{server['base']}/taxonomy")
    page.wait_for_selector('#taxonomy-root:not([aria-busy="true"])', timeout=15000)
    return page.locator("#taxonomy-root")


def propose_through_the_page(page, server, tag, rationale="why this tag"):
    """Fill the real form and submit it, returning the live region's text."""
    root = open_taxonomy(page, server)
    page.locator("#tax-action").select_option("create")
    page.locator("#tax-target").fill(tag)
    page.locator("#tax-rationale").fill(rationale)
    page.locator("#taxonomy-propose button[type=submit]").click()

    live = page.locator("#taxonomy-live")
    # Waits for the SERVER'S message, which is what arrives: the page's own fallback
    # ("Proposed. It applies once it has consent.") is only used when the response
    # carries no message, so waiting for "proposed" waits for a string this path never
    # renders -- and the first version of this helper did exactly that.
    expect(live).to_contain_text("ratified by quorum", timeout=15000)
    return live.inner_text(), root


def test_the_empty_state_says_what_is_missing(browser, server):
    """First, and anonymous: a fresh instance has no tags and nothing proposed.

    Asserted before anything is created, because the empty state is the live state on
    a new instance -- a page that only renders with data is one nobody building it
    will notice, and §4.3's page is the first to read both collections.
    """
    pg = _page(browser)
    root = open_taxonomy(pg, server)

    expect(root).to_contain_text("No tags yet")
    expect(root).to_contain_text("Nothing is proposed right now")
    expect(root).to_contain_text("Nothing has been decided yet")
    assert not pg.errors, pg.errors


def test_the_quorum_rule_is_stated_beside_the_control(browser, server):
    """The rule, on the page, next to where a consent would be given.

    Two halves of it are unguessable and both leave a first consent looking like a
    broken button: consent is counted against a quorum rather than being decisive, and
    the proposer cannot consent to their own proposal.
    """
    pg = _page(browser)
    root = open_taxonomy(pg, server)

    expect(root).to_contain_text("one consent does not ratify")
    expect(root).to_contain_text("The proposer cannot consent to their own")
    assert not pg.errors, pg.errors


def test_proposing_through_the_form_shows_the_card_its_counts_and_the_rule(browser, server):
    token, _, user = _register(server["db"], f"taxfiler-{next(_ips)}")
    pg = _page(browser, token, user)
    live_text, root = propose_through_the_page(pg, server, "topic:e2e-first")

    # The server's own message, not a sentence the client invented: 201 carries
    # "taxonomy changes are ratified by quorum; consent to this proposal to proceed".
    assert "ratified by quorum" in live_text, (
        f"the live region does not carry the server's 201 message: {live_text!r}")

    card = root.locator('[data-proposal]', has_text="e2e-first")
    expect(card).to_be_visible()
    expect(card).to_contain_text("pending")
    expect(card).to_contain_text("instance-wide")
    # The count the API had to grow two fields for. "0 of M eligible taggers", where M
    # is a number rather than the word undefined -- which is what a page written
    # before consent_count existed would render.
    expect(card).to_contain_text("0 of")
    expect(card).to_contain_text("eligible taggers have consented")
    text = card.inner_text()
    assert "undefined" not in text, text

    # And the proposer's own card explains its missing button instead of offering one
    # the server would refuse.
    expect(card).to_contain_text("You proposed this")
    assert card.locator("button[data-consent]").count() == 0, (
        "the proposer is offered a consent button on their own proposal; the server "
        "refuses it, and §5.1 is the rule the page states two lines above")
    assert not pg.errors, pg.errors


def test_consenting_below_quorum_is_answered_202_and_reads_as_information(browser, server):
    """The path a first consent takes, and the one a page is most likely to get wrong.

    Five eligible taggers and one consent is below quorum, so the server answers 202
    with a message. Rendering that red would teach people the control is broken; the
    assertion on the CSS class is the assertion that it is not.
    """
    proposer, _, _pu = _register(server["db"], f"taxproposer-{next(_ips)}")
    tag = f"topic:e2e-consent-{next(_ips)}"
    api_propose(proposer, tag)

    consenter, _, cuser = _register(server["db"], f"taxconsenter-{next(_ips)}")
    pg = _page(browser, consenter, cuser)
    root = open_taxonomy(pg, server)

    card = root.locator('[data-proposal]', has_text=tag.split(":")[1])
    expect(card).to_be_visible()
    before = _consents(card.inner_text())

    card.locator("button[data-consent]").click()
    live = pg.locator("#taxonomy-live")
    expect(live).to_contain_text("consents", timeout=15000)

    # Not an error: the 202 carries the count it reached, which is the only place the
    # quorum is explained. `error-state` is the class a refusal gets.
    cls = live.locator("p").first.get_attribute("class") or ""
    assert "error-state" not in cls, (
        f"being below quorum was rendered as a failure (class {cls!r}); it is the "
        f"ordinary outcome of the first consent")
    # The count moved, and the card is still pending -- the re-read is the point: the
    # page must show the state the server is in, not the one it hoped for.
    expect(card).to_contain_text("pending")
    after = _consents(root.locator('[data-proposal]', has_text=tag.split(":")[1]).inner_text())
    assert after == before + 1, f"the consent count did not move: {before} -> {after}"
    assert not pg.errors, pg.errors


def test_an_anonymous_visitor_is_told_why_they_cannot_consent_rather_than_refused_silently(browser, server):
    """The control is shown and the refusal is explained.

    The page does not hide the button for a visitor with no session, because a hidden
    button explains nothing: the server's refusal is the only honest answer to "why did
    nothing happen", and the page passes it on.

    What the refusal SAYS changed when guests arrived, and the change is an improvement
    worth stating: a visitor with no account now holds a guest identity, so the answer is
    no longer 401 "no identity" but the rule itself -- only an eligible tagger may
    consent. That rule was also newly enforced at the time (the store counted consents
    from outside the electorate into the quorum), which is how this test found it: it
    clicked the button as a visitor and watched a proposal move to "1/1 consents".
    """
    proposer, _, _pu = _register(server["db"], f"taxproposer-{next(_ips)}")
    tag = f"topic:e2e-anon-{next(_ips)}"
    api_propose(proposer, tag)

    pg = _page(browser)  # no token: the visitor is a guest
    root = open_taxonomy(pg, server)
    card = root.locator('[data-proposal]', has_text=tag.split(":")[1])
    expect(card.locator("button[data-consent]")).to_be_visible()

    card.locator("button[data-consent]").click()
    live = pg.locator("#taxonomy-live")
    expect(live).to_contain_text("only an eligible tagger", timeout=15000)

    expect(card).to_contain_text("pending")
    assert not pg.errors, pg.errors


def test_suggested_tags_are_labelled_rather_than_dropped(browser, server):
    """§4.3 requires machine-inferred tags to be labelled, and the default read drops them.

    Two tags, one of each: a page that used GET /tags without include_suggested would
    show only the first and look entirely correct. Nothing in the Go layer can tell the
    two reads apart -- both answer 200 with an array -- which is why this test names a
    tag that only one of them returns.
    """
    con = sqlite3.connect(server["db"])
    try:
        con.execute("INSERT INTO tags (project_id, name, namespace, suggested)"
                    " VALUES (NULL, 'e2e-human-tag', 'topic', 0)")
        con.execute("INSERT INTO tags (project_id, name, namespace, suggested)"
                    " VALUES (NULL, 'e2e-inferred-tag', 'topic', 1)")
        con.commit()
    finally:
        con.close()

    pg = _page(browser)
    root = open_taxonomy(pg, server)

    human = root.locator('[data-tag="e2e-human-tag"]')
    inferred = root.locator('[data-tag="e2e-inferred-tag"]')
    expect(human).to_be_visible()
    expect(inferred).to_be_visible()
    expect(inferred).to_contain_text("suggested")
    assert "suggested" not in human.inner_text(), (
        "a tag nobody inferred is labelled as inferred, which teaches readers to "
        "distrust the badge")
    assert not pg.errors, pg.errors


def _consents(card_text):
    """The first integer in "N of M eligible taggers have consented"."""
    import re

    m = re.search(r"(\d+)\s+of\s+\d+\s+eligible taggers", card_text)
    assert m, f"the card does not state its consent count: {card_text!r}"
    return int(m.group(1))


def api_propose(token, tag):
    """Open a proposal over the API, for tests about something else.

    Deliberately not the path under test in those tests -- and loud if it fails, since
    harness.api raises on a non-2xx. A test that found no card because the proposal was
    never created would report a broken page.
    """
    res = harness.api(BASE, "POST", "/api/v1/taxonomy/proposals", {
        "action": "create", "target_tag": tag, "rationale": "because the suite says so",
    }, token=token)
    assert res and res.get("proposal"), f"propose failed: {res}"
    return res["proposal"]["id"]
