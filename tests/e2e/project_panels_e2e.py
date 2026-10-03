"""End-to-end tests for the project-page panels (§4.10), driving real headless
Chromium.

Prerequisites:  make build
Run:            pytest tests/e2e/project_panels_e2e.py -v

Why these exist separately from the Go tests in
internal/httpapi/project_surfaces_test.go and
internal/httpapi/project_panels_script_test.go:

  - the Go API tests prove the endpoints answer with the right JSON
  - the Go script tests grep project.js for the rules it must contain
  - NEITHER proves the browser puts the two together

That gap has a history on this repository. `finder.html` shipped unregistered and
every server-rendered page test passed against markup that never reached the
browser; `finder.js` called show() without render() and the page looked loaded
with an empty picker. Both were "correct" at every layer that was actually
tested. So these tests read the RENDERED DOM, and they assert the distinctions --
disputed vs confirmed, no-claim vs unknown, rate vs no-rate -- because those are
exactly what a fetch-and-ignore or a wrong CSS class destroys silently.

The fixture seeds the database directly with sqlite rather than through the API.
A browser test whose data comes from the endpoints under test cannot fail for the
reason it exists: a bug in the write path would produce an empty panel and the
assertion "the panel is empty" would pass.
"""

import os
import sqlite3
import tempfile
import time

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

PORT = 8423
BASE = f"http://127.0.0.1:{PORT}"


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-panels-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-panels-e2e-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    seed(db)
    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


# The rate limiter buckets by client key at 100 requests/minute, and
# clientKey() honours X-Forwarded-For from loopback -- so every test sharing
# 127.0.0.1 shares one bucket, and a suite that opens ~8 pages with 4 requests
# each blows the budget partway through and starts failing with
# `{"error":"rate limit exceeded"}` rendered as the page body.
#
# The finder suite already solved this with a per-test counter; this is the same
# mechanism, copied rather than abstracted, because the two suites run in
# separate pytest processes (each browser suite opens its own sync_playwright)
# and a shared helper there would be a global fixture neither of them would
# otherwise load.
_xff_counter = {"n": 0}


@pytest.fixture()
def page(browser):
    _xff_counter["n"] += 1
    n = _xff_counter["n"]
    ip = f"10.33.{n // 200}.{n % 200}"
    ctx = browser.new_context(extra_http_headers={"X-Forwarded-For": ip})
    pg = ctx.new_page()
    # Any JS exception is a failure. A TypeError in project.js renders an empty
    # div and every "the panel says X" assertion below would fail with a message
    # pointing at the data instead of at the script.
    errors = []
    pg.on("pageerror", lambda e: errors.append(str(e)))
    pg.on("console", lambda m: errors.append(m.text) if m.type == "error" else None)
    pg.errors = errors
    yield pg
    ctx.close()


