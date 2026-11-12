"""R16/R17/R18 in a browser: the roadmap, one milestone, the board filter and the
planning panel (docs/specs/idea-intake-and-milestones-spec.md §4–§6).

Why a browser and not more Go: every claim here is about RENDERED DOM produced by
JavaScript from an API response. A Go test can prove the route returns a milestone;
only the DOM proves that `milestones.js` puts its title on the page, that the board's
filter narrows the cards it DRAWS, and that the feature page selects the right option
in the milestone dropdown.

Four assertions, each one a way this could be wrong while every route test stays green:

  1. `/roadmap` shows the milestone, its date and its progress line. The page is a
     shell; a script that never ran leaves it saying "Loading milestones".
  2. `/milestones/{id}` lists the feature under `open`. The page groups by STATUS,
     so a grouping bug puts shipped work under open.
  3. `/board?milestone=N` draws ONLY that milestone's cards. The board derives a card
     for every feature, so a broken filter would still render -- which is exactly why
     the count has to be asserted rather than the presence.
  4. The feature page's Planning panel has the milestone SELECTED, and the evidence
     badge says complaint-backed. Both are computed by the page from data it loaded;
     a default of "" would look the same on any feature.

THE FIXTURE SEEDS THROUGH SQLITE, and that is deliberate: there is no route that
creates a complaint with a validated link in one call, and a fixture that clicked its
way there would take thirty seconds per run. The API routes the feature needs are
exercised by TestAMilestone* in internal/httpapi; this suite is about the DOM.
"""

import sqlite3
import tempfile

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

# One fixed port per suite; harness_selftest_test.py asserts they are unique.
PORT = 8499
BASE = f"http://127.0.0.1:{PORT}"

SLUG = "ms-demo"
PASSWORD = "correct-horse-battery"
OWNER = "ms-owner"

MILESTONE_TITLE = "v1 — the planning pass"
OPEN_TITLE = "Make the export button export"
OUTSIDE_TITLE = "Unrelated idea outside the milestone"


def _register(username):
    reg = harness.api(BASE, "POST", "/api/v1/auth/register", {
        "username": username,
        "password": PASSWORD,
        "display_name": username,
    })
    assert reg, "register returned nothing"
    token = reg.get("token")
    user = reg.get("user") or {}
    uid = user.get("id") or reg.get("user_id") or reg.get("id")
    assert token and uid, f"register returned no token/user: {reg}"
    return token, uid


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-ms-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-ms-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    state = seed(db)
    yield {"base": base, "db": db, "logdir": logdir, **state}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


def seed(db):
    """A project, a validated complaint, two features, one milestone claiming one.

    The second feature is OUTSIDE the milestone on purpose: a board filter that shows
    everything passes any assertion built on a project with one feature.
    """
    now = 1790000000
    token, uid = _register(OWNER)
    con = sqlite3.connect(db)
    try:
        pid = con.execute(
            "INSERT INTO projects (slug, name, description, governance_model, license,"
            " created_at, updated_at, visibility, intake_mode)"
            " VALUES (?,?,?,'collective','MIT',?,?,?,'complaint_first')",
            (SLUG, "Milestone Demo", "fixture", now, now, "public")).lastrowid
        con.execute(
            "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,'owner',?)",
            (pid, uid, now))
        con.execute("UPDATE users SET trust_level = 1 WHERE id = ?", (uid,))

        # updated_at is NOT NULL on complaints, and the column is easy to miss from a
        # fixture: the first run of this suite failed on exactly that constraint.
        complaint = con.execute(
            "INSERT INTO complaints (project_id, author_id, title, body, status,"
            " severity, frequency, created_at, updated_at)"
            " VALUES (?,?,?,?,'validated',3,1.0,?,?)",
            (pid, uid, "The export button does nothing", "fixture", now, now)).lastrowid

        milestone = con.execute(
            "INSERT INTO milestones (project_id, title, body, target_date, status, created_at)"
            " VALUES (?,?,?,?, 'active', ?)",
            (pid, MILESTONE_TITLE, "the plan", float(now + 14 * 86400), now)).lastrowid

        # Board columns, because GetBoard renders nothing without them: the first run
        # of the board test found "No board yet" behind a correct filter control.
        # The phase names are the BOARD's vocabulary, not the statuses': a feature with
        # status "draft" lands in the column called "inbox" (store.featureStatusToPhase).
        # Columns named after statuses render an empty board with a correct filter.
        for i, (phase, wip) in enumerate([("inbox", 10), ("ready", 5), ("in_progress", 3)], start=1):
            con.execute(
                "INSERT INTO board_columns (project_id, phase, position, wip_limit)"
                " VALUES (?,?,?,?)", (pid, phase, i, wip))

        inside = _feature(con, pid, uid, OPEN_TITLE, complaint, milestone, 1, now)
        outside = _feature(con, pid, uid, OUTSIDE_TITLE, complaint, None, 0, now)
        con.commit()
    finally:
        con.close()
    return {"token": token, "milestone_id": milestone, "feature_id": inside,
            "outside_id": outside}


