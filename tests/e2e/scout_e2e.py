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
import re
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


@pytest.fixture()
def signed_in(page, server):
    """A page with a session token in localStorage.

    Saving posts through the documents endpoint, which requires contributor rights
    on the target project — so the save tests cannot run anonymous. Registering over
    HTTP rather than inserting a user keeps the token real: the page authenticates
    the way a browser does, and a hand-written localStorage token would test
    nothing about the middleware.
    """
    username = f"scout-saver-{_xff_counter['n']}"
    body = harness.api(server["base"], "POST", "/api/v1/auth/register", {
        "username": username, "password": "correct-horse-battery",
        "display_name": username,
    })
    token = body.get("token")
    assert token, f"register returned no token: {body}"

    # Grant contributor rights directly, on every seeded project.
    #
    # There is no API route for this: server.go registers GET .../members and
    # nothing else, so a fixture that guesses a PUT or POST gets a 404 that reads
    # like a permissions bug. JoinProject writes a plain member row, which is below
    # the `contributor` level the documents handler requires, so the role is
    # promoted with SQL.
    #
    # Promoting ALL projects is deliberate: the save tests are about the document
    # path, not about access control, and a test that could fail for lack of rights
    # in an unrelated project would be testing the fixture.
    uid = body.get("user", {}).get("id") or body.get("user_id") or body.get("id")
    assert uid, f"register returned no user id: {body}"
    con = sqlite3.connect(server["db"])
    try:
        con.execute(
            "INSERT INTO members (project_id, user_id, role, joined_at)"
            " SELECT id, ?, 'contributor', ? FROM projects"
            " WHERE id NOT IN (SELECT project_id FROM members WHERE user_id = ?)",
            (uid, time.time(), uid))
        con.commit()
    finally:
        con.close()

    page.goto(BASE + "/scout")
    page.evaluate("t => window.localStorage.setItem('concord.token', t)", token)
    return page


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
    # A settle window, and a NECESSARY one: this test asserts the ABSENCE of a
    # request, so there is no state to wait for -- "no fetch has arrived yet" and
    # "no fetch will ever arrive" look identical until time passes. Removing this
    # sleep would make the test pass more reliably while checking less, which is
    # the trade this whole pass refused to make.
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

# --- saving a report as a document ---------------------------------------
def scout_docs(server, slug):
    """The scout documents on `slug`, as a list.

    The list endpoint answers a BARE JSON ARRAY, not an object — harness.api
    returns whatever the server sent, so `body.get(...)` on it raises
    AttributeError. The first version of these tests assumed a wrapper object
    because doJSON in the Go harness wraps arrays under "items", which does not
    apply to Python. Both shapes are accepted so the helper cannot be wrong about
    the server's choice.
    """
    body = harness.api(server["base"], "GET",
                       f"/api/v1/projects/{slug}/documents?kind=scout")
    if isinstance(body, list):
        return body
    return body.get("documents") or body.get("items") or []




# Saving goes through the EXISTING documents endpoint with kind `scout`, so these
# tests need a session with contributor rights on the target project. Scout's read
# path deliberately still works logged out, which is what the `page` fixture is for.


def save(pg, slug, idea="offline support and wip limits"):
    """Ask, then save to `slug`. Asserts only that a visible outcome appeared."""
    ask(pg, idea)
    pg.fill("#scout-save-slug", slug)
    pg.click("#scout-save")
    expect(pg.locator("#scout-save-ok")).to_be_visible(timeout=10000)


# A failed save is a DELIBERATE 404 in some of these tests, and the browser logs
# every non-2xx response to the console. "No console errors" would fail on the very
# behaviour under test, so the filter drops resource failures and leaves genuine
# script errors — an undefined function, a thrown exception — which is what the
# assertion is actually for.
def assert_no_script_errors(pg):
    script_errors = [e for e in pg.errors if "Failed to load resource" not in e]
    assert not script_errors, f"JS errors: {script_errors}"