def seed(db):
    """One project per shape the panels have to tell apart.

    Shapes, and why each exists:

      panels-confirmed   two confirmations -> state `confirmed`
      panels-disputed    two contests      -> state `disputed`, value kept
      panels-unasserted  catalog capability nobody claimed -> `asserted: false`
      panels-empty       no capabilities, no field reports at all
      panels-private     full data, but private -> must not render for a stranger

    `panels-unasserted` also gets an ASSERTED `unknown` on a different
    capability, because the pair is the whole distinction: two rows that both
    need to be visible and must not be confusable.
    """
    con = sqlite3.connect(db)
    cur = con.cursor()
    now = time.time()

    def user(username, display=None):
        cur.execute(
            "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
            (username, display or username, now))
        return cur.lastrowid

    def project(slug, visibility="public"):
        # Column list checked against migrations 0001 (slug/name/description/
        # governance_model/license/created_at/updated_at), 0009 (visibility) and
        # the members table. `governance_model` is CHECK-constrained to
        # 'collective' or 'maintainer_led'; a third value is rejected at insert.
        cur.execute(
            "INSERT INTO projects (slug, name, description, governance_model,"
            " license, visibility, created_at, updated_at)"
            " VALUES (?,?,?,'maintainer_led','MIT',?,?,?)",
            (slug, slug.replace("-", " ").title(),
             f"Fixture project {slug}", visibility, now, now))
        pid = cur.lastrowid
        cur.execute(
            "INSERT INTO members (project_id, user_id, role, joined_at)"
            " VALUES (?,?,'owner',?)", (pid, owner_id, now))
        return pid

    def capability(key, label, category):
        # `capabilities.key` is the PRIMARY KEY (TEXT), not an autoincrement id,
        # and `capability_assertions.capability` REFERENCES that TEXT column
        # directly. So a capability is referred to by its key everywhere; there is
        # no capability id to look up.
        # "values" is double-quoted because VALUES is a SQLite reserved word --
        # the migration does the same and the unquoted form is a syntax error.
        cur.execute(
            "INSERT INTO capabilities (key, label, category, kind, \"values\","
            " created_at) VALUES (?,?,?,'boolean',?,?)",
            (key, label, category,
             '["yes","partial","no","unknown"]', now))

    def assert_cap(pid, cap_key, value, evidence, author):
        cur.execute(
            "INSERT INTO capability_assertions"
            " (project_id, capability, value, evidence, asserted_by, asserted_at)"
            " VALUES (?,?,?,?,?,?)", (pid, cap_key, value, evidence, author, now))
        return cur.lastrowid

    def confirm(assertion_id, user_id, agrees):
        # The column is `confirmed`, not `agrees`, and the timestamp is `at`.
        cur.execute(
            "INSERT INTO capability_confirmations"
            " (assertion_id, user_id, confirmed, at) VALUES (?,?,?,?)",
            (assertion_id, user_id, 1 if agrees else 0, now))

    # The catalog. Shared by every project, exactly as it is instance-wide in
    # production -- a capability exists before any project claims it.
    for key, label, category in [
        ("wip-limits", "WIP limits", "features"),
        ("offline", "Offline use", "features"),
        ("self-hosted", "Self-hosted", "deployment"),
    ]:
        capability(key, label, category)

    owner_id = user("panel-owner")

    # --- panels-confirmed: two independent confirmations (CapQuorum is 2) -----
    pid = project("panels-confirmed")
    a = assert_cap(pid, "wip-limits", "yes", "we enforce it in the board UI",
                   owner_id)
    confirm(a, user("conf1"), True)
    confirm(a, user("conf2"), True)

    # --- panels-disputed: contested, original value preserved ---------------
    pid = project("panels-disputed")
    a = assert_cap(pid, "offline", "no", "we removed it in 2.0", owner_id)
    confirm(a, user("dis1"), False)
    confirm(a, user("dis2"), False)

    # --- panels-unasserted: one row nobody claimed, one asserted `unknown` ---
    # Two rows, two shapes, side by side in one table, which is the only way a
    # test can catch a panel that renders them the same.
    pid = project("panels-unasserted")
    assert_cap(pid, "self-hosted", "unknown", "nobody on the team has tried it",
               owner_id)          # asserted: true,  value "unknown"
    # `wip-limits` and `offline` are left unclaimed for this project on purpose.

    # --- panels-empty: nothing at all ---------------------------------------
    project("panels-empty")

    # --- panels-private: full data, invisible --------------------------------
    pid = project("panels-private", visibility="private")
    a = assert_cap(pid, "wip-limits", "yes", "secret internal claim", owner_id)
    confirm(a, user("pconf1"), True)
    confirm(a, user("pconf2"), True)

    # --- panels-reports: field reports, with a rate that is NOT 0 or 1 -------
    pid = project("panels-reports")
    author = user("reporter")
    for version, env_name, outcome, caveats in [
        ("1.0.0", "arm64", "worked", "fast on small boards"),
        ("1.1.0", "amd64", "worked-with-caveats", "slow past 500 cards"),
        ("1.2.0", "amd64", "abandoned", "migrating was not worth it"),
    ]:
        # No `status` column: removal is the `removed` flag, and it defaults to
        # 0, so a published report is simply one that was never removed.
        cur.execute(
            "INSERT INTO field_reports"
            " (project_id, user_id, version, use_case, environment, outcome,"
            "  caveats, advice, created_at)"
            " VALUES (?,?,?,?,?,?,?,?,?)",
            (pid, author, version, "small teams", env_name, outcome,
             caveats, "fine while it stays small", now))

    con.commit()
    con.close()


def open_project(page, slug):
    """Load a project page and wait for the panels to finish hydrating.

    The wait is on the capability table rather than on load, because the page is
    a mount point that paints instantly and fills in after three fetches. A bare
    `page.goto` + assert would race the fetches, and a test that flakes is a test
    that gets deleted.
    """
    page.goto(f"{BASE}/projects/{slug}")
    # Wait for a panel ROOT, which exists in all three states -- populated, empty,
    # failed. Waiting on .cap-table instead meant a project with no rows never
    # satisfied the wait, and the failure named an element that could never
    # appear rather than the panel that should have.
    expect(page.locator(".panel-capabilities")).to_be_visible(timeout=10000)
    expect(page.locator(".panel-field-reports")).to_be_visible(timeout=10000)


# ---------------------------------------------------------------------------
# The capability panel
# ---------------------------------------------------------------------------

