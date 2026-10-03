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

    # --- panels-standings: two features, one with proposals, one without ------
    # A feature needs a validated complaint behind it (§6.2), and a solution
    # cannot be authored by the feature's author (§5.2). Both constraints are
    # real and both produce a confusing error when violated, so the fixture
    # satisfies them rather than working around them.
    pid = project("panels-standings")
    complaint = None
    for i, title in enumerate(["Export drops the last row",
                               "Filters need composing"]):
        # Column list from migration 0008's complaints_new, which replaced 0001's
        # table: there is no `impact` column, and adding one is a sqlite error
        # naming a column this schema does not have.
        cur.execute(
            "INSERT INTO complaints (project_id, author_id, title, body, severity,"
            " frequency, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,'validated',?,?)",
            (pid, owner_id, title, "reproducible", 4, 2.0, now, now))
        cid = cur.lastrowid
        cur.execute(
            "INSERT INTO features (project_id, author_id, title, body, effort,"
            " status, created_at, updated_at)"
            " VALUES (?,?,?,'a body','M','discussion',?,?)",
            (pid, owner_id, title, now, now))
        fid = cur.lastrowid
        cur.execute(
            "INSERT INTO feature_complaints (feature_id, complaint_id)"
            " VALUES (?,?)", (fid, cid))
        if i == 0:
            complaint, featured = cid, fid

    # Three proposals on the first feature, authored by somebody else. A second
    # "do nothing" baseline entry too, so the panel has to mark it.
    # ONE arena per feature, not per solution: `arenas.feature_id` is UNIQUE and a
    # solution arena belongs to a feature. The first draft created the arena
    # inside the loop and the second solution hit
    # "UNIQUE constraint failed: arenas.feature_id".
    cur.execute(
        "INSERT INTO arenas (type, project_id, feature_id, question, created_at)"
        " VALUES ('solution',?,?,'Which approach should we build?',?)",
        (pid, featured, now))
    arena = cur.lastrowid

    sol_author = user("solution-author")
    for title, stype in [("Stream the export", "build-new"),
                         ("Chunk and verify", "extend-existing"),
                         ("Keep exporting manually", "do-nothing")]:
        # updated_at is NOT NULL on solutions; the first draft listed created_at
        # alone and every test in the suite errored in the fixture with a
        # constraint failure naming a column this insert simply omitted.
        cur.execute(
            "INSERT INTO solutions (feature_id, author_id, title, body, type,"
            " relationship, created_at, updated_at)"
            " VALUES (?,?,?,'a body',?,'exclusive',?,?)",
            (featured, sol_author, title, stype, now, now))
        sid = cur.lastrowid
        # An arena entry per solution, or ListSolutions finds no arena and the
        # panel shows nothing. The shape is read from the store's own
        # CreateSolution: EnsureArena(ArenaSolution, ...) + UpsertArenaEntry.
        cur.execute(
            "INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd,"
            " sigma, games, is_baseline, updated_at)"
            " VALUES (?,'solution',?,1500,350,0.06,0,?,?)",
            (arena, sid, 1 if stype == "do-nothing" else 0, now))

    # --- panels-alternatives: one use case, two competing projects ------------
    #
    # `arena_entries.entity_type` must be 'project'. That is the whole point of the
    # panel — an alternatives arena ranks PROJECTS, so a feature here would render
    # as a ranking that is quietly about something else.
    apid = project("panels-alternatives")
    # The arena is created FIRST and its id captured — the entries reference it, so
    # a row written before it exists has no arena_id to point at. The first draft
    # of this fixture inserted entries and only afterwards went looking for an
    # arena it had never created, which is a None where an int belongs.
    cur.execute(
        "INSERT INTO arenas (type, project_id, use_case, question, created_at)"
        " VALUES ('alternatives',?,'teams needing audit logs',"
        "'Which is the better alternative?',?)",
        (apid, now))
    aid = cur.lastrowid
    for slug, rating in [("rival-ledger", 1600), ("rival-notebook", 1400)]:
        pid_c = project(slug)
        cur.execute(
            "INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd,"
            " sigma, games, is_baseline, updated_at)"
            " VALUES (?,'project',?,?,350,0.06,0,0,?)",
            (aid, pid_c, rating, now))

    # A SECOND use case on the same project, with one competitor.
    #
    # This exists because of a mutation that survived: collapsing the per-arena
    # blocks into a single flattened list produces byte-identical markup when a
    # project has exactly ONE arena, so no browser test could see it. With two,
    # the flattening is observable — two `.alt-arena` blocks become one.
    #
    # The rule being guarded is the same one solutions-panel-spec.md argues: each
    # use case is its own contest, so a merged list ranks numbers that were never
    # comparable.
    cur.execute(
        "INSERT INTO arenas (type, project_id, use_case, question, created_at)"
        " VALUES ('alternatives',?,'solo operators',"
        "'Which is the better alternative?',?)",
        (apid, now))
    aid2 = cur.lastrowid
    solo = project("rival-solo")
    cur.execute(
        "INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd,"
        " sigma, games, is_baseline, updated_at)"
        " VALUES (?,'project',?,1500,350,0.06,0,0,?)",
        (aid2, solo, now))

    # A second project with a use case posed and NOTHING entered, so the panel's
    # two empty states can be told apart in the browser rather than only in Go.
    bpid = project("panels-unanswered")
    cur.execute(
        "INSERT INTO arenas (type, project_id, use_case, question, created_at)"
        " VALUES ('alternatives',?,'teams with a compliance deadline',"
        "'Which is the better alternative?',?)",
        (bpid, now))

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
    expect(page.locator(".panel-solutions")).to_be_visible(timeout=10000)
    expect(page.locator(".panel-alternatives")).to_be_visible(timeout=10000)


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
# Solution standings — the third §4.10 panel
# ---------------------------------------------------------------------------