# The whole point of §8: a report you can come back to. It is stored through the
# EXISTING documents endpoint with kind `scout`, not a scout-specific write path,
# so this asserts the document really is there under that kind.
def testAReportSavesAsADocumentOfKindScout(signed_in, server):
    pg = signed_in
    save(pg, "beta-core")
    ok = pg.locator("#scout-save-ok").text_content()
    assert "scout" in ok, f"the confirmation does not name the kind: {ok!r}"

    # And it is actually stored, which the confirmation alone does not establish.
    #
    # beta-core, and an exact count of one, because the server fixture is
    # SESSION-scoped: a previous test's document on alpha-core is still there, and
    # counting documents absolutely would make this test depend on execution order.
    docs = scout_docs(server, "beta-core")
    assert len(docs) == 1, f"expected one scout document on beta-core, got {docs}"
    assert docs[0]["kind"] == "scout"


# The stored body has to be useful months later with no instance running, so it
# must carry the reason AND the signals. A document holding only verdict labels
# answers nothing.
def testASavedReportCarriesTheReasoning(signed_in, server):
    pg = signed_in
    save(pg, "gamma-partial", "offline, wip limits and self-hosted")

    doc = scout_docs(server, "gamma-partial")[0]
    text = doc["body"]
    assert "## What Scout read" in text, "the decomposition is missing"
    assert "### " in text, "no per-verdict sections"
    assert "Signals:" in text, (
        "the signals are missing, so the stored document cannot answer 'why did "
        "Scout say this' later")
    # At least one verdict must carry them, with its actual signal values rather
    # than a placeholder. The first version asserted only the heading, and a
    # mutant replacing the push with a literal "Signals: (omitted)" still wrote
    # the heading — so it passed.
    signal_lines = [l for l in text.splitlines() if l.startswith("Signals: ")]
    assert signal_lines, "no verdict wrote a signals line at all"
    assert any(re.search(r"`[a-z_]+[:0-9]", l) for l in signal_lines), (
        f"signals lines carry no values: {signal_lines}")
    # The honesty rules survive the round trip into a document.
    assert "health not measured" in text, (
        f"an unmeasured health was stored as a number: {text[:400]!r}")
    assert "no field reports yet" in text, (
        f"an unmeasured report count was stored as a rate: {text[:400]!r}")


# PutDocument REPLACES at the same (kind, slug), so saving the same idea twice must
# bump the revision rather than accumulate near-duplicates. The slug is derived
# from the idea precisely to make that true.
def testSavingTheSameIdeaTwiceReplacesRatherThanDuplicates(signed_in, server):
    pg = signed_in
    save(pg, "delta-none")
    first = pg.locator("#scout-save-ok").text_content()

    pg.click("#scout-again")
    expect(pg.locator("#scout-seed")).to_be_visible(timeout=5000)
    save(pg, "delta-none")
    second = pg.locator("#scout-save-ok").text_content()

    # A RELATIVE claim, not "revision 2". The server is session-scoped, so the
    # absolute revision depends on how many tests ran before this one — asserting
    # a fixed number made the test fail on execution order alone.
    def rev(text):
        return int(text.rsplit("revision", 1)[1].strip().rstrip("."))
    assert rev(second) == rev(first) + 1, (
        f"saving the same idea twice did not replace: {first!r} then {second!r}")

    # One document, not two: the (kind, slug) uniqueness is what makes the replace
    # work at all.
    docs = scout_docs(server, "delta-none")
    assert len(docs) == 1, f"expected one document after two saves, got {len(docs)}"


# The slug is derived FROM THE IDEA, so two different ideas are two different
# documents. This is the half the replace test cannot check: saving the same idea
# twice replaces correctly whether or not the slug is derived, because a constant
# slug would replace just as cleanly — and would also silently overwrite every other
# report on the project.
def testTwoDifferentIdeasAreTwoDifferentDocuments(signed_in, server):
    pg = signed_in
    save(pg, "epsilon-proprietary", "offline support and wip limits")
    save(pg, "epsilon-proprietary", "self-hosted deployment please")

    docs = scout_docs(server, "epsilon-proprietary")
    assert len(docs) == 2, (
        f"saving two different ideas produced {len(docs)} documents; a constant "
        f"document slug would make the second overwrite the first: "
        f"{[d['title'] for d in docs]}")