def test_a_confirmed_claim_reads_as_confirmed(page):
    open_project(page, "panels-confirmed")
    row = page.locator(".cap-table tr", has_text="WIP limits")
    expect(row).to_be_visible()
    expect(row.locator(".cap-value")).to_have_text("yes")
    expect(row.locator(".badge-green")).to_contain_text("confirmed")
    # And NOT as disputed. A regression that showed the confirmed badge for both
    # states would satisfy the assertion above.
    expect(row.locator(".badge-amber")).to_have_count(0)
    # The reason somebody gave is shown; without it "yes" is an assertion with
    # nothing behind it.
    expect(row.locator(".cap-evidence")).to_contain_text("enforce it")


def test_a_disputed_claim_reads_as_disputed_and_keeps_its_value(page):
    """The distinction the matrix exists for.

    A dispute is not a retraction and not a refutation. The row must say both
    that people disagree AND what was originally claimed; rendering it as `no`
    would make a contested claim look like a settled refutation.
    """
    open_project(page, "panels-disputed")
    row = page.locator(".cap-table tr", has_text="Offline use")
    expect(row).to_be_visible()

    expect(row.locator(".badge-amber")).to_contain_text("disputed")
    expect(row.locator(".badge-green")).to_have_count(0)
    # The original value survives the dispute.
    expect(row.locator(".cap-value")).to_have_text("no")


def test_a_claim_nobody_has_voted_on_is_not_shown_as_settled(page):
    """A `unknown` assertion is between two other states.

    It is not "no claim yet" -- somebody did record that nobody knows -- and it is
    not a claim like `yes`. If this rendered as a bare value, the capability
    matrix's central claim, that a recorded unknown is information, would not
    survive to the page.
    """
    open_project(page, "panels-unasserted")
    row = page.locator(".cap-table tr", has_text="Self-hosted")
    expect(row).to_be_visible()
    expect(row.locator(".cap-value")).to_have_text("unknown")
    # Not the no-claim treatment...
    expect(row.locator(".cap-unknown")).to_have_count(0)
    # ...and not a confirmed or disputed badge either.
    expect(row.locator(".badge-green")).to_have_count(0)
    expect(row.locator(".badge-amber")).to_have_count(0)


def test_a_capability_nobody_claimed_says_so_and_is_counted(page):
    open_project(page, "panels-unasserted")
    row = page.locator(".cap-table tr", has_text="WIP limits")
    expect(row.locator(".cap-unknown")).to_contain_text("no claim yet")
    expect(row.locator(".cap-value")).to_have_count(0)

    # The unclaimed ones are a contribution queue, so the count is stated.
    # Scoped to the capability panel: the Complaints blurb is also a .section-sub
    # and a bare selector matched that instead.
    expect(page.locator(".panel-capabilities .section-sub")).to_contain_text("2 of 3")


def test_the_panel_lists_every_catalog_capability_not_only_the_claimed_ones(page):
    """The absence is the useful part.

    `ListCapabilitiesForSet` returns only rows that exist, so the panel gets the
    catalog separately and merges. If that merge were dropped, a project with one
    claim would show one row and a reader would see nothing to contribute.
    """
    open_project(page, "panels-unasserted")
    expect(page.locator(".cap-table tbody tr")).to_have_count(3)


def test_a_project_with_no_reports_says_so_and_does_not_invent_a_rate(page):
    """A project nobody has tried has no rate -- not a rate of zero.

    This also pins down a distinction the fixture forced into the open: an empty
    PROJECT is not an empty CATALOG. Capabilities are created instance-wide, so
    a project that has asserted nothing still lists all three of them as
    unclaimed, and "no capability matrix yet" would be a false statement about it.
    """
    open_project(page, "panels-empty")

    # All three catalog capabilities are listed, none claimed.
    expect(page.locator(".panel-capabilities .cap-table tbody tr")).to_have_count(3)
    expect(page.locator(".panel-capabilities .cap-unknown")).to_have_count(3)

    # No rate is displayed at all. A 0% here would say "reporters tried it and it
    # did not work", which is a different and much worse claim.
    expect(page.locator(".fr-summary")).to_have_count(0)
    expect(page.locator("text=No field reports yet")).to_be_visible()

    # And nothing failed: these are empty states, not error states.
    expect(page.locator(".panel-error")).to_have_count(0)


# ---------------------------------------------------------------------------
# The field report panel
# ---------------------------------------------------------------------------

