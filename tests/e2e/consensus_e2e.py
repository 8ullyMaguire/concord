"""The Consensus page (docs/specs/consensus-page-spec.md).

The fixture's whole reason is ONE position split: 4 consent, 3 stand-aside, 0 block.
That is the case the spec is about, because it is the one where the two required
ratios disagree --

    support  = 4 / (4 + 3 + 0) = 0.57   reservations count against support
    decisive = 4 / (4 + 0)      = 1.00   reservations are not opposition

A page that showed one number, or derived them itself, would display a bug §6.3
already fixed in the server. So every test here that touches the tally checks BOTH
and checks that they differ.
"""

import sqlite3
import tempfile
import time

import pytest
from playwright.sync_api import expect, sync_playwright

import harness
import project_panels_e2e

PORT = 8481
BASE = f"http://127.0.0.1:{PORT}"

SLUG = "consensus-demo"
PRIVATE = "consensus-private"


def seed(db):
    """A project with an open call carrying the 4/3/0 split, plus a private one."""
    project_panels_e2e.seed(db)

    con = sqlite3.connect(db)
    cur = con.cursor()
    now = time.time()

    owner = cur.execute(
        "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
        ("consensus-owner", "Consensus Owner", now)).lastrowid

    def project(slug, visibility="public"):
        pid = cur.execute(
            "INSERT INTO projects (slug, name, description, governance_model,"
            " license, visibility, created_at, updated_at)"
            " VALUES (?,?,?,'collective','AGPL-3.0',?,?,?)",
            (slug, slug.replace("-", " ").title(), "Fixture project", visibility,
             now, now)).lastrowid
        cur.execute(
            "INSERT INTO members (project_id, user_id, role, joined_at)"
            " VALUES (?,?,'owner',?)", (pid, owner, now))
        return pid

    pid = project(SLUG)
    project(PRIVATE, visibility="private")

    # No created_at: the column does not exist. store.ConsensusCall has carried a
    # CreatedAt field since before migration 0014 rebuilt the table, and the field is
    # always zero -- which is why this fixture's first version failed on a column the
    # Go struct insists on. The page therefore shows opens_at, not created_at.
    call = cur.execute(
        "INSERT INTO consensus_calls (project_id, feature_id, opened_by, opens_at,"
        " closes_at, status, question, description)"
        " VALUES (?,NULL,?,?,?,'open','Should we adopt the offline sync?',"
        "'It would let the editor work with no network.')",
        (pid, owner, now, now + 7 * 86400)).lastrowid

    # 4 consent, 3 stand-aside. Seven DISTINCT users, because positions is keyed on
    # (call_id, user_id): casting as the same identity seven times would leave one
    # row and a tally of 1/1, where both ratios are equal and the page's whole point
    # is invisible.
    for i, stance in enumerate(["consent"] * 4 + ["stand_aside"] * 3):
        uid = cur.execute(
            "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
            (f"consensus-voter-{i}", f"Voter {i}", now)).lastrowid
        cur.execute(
            "INSERT INTO positions (call_id, user_id, position, updated_at)"
            " VALUES (?,?,?,?)", (call, uid, stance, now))

    # One open objection, so the objection section renders and the open_objections
    # count in the tally is non-zero.
    oid = cur.execute(
        "INSERT INTO objections (call_id, user_id, principle, violation, remedy,"
        " status, created_at) VALUES (?,?,'Reversibility',"
        "'A migration cannot be undone once shipped.','Ship behind a flag.',"
        "'open',?)", (call, owner, now)).lastrowid

    # A second, resolved objection: the page must show both states apart.
    cur.execute(
        "INSERT INTO objections (call_id, user_id, principle, violation, remedy,"
        " status, created_at) VALUES (?,?,'Storage',"
        "'SQLite serialises writers.','Use WAL mode.','resolved',?)",
        (call, owner, now + 60))

    con.commit()
    con.close()

    # No contributor is granted here: closing a call needs one, but the only way to
    # give a user a password is `store.SetPassword`, which HASHES it in Go. A SQL
    # fixture can insert a members row but cannot produce a credential, so the closer
    # is registered over HTTP by the test that needs it and granted rights after that.
    # See `closer_token()`.
    return call


# The closer is created over HTTP and granted contributor rights immediately after,
# because the password hash cannot be written from SQL. Memoised so several tests can
# ask for the token without re-registering (a second register is a 409).
_closer = {}