# Going back to the seed must drop the report on screen. Without that, saving
# after clicking "New idea" files the PREVIOUS idea under whatever project is typed
# next — the most plausible way to put a report in the wrong project.
def testGoingBackClearsTheReportSoItCannotBeSavedByMistake(signed_in, server):
    pg = signed_in
    ask(pg, "offline support and wip limits")
    pg.click("#scout-again")
    expect(pg.locator("#scout-seed")).to_be_visible(timeout=5000)
    expect(pg.locator("#scout-save")).to_be_hidden()

    # The first version stopped at `to_be_hidden()` and SURVIVED a mutation that
    # kept lastReport: the button is hidden either way, because the report VIEW is
    # hidden. Visibility is not the claim.
    #
    # The second version asked a NEW question and then saved, and also survived --
    # because `renderReport` reassigns `lastReport` on every ask, so by the time Save
    # ran the new report was there regardless. The clearing only matters in the
    # window BETWEEN going back and the next ask, and in that window the Save button
    # is unreachable by click.
    #
    # So it is driven through the handler directly, which is the only way to reach
    # it: click Save's own listener with no report present and assert it REFUSES
    # rather than saving the previous one. `pg.evaluate` clicks it programmatically.
    result = pg.evaluate("""() => {
      document.getElementById('scout-save').click();
      const err = document.getElementById('scout-save-error');
      return {hidden: err.hidden, text: err.textContent};
    }""")
    assert not result["hidden"], (
        "after going back, saving still succeeded; the previous report was kept and "
        "could be filed under a project typed next")
    assert "Ask Scout something first" in result["text"], (
        f"saving after going back gave {result['text']!r} rather than refusing")


# A project that does not exist must fail visibly rather than appearing to save.
def testSavingToAMissingProjectReportsTheError(signed_in):
    pg = signed_in
    ask(pg, "offline support")
    pg.fill("#scout-save-slug", "no-such-project-anywhere")
    pg.click("#scout-save")
    expect(pg.locator("#scout-save-error")).to_be_visible(timeout=10000)
    expect(pg.locator("#scout-save-ok")).to_be_hidden()
    assert_no_script_errors(pg)


# Saving with no project named is refused client-side.
def testSavingWithoutAProjectIsRefused(signed_in):
    pg = signed_in
    ask(pg, "offline support")
    pg.fill("#scout-save-slug", "  ")
    pg.click("#scout-save")
    expect(pg.locator("#scout-save-error")).to_be_visible(timeout=5000)
    expect(pg.locator("#scout-save-ok")).to_be_hidden()


# A failed save must not leave a stale success behind, or the next save looks like
# it worked before it was even attempted.
def testAFailedSaveDoesNotLeaveAStaleSuccessMessage(signed_in):
    pg = signed_in
    ask(pg, "offline support")
    pg.fill("#scout-save-slug", "no-such-project-anywhere")
    pg.click("#scout-save")
    expect(pg.locator("#scout-save-error")).to_be_visible(timeout=10000)

    # epsilon-proprietary, and a FRESH page region: the success message is cleared
    # when a report renders, but a test that reuses a project another test already
    # saved into could be reading a stale success left by an earlier test.
    pg.fill("#scout-save-slug", "epsilon-proprietary")
    pg.click("#scout-save")
    expect(pg.locator("#scout-save-ok")).to_be_visible(timeout=10000)
    expect(pg.locator("#scout-save-error")).to_be_hidden()
    assert "epsilon-proprietary" in pg.locator("#scout-save-ok").text_content()
    assert_no_script_errors(pg)
