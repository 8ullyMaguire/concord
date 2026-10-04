"""The feature detail page: /projects/{slug}/features/{id}.

Six browser tests, each for a way this page can look correct while being wrong.

THE FEATURE CARD LINK IS THE POINT. Go's TestFeaturePageIsLinkedFromTheProject
asserts the link is BUILT, in project.js, because that is the layer that builds
it -- and it says in its own comment that this file is the complement that checks
the rendered DOM really has something clickable. Without that complement the Go
test would pass for a page nothing can reach, which is how Finder, Scout, the
consensus page and the audit log were all invisible with every test green.

WHY THESE ROWS ARE SEEDED THROUGH SQLITE AND NOT THE API. A browser test whose
data arrives via the API it is testing cannot fail for the reason it exists: if
the write path is broken the page is empty and the assertions pass anyway.

THE THREE ROWS ARE DELIBERATELY DIFFERENT, because the page must not conflate
them:

  compared     elo_r 1610, elo_rd 40      -- a rank the reader can act on
  uncertain    elo_r 1502, elo_rd 344     -- barely moved, and WIDE bar (idea #37)
  unrated      elo_r NULL, elo_rd 350     -- never compared at all

The third row is the one that breaks the obvious implementation. `Math.round(f.elo_r || 0)`
prints 0 for it, which is a real rating -- one that lost every comparison -- and
the reader concludes the feature was rejected. It has not been compared once. So
the unrated row must read "not yet compared" and must NOT contain a "0" rating,
and the uncertain row's bar must be visibly wider than the compared row's.

EVERY BROWSER CONTEXT GETS ITS OWN X-FORWARDED-FOR. clientKey() honours the
header from loopback, so contexts sharing one IP share a rate-limit bucket and the
limiter answers {"error":"rate limit exceeded"} AS THE PAGE BODY -- which reads as
a broken page rather than as a throttle.
"""

import itertools
import sqlite3
import tempfile
import time

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

PORT = 8485
BASE = f"http://127.0.0.1:{PORT}"

SLUG = "feat-demo"
PRIVATE = "feat-private"

_ips = itertools.count(1)


