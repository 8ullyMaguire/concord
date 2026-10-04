"""The audit log viewer (docs/plans/audit-log.md step A5).

Four browser tests, and the reason each exists is a way this page can be wrong
that looks correct:

  - the page shows every row, so "the filter control is there" proves nothing;
  - a filter that returns everything looks exactly like a filter that worked;
  - "no audit activity" and "your filter matched nothing" are different facts
    about a project and must not render the same words;
  - without the count line a reader cannot tell which of those two it is.

THE FIXTURE SEEDS THROUGH SQLITE, NEVER THROUGH THE ENDPOINTS UNDER TEST. A
browser test whose data arrives via the API it is testing cannot fail for the
reason it exists -- if the write path is broken, the page is empty and the page
assertions pass anyway.

EVERY BROWSER CONTEXT GETS ITS OWN X-FORWARDED-FOR. clientKey() honours the
header from loopback, so contexts sharing one IP share a rate-limit bucket and
the limiter answers {"error":"rate limit exceeded"} AS THE PAGE BODY -- which
reads as a broken page rather than as a throttle.
"""

import itertools
import sqlite3
import tempfile
import time

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

PORT = 8484
BASE = f"http://127.0.0.1:{PORT}"

SLUG = "audit-demo"
PRIVATE = "audit-private"
EMPTY = "audit-empty"

_ips = itertools.count(1)


def seed(db):
    """Three projects: one with a mixed log, one private, one with no log."""
    con = sqlite3.connect(db)
    cur = con.cursor()
    now = time.time()

    owner = cur.execute(
        "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
        ("audit-owner", "Olive Owner", now)).lastrowid
    ghost = cur.execute(
        "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
        ("audit-ghost", "Ghost Account", now)).lastrowid

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
    project(EMPTY)

    def audit(project_id, actor, action, detail, at):
        # created_at is explicit rather than defaulted: the store orders by it,
        # and a fixture that relies on insert speed cannot assert an order.
        cur.execute(
            "INSERT INTO audit_log (project_id, actor_id, action, entity,"
            " entity_id, detail, created_at) VALUES (?,?,?,?,?,?,?)",
            (project_id, actor, action, "project", pid, detail, at))

    # Two actions, so filtering has something to exclude, and the names are
    # ordered deliberately unlike the timestamps so a page that renders in
    # insertion order is caught.
    audit(pid, owner, "set_visibility", "made the project public", now - 300)
    audit(pid, owner, "create_invite", "invited a collaborator", now - 200)
    audit(pid, ghost, "merge", "merged request 41", now - 100)

    # A row with a literal % in the detail: the search escapes LIKE wildcards,
    # and a page that sent the text unescaped would match this row for every
    # search. Two rows here would make that visible; one is enough to show the
    # escape is not dropping the match.
    audit(pid, owner, "set_weight", "weight is now 50% of the default", now - 50)

# A user whose DISPLAY NAME carries HTML, so the "Who" column is reached by
    # something other than an inert fixture. Without this the unescaped-name
    # mutant survives: esc() and no esc() render "Olive Owner" identically.
    htmluser = cur.execute(
        "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
        ("audit-html", "<b>Bold</b><script>window.__nameXSS = true</script>", now)).lastrowid
    audit(pid, htmluser, "<img src=x onerror=\"boom\">",
          "an action name carrying html", now - 8)

    # A row whose detail carries HTML and a script tag. `detail` is free text
    # from twenty-odd AddAudit call sites, so the viewer's esc() is the only
    # thing standing between a caller's string and the reader's DOM. Every
    # other fixture row here is inert, which is exactly why an unescaped-detail
    # mutant SURVIVED the first attempt at this suite: with no HTML in the
    # data, esc() and no esc() render identically.
    audit(pid, owner, "set_visibility",
          "<script>window.__auditXSS = true</script><img src=x onerror=\"boom\">",
          now - 5)

    # A row whose actor has no users row, so the page must render it as a
    # deleted account rather than as an id.
    audit(pid, 987654, "revoke_invite", "by an account that no longer exists",
          now - 10)

    con.commit()
    con.close()


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-audit-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-audit-e2e-log-")
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
    ctx = browser.new_context(
        extra_http_headers={"X-Forwarded-For": f"10.99.{next(_ips) // 256}.{next(_ips) % 256}"})
    pg = ctx.new_page()
    pg.errors = []
    pg.on("pageerror", lambda e: pg.errors.append("PAGEERROR: " + str(e)))
    pg.on("console", lambda m: pg.errors.append("CONSOLE: " + m.text)
          if m.type == "error" else None)
    pg.goto(BASE + "/")
    return pg