def closer_token(server):
    """Register the closer once, grant it contributor rights, return its token."""
    if "token" in _closer:
        return _closer["token"]
    status, out = api_allowing_error(BASE, "POST", "/api/v1/auth/register", {
        "username": "closer", "password": "correct-horse-battery",
        "display_name": "Closer",
    })
    assert status == 201, f"registering the closer answered {status}: {out}"
    _closer["token"] = out["token"]
    grant_contributor(server["db"], SLUG, "closer")
    return _closer["token"]


def grant_contributor(db, slug, username):
    """Give `username` contributor rights on `slug`, creating the user if needed."""
    con = sqlite3.connect(db)
    try:
        now = time.time()
        row = con.execute("SELECT id FROM users WHERE username = ?", (username,)).fetchone()
        if row is None:
            con.execute(
                "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
                (username, username, now))
            row = con.execute("SELECT id FROM users WHERE username = ?",
                              (username,)).fetchone()
        uid = row[0]
        pid = con.execute("SELECT id FROM projects WHERE slug = ?", (slug,)).fetchone()[0]
        con.execute(
            "INSERT OR IGNORE INTO members (project_id, user_id, role, joined_at)"
            " VALUES (?,?,'contributor',?)", (pid, uid, now))
        con.commit()
    finally:
        con.close()


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-consensus-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-consensus-e2e-log-")
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
    ctx = browser.new_context()
    pg = ctx.new_page()
    pg.errors = []
    pg.on("pageerror", lambda e: pg.errors.append("PAGEERROR: " + str(e)))
    pg.on("console", lambda m: pg.errors.append("CONSOLE: " + m.text)
          if m.type == "error" else None)
    pg.goto(BASE + "/")
    return pg


def open_call(pg, slug=SLUG, call=1):
    pg.goto(f"{BASE}/projects/{slug}/consensus?call={call}")
    expect(pg.locator("#consensus-root")).to_have_attribute("aria-busy", "false",
                                                            timeout=15000)


def close_seeded_call(server):
    """Close call 1, as a registered contributor.

    Every test that asserts on RATIOS or OBJECTION COUNTS has to do this first.
    Those assertions were written when the page showed live counts, which §6.6
    forbids: "Running counts are hidden until the call closes, to prevent
    bandwagoning." They were not weakened -- they were moved onto the path where the
    numbers legitimately exist, which is what §6.6 says to do.
    """
    status, out = api_allowing_error(
        BASE, "POST", f"/api/v1/projects/{SLUG}/consensus/1/close",
        token=closer_token(server))
    # Idempotent on purpose. The server fixture is SESSION-scoped, so the first test
    # that closes call 1 closes it for every test after it. Making this tolerant of an
    # already-closed call keeps each test independent of execution order, rather than
    # letting a re-run fail on whichever test happens to run second.
    if status != 200:
        body = json.dumps(out) if not isinstance(out, str) else out
        if "closed" not in body.lower():
            raise AssertionError(f"closing the seeded call answered {status}: {body}")


def ratio(pg, which):
    text = pg.locator(f"#tally-{which}").text_content()
    return int(text.rstrip("%"))


# --- the page ---------------------------------------------------------------

def test_the_consensus_page_renders_a_call(page):
    open_call(page)
    expect(page.locator("#consensus-call")).to_be_visible(timeout=10000)
    expect(page.locator("#consensus-question")).to_contain_text("offline sync")
    # Quorum progress, before any ratio means anything.
    expect(page.locator("#consensus-quorum")).to_contain_text("7")
    expect(page.locator("#consensus-quorum")).to_contain_text("eligible")
    assert not page.errors, f"JS errors: {page.errors}"


# The headline. Both ratios, and they must DIFFER -- 57% and 100%. A page showing
# one number, or deriving them, fails here.
def test_both_ratios_are_shown_and_they_differ(page, server):
    close_seeded_call(server)
    open_call(page)
    expect(page.locator("#tally-support")).to_be_visible(timeout=10000)
    expect(page.locator("#tally-decisive")).to_be_visible(timeout=10000)

    support = ratio(page, "support")
    decisive = ratio(page, "decisive")

    assert support == 57, f"support is {support}%, want 57% (4 of 7 including reservations)"
    assert decisive == 100, f"decisive is {decisive}%, want 100% (4 of 4 who took a side)"
    assert support != decisive, (
        "the two ratios are equal, so the page has collapsed them into one number "
        "-- which is the bug §6.3 fixed and this page exists not to reintroduce")