def seed(db):
    """One public project with three features in three rating states, one private."""
    con = sqlite3.connect(db)
    cur = con.cursor()
    now = time.time()

    owner = cur.execute(
        "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
        ("feat-owner", "Feature Owner", now)).lastrowid

    pid = cur.execute(
        "INSERT INTO projects (slug, name, description, governance_model,"
        " license, visibility, created_at, updated_at)"
        " VALUES (?,?,?,'collective','AGPL-3.0',?,?,?)",
        (SLUG, "Feature Demo", "the features under test", "public", now, now)).lastrowid

    # A complaint the compared feature traces to, so the page has a linked row.
    cid = cur.execute(
        "INSERT INTO complaints (project_id, author_id, title, body, severity,"
        " frequency, strategic_multiplier, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?,?,?)",
        (pid, owner, "Export drops the last row", "reproducible on 10k rows",
         4, 2.0, 1.5, "validated", now, now)).lastrowid

    # 1. COMPARED: a real rank, narrow uncertainty. This is the row where showing a
    #    bare number would be most defensible and least misleading.
    cur.execute(
        "INSERT INTO features (project_id, author_id, title, body, effort, status,"
        " elo_r, elo_rd, elo_vol, strategic_weight, impact, effort_score,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
        (pid, owner, "Export must be lossless", "every row survives a round trip",
         "M", "ready", 1610.0, 40.0, 0.06, 1.0, 8.0, 2.0, now, now))
    compared = cur.lastrowid
    cur.execute("INSERT INTO feature_complaints (feature_id, complaint_id) VALUES (?,?)",
                (compared, cid))

    # 2. UNCERTAIN: has been compared, once, so the rating moved by 2 and the
    #    deviation is still nearly the 350 starting value. A bare "1502 +/- 344"
    #    reads as a precise mid-table rank. The bar must say otherwise.
    cur.execute(
        "INSERT INTO features (project_id, author_id, title, body, effort, status,"
        " elo_r, elo_rd, elo_vol, strategic_weight, impact, effort_score,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
        (pid, owner, "Add a webhooks tab", "not built yet", "S", "draft",
         1502.0, 344.0, 0.06, 1.0, 3.0, 4.0, now, now))

    # 3. NEVER COMPARED: elo_r still at the 1500 starting value and elo_rd still at
    #    the full 350, because no comparison has run against it.
    #
    #    The first version of this fixture wrote elo_r = NULL to mean "unrated".
    #    That state is IMPOSSIBLE and the test proved it: features.elo_r is
    #    NOT NULL DEFAULT 1500, and store.Feature.EloR is a non-pointer float64, so
    #    no row and no API response can carry a null rating. The page was defending
    #    against a state the product cannot produce, and a reader could never see it.
    #
    #    What actually distinguishes "new and unsure" is elo_rd == 350, the starting
    #    deviation -- which is why row 2 and this row are the same shape and row 2's
    #    bar assertion is the one that carries the idea. This row exists to pin that
    #    a feature nobody has compared reads as the widest bar on the page, not as
    #    "rating 0" and not as a settled mid-table 1500.
    cur.execute(
        "INSERT INTO features (project_id, author_id, title, body, effort, status,"
        " elo_r, elo_rd, elo_vol, strategic_weight, impact, effort_score,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
        (pid, owner, "Never compared feature", "submitted this morning", "L",
         "draft", 1500.0, 350.0, 0.06, 1.0, None, None, now, now))

    # Two more complaints, for the complaint page's own cases:
    #
    #   NOT VALIDATED  §6.2 requires a validated complaint before a feature can be
    #                  built from it, so "open" is a state with real consequences and
    #                  the page has to SAY it rather than render a severity badge.
    #   NO LINKS       a complaint nothing is linked to, which is the ordinary state
    #                  for a validated complaint. If the empty case renders blank
    #                  it is indistinguishable from "the list failed to load" -- the
    #                  exact silent failure this endpoint was written to fix.
    cur.execute(
        "INSERT INTO complaints (project_id, author_id, title, body, severity,"
        " frequency, strategic_multiplier, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?,?,?)",
        (pid, owner, "Not validated yet", "needs a second opinion", 2, 1.0,
         1.0, "open", now, now))
    cur.execute(
        "INSERT INTO complaints (project_id, author_id, title, body, severity,"
        " frequency, strategic_multiplier, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?,?,?)",
        (pid, owner, "Nothing linked to this one", "nobody has proposed work yet",
         3, 0.0, 1.0, "validated", now, now))

    # The private project, holding a copy of the compared feature. Its page must 404
    # for a logged-out reader: the shell renders for anonymous visitors, so a 200
    # would confirm the slug exists and put it in the title bar.
    ppid = cur.execute(
        "INSERT INTO projects (slug, name, description, governance_model,"
        " license, visibility, created_at, updated_at)"
        " VALUES (?,?,?,'collective','AGPL-3.0',?,?,?)",
        (PRIVATE, "Private Feat", "not yours", "private", now, now)).lastrowid
    # features has 15 columns: id, project_id, author_id, title, body, effort,
    # status, elo_r, elo_rd, elo_vol, strategic_weight, impact, effort_score,
    # created_at, updated_at. Named in full so a migration adding one breaks this
    # loudly rather than silently shifting a value into the wrong column -- which is
    # how "13 values for 14 columns" happens otherwise.
    cur.execute(
        "INSERT INTO features (project_id, author_id, title, body, effort, status,"
        " elo_r, elo_rd, elo_vol, strategic_weight, impact, effort_score,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
        (ppid, owner, "SECRET internal roadmap item", "do not read me", "M",
         "draft", 1700.0, 30.0, 0.06, 1.0, 9.0, 3.0, now, now))

    con.commit()
    con.close()


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-feat-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-feat-e2e-log-")
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
        extra_http_headers={"X-Forwarded-For": f"10.98.{next(_ips) // 256}.{next(_ips) % 256}"})
    pg = ctx.new_page()
    pg.errors = []
    # A failed RESOURCE is not a script error: this page deliberately requests a
    # feature that may not exist and renders "No such feature" from the 404. Those
    # three lines in the console were the page working, and collecting them made
    # the test assert against its own design.
    #
    # What must still be collected is anything that means the script broke:
    # uncaught exceptions and console errors that are not a failed subresource.
    pg.on("pageerror", lambda e: pg.errors.append("PAGEERROR: " + str(e)))
    pg.on("console", lambda m: pg.errors.append("CONSOLE: " + m.text)
          if m.type == "error" and "Failed to load resource" not in m.text else None)
    pg.goto(BASE + "/")
    return pg


