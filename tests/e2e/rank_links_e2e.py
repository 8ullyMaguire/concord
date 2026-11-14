"""M3's outbound links, in a browser (docs/plans/moderation.md §4b).

Three links and one absence, none of which a Go test can see:

  * `/ranking` rows carry a "Moderate" link ONLY for a reader with standing. The page is
    server-rendered with `data-can-moderate`, and the rows are client-rendered from it, so the
    claim "an anonymous reader is not offered a control the server would refuse" is a DOM
    assertion: `ranking.js` reads an attribute a Go test can only prove is present in the HTML.
  * `/rank` cards link to their own idea page. The link's href must name THAT card's feature id,
    which is the defect a shared href would produce (both cards pointing at one idea).
  * The vote buttons still work while the link sits in the card. A link added to a card is one
    misplaced element away from eating the click that was meant for a vote.

THE FIXTURE SEEDS THE RANKING THROUGH SQLITE and the accounts through the real register route,
because the register route is how a browser gets a token. Membership and trust have no write
route, so they come from the database (curation_e2e.py's shape).

THE REVIEWER IS REACHED THE WAY A BROWSER IS: the `concord_nav` cookie for the navigation (a
navigation cannot carry a bearer header) plus the token in localStorage for the page's own API
reads. A test that set only the token would see `data-can-moderate="false"` and conclude the
feature is missing.
"""

import itertools
import sqlite3
import tempfile

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

# One fixed port per suite; harness_selftest_test.py asserts they are unique.
PORT = 8498
BASE = f"http://127.0.0.1:{PORT}"

SLUG = "rank-links-demo"
PASSWORD = "correct-horse-battery"
OWNER = "rank-owner"
AUTHOR = "rank-author"
REVIEWER = "rank-reviewer"
CONTRIBUTOR = "rank-contributor"