# The labels are what make two numbers meaningful. An unlabelled 57% invites the
# reader to apply it to the wrong measure.
def test_each_ratio_explains_its_own_denominator(page, server):
    close_seeded_call(server)
    open_call(page)
    expect(page.locator("#tally-support-detail")).to_contain_text("reservations",
                                                                 timeout=10000)
    expect(page.locator("#tally-decisive-detail")).to_contain_text("blocks")


def test_open_and_resolved_objections_are_shown_apart(page, server):
    close_seeded_call(server)
    open_call(page)
    expect(page.locator("#objection-list")).to_be_visible(timeout=10000)
    expect(page.locator(".objection")).to_have_count(2)
    expect(page.locator(".objection-resolved")).to_have_count(1)


# --- signed out (frontend-spec §8 rule 3) -----------------------------------

def test_signed_out_reads_the_call_with_a_sign_in_prompt(page):
    open_call(page)
    expect(page.locator("#consensus-call")).to_be_visible(timeout=10000)
    expect(page.locator("#consensus-signed-out")).to_be_visible()
    expect(page.locator("#consensus-signed-out a")).to_contain_text("Sign in")
    # Read-only: the write controls are not merely disabled, they are absent.
    expect(page.locator("#consensus-positions")).to_be_hidden()


# --- visibility (frontend-spec §8 rule 4) -----------------------------------

def test_a_private_project_is_a_404_page_to_a_stranger(page):
    res = page.request.get(f"{BASE}/projects/{PRIVATE}/consensus?call=1")
    assert res.status == 404, f"a private project's page answered {res.status}, want 404"
    assert PRIVATE not in res.text(), f"the 404 page leaks the slug: {res.text()[:200]}"


# --- failure path (frontend-spec §8 rule 1) ---------------------------------

def test_a_failed_read_clears_the_skeleton_and_shows_an_error(page):
    page.goto(f"{BASE}/projects/{SLUG}/consensus?call=999999")
    # The point: the page is no longer busy and says why. A skeleton left behind by
    # a failed read looks like a page still loading.
    expect(page.locator("#consensus-root")).to_have_attribute("aria-busy", "false",
                                                            timeout=15000)
    expect(page.locator("#consensus-error")).to_be_visible()
    expect(page.locator("#consensus-retry")).to_be_visible()
    expect(page.locator(".skeleton-row")).to_have_count(0)
    # A deliberate 404 is logged by the browser; only script errors are a failure.
    script_errors = [e for e in page.errors if "Failed to load resource" not in e]
    assert not script_errors, f"JS errors: {script_errors}"


# --- §6.6's hidden tally ---------------------------------------------------

# The rule the page got wrong first time. Both paths are in one test because the
# claim is a contrast: the same page hides the counts while open and shows them once
# closed, and a page that simply never shows them would satisfy half of it.
def test_an_open_call_hides_the_running_tally_but_keeps_the_quorum_bar(page, server):
    # A SECOND call, always open, because the session-scoped server means the tests
    # above close call 1 for everyone. Reading call 1 here would depend on execution
    # order -- and would have silently stopped testing the open path.
    open_call_id = seed_extra_open_call(server["db"])
    open_call(page, call=open_call_id)
    expect(page.locator("#consensus-call")).to_be_visible(timeout=10000)

    # Hidden, with the reason stated.
    expect(page.locator("#consensus-tally")).to_be_hidden()
    expect(page.locator("#consensus-tally-hidden")).to_be_visible()
    # Asserted on the COPY the page actually shows, not on the spec's word. The first
    # version asserted "bandwagon" because that is §6.6's word; the page says
    # "influenced by how others have voted", which is the same reason in plainer
    # language, and a test pinned to spec vocabulary tests the spec, not the page.
    # What matters is that the reason is STATED -- an unexplained gap reads as a bug.
    expect(page.locator("#consensus-tally-hidden")).to_contain_text(
        "hidden until this call closes")

    # Kept: §6.6 preserves participation progress and the quorum bar.
    expect(page.locator("#consensus-quorum")).to_be_visible()
    expect(page.locator("#consensus-quorum")).to_contain_text("quorum")

    # And the raw response really carries no stance, so this is not the client
    # choosing to hide something the server sent.
    body = page.request.get(
        f"{BASE}/api/v1/projects/{SLUG}/consensus/{open_call_id}").json()
    assert body["tally_visible"] is False, (
        f"the server reports tally_visible={body['tally_visible']} on an open call")
    for key in ("consent", "stand_aside", "block", "support_ratio", "decisive_ratio"):
        assert body["tally"][key] is None, (
            f"the server sent {key}={body['tally'][key]!r} on an open call; §6.6 hides "
            f"the running counts")
    assert body["positions"] == [], (
        f"the server sent other voters' positions: {body['positions']}")