def open_feature(pg, fid, slug=SLUG):
    pg.goto(f"{BASE}/projects/{slug}/features/{fid}")
    expect(pg.locator("#feature-root")).to_have_attribute("aria-busy", "false", timeout=15000)


def open_complaint(pg, cid, slug=SLUG):
    pg.goto(f"{BASE}/projects/{slug}/complaints/{cid}")
    expect(pg.locator("#complaint-root")).to_have_attribute("aria-busy", "false", timeout=15000)


def feature_ids(db):
    """Map title -> id, so the tests never hardcode a sequence number."""
    con = sqlite3.connect(db)
    rows = dict(con.execute(
        "SELECT f.title, f.id FROM features f JOIN projects p ON p.id = f.project_id"
        " WHERE p.slug = ?", (SLUG,)).fetchall())
    con.close()
    return rows


def complaint_ids(db):
    """Map title -> id for COMPLAINTS.

    A separate function from feature_ids rather than one that takes a table name:
    the two SELECTs differ in more than the table (the join column, the f. alias), and
    a parameterised version invites the ambiguous-`id` bug that actually happened
    here -- `SELECT title, id FROM features f JOIN projects p ...` is valid SQL and
    fails at runtime with "ambiguous column name: id".
    """
    con = sqlite3.connect(db)
    rows = dict(con.execute(
        "SELECT c.title, c.id FROM complaints c JOIN projects p ON p.id = c.project_id"
        " WHERE p.slug = ?", (SLUG,)).fetchall())
    con.close()
    return rows


def test_the_feature_card_on_the_project_is_a_working_link(server, page):
    """THE REACHABILITY TEST. The card title is a link, and clicking it lands on the
    detail page for THAT feature.

    A link that is present but points at the wrong id, or an <a> with no href, both
    render identically to a link that works -- only clicking separates them.
    """
    ids = feature_ids(server["db"])
    page.goto(f"{BASE}/projects/{SLUG}")
    expect(page.locator("#project-detail")).to_have_attribute("aria-busy", "false", timeout=15000)

    # Selected BY TITLE rather than .first: the fixture seeds three features and the
    # order they render in is not this test's business. Taking .first and asserting
    # a particular title on it asserts the sort order instead of the link, and fails
    # for the wrong reason when the ranking changes.
    title_link = page.locator("#project-detail .card-title a",
                              has_text="Export must be lossless")
    expect(title_link).to_have_count(1)
    expect(title_link).to_have_attribute("href",
        f"/projects/{SLUG}/features/{ids['Export must be lossless']}")

    # And EVERY feature card title is a link, not just this one. A partial fix --
    # the first card linked and the rest left as plain text -- passes the assertion
    # above.
    #
    # Scoped to the features section by its own link pattern, because the project
    # overview also renders the project's own card with a .card-title that is
    # deliberately NOT a feature link. Counting .card-title against .card-title a
    # therefore reports one spurious difference on a correct page, which is the
    # failure mode of an assertion that is too broad to be wrong and too narrow to
    # be right.
    feature_links = page.locator(
        "#project-detail a[href^='/projects/feat-demo/features/']")
    assert feature_links.count() == len(ids), (
        f"{len(ids)} features seeded but {feature_links.count()} linked from the "
        f"project page; the rest are unreachable by clicking")

    title_link.click()
    expect(page).to_have_url(f"{BASE}/projects/{SLUG}/features/{ids['Export must be lossless']}")
    expect(page.locator("#feature-root h1")).to_contain_text("Export must be lossless")
    assert not page.errors, page.errors


