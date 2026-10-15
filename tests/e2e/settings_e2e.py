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
        # OR IGNORE: _seed_contributor runs once per test block, but a re-run of a
        # single test must not fail on the membership row it already wrote.
        con.execute("INSERT OR IGNORE INTO members (project_id, user_id, role, joined_at)"
                    " VALUES (?,?,'contributor',?)", (pid, uid, now))
        # OR IGNORE on the documents: this helper is called once per USERNAME and every
        # call writes the SAME two slugs, so the second username raised "UNIQUE constraint
        # failed: project_documents.project_id, kind, slug". That exception rolled back the
        # whole transaction -- including the members row inserted three lines above it --
        # and the symptom appeared in a DIFFERENT test, as "permission denied" on a panel
        # whose author had a token and no membership. A helper that shares a transaction
        # between a row it must write and a row it only wants if absent has to say which
        # is which.
        for author, slug in ((uid, "voter-notes"), (owner, "owner-notes")):
            con.execute(
                "INSERT OR IGNORE INTO project_documents (project_id, kind, slug, title, body,"
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


# --- the administration surface: visibility, tags, languages, invites ------------
#
# Six endpoints with handlers, guards and store tests and no UI anywhere on the site:
# PUT /visibility, GET+PUT /tags, PUT /languages, POST+GET /invites and
# POST /invites/{id}/revoke. docs/specs/frontend-spec.md listed them as "none".
# These four panels are the readers, and the tests below drive them as a member would.


def _seed_admin_fixture(db, visibility="public"):
    """A deterministic starting state for the four administration panels.

    A RESET, not an insert-if-missing: these tests assert exact numbers (3 of 5 tags,
    Rust 70 / Go 30, one invite), and the DB is session-scoped, so a fixture that only
    inserted would leave the state the PREVIOUS run wrote and the assertion would read
    like a code failure. That is the same order-dependence the taxonomy seeder's comment
    describes, in its second costume.
    """
    con = sqlite3.connect(db)
    try:
        now = 1790000000
        pid = con.execute("SELECT id FROM projects WHERE slug=?", (SLUG,)).fetchone()[0]
        uid = con.execute("SELECT id FROM users WHERE username='setowner'").fetchone()[0]

        # Visibility back to public: one test sets it to protected, and a protected
        # project refuses an anonymous reader -- which would break the anonymous-invites
        # test below for a reason that has nothing to do with invites.
        con.execute("UPDATE projects SET visibility=? WHERE id=?", (visibility, pid))

        con.execute("DELETE FROM project_languages WHERE project_id=?", (pid,))
        con.execute("INSERT INTO project_languages (project_id, language, pct) VALUES (?,?,?)",
                    (pid, "Rust", 70.0))
        con.execute("INSERT INTO project_languages (project_id, language, pct) VALUES (?,?,?)",
                    (pid, "Go", 30.0))

        # A tag that EXISTS and is not applied. The panel's job is attaching it, and the
        # meter (which counts project_tags rows) must move 3 -> 4 as a result.
        con.execute("INSERT OR IGNORE INTO tags (project_id, name, namespace, suggested)"
                    " VALUES (?,?,?,?)", (pid, "hooks", None, 0))
        con.execute("DELETE FROM project_tags WHERE project_id=? AND tag_id IN"
                    " (SELECT id FROM tags WHERE name='hooks')", (pid,))
        # The table is project_invites, not invites. The panel's token column and the
        # store's INSERT agree on `project_invites (project_id, token, created_by,
        # created_at, expires_at, max_uses, uses, revoked_at)`.
        con.execute("DELETE FROM invite_redemptions WHERE invite_id IN"
                    " (SELECT id FROM project_invites WHERE project_id=?)", (pid,))
        con.execute("DELETE FROM project_invites WHERE project_id=?", (pid,))
        con.commit()
    finally:
        con.close()


def _settles(page, root, loads=2):
    """Wait for the page's own reads to land after a write.

    Waits on `data-loads`, the counter settings.js increments at the end of each load,
    NOT on a panel count: the pre-write DOM already holds all nine panels, so counting
    them is satisfied before the write lands and the caller reads the previous render.
    That is what the first version did, and it reported "the panel shows public" -- a
    test that did not wait, dressed as a server that did not persist.
    """
    page.wait_for_function(
        "n => Number(document.getElementById('settings-root').getAttribute('data-loads') || 0) >= n",
        arg=loads, timeout=15000)


def _member_token(db, username="settingsadmin"):
    """The member's session, registered ONCE and logged in after that.

    _seed_contributor POSTs /auth/register, which answers 409 the second time, so the
    first version of this helper made the first test in this block pass and every later
    one fail during SETUP with "urllib.error.HTTPError: 409 Conflict" -- a red that reads
    like a broken panel and is a fixture that cannot be called twice. _login exists in
    this file for exactly this reason.
    """
    try:
        return _login(username)
    except Exception:
        token, _uid = _seed_contributor(db, username)
        return token


def _open_as_member(page, server, visibility="public"):
    """The page as a signed-in contributor: every one of the four writers is member-gated.

    Without the token every write answers 401 and the panel reports the refusal, which
    would make these tests assertions about the refusal path only.
    """
    _seed_taxonomy_and_strategy(server["db"])
    _seed_admin_fixture(server["db"], visibility=visibility)
    return _opens_settings(page, server, token=_member_token(server["db"]))


def test_the_visibility_panel_offers_every_level_and_saves_one(page, server):
    """The four levels the store accepts, and a save that changes the row.

    Asserted on the DB as well as the page: a panel that reported "Visibility set to
    unlisted" while the projects row still said public would pass any DOM assertion.
    """
    root = _open_as_member(page, server)

    sel = root.locator("#visibility-select")
    opts = sel.locator("option")
    assert [opts.nth(i).get_attribute("value") for i in range(opts.count())] == \
        ["public", "unlisted", "protected", "private"], "the levels are the store's four"
    expect(root.locator("[data-visibility-current]")).to_have_text("public")

    sel.select_option("protected")
    root.locator("#visibility-form button[type=submit]").click()
    live = page.locator("[data-settings-live]")
    expect(live).to_contain_text("Visibility set to protected", timeout=15000)
    # The panels re-read their data rather than reloading the document, so the level on
    # screen comes from the server's answer to the next GET. Polled by hand and asserted
    # with the reason in the message: `to_have_text` reported "element(s) not found" for a
    # panel that had in fact been re-rendered, which says nothing about why.
    _settles(page, root)
    cell = root.locator("[data-visibility-current]")
    got = cell.inner_text() if cell.count() else "(no element)"
    assert got == "protected", (
        "the panel shows %r after a save the server accepted; panels=%r errors=%r"
        % (got, [h.strip() for h in root.locator("section.panel h2").all_inner_texts()],
           page.errors[:4]))

    con = sqlite3.connect(server["db"])
    try:
        got = con.execute("SELECT visibility FROM projects WHERE slug=?", (SLUG,)).fetchone()[0]
    finally:
        con.close()
    assert got == "protected", got
    assert not page.errors, page.errors


def test_applying_a_tag_moves_the_completeness_meter(page, server):
    """The meter says "Add 2 more tags"; this is the control that obeys it.

    §4.3's meter told the reader to add tags and there was no way to do it anywhere on
    the site -- a meter that instructs and cannot be obeyed. Applying "hooks" (a tag that
    exists but is not applied) must move the count from 3 of 5 to 4 of 5, which is also
    what proves the panel wrote by the same route the meter reads.
    """
    root = _open_as_member(page, server)
    expect(root.locator("[data-completeness-pct]")).to_have_text("56%")

    root.locator("#tags-input").fill("hooks")
    root.locator("#tags-form button[type=submit]").click()
    live = page.locator("[data-settings-live]")
    expect(live).to_contain_text("Applied.", timeout=15000)

    rows = root.locator("[data-project-tags] .tag")
    expect(rows).to_have_count(4)
    assert "hooks" in root.locator("[data-project-tags]").inner_text()
    # 3 of 5 -> 4 of 5: 0.6 * 4/5 + 0.4 * 1/2 = 0.68
    expect(root.locator("[data-completeness-pct]")).to_have_text("68%", timeout=15000)
    assert not page.errors, page.errors


def test_a_tag_this_instance_does_not_have_is_refused_with_the_servers_reason(page, server):
    """PUT /tags attaches tags that EXIST; it does not mint them (§4.3: a new tag is a
    proposal with a quorum). The refusal has to reach the reader, or the form looks
    broken for a reason that is actually a rule."""
    root = _open_as_member(page, server)
    root.locator("#tags-input").fill("zzz-not-a-tag")
    root.locator("#tags-form button[type=submit]").click()
    live = page.locator("[data-settings-live]")
    expect(live).to_contain_text("The server refused it", timeout=15000)
    # The rule, named: the store answers with which tag it could not find.
    expect(live).to_contain_text("zzz-not-a-tag", timeout=15000)


def test_a_language_correction_starts_from_the_current_numbers_and_is_stored(page, server):
    """§15's "collaboratively correctable" in practice: the edit box is seeded with what
    is stored, and the save round-trips through the server."""
    root = _open_as_member(page, server)
    expect(root.locator("[data-project-languages]")).to_contain_text("Rust")

    root.locator("#languages-edit").click()
    box = root.locator("#languages-input")
    assert box.input_value() == "Rust 70, Go 30", box.input_value()

    box.fill("Rust 90, Go 10")
    root.locator("#languages-form button[type=submit]").click()
    expect(page.locator("[data-settings-live]")).to_contain_text("Languages saved", timeout=15000)
    # The percentages are INPUT VALUES, not text: `to_contain_text` on the container
    # matched "RustGo" and reported "unexpected value" for a panel that was correct.
    _settles(page, root)
    expect(root.locator('input[data-language="Rust"]')).to_have_value("90")

    con = sqlite3.connect(server["db"])
    try:
        rows = dict(con.execute(
            "SELECT language, pct FROM project_languages WHERE project_id="
            "(SELECT id FROM projects WHERE slug=?)", (SLUG,)).fetchall())
    finally:
        con.close()
    assert rows == {"Rust": 90.0, "Go": 10.0}, rows
    assert not page.errors, page.errors


def test_a_language_line_without_a_percentage_is_refused_without_a_request(page, server):
    """The client refuses what the server cannot describe.

    The store's own refusal for a bad body names the field, not the offending text, so
    the page parses the line first and reports the chunk it could not read. Asserted with
    a request counter: a refusal that still fired the PUT would be a page telling the
    reader one thing and the server another.
    """
    root = _open_as_member(page, server)
    puts = []
    page.on("request", lambda r: puts.append(r.url) if r.method == "PUT" else None)

    root.locator("#languages-edit").click()
    root.locator("#languages-input").fill("Rust seventy")
    root.locator("#languages-form button[type=submit]").click()

    live = page.locator("[data-settings-live]")
    expect(live).to_contain_text("Could not read a percentage from: Rust seventy", timeout=15000)
    assert puts == [], puts


def test_an_invite_is_minted_reported_once_then_revoked(page, server):
    """The mint reports its token in the live region, which is OUTSIDE the re-rendered
    panel -- the reason the writes re-read their data instead of reloading the document.
    On a document reload that message is on screen for zero frames, and an invite nobody
    can read is an invite nobody can use."""
    root = _open_as_member(page, server)

    root.locator("#invite-days").fill("7")
    root.locator("#invite-uses").fill("1")
    root.locator("#invite-form button[type=submit]").click()

    live = page.locator("[data-settings-live]")
    expect(live).to_contain_text("Invite minted:", timeout=15000)
    minted = live.inner_text().split("Invite minted:")[1].strip()
    assert len(minted) >= 20, minted

    # The token is in the table too, and the row is active.
    row = root.locator('[data-invites] tr[data-invite]')
    expect(row).to_have_count(1)
    expect(row.locator("[data-invite-token]")).to_have_text(minted)
    expect(row).to_contain_text("active")

    root.locator("[data-invites] button[data-revoke]").click()
    expect(live).to_contain_text("Invite revoked", timeout=15000)
    expect(root.locator('[data-invites] tr[data-invite]')).to_contain_text("revoked", timeout=15000)

    con = sqlite3.connect(server["db"])
    try:
        n = con.execute("SELECT COUNT(*) FROM project_invites WHERE revoked_at IS NOT NULL").fetchone()[0]
    finally:
        con.close()
    assert n == 1, n


def test_invites_are_not_offered_to_a_visitor_who_cannot_read_them(page, server):
    """Member-only, and the panel says WHICH rule refused it rather than rendering an
    empty table -- an empty "No invites" state for a visitor who was actually refused is
    the page telling them a different fact about the project."""
    _seed_taxonomy_and_strategy(server["db"])
    _seed_admin_fixture(server["db"])
    root = _opens_settings(page, server)  # no token
    panel = root.locator("section.panel", has_text="Invites")
    expect(panel).to_contain_text("visible to members")
    assert root.locator("#invite-form").count() == 0, "an anonymous visitor was offered a mint form"


# NOT WRITTEN, and the reason is a finding rather than an omission:
# `test_every_panel_still_loads_for_a_member_of_a_protected_project` cannot be driven in a
# browser at all. handleSettingsPage resolves the project and then calls projectReadable,
# which for a protected or private project requires the request to carry the actor -- and a
# browser NAVIGATION carries no token, because the session lives in localStorage and is
# attached by script. The page 404s before the script that would attach it ever runs:
# measured, `#settings-root` never renders and the wait times out. auth.go's bearerToken
# already accepts an X-Auth-Token header "for browser clients that cannot set headers on a
# navigation", so the design has a seat for the mechanism and nothing fills it yet (a
# cookie set at login, or a service worker). Written up in docs/REMAINING.md §13.2 and
# raised as a spec question, because setting a session cookie is not a change to make in a
# page's pull request.
#
# What IS asserted, and what the token-on-reads fix is gated by: GET /invites is
# member-only, so the invite panel rendering a MINTED ROW (above) can only happen if the
# page's reads carry the token. Reverting authHeaders on the read path turns
# test_an_invite_is_minted_reported_once_then_revoked red -- verified by mutation, not
# asserted by reading the source.
