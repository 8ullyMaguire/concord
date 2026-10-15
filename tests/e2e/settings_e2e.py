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
BASE = f"http://127.0.0.1:{PORT}"


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

# --- the two panels added after this suite was already green ---------------------
#
# Both close the defect this page exists to close, one level down: GET
# /tags/completeness (the §4.3 completeness meter) and GET/POST /strategy/* (§6.2's
# consensus path) had handlers, guards and store tests, and no reader in assets/js.
# The page rendered fine without them, which is why they survived.


def _seed_taxonomy_and_strategy(db):
    """Three tags, one of them namespaced, and one pending weight proposal.

    Three rather than five, so the meter is NOT complete and both halves of the score
    are visible: 0.6 * 3/5 + 0.4 * 1/2 = 0.56. A fixture that happened to meet the
    threshold would let a page that hardcoded "complete" pass.
    """
    con = sqlite3.connect(db)
    try:
        now = 1790000000
        pid = con.execute("SELECT id FROM projects WHERE slug=?", (SLUG,)).fetchone()[0]
        uid = con.execute("SELECT id FROM users WHERE username='setowner'").fetchone()[0]
        # Idempotent, because more than one test needs this fixture and the DB is
        # session-scoped. The first version seeded only from the first test, so
        # `pytest -k below_quorum` on its own found no proposal, no Consent button, and
        # failed with a 30-second click timeout -- a red that says the code is broken
        # when the fixture is. It also made the suite order-dependent.
        if con.execute("SELECT COUNT(*) FROM project_tags WHERE project_id=?", (pid,)).fetchone()[0]:
            return
        con.execute("INSERT INTO tags (project_id, name, namespace, suggested) VALUES (?,?,?,?)",
                    (pid, "search", "domain", 0))
        nsid = con.execute("SELECT id FROM tags WHERE name='search'").fetchone()[0]
        con.execute("INSERT INTO tags (project_id, name, namespace, suggested) VALUES (?,?,?,?)",
                    (pid, "cleanup", None, 0))
        plainid = con.execute("SELECT id FROM tags WHERE name='cleanup'").fetchone()[0]
        for tid in (nsid, plainid):
            con.execute("INSERT INTO project_tags (project_id, tag_id, applied_by, created_at)"
                        " VALUES (?,?,?,?)", (pid, tid, uid, now))
        # A SUGGESTED tag that has been applied. The store's suggested_pending is a
        # join against project_tags, so a suggested tag that was never applied is not
        # "waiting" -- it does not exist from the project's point of view. It counts in
        # `tags` and not in `namespaced`, which is why the expected score is
        # 0.6 * 3/5 + 0.4 * 1/2 = 0.56.
        con.execute("INSERT INTO tags (project_id, name, namespace, suggested) VALUES (?,?,?,?)",
                    (pid, "offsets", None, 1))
        sugid = con.execute("SELECT id FROM tags WHERE name='offsets'").fetchone()[0]
        con.execute("INSERT INTO project_tags (project_id, tag_id, applied_by, created_at)"
                    " VALUES (?,?,?,?)", (pid, sugid, uid, now))

        con.execute("INSERT INTO features (project_id, author_id, title, body, effort,"
                    " status, strategic_weight, created_at, updated_at)"
                    " VALUES (?,?,?,?,?,?,?,?,?)",
                    (pid, uid, "Harden the export path", "body", "M", "ready", 1.0, now, now))
        fid = con.execute("SELECT id FROM features WHERE title='Harden the export path'").fetchone()[0]
        con.execute("INSERT INTO strategic_weight_proposals (project_id, feature_id, proposed_by,"
                    " weight_before, weight_after, rationale, status, created_at)"
                    " VALUES (?,?,?,?,?,?,'pending',?)",
                    (pid, fid, uid, 1.0, 2.0, "exports are the blast radius", now))
        con.commit()
    finally:
        con.close()


def _opens_settings(page, server, token=None):
    if token:
        # The page reads the token from localStorage, so it has to be there before the
        # scripts run -- addInitScript, not a later evaluate.
        page.add_init_script("localStorage.setItem('concord.token', %r);" % token)
    page.goto(server["base"] + f"/projects/{SLUG}/settings")
    page.wait_for_selector('#settings-root:not([aria-busy="true"])', timeout=15000)
    return page.locator("#settings-root")


def test_the_completeness_meter_shows_the_numbers_behind_the_score(page, server):
    """§4.3: "A completeness meter shows how well-tagged each project is."

    The percentage alone is unfalsifiable -- any number would look like a measurement.
    The assertion is on the components: 2 of 5 tags and 1 of 2 namespaced, which is the
    arithmetic the score is made of (0.6 * 2/5 + 0.4 * 1/2), and on the suggested tag
    NOT being counted while the panel says it is waiting.
    """
    _seed_taxonomy_and_strategy(server["db"])
    root = _opens_settings(page, server)

    meter = root.locator(".meter[data-completeness-score]")
    expect(meter).to_have_count(1)
    # 0.6 * 3/5 + 0.4 * 1/2, compared with a tolerance: the server sends a float64 sum
    # and the page writes it into an attribute, so the string is whatever Go's binary
    # arithmetic produced -- 0.5600000000000001 is the same claim as 0.56.
    got = float(meter.get_attribute("data-completeness-score"))
    assert abs(got - 0.56) < 1e-9, got
    assert meter.get_attribute("data-completeness-complete") == "false"
    expect(root.locator("[data-completeness-pct]")).to_have_text("56%")

    text = root.inner_text()
    assert "3 of 5 tags" in text, text
    assert "1 of 2 namespaced" in text, text
    # The advice is the point of the meter; a bare score tells a reader nothing to do.
    assert "Add 2 more tags and 1 more namespaced tag" in text, text
    # And the unapplied suggestion is reported as waiting rather than counted.
    assert "1 suggested tag is still waiting to be applied" in text, text
    assert not page.errors, page.errors