def open_audit(pg, slug=SLUG):
    pg.goto(f"{BASE}/projects/{slug}/audit")
    expect(pg.locator("#audit-root")).to_have_attribute("aria-busy", "false",
                                                        timeout=15000)


def apply_filter(pg, action=None, q=None, expect_rows=None):
    """Apply a filter and WAIT for the list it produced.

    Waiting is not optional. Clicking "Apply" re-renders asynchronously, and the
    old DOM is still on screen when the click returns, so an assertion run
    immediately afterwards reads the PREVIOUS render. The count test failed on
    first run this way, reporting "Showing all 5 entries" for a list the test
    had just filtered to one -- the assertion was looking at the render before
    the one it asked for.

    The wait is on the ROW COUNT, not on a timeout and not on a substring.
    expect_rows is what the filter is supposed to produce, so waiting for it is
    waiting for the actual outcome. A wait that merely slept long enough would
    be a flake generator, and the first version's "the count line mentions 'of'"
    test passes against the UNFILTERED render too, because the phrase appears
    there as well.
    """
    if action is not None:
        pg.select_option("#audit-action", action)
    if q is not None:
        pg.fill("#audit-q", q)
    pg.click("#audit-apply")
    if expect_rows is None:
        # The caller asserts nothing afterwards, so there is no state to wait for
        # -- this only keeps the click from overlapping the next action. Every path
        # that DOES assert goes through wait_for_function below, which is the real
        # synchronisation.
        pg.wait_for_timeout(300)
        return
    pg.wait_for_function(
        """(want) => document.querySelectorAll('table tbody tr').length === want""",
        arg=expect_rows,
        timeout=15000)


# --- the page ---------------------------------------------------------------

def test_the_audit_page_lists_the_seeded_entries(page):
    open_audit(page)
    # Every seeded action, not just the first: a page that renders one row and
    # stops satisfies "the list works".
    #
    # Asserted against the tbody as a whole, not against "table tbody tr".
    # A locator matching five rows is a strict-mode violation under
    # to_contain_text, and the resulting error names the ROWS rather than the
    # missing text -- so the failure reads "resolved to 5 elements" and looks
    # like a selector problem when the assertion passed against a page that
    # rendered every action correctly.
    tbody = page.locator("table tbody")
    expect(tbody).to_be_visible(timeout=10000)
    for action in ["set_visibility", "create_invite", "merge", "set_weight",
                   "revoke_invite"]:
        expect(tbody).to_contain_text(action, timeout=10000)

    # The display name, resolved from the actor id. The viewer shows ids nobody
    # can act on otherwise.
    expect(tbody).to_contain_text("Olive Owner")
    expect(tbody).to_contain_text("Ghost Account")
    # And an unresolved actor is a dash, not "user 987654".
    expect(tbody).to_contain_text("deleted account")
    assert not page.errors, f"JS errors: {page.errors}"


def test_filtering_by_action_narrows_the_list(page):
    open_audit(page)
    before = page.locator("table tbody tr").count()
    assert before >= 7, f"the unfiltered page listed {before} rows, expected the 7 seeded"

    apply_filter(page, action="merge", expect_rows=1)

    # The negative assertion is the load-bearing one. Asserting only that the
    # count fell passes under a filter that returns the whole table sliced to
    # fit, and under one that returns nothing but still shrinks.
    expect(page.locator("table tbody")).to_contain_text("merge")
    for absent in ["set_visibility", "create_invite", "revoke_invite"]:
        expect(page.locator("table tbody")).not_to_contain_text(absent)