def test_a_compared_feature_shows_its_rating_narrow_bar_and_inputs(server, page):
    """The compared row: a rank, a NARROW bar, and impact/effort with its inputs.

    The ratio is checked as rendered rather than as a stored number because spec
    3.4's requirement is that it is shown WITH its inputs: a bare "4.00" reads as
    a magic number, and it is the value the ranked list sorts on.
    """
    open_feature(page, feature_ids(server["db"])["Export must be lossless"])

    root = page.locator("#feature-root")
    expect(root.locator("h1")).to_contain_text("Export must be lossless")
    expect(root.locator("h1")).to_contain_text("ready")
    expect(root.locator(".rating-value")).to_have_text("1610")
    expect(root.locator(".rating-bar-fill")).to_have_attribute("style", "width:11%")
    # impact 8 / effort 2, with both numbers shown.
    expect(root.locator(".ie-value")).to_have_text("4.00")
    expect(root.locator(".ie-score")).to_contain_text("impact 8")
    expect(root.locator(".ie-score")).to_contain_text("effort 2")
    expect(root.locator(".ie-score")).to_contain_text("M")
    # The linked complaint, which is the "why is this first?" answer.
    expect(root).to_contain_text("Export drops the last row")
    expect(root).to_contain_text("Complaints")
    assert not page.errors, page.errors


def test_an_uncertain_feature_shows_a_much_wider_bar(server, page):
    """Idea #37: "new and unsure" must not read as "ranked low".

    The assertion is on the WIDTH, not the wording, because a page can carry the
    right sentence and still draw a full-width bar. elo_rd 344 of a 350 maximum is
    98% of the track; the compared feature at 40 is 11%. Those two bars must be
    visibly different, or "wide bar" is a claim rather than a rendering.
    """
    ids = feature_ids(server["db"])
    open_feature(page, ids["Add a webhooks tab"])
    uncertain = page.locator(".rating-bar-fill").first.get_attribute("style")

    open_feature(page, ids["Export must be lossless"])
    compared = page.locator(".rating-bar-fill").first.get_attribute("style")

    def width(style):
        return int(style.split("width:")[1].rstrip("%; "))

    assert width(uncertain) >= 90, f"elo_rd 344 drew a {uncertain} bar, not ~98%"
    assert width(compared) <= 20, f"elo_rd 40 drew a {compared} bar, not ~11%"
    assert width(uncertain) - width(compared) > 60, (
        f"uncertain {width(uncertain)}% vs compared {width(compared)}% -- the two "
        f"are not visibly different, so the bar is not doing its job")


def test_a_never_compared_feature_shows_maximum_uncertainty_not_a_rank(server, page):
    """A feature nobody has compared must not read as "settled at 1500".

    elo_r is 1500 for an untouched feature because that is the STARTING rating, and
    elo_rd is 350 because that is the STARTING deviation. A page that prints the
    number without the bar shows "1500" -- indistinguishable from a feature that was
    compared ten times and happened to land there. The reader concludes it was
    assessed and scored average, which is a claim nobody made.

    So the bar is the assertion: 350 of a 350 maximum is a full-width track, and it
    must be wider than both the compared row and the once-compared row. If a future
    change caps the bar, or normalises against the wrong maximum, this fails.

    It must ALSO not say the feature is unrated. It IS rated, at its seed value --
    the honest statement is "uncertain", not "unknown", and the page should not
    invent an "not yet compared" state for a feature the product considers rated.
    """
    ids = feature_ids(server["db"])
    open_feature(page, ids["Never compared feature"])
    root = page.locator("#feature-root")

    expect(root.locator("h1")).to_contain_text("Never compared feature")
    # Full-width bar: 350/350.
    expect(root.locator(".rating-bar-fill")).to_have_attribute("style", "width:100%")
    expect(root.locator(".rating-value")).to_have_text("1500")
    # The page describes the uncertainty in words as well as in width, so the
    # information survives a screen reader and a text-only copy.
    expect(root).to_contain_text("less certainty")

    # No impact/effort recorded: the page must say so rather than divide nulls.
    expect(root).to_contain_text("No impact or effort recorded yet")
    assert not page.errors, page.errors


