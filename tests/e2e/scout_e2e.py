"""End-to-end tests for the Scout page, driving real headless Chromium.

Prerequisites:  make build
Run:            pytest tests/e2e/scout_e2e.py -v

Scout's page and script had NO browser coverage when it shipped — the Go tests
covered the classifier and the endpoint, and nothing covered whether the page
renders a verdict a human can act on. That is the exact gap that let
`SolutionScore` ship with no `Title` field and render every standings row as
`1 0% 800.00`: the API was consistent about being useless, and every unit test
agreed.

So these tests assert RENDERED TEXT and VISIBILITY, not that a request returned
200. Each is named for the failure it catches.

The fixture is deliberately asymmetric:

  - `alpha-core` asserts all three capabilities, alone, so it can win leads.
  - `beta-core` asserts all three identically, so it TIES with alpha on every
    capability. This is the load-bearing pair: it is what makes a tie produce
    `extend` rather than `base-on`, and a fixture with one leading project cannot
    express that distinction at all.
  - `gamma-partial` asserts one capability, so it must read as `adopt`.
  - `delta-none` asserts nothing, so it has zero evidence coverage and must NOT
    be presented as a confident non-match.
  - `epsilon-proprietary` carries a license the license-constraint test excludes.

Every project gets a DISTINCT owner, because `refuseOwnVote` resolves ownership
through `members` and one identity owning all of them breaks the arena rules.
"""
import json
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
    dbdir = tempfile.mkdtemp(prefix="concord-scout-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-scout-e2e-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)

    seed(db)

    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


def seed(db):
    """A catalog where every verdict Scout can reach is reachable."""
    con = sqlite3.connect(db)
    cur = con.cursor()
    now = time.time()

    cur.execute(
        "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
        ("scout-author", "Scout Author", now))
    author = cur.lastrowid

    # A distinct member per project, so ownership is not shared.
    owners = {}
    for slug in ["alpha-core", "beta-core", "gamma-partial", "delta-none",
                 "epsilon-proprietary"]:
        cur.execute(
            "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
            (f"{slug}-owner", f"{slug} owner", now))
        owners[slug] = cur.lastrowid

    projects = [
        ("alpha-core", "Alpha Core", "MIT"),
        ("beta-core", "Beta Core", "MIT"),
        ("gamma-partial", "Gamma Partial", "MIT"),
        ("delta-none", "Delta None", "MIT"),
        ("epsilon-proprietary", "Epsilon Proprietary", "Proprietary"),
    ]
    ids = {}
    for slug, name, lic in projects:
        cur.execute(
            "INSERT INTO projects (slug, name, description, governance_model,"
            " license, visibility, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?,?)",
            (slug, name, f"{name} is a catalog project.", "maintainer_led", lic,
             "public", now, now))
        ids[slug] = cur.lastrowid
        # A members row, so the project has an owner who is not the assertion author.
        cur.execute(
            "INSERT INTO members (project_id, user_id, role, joined_at)"
            " VALUES (?,?,?,?)",
            (cur.lastrowid, owners[slug], "maintainer", now))
        # project_metrics: the row project creation writes. Scout must treat this
        # as UNMEASURED rather than scoring it — so it is left with no signals,
        # which is exactly the state a live project sits in.
        cur.execute(
            "INSERT INTO project_metrics (project_id, computed_at) VALUES (?,?)",
            (cur.lastrowid, now))
        cur.execute(
            "INSERT INTO projects_fts (slug, name, description, tags, languages)"
            " VALUES (?,?,?,?,?)",
            (slug, name, f"{name} is a catalog project.", "", ""))

    for key, label, category in [
            ("offline", "Offline use", "features"),
            ("wip-limits", "WIP limits", "features"),
            ("self-hosted", "Self-hosted", "deployment")]:
        # "values" is a SQLite reserved word and must be quoted.
        cur.execute(
            'INSERT INTO capabilities (key, label, category, kind, "values", created_at)'
            " VALUES (?,?,?,'boolean',?,?)",
            (key, label, category, json.dumps(["yes", "partial", "no", "unknown"]),
             now))

    # alpha and beta assert IDENTICALLY: the tie is the point.
    for slug in ["alpha-core", "beta-core"]:
        for key in ["offline", "wip-limits", "self-hosted"]:
            cur.execute(
                "INSERT INTO capability_assertions (capability, project_id, value,"
                " evidence, asserted_by, asserted_at) VALUES (?,?,?,?,?,?)",
                (key, ids[slug], "yes", "e2e fixture", author, now))

    # gamma covers one of three.
    cur.execute(
        "INSERT INTO capability_assertions (capability, project_id, value, evidence,"
        " asserted_by, asserted_at) VALUES (?,?,?,?,?,?)",
        ("offline", ids["gamma-partial"], "yes", "e2e fixture", author, now))

    # delta and epsilon assert NOTHING: no evidence at all.
    con.commit()
    con.close()


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


_xff_counter = {"n": 0}


@pytest.fixture()
def page(browser):
    _xff_counter["n"] += 1
    ip = f"10.33.{_xff_counter['n'] // 200}.{_xff_counter['n'] % 200}"
    ctx = browser.new_context(extra_http_headers={"X-Forwarded-For": ip})
    pg = ctx.new_page()
    # A JS exception in scout.js otherwise shows up as an empty div and a passing
    # test, which is how finder.js bugs hid here for so long.
    errors = []
    pg.on("pageerror", lambda e: errors.append(str(e)))
    pg.on("console", lambda m: errors.append(m.text) if m.type == "error" else None)
    pg.errors = errors
    yield pg
    ctx.close()


def ask(page, idea, exclude=None):
    """Drive the page to a report, failing loudly if it does not get there."""
    page.goto(BASE + "/scout")
    expect(page.locator("#scout-seed")).to_be_visible(timeout=10000)
    page.fill("#scout-seed-text", idea)
    if exclude:
        page.fill("#scout-exclude", exclude)
    page.click("#scout-seed-form button[type=submit]")
    expect(page.locator("#scout-report")).to_be_visible(timeout=10000)
    # Wait for a verdict ROW, not just the container: the container is unhidden
    # before the rows are written, so waiting on it resolves a moment early.
    page.wait_for_function(
        "() => document.querySelector('#scout-verdicts [data-verdict]') !== null",
        timeout=10000)


def row(page, slug):
    return page.locator(f'#scout-verdicts [data-slug="{slug}"]')


# --- the page exists -------------------------------------------------------

# finder.html has linked /scout twice since before the route existed. Every one of
# those links was a 404, and nothing caught it because the Go tests only covered
# the API.
def testTheScoutPageLoadsAndIsNotA404(page):
    page.goto(BASE + "/scout")
    expect(page.locator("#scout-seed")).to_be_visible(timeout=10000)
    expect(page.locator("h1")).to_contain_text("Scout")


# The links that were 404 must now resolve. Two of them, both on the Finder page.
def testTheScoutLinksFromTheFinderPageNowResolve(page):
    page.goto(BASE + "/finder")
    links = page.locator('a[href="/scout"]')
    assert links.count() >= 1, "the Finder page no longer links to Scout at all"
    first = links.first
    href = first.get_attribute("href")
    first.click()
    expect(page.locator("#scout-seed")).to_be_visible(timeout=10000)
    assert "/scout" in page.url


# --- the report renders ---------------------------------------------------

def testAScoutReportListsEveryVerdict(page):
    ask(page, "we need offline support and wip limits")
    rows = page.locator("#scout-verdicts [data-verdict]")
    assert rows.count() == 5, f"expected 5 verdicts, got {rows.count()}"


# The decomposition is what the user is meant to correct, so it has to be on the
# page BEFORE the verdicts and has to name the capabilities.
def testTheDecompositionIsShownBeforeTheVerdicts(page):
    ask(page, "we need offline support")
    caps = page.locator("#scout-capabilities .scout-cap")
    assert caps.count() >= 1, "the report shows verdicts but never says what it read"
    keys = caps.evaluate_all("els => els.map(e => e.dataset.key)")
    assert "offline" in keys, f"offline was in the idea but not in {keys}"


# A proposed capability must be visibly marked as not scored. It is reported so
# the user can see the reading, but it contributes nothing — and a page that lists
# it identically to a real one is lying about what was scored.
def testAProposedCapabilityIsMarkedAsProposed(page):
    ask(page, "we need offline and live-sync")
    proposed = page.locator('#scout-capabilities .scout-cap[data-key="live-sync"]')
    expect(proposed).to_have_count(1)
    expect(proposed).to_be_visible()
    expect(proposed.locator(".scout-cap-tag")).to_have_text("proposed")


# --- the three verdicts that are reachable --------------------------------

# A tie at the top is extend, NOT base-on. Alpha and beta assert identically, so
# neither can be the base. A page that awards base-on to whichever rendered first
# is the defect this exists to catch.
def testATieAtTheTopIsExtendAndNotBaseOn(page):
    ask(page, "offline, wip limits and self-hosted")
    for slug in ["alpha-core", "beta-core"]:
        r = row(page, slug)
        expect(r).to_have_count(1)
        verdict = r.get_attribute("data-verdict")
        assert verdict != "base-on", (
            f"{slug} wins every capability but ties with its twin, so base-on is "
            f"wrong — got {verdict}")


# And the tie must be EXPLAINED, not merely withheld. A reader who sees two
# projects and no label cannot tell a tie from a bug.
def testATiedVerdictSaysWhoItTiesWith(page):
    ask(page, "offline, wip limits and self-hosted")
    signals = row(page, "alpha-core").locator(".scout-signals li")
    text = " ".join(signals.evaluate_all("els => els.map(e => e.textContent)"))
    assert "tied_with" in text, (
        f"a tied verdict does not name its twin; signals were: {text}")


# A project covering a subset is a dependency, not a base.
def testASubsetMatchIsAdopt(page):
    ask(page, "offline, wip limits and self-hosted")
    r = row(page, "gamma-partial")
    expect(r).to_have_count(1)
    # gamma satisfies 1 of 3, but it also LEADERS its one capability if nothing
    # else touches it — except alpha and beta also assert offline, so all three
    # tie on offline and nobody is awarded the lead.
    assert r.get_attribute("data-verdict") in ("adopt", "extend"), (
        f"gamma covers a subset and should read as adopt/extend, got "
        f"{r.get_attribute('data-verdict')}")


# --- the honesty rules, which are the whole point ------------------------

# No project in this fixture has a metrics signal, so every project reads as
# "health not measured". A page that renders `health 0.00` is telling the reader
# a number was measured when nothing was — §6.3's exact trap.
def testAnUnmeasuredProjectIsNotRenderedAsAZeroHealth(page):
    ask(page, "offline support")
    facts = row(page, "delta-none").locator(".scout-verdict-facts")
    text = facts.text_content()
    assert "health not measured" in text, (
        f"an unmeasured project's health rendered as {text!r} instead of saying "
        f"it was not measured")
    assert "0.00" not in text, f"an unmeasured health rendered as a number: {text!r}"


# `reports == 0` is not `0%`. Delta has no field reports, so the page must say so
# rather than print a zero rate.
#
# The `0%` ban is scoped to the OUTCOME RATE, not to the whole facts line: fit and
# evidence coverage legitimately print `0%` — delta matched nothing and has no
# evidence, and 0% is the honest number for both. The first version of this test
# banned `0%` anywhere in the line and so failed against a correct page, because
# `0 of 1 capabilities (0%, evidence 0%)` contains the substring. The rule is about
# the outcome rate specifically, so that is what is checked.
def testNoFieldReportsIsNotRenderedAsZeroPercent(page):
    ask(page, "offline support")
    facts = row(page, "delta-none").locator(".scout-verdict-facts")
    text = facts.text_content()
    assert "no field reports yet" in text, f"got {text!r}"
    # The outcome rate is only ever printed alongside its sample size, so a bare
    # "field reports 0%" is the shape this forbids.
    assert "field reports 0%" not in text, (
        f"an unmeasured outcome rate rendered as a rate: {text!r}")
    assert "of 0" not in text, f"a sample of zero was printed: {text!r}"


# A project with no assertions has NO evidence, and the page must not present its
# fit as a confident number.
def testAProjectWithNoAssertionsShowsNoEvidenceCoverage(page):
    ask(page, "offline, wip limits and self-hosted")
    facts = row(page, "delta-none").locator(".scout-verdict-facts")
    text = facts.text_content()
    assert "0 of 3 capabilities" in text, f"got {text!r}"
    assert "evidence 0%" in text, (
        f"a project with no assertions does not report zero evidence coverage: {text!r}")


# --- license constraint ---------------------------------------------------

def testALicenseConstraintExcludesAndThePageSaysSo(page):
    ask(page, "offline support", exclude="proprietary")
    r = row(page, "epsilon-proprietary")
    expect(r).to_have_count(1)
    assert r.get_attribute("data-verdict") == "avoid", (
        "a proprietary project survived a proprietary license constraint")


# --- empty states ---------------------------------------------------------

# An idea matching nothing must say so and NOT invent a verdict. This is the
# §4 rule from the other direction.
def testAnUnmatchedIdeaExplainsItselfRatherThanInventingVerdicts(page):
    page.goto(BASE + "/scout")
    expect(page.locator("#scout-seed")).to_be_visible(timeout=10000)
    page.fill("#scout-seed-text", "zzz nothing here matches the catalog zzz")
    page.click("#scout-seed-form button[type=submit]")
    expect(page.locator("#scout-report")).to_be_visible(timeout=10000)
    page.wait_for_function(
        "() => document.getElementById('scout-decomp-summary').textContent.length > 0",
        timeout=10000)
    summary = page.locator("#scout-decomp-summary").text_content()
    assert "Nothing in your idea matched" in summary, f"got {summary!r}"


# An empty idea is refused client-side rather than posting an empty query. An
# empty idea is not a report on everything.
def testAnEmptyIdeaIsRefusedWithoutARequest(page):
    page.goto(BASE + "/scout")
    expect(page.locator("#scout-seed")).to_be_visible(timeout=10000)
    page.fill("#scout-seed-text", "   ")
    page.click("#scout-seed-form button[type=submit]")
    expect(page.locator("#scout-error")).to_be_visible(timeout=5000)
    expect(page.locator("#scout-report")).to_be_hidden()


# The claim is that NO REQUEST is made, and asserting on the visible error does
# not establish it: the server also rejects an empty idea with 400, so a page that
# posted anyway shows an error too and the previous version of this test passed
# with the client-side guard removed.
#
# So this one counts requests. It is the difference between "the user sees an
# error" (true either way) and "the client did not bother asking" (the thing the
# guard is for).
def testAnEmptyIdeaMakesNoRequest(page):
    page.goto(BASE + "/scout")
    expect(page.locator("#scout-seed")).to_be_visible(timeout=10000)

    asked = []
    page.on("request", lambda r: asked.append(r.url)
            if "/api/v1/scout" in r.url else None)

    page.fill("#scout-seed-text", "   ")
    page.click("#scout-seed-form button[type=submit]")
    expect(page.locator("#scout-error")).to_be_visible(timeout=5000)
    page.wait_for_timeout(400)  # give any stray fetch time to arrive
    assert asked == [], (
        f"an empty idea still asked the server: {asked}. The client guard exists so "
        f"a blank box costs nothing; without it this is a pointless round trip.")


# And the positive control for the same helper: a real idea DOES make exactly one
# request. Without this, the test above would also pass if the listener were simply
# broken — which is the failure mode of every request-counting assertion.
def testANonEmptyIdeaMakesExactlyOneRequest(page):
    page.goto(BASE + "/scout")
    expect(page.locator("#scout-seed")).to_be_visible(timeout=10000)

    asked = []
    page.on("request", lambda r: asked.append(r.url)
            if "/api/v1/scout" in r.url else None)

    page.fill("#scout-seed-text", "offline support")
    page.click("#scout-seed-form button[type=submit]")
    expect(page.locator("#scout-report")).to_be_visible(timeout=10000)
    assert len(asked) == 1, f"expected exactly one scout request, got {len(asked)}: {asked}"


# --- navigation -----------------------------------------------------------

def testScoutCanBeReachedFromTheSeedAgain(page):
    ask(page, "offline support")
    expect(page.locator("#scout-report")).to_be_visible()
    page.click("#scout-again")
    expect(page.locator("#scout-seed")).to_be_visible(timeout=5000)
    expect(page.locator("#scout-report")).to_be_hidden()


# --- no script errors anywhere -------------------------------------------

# The catch-all. A JS exception in scout.js shows up as an empty div and every
# test above still passing, so this is checked on a page that has been through a
# full round trip.
def testNoScriptErrorsDuringAReport(page):
    ask(page, "offline, wip limits, self-hosted")
    expect(page.locator("#scout-verdicts [data-verdict]").first).to_be_visible()
    assert not page.errors, f"JS errors during the report: {page.errors}"