def test_the_count_reports_the_filtered_total(page):
    open_audit(page)
    unfiltered = page.locator("[data-audit-count]").text_content()
    assert "7" in unfiltered, f"the unfiltered count reads {unfiltered!r}, want all 7 seeded"

    apply_filter(page, action="merge", expect_rows=1)
    expect(page.locator("[data-audit-count]")).to_be_visible(timeout=10000)

    text = page.locator("[data-audit-count]").text_content()
    # "1 of 7" and the excluded count. The API's `total` is COUNT(*) over the
    # FILTERED set (spec §4), so a page that compares shown against page.total
    # always reads "Showing all N entries" -- claiming the filter excluded
    # nothing when it excluded six. This failed on first run with exactly that
    # string.
    assert "1" in text and "7" in text, (
        f"the count line reads {text!r}: it must show 1 of 7, not a single number")
    assert "excluded 6" in text, (
        f"the count line reads {text!r} and does not say how many rows the filter removed")


def test_a_filter_matching_nothing_says_so(page):
    open_audit(page)
    apply_filter(page, q="no-such-thing-anywhere", expect_rows=0)

    body = page.locator("#audit-root").text_content()
    assert "Nothing matches this filter" in body, (
        f"a filter matching nothing did not say so: {body!r}")
    # And it must NOT read as "this project has no history" -- the project has
    # five rows, and conflating the two is the bug this test exists for.
    assert "No audit activity yet" not in body, (
        "a filtered empty result claims the project has no audit activity, which is false")


def test_a_project_with_no_log_says_it_has_no_activity(page):
    open_audit(page, EMPTY)
    body = page.locator("#audit-root").text_content()
    assert "No audit activity yet" in body, (
        f"an empty project did not say it has no activity: {body!r}")
    # The mirror image: with no filter applied, this is NOT the filtered message.
    assert "Nothing matches this filter" not in body


def test_searching_for_a_literal_percent_finds_the_row(page):
    open_audit(page)
    apply_filter(page, q="50%", expect_rows=1)
    expect(page.locator("table tbody")).to_contain_text("50% of the default")

    # The load-bearing half. Without ESCAPE, a search for "50%" matches NOTHING
    # and the row above disappears; and a search whose wildcard is left live
    # matches every row instead. Both failures are silent 200s.
    apply_filter(page, q="50x", expect_rows=0)


def test_the_private_projects_audit_page_is_not_reachable(page):
    resp = page.goto(f"{BASE}/projects/{PRIVATE}/audit")
    assert resp.status == 404, (
        f"a private project's audit page answered {resp.status}: the shell alone "
        f"confirms the slug exists")


# The API behind the page, asserted at the same time as the page so a browser
# test and the endpoint cannot drift apart unnoticed.
def test_the_api_behind_the_page_is_reachable_without_an_account(page):
    resp = page.request.get(f"{BASE}/api/v1/projects/{SLUG}/audit")
    assert resp.status == 200, f"the audit API answered {resp.status} anonymously"
    out = resp.json()
    assert out["total"] == 7, f"total is {out['total']}, want the 7 seeded rows"
    assert out["limit"] == 50, f"limit is {out['limit']}, want the documented default"

# The XSS test. `detail` is free text from twenty-odd AddAudit call sites, and
# the fixture seeds one carrying a <script> tag and an onerror handler.
def test_a_detail_containing_html_is_escaped_not_executed(page):
    open_audit(page)
    # The text is SHOWN, as text.
    expect(page.locator("table tbody")).to_contain_text("<script>", timeout=10000)
    # And nothing ran. This is the assertion that matters: with esc() removed the
    # string above still renders, and only this catches it.
    assert page.evaluate("() => window.__auditXSS === undefined"), (
        "a <script> tag in an audit detail EXECUTED")
    # No element was created from the payload either.
    assert page.evaluate("() => document.querySelectorAll('table tbody script').length") == 0, (
        "an audit detail produced a live <script> element in the page")
    # Nor from the action name or the display name, escaped by the same esc()
    # and equally unproven: both of those fixtures were inert text, and esc()
    # and no esc() render inert text identically.
    assert page.evaluate("() => window.__nameXSS === undefined"), (
        "a <script> tag in a display name EXECUTED")
    assert page.evaluate("() => document.querySelectorAll('table tbody img').length") == 0, (
        "an audit action name or display name produced a live <img> element")