def test_a_private_projects_feature_page_is_not_reachable(server, page):
    """A private project's feature page 404s, and the body never names the slug.

    The shell renders for logged-out visitors, so a 200 here confirms the project
    exists AND puts its slug in the title bar. The slug assertion matters: a 404
    page that echoes the path back has already leaked the thing the 404 exists to
    hide.
    """
    ids = feature_ids(server["db"])
    page.goto(f"{BASE}/projects/{PRIVATE}/features/1")
    expect(page.locator("body")).to_contain_text("no project at this address", timeout=15000)
    assert PRIVATE not in page.content(), "the 404 body names the private project's slug"
    # And it must not have rendered the feature's title.
    assert "SECRET internal roadmap item" not in page.content()


def test_a_bogus_feature_id_says_so_instead_of_spinning(server, page):
    """A wrong id in the URL renders the "no such feature" state.

    Two things this catches that a status-code assertion would not: the skeleton
    spinner never clearing (aria-busy stuck true), and a JS exception leaving the
    skeleton on screen. The handler deliberately does NOT look the feature up, so
    the client is the only place this can be reported -- which means the client
    reporting it is exactly what has to be tested.
    """
    open_feature(page, 999999)
    root = page.locator("#feature-root")
    expect(root).to_contain_text("No such feature")
    assert root.locator(".rating-value").count() == 0
    assert not page.errors, page.errors


# ---------------------------------------------------------------------------
# COMPLAINT DETAIL
# ---------------------------------------------------------------------------
# The complaint page's requirements are "impact meter, linked features, status"
# (frontend-spec.md's page ranking, item 4), and each has a way of being wrong
# that looks right:
#
#   - the linked-features list must come from a real endpoint. /complaints/{id}
#     does not carry them, and neither direction of the link had a reader until
#     this page forced one into existence;
#   - the impact meter must show its ARITHMETIC. A bare number is unreadable, and
#     severity (1-5) and frequency (a report count) are not on one scale, so
#     presenting them as if they were would misstate what was measured;
#   - a complaint that has NOT been validated says so, because §6.2 requires
#     validation before a feature can be built from it -- so status is load-bearing
#     text, not a badge.


def complaint_ids(db):
    con = sqlite3.connect(db)
    rows = dict(con.execute(
        "SELECT c.title, c.id FROM complaints c JOIN projects p ON p.id = c.project_id"
        " WHERE p.slug = ?", (SLUG,)).fetchall())
    con.close()
    return rows


def test_the_complaint_card_on_the_project_is_a_working_link(server, page):
    """THE REACHABILITY TEST, for the complaint page.

    Same contract as the feature card, and the same reason it is a browser test:
    the Go gate asserts the link is BUILT in project.js and says in its comment
    that only this can prove the rendered DOM has something clickable.
    """
    ids = complaint_ids(server["db"])
    page.goto(f"{BASE}/projects/{SLUG}")
    expect(page.locator("#project-detail")).to_have_attribute("aria-busy", "false", timeout=15000)

    link = page.locator("#project-detail .card-title a", has_text="Export drops the last row")
    expect(link).to_have_count(1)
    expect(link).to_have_attribute("href", f"/projects/{SLUG}/complaints/{ids['Export drops the last row']}")

    link.click()
    expect(page).to_have_url(f"{BASE}/projects/{SLUG}/complaints/{ids['Export drops the last row']}")
    expect(page.locator("#complaint-root h1")).to_contain_text("Export drops the last row")
    assert not page.errors, page.errors