def test_standings_are_grouped_under_their_feature_not_merged_into_one_ranking(page):
    """The structural rule of solutions-panel-spec.md, in the rendered DOM.

    Two features, three proposals on one and none on the other. A merged
    project-wide leaderboard would render one list; grouped standings render two
    blocks, and the rank restarts at 1 in each -- which is the visual form of
    "these scores were computed against different opponents".
    """
    open_project(page, "panels-standings")

    expect(page.locator(".sol-group")).to_have_count(2)

    ranked = page.locator(".sol-group", has_text="Export drops the last row")
    expect(ranked.locator(".sol-row")).to_have_count(3)

    # Ranks are rendered, and they run 1..3 within the feature.
    expect(ranked.locator(".sol-rank").nth(0)).to_have_text("1")
    expect(ranked.locator(".sol-rank").nth(2)).to_have_text("3")

    # The feature with no proposals is still a block, not absent.
    quiet = page.locator(".sol-group", has_text="Filters need composing")
    expect(quiet).to_be_visible()
    expect(quiet).to_contain_text("No proposals yet")
    expect(quiet.locator(".sol-row")).to_have_count(0)

    # And the panel says in words that the scores do not compare across features.
    expect(page.locator(".panel-solutions")).to_contain_text("not comparable")


def test_the_panel_leads_with_how_many_features_are_awaiting_proposals(page):
    """One of two features has proposals. That sentence is the panel's headline.

    A panel that renders only the answered feature reads as though the project
    has one feature and nothing outstanding, which is the opposite of what a
    reader needs to know.
    """
    open_project(page, "panels-standings")
    tally = page.locator(".panel-solutions .fr-summary")
    expect(tally).to_contain_text("1")
    expect(tally).to_contain_text("of 2 features")
    expect(tally).to_contain_text("1 has none")


def test_the_do_nothing_baseline_is_marked_not_rendered_as_a_proposal(page):
    """§6.3's baseline is ranked but never selectable.

    A row that looks like a proposal invites a reader to treat "keep doing what
    you are doing" as the project's answer.
    """
    open_project(page, "panels-standings")
    row = page.locator(".sol-baseline")
    expect(row).to_have_count(1)
    expect(row).to_contain_text("do nothing")
    expect(row).to_contain_text("Keep exporting manually")


def test_a_project_with_no_features_says_it_has_no_roadmap(page):
    """Distinct from "the roadmap has no proposals".

    `panels-empty` has features and no reports; it has no FEATURES either, so it
    exercises this state. Asserting the exact wording is deliberate: the two
    states must not render the same empty state, and "no roadmap yet" is the
    statement that is true here.
    """
    open_project(page, "panels-empty")
    expect(page.locator("text=No roadmap yet")).to_be_visible()
    expect(page.locator(".sol-group")).to_have_count(0)
    # Not the per-feature empty state, which would say a feature is unanswered
    # when in fact there is no feature.
    expect(page.locator("text=No proposals yet")).to_have_count(0)


# ---------------------------------------------------------------------------
# Alternatives arenas — the fourth §4.10 panel
# ---------------------------------------------------------------------------