def test_the_outcome_rate_is_shown_with_its_denominator(page):
    """A rate without its sample size is the misleading form.

    "100% worked" and "100% of 1 reporter" are different claims, and the second
    is the one that matters when deciding whether to trust a project.
    """
    open_project(page, "panels-reports")
    summary = page.locator(".fr-summary")
    expect(summary).to_be_visible()
    expect(summary).to_contain_text("of 3")
    expect(summary).to_contain_text("reporters")
    # One worked, one worked-with-caveats, one abandoned, so the rate is neither
    # extreme and a 0% or 100% would be visibly wrong.
    pct = summary.locator("strong")
    text = pct.inner_text().strip("%")
    assert 0 < int(text) < 100, f"rate {text}% is an extreme for this fixture"


def test_each_field_report_shows_its_outcome_version_and_caveats(page):
    open_project(page, "panels-reports")
    expect(page.locator(".fr-card")).to_have_count(3)

    abandoned = page.locator(".fr-card", has_text="migrating was not worth it")
    expect(abandoned).to_be_visible()
    expect(abandoned.locator(".badge-purple")).to_contain_text("abandoned")
    expect(abandoned.locator(".badge-slate").first).to_have_text("1.2.0")
    expect(abandoned).to_contain_text("fine while it stays small")


def test_the_per_environment_split_is_shown(page):
    """§4.7's wording is "worked for 83% of reporters on arm64" -- the split is
    part of the claim, and one arm64 report among three is the whole
    difference."""
    open_project(page, "panels-reports")
    expect(page.locator(".fr-summary ~ .pill-row .badge", has_text="arm64")).to_be_visible()
    expect(page.locator(".fr-summary ~ .pill-row .badge", has_text="amd64")).to_be_visible()


# ---------------------------------------------------------------------------
# Refusals. A panel that leaks is worse than one that is missing.
# ---------------------------------------------------------------------------

def test_an_instance_with_no_capability_catalog_says_so_distinctly(page, server):
    """The catalog being empty is a DIFFERENT state from a project with no claims.

    A project with no claims shows every catalog capability as unclaimed -- the
    rows are the contribution queue. An instance with no catalog has no rows to
    show, and the panel says so rather than showing a table with nothing in it.

    `panels-empty` has claims nowhere and a populated catalog, so it exercises
    the first state. This needs a second instance, and the suite's session-scoped
    server is shared, so the catalog is emptied and restored around one test --
    asserted restored in the finally, because a leaked deletion would silently
    change every later test in the run.
    """
    import sqlite3
    con = sqlite3.connect(server["db"])
    try:
        con.execute("DELETE FROM capabilities")
        con.commit()
        open_project(page, "panels-empty")
        # This test mutates the instance's capability catalog, which is
        # session-shared, so it depends on `server` rather than on `page` alone.
        expect(page.locator("text=No capability matrix yet")).to_be_visible()
        expect(page.locator(".panel-capabilities .cap-table")).to_have_count(0)
        # Field reports are unaffected: an empty catalog is not an empty panel.
        expect(page.locator("text=No field reports yet")).to_be_visible()
    finally:
        # Restored in a finally, not after the assertions. A leaked DELETE would
        # make every test that runs after this one in the same session see an
        # empty catalog, and the failure would point at the panels.
        now = time.time()
        con.executemany(
            'INSERT INTO capabilities (key, label, category, kind, "values",'
            ' created_at) VALUES (?,?,?,\'boolean\',?,?)',
            [(k, label, cat, '["yes","partial","no","unknown"]', now)
             for k, label, cat in [
                 ("wip-limits", "WIP limits", "features"),
                 ("offline", "Offline use", "features"),
                 ("self-hosted", "Self-hosted", "deployment")]])
        con.commit()
        con.close()


def test_a_private_projects_panels_are_not_rendered(page):
    """A private project page must not carry its data into the DOM.

    Asserting on the rendered DOM rather than on the HTTP status is the point:
    a 404 body containing the capability name would still ship the claim to the
    browser, and this catches that where a Go status-code test cannot.
    """
    page.goto(f"{BASE}/projects/panels-private")
    # The page refuses to render the project at all (notFound() path).
    expect(page.locator("text=not found")).to_be_visible(timeout=10000)
    assert "secret internal claim" not in page.content()
    assert "enforce it in the board UI" not in page.content()


def test_no_console_errors_on_any_panelled_page(page):
    """The fixture's page listener collects errors; a page that renders cleanly
    has none. Checked across every panel shape so a TypeError in one branch
    cannot hide behind the happy path."""
    for slug in ["panels-confirmed", "panels-disputed", "panels-unasserted",
                 "panels-empty", "panels-reports"]:
        open_project(page, slug)
    assert not page.errors, f"console/page errors: {page.errors}"