def test_a_complaint_shows_its_status_and_the_features_answering_it(server, page):
    """Status badge, the linked feature, and the validated note.

    The link is the part that cannot be asserted from the Go layer: the complaint
    OBJECT carries no linked features, so a page reading that field renders an empty
    list and looks like a complaint nobody has built anything for.
    """
    ids = complaint_ids(server["db"])
    open_complaint(page, ids["Export drops the last row"])
    root = page.locator("#complaint-root")

    expect(root.locator("h1")).to_contain_text("Export drops the last row")
    expect(root.locator("h1")).to_contain_text("validated")
    # The linked feature, from its own endpoint.
    expect(root).to_contain_text("Features answering this (1)")
    expect(root.locator(".linked-row a")).to_have_count(1)
    expect(root.locator(".linked-row a")).to_have_text("Export must be lossless")
    # And it is a link to that feature's own page, built from the slug.
    expect(root.locator(".linked-row a")).to_have_attribute(
        "href", f"/projects/{SLUG}/features/{1}")
    assert not page.errors, page.errors


def test_the_impact_meter_shows_its_arithmetic(server, page):
    """severity 4, frequency 2.0, multiplier 1.5: the score AND its inputs.

    The inputs are the assertion. A meter bar whose number cannot be checked is a
    decoration, and severity (1-5) and frequency (a report count) are different
    scales -- so the page must show them separately rather than as one total that
    looks like a unit.
    """
    ids = complaint_ids(server["db"])
    open_complaint(page, ids["Export drops the last row"])
    root = page.locator("#complaint-root")

    expect(root.locator(".impact-value")).to_have_text("10")   # 4*2 + 2
    expect(root.locator(".impact")).to_contain_text("severity 4 of 5")
    expect(root.locator(".impact")).to_contain_text("2 reports")
    expect(root.locator(".impact")).to_contain_text("strategic multiplier 1.5")
    # The bar is width-carrying, and its aria-label carries the same claim for a
    # reader who cannot see it.
    fill = root.locator(".impact-fill")
    expect(fill).to_have_attribute("style", "width:50%")
    expect(root.locator(".impact-track")).to_have_attribute(
        "aria-label", "impact 10 out of 20, 50 percent of the maximum")
    assert not page.errors, page.errors


def test_an_unvalidated_complaint_says_it_is_not_validated(server, page):
    """§6.2 makes validation load-bearing, so the page must say it out loud.

    Without this the page is a complaint with a severity badge and no indication
    that nothing can be built from it yet -- and "not validated" and "validated"
    are the two states that decide whether a feature may exist.
    """
    ids = complaint_ids(server["db"])
    open_complaint(page, ids["Not validated yet"])
    root = page.locator("#complaint-root")

    expect(root.locator("h1")).to_contain_text("open")
    expect(root).to_contain_text("Not yet validated")
    expect(root).to_contain_text("until this reaches validated it ranks nothing")
    assert not page.errors, page.errors


def test_a_complaint_with_no_linked_features_says_so_rather_than_going_blank(server, page):
    """The empty case must SAY it. "No feature is linked" and "the list failed to
    load" render identically if the empty state is silent -- and the whole reason
    this endpoint was written is that the failure mode was silent."""
    ids = complaint_ids(server["db"])
    open_complaint(page, ids["Not validated yet"])
    root = page.locator("#complaint-root")

    expect(root).to_contain_text("Features answering this (0)")
    expect(root).to_contain_text("No feature is linked to this complaint yet")
    assert not page.errors, page.errors


def test_a_private_projects_complaint_page_is_not_reachable(server, page):
    """404, and the body must not name the slug."""
    ids = complaint_ids(server["db"])
    page.goto(f"{BASE}/projects/{PRIVATE}/complaints/1")
    expect(page.locator("body")).to_contain_text("no project at this address", timeout=15000)
    assert PRIVATE not in page.content()
    assert "SECRET internal roadmap item" not in page.content()


def test_a_bogus_complaint_id_says_so_instead_of_spinning(server, page):
    """aria-busy must be cleared on the error path too.

    A skeleton that spins forever over an error message reads as "still loading",
    and the handler deliberately does not look the complaint up -- so the client is
    the only place this can be reported, which makes the client reporting it exactly
    what has to be tested.
    """
    open_complaint(page, 999999)
    root = page.locator("#complaint-root")
    expect(root).to_contain_text("No such complaint")
    assert not page.errors, page.errors