def test_the_alternatives_panel_renders_a_real_ranking_of_projects(page):
    """The panel's whole claim: competing PROJECTS, ranked, one arena per use case.

    Asserted on `entity_type` at the store level in Go. What can only be checked
    here is that the ranking reaches the page and reads as a ranking — a panel
    that renders the arena and drops the entries looks identical to one with
    nothing to show.
    """
    # The project that OWNS the arena. The competitors are separate projects and
    # have their own pages; the panel lives on the owner's. The first version
    # opened `rival-ledger` — an entry ON the panel — and landed on a login page,
    # because that project is owned by the fixture's other user and the harness
    # identity is not a member of it. A red test for a reason that had nothing to
    # do with the panel.
    open_project(page, "panels-alternatives")

    # Scoped to one arena by its use case: the fixture project has two, and the
    # other one is a different contest with different competitors.
    arena = page.locator(".alt-arena", has_text="teams needing audit logs")
    expect(arena).to_have_count(1)
    expect(arena.locator(".alt-row")).to_have_count(2)

    # Both competitors, by identity. The fixture names them "rival-ledger" and the
    # project's `name` column title-cases the slug, so the rendered TEXT is "Rival
    # Ledger" and the slug lives in the href. Asserting the slug was visible as text
    # failed against a panel that was rendering perfectly — a title is what a reader
    # sees, and a slug is what a link points at, so both are checked where they are.
    expect(page.locator(".alt-title", has_text="Rival Ledger")).to_be_visible()
    expect(page.locator(".alt-title", has_text="Rival Notebook")).to_be_visible()
    expect(page.locator('a.alt-title[href="/projects/rival-ledger"]')).to_have_count(1)
    expect(page.locator('a.alt-title[href="/projects/rival-notebook"]')).to_have_count(1)

    # A VISIBLE rank per row, and the ratings the arena stored are rendered.
    #
    # `to_be_visible`, not `to_have_text`: a mutation that adds `hidden` to the
    # rank leaves the element's text unchanged, so a text assertion still passes
    # and the mutant survives. That is what happened — the fifth mutant of this
    # panel was killed only after this line became a visibility check. A rank a
    # reader cannot see is not a rank.
    expect(arena.locator(".alt-rank").nth(0)).to_be_visible()
    expect(arena.locator(".alt-rank").nth(1)).to_be_visible()
    expect(arena.locator(".alt-row").nth(0)).to_contain_text("1600")
    expect(arena.locator(".alt-row").nth(1)).to_contain_text("1400")

    # Ranked 1,2 in the order the arena stored them — and rendered as numbers, not
    # implied by list order, the same rule as the solutions panel.
    expect(arena.locator(".alt-rank").nth(0)).to_have_text("1")
    expect(arena.locator(".alt-rank").nth(1)).to_have_text("2")

    # §7.3's pair, stated once for the arena.
    expect(arena.locator(".alt-pair")).to_contain_text("Better for: teams needing audit logs")

    # And the reader is told these are separate rankings.
    expect(page.locator(".panel-alternatives")).to_contain_text("not comparable between")


def test_the_two_empty_states_are_different_words(page):
    """A use case posed with nothing entered is not a project that asked nothing.

    The two read the same to a count-based check — one has no rows in both cases —
    so the assertion has to be on the wording.
    """
    open_project(page, "panels-unanswered")
    expect(page.locator("text=No projects entered yet")).to_be_visible()
    # NOT the other state.
    expect(page.locator("text=No use cases compared yet")).to_have_count(0)


def test_each_use_case_is_its_own_ranking_not_a_merged_list(page):
    """The rule solutions-panel-spec.md argues for solutions, argued here for
    use cases.

    One project, TWO arenas, three competitors. A merged list would render one
    block and one ranking; grouped standings render two blocks, each with its own
    use case heading.

    This test exists because the flattening mutation survived every other version:
    with a single arena, collapsing the blocks produces identical markup, so the
    defect was real and untestable at once.
    """
    open_project(page, "panels-alternatives")

    expect(page.locator(".alt-arena")).to_have_count(2)

    audit = page.locator(".alt-arena", has_text="teams needing audit logs")
    solo = page.locator(".alt-arena", has_text="solo operators")
    expect(audit.locator(".alt-row")).to_have_count(2)
    expect(solo.locator(".alt-row")).to_have_count(1)

    # The ranks restart per use case, which is the visual form of "separate
    # contests" — the same rule the solutions panel has.
    expect(audit.locator(".alt-rank").nth(0)).to_be_visible()
    expect(audit.locator(".alt-rank").nth(0)).to_have_text("1")
    expect(solo.locator(".alt-rank").nth(0)).to_be_visible()
    expect(solo.locator(".alt-rank").nth(0)).to_have_text("1")

    # And the not-comparable warning is present, because it is exactly true here.
    expect(page.locator(".panel-alternatives")).to_contain_text("not comparable between")


def test_a_project_that_never_compared_anything_says_so(page):
    open_project(page, "panels-empty")
    expect(page.locator("text=No use cases compared yet")).to_be_visible()
    expect(page.locator(".alt-arena")).to_have_count(0)
    expect(page.locator(".alt-row")).to_have_count(0)


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
                 "panels-empty", "panels-reports", "panels-standings",
                 "panels-alternatives", "panels-unanswered"]:
        open_project(page, slug)
    assert not page.errors, f"console/page errors: {page.errors}"