_ips = itertools.count(1)
FEATURES = []  # filled by seed(): [(id, title), (id, title)]
TOKENS = {}    # username -> bearer token, registered once by seed()


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
    dbdir = tempfile.mkdtemp(prefix="concord-rankl-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-rankl-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    seed(db)
    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


def _account(db, username, role, trust):
    """Register the account, put it on the project with `role`, set its trust.

    Called ONCE per account, from seed(). A second call would be a 409 from the register route,
    and a test that re-registered to obtain a token would fail for a reason that has nothing to
    do with the page -- so the tokens are kept in TOKENS instead.
    """
    token, uid = _register(username)
    con = harness.connect(db)
    try:
        pid = con.execute("SELECT id FROM projects WHERE slug = ?", (SLUG,)).fetchone()[0]
        con.execute(
            "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
            (pid, uid, role, 1790000000))
        con.execute("UPDATE users SET trust_level = ? WHERE id = ?", (trust, uid))
        con.commit()
    finally:
        con.close()
    TOKENS[username] = token
    return token, uid


def seed(db):
    """A project with two ranked ideas and three readers: owner, reviewer, contributor."""
    now = 1790000000
    owner_token, owner_uid = _register(OWNER)
    con = harness.connect(db)
    try:
        pid = con.execute(
            "INSERT INTO projects (slug, name, description, governance_model, license,"
            " created_at, updated_at, visibility) VALUES (?,?,?,'collective','MIT',?,?,?)",
            (SLUG, "Rank Links Demo", "fixture", now, now, "public")).lastrowid
        con.execute(
            "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,'owner',?)",
            (pid, owner_uid, now))
        con.execute("UPDATE users SET trust_level = 1 WHERE id = ?", (owner_uid,))

        # Authored by somebody OTHER than the voter: the vote route refuses a self-vote ("a
        # feature cannot be ranked by the person who proposed it"), so a fixture that writes both
        # ideas as the owner can never cast the vote this test needs -- measured, first run.
        author = con.execute(
            "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
            (AUTHOR, "Ada Author", now)).lastrowid
        con.execute(
            "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,'contributor',?)",
            (pid, author, now))
        for title in ("An idea worth opening", "Another idea worth opening"):
            FEATURES.append((con.execute(
                "INSERT INTO features (project_id, author_id, title, body, effort, status,"
                " created_at, updated_at) VALUES (?,?,?,'a body','M','discussion',?,?)",
                (pid, author, title, now, now)).lastrowid, title))

        # `feature_id` is NULL, not 0. The column is a foreign key to `features` and the suite's original
        # SQLite database did not enforce it, so `0` stood for "this arena is the project's, not one
        # feature's" and meant nothing else. PostgreSQL rejects the row outright
        # (`Key (feature_id)=(0) is not present in table "features"`), and NULL is what the schema says
        # "no feature" with — the fixture's INTENT, in the dialect the candidate runs on.
        arena = con.execute(
            "INSERT INTO arenas (type, project_id, feature_id, question, use_case, created_at)"
            " VALUES ('feature-priority',?,NULL,'Which idea should we build next?','fixture',?)",
            (pid, now)).lastrowid
        for fid, _ in FEATURES:
            con.execute(
                "INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd, sigma,"
                " games, is_baseline, parent_entry_id, updated_at)"
                # `is_baseline` is a LITERAL, so the adapter cannot coerce it: PostgreSQL wants
                # `false` where SQLite accepted `0` and the row was refused outright
                # (`column "is_baseline" is of type boolean but expression is of type integer`).
                " VALUES (?,'feature',?,1500.0,350.0,0.06,0,false,NULL,?)",
                (arena, fid, now))
        con.commit()
    finally:
        con.close()

    assert len(FEATURES) == 2, f"the fixture built {len(FEATURES)} features, want 2"
    TOKENS[OWNER] = owner_token
    _account(db, REVIEWER, "reviewer", 0)
    _account(db, CONTRIBUTOR, "contributor", 0)


def _page(browser, token=None, nav=False):
    """A browser page, optionally signed in.

    `nav=True` adds the navigation cookie, which is what makes the SERVER know the reader on a
    page request. Without it the page renders anonymous no matter what the token says.
    """
    ctx = browser.new_context(
        extra_http_headers={"X-Forwarded-For": f"10.97.{next(_ips) % 250}.{next(_ips) % 250}"})
    if nav and token:
        ctx.add_cookies([{"name": "concord_nav", "value": token, "url": BASE + "/"}])
    pg = ctx.new_page()
    pg.errors = []
    pg.on("pageerror", lambda e: pg.errors.append("PAGEERROR: " + str(e)))
    pg.on("console", lambda m: pg.errors.append("CONSOLE: " + m.text)
          if m.type == "error" and "Failed to load resource" not in m.text else None)
    pg.goto(BASE + "/")
    if token:
        pg.evaluate("t => window.localStorage.setItem('concord.token', t)", token)
    return pg


def _open_ranking(pg):
    pg.goto(f"{BASE}/projects/{SLUG}/ranking")
    expect(pg.locator("#ranking-root")).to_have_attribute("aria-busy", "false", timeout=15000)
    expect(pg.locator("#ranking-root table tbody tr")).to_have_count(2, timeout=15000)


def test_an_anonymous_reader_is_offered_no_moderation_control(browser, server):
    """No standing, no link -- and the page must still render the ranking it is public for."""
    pg = _page(browser)
    _open_ranking(pg)
    expect(pg.locator("#ranking-root")).to_have_attribute("data-can-moderate", "false")
    expect(pg.locator("#ranking-root .rank-moderate")).to_have_count(0)
    assert pg.errors == [], pg.errors
    pg.close()


def test_a_reviewer_gets_a_link_to_the_panel_on_every_row(browser, server):
    """The link is per row, and it names that row's idea -- not the first one twice."""
    token = TOKENS[REVIEWER]
    pg = _page(browser, token=token, nav=True)
    _open_ranking(pg)
    expect(pg.locator("#ranking-root")).to_have_attribute("data-can-moderate", "true")
    links = pg.locator("#ranking-root a.rank-moderate")
    expect(links).to_have_count(2)

    hrefs = links.evaluate_all("els => els.map(e => e.getAttribute('href'))")
    want = {f"/projects/{SLUG}/features/{fid}#moderation-panel" for fid, _ in FEATURES}
    assert set(hrefs) == want, f"the row links name {hrefs}, want {sorted(want)}"
    assert pg.errors == [], pg.errors
    pg.close()


def test_a_contributor_is_offered_no_moderation_control(browser, server):
    """Standing is the role ladder, not merely being signed in.

    This is the assertion that keeps the link from becoming decoration: a signed-in reader
    without `reviewer` must see exactly what an anonymous one sees.
    """
    token = TOKENS[CONTRIBUTOR]
    pg = _page(browser, token=token, nav=True)
    _open_ranking(pg)
    expect(pg.locator("#ranking-root")).to_have_attribute("data-can-moderate", "false")
    expect(pg.locator("#ranking-root .rank-moderate")).to_have_count(0)
    assert pg.errors == [], pg.errors
    pg.close()


def test_each_rank_card_links_to_its_own_idea_and_the_vote_still_posts(browser, server):
    """The pair's two links must differ, and the vote buttons must still work beside them."""
    token = TOKENS[OWNER]
    pg = _page(browser, token=token, nav=True)
    pg.goto(f"{BASE}/projects/{SLUG}/rank")
    expect(pg.locator(".rank-card")).to_have_count(2, timeout=15000)

    cards = pg.locator(".rank-card")
    ids = [c.get_attribute("data-feature") for c in cards.all()]
    assert all(ids), "a card does not name its feature; the link cannot be checked"
    hrefs = pg.locator(".rank-card a.rank-open").evaluate_all(
        "els => els.map(e => e.getAttribute('href'))")
    assert hrefs == [f"/projects/{SLUG}/features/{i}" for i in ids], (
        f"the cards link to {hrefs}, want one link per card saying {ids}")

    pg.locator('[data-outcome="a"]').click()
    expect(pg.locator("[data-cast]")).to_have_text("1", timeout=15000)
    assert pg.errors == [], pg.errors
    pg.close()