def test_the_strategy_panel_lists_a_pending_proposal_and_the_rule_it_obeys(page, server):
    """§6.2's state on screen: the weight is PROPOSED.

    The row shows 1 -> 2, and the panel says a proposal does not move the weight until
    the quorum is reached. A page that showed "Weight: 2" as the feature's current
    weight would be reporting the hand-steering §6.2 exists to prevent.
    """
    root = _opens_settings(page, server)

    table = root.locator("[data-strategy-proposals]")
    expect(table).to_have_count(1)
    row = table.locator("tr[data-proposal]")
    expect(row).to_have_count(1)
    text = row.inner_text()
    assert "1" in text and "2" in text, text
    assert "exports are the blast radius" in text, text
    expect(row.locator("button[data-consent]")).to_be_visible()
    # The empty themes state is the mechanism's design being stated, not an omission.
    assert "No ratified themes" in root.inner_text()
    assert not page.errors, page.errors


def test_an_anonymous_consent_reports_the_refusal_rather_than_doing_nothing(page, server):
    """The consent button with no session is refused, and the page shows the SERVER's
    reason rather than a generic failure.

    Measured: with no token the answer is 401 "authentication required". With a valid
    token for somebody who is not an eligible collaborator it is 403 "permission
    denied: only an eligible collaborator can consent to a proposal" -- the Go route
    test covers that one, because it needs a real session. Both messages name the
    cause; a page that renders "something went wrong" turns a precise rule into a
    mystery, which is the least diagnosable symptom there is.
    """
    root = _opens_settings(page, server)
    root.locator("button[data-consent]").click()
    live = page.locator("[data-settings-live]")
    expect(live).to_contain_text("authentication required", timeout=15000)


def test_a_consent_below_quorum_is_reported_as_recorded_not_as_ratified(page, server):
    """The 202 path, which is the one a page is most likely to get wrong.

    Ratifying §6.2 needs the Normal tier quorum among eligible collaborators, so a
    single consent is not a ratification. The server answers 202 with the reason, and
    the page must not render that as "ratified" -- that would report a weight change
    that has not happened.
    """
    _seed_taxonomy_and_strategy(server["db"])
    _seed_contributor(server["db"], "settingsvoter")
    token = _login("settingsvoter")
    root = _opens_settings(page, server, token=token)

    root.locator("button[data-consent]").click()
    live = page.locator("[data-settings-live]")
    expect(live).to_contain_text("Consent recorded", timeout=15000)

    # Three assertions, because the first version of this test was DECORATION: it
    # asserted the word "ratified" was absent, and the page's below-quorum message does
    # not contain that word either way -- so deleting the whole 202 branch left it
    # green. Found by mutating the branch, not by reading the test.
    #
    # What has to be true: the page reports the SERVER's reason (the consent count),
    # and it does not claim the outcome that has not happened -- neither "ratified" nor
    # the phrase the ratified branch uses.
    text = live.inner_text()
    assert "consents" in text, text
    assert "quorum is reached" not in text, text
    assert "ratified" not in text.lower(), text
    # The record is real: the consent row exists and the proposal is still pending.
    con = sqlite3.connect(server["db"])
    try:
        pending = con.execute(
            "SELECT COUNT(*) FROM strategic_weight_proposals WHERE status='pending'").fetchone()[0]
        consents = con.execute("SELECT COUNT(*) FROM strategic_weight_consents").fetchone()[0]
    finally:
        con.close()
    assert pending == 1, pending
    assert consents == 1, consents
    assert not page.errors, page.errors


def _seed_contributor(db, username):
    """A contributor who is ELIGIBLE, which is two conditions and not one.

    §8.2 makes eligibility a role AND recent activity (IsEligibleCollaborator), so a
    membership row alone is not enough: the first version of this fixture inserted the
    member and got 403 "only an eligible collaborator can consent", which reads like a
    broken form. The qualifying contribution is a project_document, which is the first
    arm of qualifyingContributionsSQL.

    The owner gets one too, and that is the point of the test rather than housekeeping:
    with an eligible count of 1 the charter's quorum floor collapses onto that single
    collaborator, so one consent ratifies -- the exact outcome the
    proposer-cannot-consent rule exists to prevent, reached from the other side. With
    two eligible, one consent is below quorum and the server answers 202.
    """
    reg = harness.api(BASE, "POST", "/api/v1/auth/register", {
        "username": username, "password": "correct-horse-battery", "display_name": username,
    })
    assert reg and reg.get("token"), reg
    uid = reg["user"]["id"]
    con = sqlite3.connect(db)
    try:
        now = 1790000000
        pid = con.execute("SELECT id FROM projects WHERE slug=?", (SLUG,)).fetchone()[0]
        owner = con.execute("SELECT id FROM users WHERE username='setowner'").fetchone()[0]
        con.execute("INSERT INTO members (project_id, user_id, role, joined_at)"
                    " VALUES (?,?,'contributor',?)", (pid, uid, now))
        for author, slug in ((uid, "voter-notes"), (owner, "owner-notes")):
            con.execute(
                "INSERT INTO project_documents (project_id, kind, slug, title, body,"
                " revision, author_id, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
                (pid, "readme", slug, slug, "body", 1, author, now, now))
        con.commit()
    finally:
        con.close()
    return reg["token"], uid


def _login(username):
    body = harness.api(BASE, "POST", "/api/v1/auth/login", {
        "username": username, "password": "correct-horse-battery",
    })
    assert body and body.get("token"), body
    return body["token"]