def _feature(con, pid, uid, title, complaint_id, milestone_id, position, now):
    fid = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, effort, status,"
        # 'draft', not 'proposed': the column has a CHECK listing eight statuses and
        # 'proposed' is not one of them, even though the template default reads like it.
        " elo_r, elo_rd, elo_vol, strategic_weight, created_at, updated_at, milestone_id,"
        " milestone_position) VALUES (?,?,?,?,'S','draft',1500,350,0.06,1,?,?,?,?)",
        (pid, uid, title, "body", now, now, milestone_id, position)).lastrowid
    # feature_complaints is a bare many-to-many (feature_id, complaint_id): there is no
    # linked_at column, which the first run of this suite discovered.
    con.execute(
        "INSERT INTO feature_complaints (feature_id, complaint_id) VALUES (?,?)",
        (fid, complaint_id))
    return fid


def _goto(page, path, token):
    """Load a page as a signed-in reader.

    The nav cookie for the server-rendered navigation, the token in localStorage for
    the page's own API reads: a test that set only the token would see an anonymous
    page, and one that set only the cookie would see every panel 404.
    """
    page.context.add_cookies([{
        "name": "concord_nav", "value": token, "domain": "127.0.0.1", "path": "/",
    }])
    page.add_init_script(
        "window.localStorage.setItem('concord_token', %r);" % token)
    page.goto(BASE + path)
    expect(page.locator("body")).to_be_visible()


def test_the_roadmap_renders_the_milestone_and_its_progress(server, browser):
    page = browser.new_page()
    _goto(page, f"/projects/{SLUG}/roadmap", server["token"])

    column = page.locator(".roadmap-column").first
    expect(column).to_be_visible(timeout=15000)
    expect(column.locator(".roadmap-title")).to_have_text(MILESTONE_TITLE)
    # The progress line names the counts, not a bare percentage: 0/1 shipped with 1
    # open is the honest reading of a milestone whose only feature is proposed.
    expect(column.locator(".ms-counts")).to_contain_text("0/1 shipped")
    expect(column.locator(".ms-counts")).to_contain_text("1 open")
    page.close()


def test_a_milestone_page_groups_its_features_by_status(server, browser):
    page = browser.new_page()
    _goto(page, f"/projects/{SLUG}/milestones/{server['milestone_id']}", server["token"])

    expect(page.locator("#ms-title")).to_have_text(MILESTONE_TITLE, timeout=15000)
    open_group = page.locator(".ms-group", has_text="open")
    expect(open_group).to_be_visible()
    expect(page.locator(f"a[href$='/features/{server['feature_id']}']").first).to_be_visible()
    # The feature outside the milestone must NOT appear on its page.
    expect(page.locator(f"a[href$='/features/{server['outside_id']}']")).to_have_count(0)
    page.close()


def test_the_board_filter_draws_only_the_milestones_cards(server, browser):
    page = browser.new_page()
    _goto(page, f"/projects/{SLUG}/board?milestone={server['milestone_id']}", server["token"])

    # The filter control is offered because milestones exist, and it shows the filter
    # that is in force rather than resetting to "Whole board".
    select = page.locator("[data-board-filter] select")
    expect(select).to_be_visible(timeout=15000)
    expect(select).to_have_value(str(server["milestone_id"]))

    # The derived card for the outside feature is what a broken filter leaves behind.
    expect(page.locator("text=" + OUTSIDE_TITLE)).to_have_count(0)
    expect(page.locator("text=" + OPEN_TITLE).first).to_be_visible()
    page.close()


def test_the_feature_page_selects_its_milestone_and_shows_the_evidence_badge(server, browser):
    page = browser.new_page()
    _goto(page, f"/projects/{SLUG}/features/{server['feature_id']}", server["token"])

    select = page.locator("[data-milestone-select] select")
    expect(select).to_be_visible(timeout=15000)
    expect(select).to_have_value(str(server["milestone_id"]))

    badge = page.locator("[data-evidence-badge]")
    expect(badge).to_have_attribute("data-evidence-badge", "complaint")
    expect(badge).to_have_text("complaint-backed")
    page.close()