def test_a_closed_call_reveals_the_tally(page, server):
    # Close the seeded call through the API, then reload. The call the fixture opens
    # is id 1; closing it is what moves the page onto the revealed path.
    #
    # Two dead ends on the way here, both worth recording because each looked like a
    # product bug: registering "closer" in the SQL fixture and then registering again
    # over HTTP is a 409, which reads exactly like "closing a call is a conflict"; and
    # logging in instead returns 401, because a user inserted by SQL has no password
    # and store.SetPassword hashes in Go. closer_token() registers over HTTP and grants
    # rights after.
    status, out = api_allowing_error(
        BASE, "POST", f"/api/v1/projects/{SLUG}/consensus/1/close",
        token=closer_token(server))
    assert status == 200, f"closing the call answered {status}: {out}"

    open_call(page)
    expect(page.locator("#consensus-call")).to_be_visible(timeout=10000)
    expect(page.locator("#consensus-tally")).to_be_visible(timeout=10000)
    expect(page.locator("#tally-support")).to_contain_text("%")
    expect(page.locator("#tally-decisive")).to_contain_text("%")
    expect(page.locator("#consensus-tally-hidden")).to_be_hidden()

    # Both still shown, and still different if the split still differs -- the
    # collapsed-ratio bug must stay dead on this path too.
    support = ratio(page, "support")
    decisive = ratio(page, "decisive")
    assert support != decisive, (
        f"a closed call shows support={support}% and decisive={decisive}%, which are "
        f"equal -- the two ratios have been collapsed into one number")


def api_allowing_error(base, method, path, body=None, token=None):
    """harness.api but returning (status, body) instead of raising on non-2xx.

    harness.api raises urllib's HTTPError, whose message is the status code and
    nothing else -- a 409 from "the username exists" and a 409 from "the call is
    suspended by an emergency hold" are the same string. This returns the body so a
    failure can say which one it was.
    """
    import json
    import urllib.error
    import urllib.request
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(base + path, data=data, method=method,
                                 headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            raw = resp.read()
            return resp.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except ValueError:
            return e.code, raw.decode("utf-8", "replace")


def seed_extra_open_call(db):
    """Add an OPEN call with the same 4/3/0 split, and return its id.

    The hidden-tally tests need a call that is still open, and the server fixture is
    session-scoped, so the ratio tests close call 1 for the whole run. Without a second
    call the hidden-tally test would be testing a CLOSED call and pass for the wrong
    reason -- it asserts the tally is absent, which a closed call also satisfies once
    the fixture data has been closed.
    """
    con = sqlite3.connect(db)
    try:
        cur = con.cursor()
        now = time.time()
        owner = cur.execute(
            "SELECT id FROM users WHERE username = 'consensus-owner'").fetchone()[0]
        pid = cur.execute(
            "SELECT id FROM projects WHERE slug = ?", (SLUG,)).fetchone()[0]
        call = cur.execute(
            "INSERT INTO consensus_calls (project_id, feature_id, opened_by, opens_at,"
            " closes_at, status, question, description)"
            " VALUES (?,NULL,?,?,?,'open','Should the hidden tally stay hidden?',"
            "'A second open call.')",
            (pid, owner, now, now + 7 * 86400)).lastrowid
        for i, stance in enumerate(["consent"] * 4 + ["stand_aside"] * 3):
            uid = cur.execute(
                "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
                (f"hidden-tally-voter-{i}", f"Hidden {i}", now)).lastrowid
            cur.execute(
                "INSERT INTO positions (call_id, user_id, position, updated_at)"
                " VALUES (?,?,?,?)", (call, uid, stance, now))
        con.commit()
        return call
    finally:
        con.close()
