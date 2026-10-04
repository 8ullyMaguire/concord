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
    return call


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
def test_both_ratios_are_shown_and_they_differ(page):
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
def test_each_ratio_explains_its_own_denominator(page):
    open_call(page)
    expect(page.locator("#tally-support-detail")).to_contain_text("reservations",
                                                                 timeout=10000)
    expect(page.locator("#tally-decisive-detail")).to_contain_text("blocks")


def test_open_and_resolved_objections_are_shown_apart(page):
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
