"""End-to-end tests for the criteria comparison table (spec §6.4).

Prerequisites:  make build
Run:            pytest tests/e2e/criteria_e2e.py -v

Why this suite exists separately from `internal/httpapi/criteria_page_test.go`:

  - the Go gate proves the page is registered, renders its slug and mounts the root
    criteria.js writes into, and that the script still contains the rules;
  - `internal/httpapi/criteria_api_test.go` proves the endpoints answer the right
    JSON under a weighting;
  - NEITHER proves a browser gets the two together, and this page is the first whose
    whole output is arithmetic done server-side and rendered client-side. A page
    that posts an empty body and ignores the reply passes every source check.

The fixture seeds criteria, ratings and votes DIRECTLY in sqlite. A browser test
whose data comes from the endpoints under test cannot fail for the reason it exists:
a broken write path would produce an empty table and an assertion that the table is
empty would pass.

The three claims worth a browser here, in the spec's own terms:

  1. a criterion with no votes reads `unrated`, not the normalised midpoint of an
     empty distribution (0.50), which would present an artifact as a measurement;
  2. the weight vector CHANGES THE LEADER -- two criteria with opposite winners, so
     a page that ignores the weights cannot produce both orders;
  3. `skip` and `both`/`neither` are recorded and move no rating, which is the
     difference between a vote that did nothing and a vote that was lost.
"""

import os
import sqlite3
import tempfile
import time

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

PORT = 8426
BASE = f"http://127.0.0.1:{PORT}"

# The rate limiter buckets by client key at 100 requests/minute and clientKey()
# honours X-Forwarded-For from loopback, so every test sharing 127.0.0.1 shares one
# bucket. This suite opens ~10 pages with three requests each and would blow the
# budget partway through, failing with the error rendered as the page body. Same
# per-test counter as the finder and project-panel suites.
_xff_counter = {"n": 0}


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-criteria-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-criteria-e2e-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    seed(db)
    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


def _context(browser, token=None):
    """A browser context, optionally signed in with both credentials.

    The cookie is what makes the PAGE request carry the actor (a navigation cannot
    carry a token); the localStorage token is what the page's own fetches present.
    They are different credentials on purpose -- the cookie is not an API
    credential -- so a signed-in page needs both.
    """
    _xff_counter["n"] += 1
    n = _xff_counter["n"]
    ctx = browser.new_context(
        extra_http_headers={"X-Forwarded-For": f"10.55.{n // 200}.{n % 200}"})
    if token:
        ctx.add_init_script("localStorage.setItem('concord.token', %r);" % token)
        ctx.add_cookies([{"name": "concord_nav", "value": token, "url": BASE + "/"}])
    return ctx


@pytest.fixture()
def page(browser, server):
    ctx = _context(browser, _mint_token(server["db"], "crit-owner"))
    pg = ctx.new_page()
    # Any JS exception is a failure: a TypeError in criteria.js renders an empty
    # table and every "the table says X" assertion below would fail with a message
    # pointing at the data instead of at the script.
    errors = []
    pg.on("pageerror", lambda e: errors.append(str(e)))
    pg.on("console", lambda m: errors.append(m.text) if m.type == "error" else None)
    pg.errors = errors
    yield pg
    ctx.close()


@pytest.fixture()
def anon(browser, server):
    """A visitor who cannot do anything but read: no token, no cookie."""
    ctx = _context(browser, None)
    pg = ctx.new_page()
    yield pg
    ctx.close()


def _mint_token(db, username):
    """A token row for `username`, the way the login handler would write one.

    The fixture seeds users with sqlite and no password, so there is no login to
    perform. Two details are the server's, not invented here, and both cost a
    debugging round when guessed wrong (the same note is in
    tests/e2e/project_panels_e2e.py):

      - api_tokens.token_hash is base64 RawStd of the sha256 digest, NOT hex, and
        NOT what 0001_init.sql's comment says ("sha256 hex"). The code is authority.
      - ResolveToken rejects a token that does not start with `clt_` before it looks
        it up, so the raw value carries the prefix newToken() writes.
    """
    import base64
    import hashlib
    raw = "clt_criteria-" + username
    con = sqlite3.connect(db)
    uid = con.execute("SELECT id FROM users WHERE username = ?", (username,)).fetchone()[0]
    digest = hashlib.sha256(raw.encode()).digest()
    con.execute(
        "INSERT OR REPLACE INTO api_tokens (token_hash, user_id, created_at, last_used_at)"
        " VALUES (?,?,?,?)",
        (base64.b64encode(digest).decode().rstrip("="), uid, time.time(), time.time()))
    con.commit()
    con.close()
    return raw


# The criterion named with HTML, so escaping is observable in the DOM rather than
# argued about in review. A UI mutation gate needs hostile INPUT: `esc()` and no
# `esc()` render `Risk` identically.
HOSTILE_NAME = '<img src=x onerror="window.__xss=1">'


def seed(db):
    """Two criteria with OPPOSITE winners, one retired, one unrated, one hostile.

    Shapes, and why each exists:

      Simplicity   higher_is_better; Fast cache 1700 > Risky rewrite 1500 > Small fix 1300
      Risk         lower_is_better;  Risky rewrite 1100 < Fast cache 1500, Small fix UNRATED
      Retired      active=0, with ratings: must be out of the blend unless asked for
      <img ...>    a criterion whose NAME is markup

    Opposite winners are the point: a page that ignores the weight vector cannot
    show Fast cache leading under Simplicity and Risky rewrite leading under Risk.
    """
    con = sqlite3.connect(db)
    cur = con.cursor()
    now = time.time()

    def user(username):
        cur.execute("INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
                    (username, username, now))
        return cur.lastrowid

    owner = user("crit-owner")
    voter = user("crit-voter")

    cur.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " visibility, created_at, updated_at)"
        " VALUES (?,?,?,'maintainer_led','MIT','public',?,?)",
        ("crit-e2e", "Criteria E2E", "Fixture project", now, now))
    pid = cur.lastrowid
    cur.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,'owner',?)",
                (pid, owner, now))

    def feature(title):
        cur.execute(
            "INSERT INTO features (project_id, author_id, title, body, effort, status,"
            " created_at, updated_at) VALUES (?,?,?,'','M','discussion',?,?)",
            (pid, owner, title, now, now))
        return cur.lastrowid

    fast = feature("Fast cache")
    risky = feature("Risky rewrite")
    small = feature("Small fix")

    def criterion(slug, name, direction, active=1, weight=1.0):
        cur.execute(
            "INSERT INTO criteria (project_id, slug, name, description, direction,"
            " default_weight, active, created_by, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?,?,?,?)",
            (pid, slug, name, name + " — fixture criterion", direction, weight, active, owner, now, now))
        return cur.lastrowid

    sim = criterion("simplicity", "Simplicity", "higher_is_better")
    risk = criterion("risk", "Risk", "lower_is_better")
    retired = criterion("retired", "Migrating is easy", "higher_is_better", active=0)
    # Seeded with rows at the Glicko starting state and zero games: a real column of
    # "unrated" cells, which is what makes "ignore criteria with no votes"
    # observable -- an empty pool contributes nothing and is invisible either way.
    speculative = criterion("speculative", "Speculative", "higher_is_better")
    criterion("hostile", HOSTILE_NAME, "higher_is_better")

    def rating(criterion_id, feature_id, r, rd=50, games=5):
        cur.execute(
            "INSERT INTO criterion_ratings (criterion_id, feature_id, r, rd, sigma,"
            " games, updated_at) VALUES (?,?,?,?,0.06,?,?)",
            (criterion_id, feature_id, r, rd, games, now))

    rating(sim, fast, 1700, games=5)
    rating(sim, risky, 1500, games=4)
    rating(sim, small, 1300, games=3)
    rating(risk, risky, 1100, games=5)
    rating(risk, fast, 1500, games=4)
    # Small fix has NO risk rating on purpose: that cell must read `unrated`, and an
    # absent contribution is the same fact as a games == 0 one.
    rating(retired, fast, 1600, games=3)
    rating(retired, risky, 1400, games=2)
    # rows at the starting state: RD at InitialRD (350) and no games played
    rating(speculative, fast, 1500, rd=350, games=0)
    rating(speculative, risky, 1500, rd=350, games=0)

    con.commit()
    con.close()
    return {"owner": owner, "voter": voter, "pid": pid,
            "features": {"fast": fast, "risky": risky, "small": small}}


def open_criteria(page):
    """Load the page and wait for the table to be hydrated.

    The wait is on the table element rather than on load, because the page paints
    instantly and fills in after four fetches. A bare goto + assert would race them,
    and a test that flakes is a test that gets deleted.
    """
    page.goto(f"{BASE}/projects/crit-e2e/criteria")
    page.wait_for_selector(".crit-table", timeout=15000)
    return page.locator("#criteria-root")


def column_index(page, name):
    """The cell index of a criterion's column, read from the rendered header.

    Read from the DOM rather than computed: the column set depends on which
    criteria the composite used, which is exactly what several tests assert about.
    """
    headers = page.locator(".crit-table thead th").all_inner_texts()
    for i, h in enumerate(headers):
        if name in h:
            return i
    return -1


def cell_text(page, row, index):
    return page.locator(".crit-table tbody tr").nth(row).locator("td").nth(index).inner_text()


def weight_input(page, name):
    return page.locator(".crit-weight", has_text=name).locator("input")


def rank_with(page, weights=None, include_inactive=False, require_min_rd=False):
    """Set the controls, press Rank, and wait for the table to come back.

    `weights` maps criterion name -> number. Anything not named is left as it is.
    The wait is on the request finishing (the button is re-rendered), because an
    immediate assertion reads the PREVIOUS order.
    """
    for name, value in (weights or {}).items():
        weight_input(page, name).fill(str(value))
    box = page.locator("#crit-include-inactive")
    if box.is_checked() != include_inactive:
        box.click()
    box2 = page.locator("#crit-require-min-rd")
    if box2.is_checked() != require_min_rd:
        box2.click()
    page.locator("[data-rank]").click()
    page.wait_for_function(
        "() => !document.getElementById('criteria-notice').textContent", timeout=15000)


def first_row_feature(page):
    return page.locator(".crit-table tbody tr").first.locator("td").nth(1).inner_text()


# ---------------------------------------------------------------------------
# The comparison table
# ---------------------------------------------------------------------------

def test_the_table_has_a_column_per_active_criterion_and_a_row_per_feature(page):
    open_criteria(page)
    rows = page.locator(".crit-table tbody tr")
    expect(rows).to_have_count(3)

    titles = [cell_text(page, i, 1) for i in range(3)]
    for want in ("Fast cache", "Risky rewrite", "Small fix"):
        assert want in titles, f"{want} is not in the table: {titles}"

    headers = page.locator(".crit-table thead th").all_inner_texts()
    joined = " | ".join(headers)
    assert "Simplicity" in joined and "Risk" in joined, headers
    # The retired criterion is out of the blend by default, so it is out of the
    # table: a column that contributed nothing would be a column of dashes.
    assert "Migrating is easy" not in joined, (
        "a retired criterion is in the table without being asked for: " + joined)
    # The three leading columns are rank, feature and composite; every column after
    # them is a criterion and must state the weight it was ranked with.
    for col in headers[3:]:
        assert "w " in col, (
            "a criterion column does not state the weight it was ranked with, so the "
            "reader cannot see what produced the order: " + col)


def test_a_criterion_with_no_votes_reads_unrated_rather_than_a_number(page):
    """The lie this test exists to prevent: 0.50 for an empty distribution."""
    open_criteria(page)
    idx = column_index(page, "Risk")
    assert idx > 0, "the Risk column is not in the table"
    rows = page.locator(".crit-table tbody tr")
    titles = [cell_text(page, i, 1) for i in range(rows.count())]
    small_row = titles.index("Small fix")
    cell = cell_text(page, small_row, idx)
    assert cell.strip() == "unrated", (
        f"a feature with no risk ratings renders {cell!r}; the Glicko starting "
        "rating's normalised midpoint is an artifact of an empty distribution, not a "
        "measurement")
    assert "0.50" not in cell, cell

    # And the rated cells DO carry a number and the count behind it.
    risky_row = titles.index("Risky rewrite")
    rated = cell_text(page, risky_row, idx)
    assert "5" in rated, (
        f"a rated cell does not say how many comparisons are behind it: {rated!r}")

    # The other form of the same fact: a criterion with rating ROWS but no games
    # played. The normalised value there is 0.5 by construction -- literally the
    # midpoint of a distribution with nothing in it.
    spec = column_index(page, "Speculative")
    if spec > 0:
        cells = [cell_text(page, i, spec) for i in range(rows.count())]
        assert all(c.strip() == "unrated" for c in cells), (
            "a criterion whose ratings are all at the starting state renders numbers: "
            f"{cells}")


def test_the_weight_vector_changes_which_feature_leads(page):
    """The whole point of a weight vector, and an assertion a page that ignores the
    request body cannot satisfy: the two criteria have opposite winners."""
    open_criteria(page)
    rank_with(page, weights={"Simplicity": 10, "Risk": 0})
    expect(page.locator("#criteria-notice")).to_have_text("")
    on_simplicity = first_row_feature(page)
    assert on_simplicity == "Fast cache", (
        "weighting Simplicity 10:0 does not lead with the best-simplest feature, so "
        f"the weights did not reach the ranking: leader was {on_simplicity!r}")

    rank_with(page, weights={"Simplicity": 0, "Risk": 10})
    on_risk = first_row_feature(page)
    assert on_risk == "Risky rewrite", (
        "weighting Risk 10:0 does not lead with the lowest-risk feature: leader was "
        f"{on_risk!r}. Risk is lower_is_better, so the smallest rating wins")
    assert on_risk != on_simplicity, (
        "the leader did not change when the weighting flipped; the page is rendering "
        "a fixed order")


def test_retired_criteria_join_the_table_only_when_asked_for(page):
    open_criteria(page)
    assert column_index(page, "Migrating is easy") == -1
    rank_with(page, include_inactive=True)
    assert column_index(page, "Migrating is easy") > 0, (
        "ticking 'include retired criteria' did not bring the retired criterion into "
        "the table, so the flag is decorative")
    rank_with(page, include_inactive=False)
    assert column_index(page, "Migrating is easy") == -1, (
        "unticking it did not take the retired criterion back out")


def test_ignoring_criteria_with_no_votes_changes_the_ranking(page):
    """A dimension nobody has an opinion about contributes its starting value to
    every feature equally, which dilutes a result without a vote behind it."""
    open_criteria(page)
    assert column_index(page, "Speculative") > 0, (
        "the fixture's starting-state criterion is not in the table, so this control "
        "has nothing to change")
    rank_with(page, require_min_rd=True)
    expect(page.locator("#criteria-notice")).to_have_text("")
    assert column_index(page, "Speculative") == -1, (
        "ticking 'ignore criteria with no votes' did not drop a criterion whose every "
        "rating is still at the starting state")
    assert column_index(page, "Simplicity") > 0, "the table lost a criterion that has votes"
    rank_with(page, require_min_rd=False)
    assert column_index(page, "Speculative") > 0, (
        "unticking it did not bring the criterion back")


# ---------------------------------------------------------------------------
# Managing criteria
# ---------------------------------------------------------------------------

def test_creating_a_criterion_adds_it_to_the_page(page):
    open_criteria(page)
    before = page.locator(".crit-row").count()
    page.fill("#crit-slug", "ux")
    page.fill("#crit-name", "UX")
    page.fill("#crit-desc", "How it feels to use")
    page.select_option("#crit-direction", "higher_is_better")
    page.locator("[data-create]").click()
    expect(page.locator("#criteria-notice")).to_contain_text("Added")

    expect(page.locator(".crit-row")).to_have_count(before + 1)
    row = page.locator(".crit-row", has_text="UX")
    expect(row).to_have_count(1)
    # A criterion with no votes has no column in the composite (no ratings at all)
    # but it IS in the manage list, because that is where you vote on it.
    expect(row.locator("[data-vote]")).to_have_count(1)
    assert not page.errors, page.errors


def test_retiring_a_criterion_reports_what_that_means(page):
    open_criteria(page)
    row = page.locator(".crit-row", has_text="UX")
    row.locator("[data-toggle-active]").click()
    expect(page.locator("#criteria-notice")).to_contain_text("Retired")
    expect(page.locator("#criteria-notice")).to_contain_text("include retired criteria")
    expect(page.locator(".crit-row", has_text="UX")).to_contain_text("retired")


def test_the_hostile_criterion_name_renders_as_text(page):
    """esc() is the rule; this is the observation. A name carrying markup must be
    text in the DOM, and the handler in it must never run."""
    open_criteria(page)
    xss = page.evaluate("window.__xss")
    assert xss is None, "a criterion name executed script: window.__xss = %r" % (xss,)
    assert page.locator(".crit-table img").count() == 0, (
        "markup from a criterion name became an element in the table")
    assert page.locator(".crit-row img").count() == 0, (
        "markup from a criterion name became an element in the criteria list")
    assert HOSTILE_NAME in page.inner_text("#criteria-root"), (
        "the name is not rendered as text either, so it vanished rather than escaping")


# ---------------------------------------------------------------------------
# Voting, and what a vote does to a rating
# ---------------------------------------------------------------------------

def vote(page, criterion_name, left, right, outcome):
    """Record a comparison, and return the NOTICE.

    The outcome is reported on the notice rather than inside the criterion's row
    because the rank that follows re-renders the whole page: the first version wrote
    the two updated ratings into the row and they were destroyed by its own
    follow-up render, so "Recorded." was the only thing that ever survived.
    """
    row = page.locator(".crit-row", has_text=criterion_name)
    row.locator("[data-vote-a]").select_option(label=left)
    row.locator("[data-vote-b]").select_option(label=right)
    row.locator("[data-vote-outcome]").select_option(outcome)
    row.locator("[data-vote]").click()
    return page.locator("#criteria-notice")


def games_for(page, title, criterion_name):
    open_rows = page.locator(".crit-table tbody tr")
    titles = [cell_text(page, i, 1) for i in range(open_rows.count())]
    idx = column_index(page, criterion_name)
    text = cell_text(page, titles.index(title), idx)
    if text.strip() == "unrated":
        return 0
    digits = "".join(ch for ch in text.split("·")[-1] if ch.isdigit())
    return int(digits or 0)


def test_a_comparison_moves_the_rating_and_reports_both_sides(page):
    open_criteria(page)
    before = games_for(page, "Fast cache", "Simplicity")
    result = vote(page, "Simplicity", "Fast cache", "Small fix", "a")
    expect(result).to_contain_text("Fast cache")
    expect(result).to_contain_text("comparison(s)")
    open_criteria(page)
    after = games_for(page, "Fast cache", "Simplicity")
    assert after == before + 1, (
        f"voting did not move the rating: games went {before} -> {after}. A recorded "
        "vote that changes nothing is indistinguishable from a lost one")


def test_skip_is_recorded_and_moves_nothing(page):
    """Abstention is data, and its whole meaning is that it changes no rating."""
    open_criteria(page)
    before = games_for(page, "Risky rewrite", "Simplicity")
    vote(page, "Simplicity", "Risky rewrite", "Small fix", "skip")
    open_criteria(page)
    after = games_for(page, "Risky rewrite", "Simplicity")
    assert after == before, (
        f"'skip' moved the rating: games went {before} -> {after}. It is recorded for "
        "the audit trail and must change nothing")


def test_a_second_vote_on_the_same_pair_is_refused_with_the_reason(page):
    open_criteria(page)
    vote(page, "Simplicity", "Fast cache", "Risky rewrite", "b")
    expect(page.locator("#criteria-notice")).to_contain_text("Recorded.")
    vote(page, "Simplicity", "Fast cache", "Risky rewrite", "b")
    expect(page.locator("#criteria-notice")).to_contain_text("already voted")
    # The refusal is the server's own words, not a generic failure.
    expect(page.locator("#criteria-notice")).not_to_contain_text("something went wrong")


def test_an_anonymous_visitor_can_read_the_table_but_not_vote(page, server, anon):
    open_criteria(page)
    expect(page.locator(".crit-table")).to_be_visible()

    anon.goto(f"{BASE}/projects/crit-e2e/criteria")
    anon.wait_for_selector(".crit-table", timeout=15000)
    # Reading needs no account: the ranking is the output of a process the public is
    # invited to take part in.
    expect(anon.locator(".crit-table")).to_be_visible()

    row = anon.locator(".crit-row", has_text="Simplicity")
    row.locator("[data-vote-a]").select_option(label="Fast cache")
    row.locator("[data-vote-b]").select_option(label="Small fix")
    row.locator("[data-vote-outcome]").select_option("a")
    row.locator("[data-vote]").click()
    notice = anon.locator("#criteria-notice")
    expect(notice).to_contain_text("signed-in")
    # Preserve the status: an anonymous write must be refused for the right reason,
    # which is what tells a reader to sign in rather than to retry.
    assert "401" not in notice.inner_text() or "sign in" in notice.inner_text().lower()


def test_no_console_errors_on_the_criteria_page(page):
    open_criteria(page)
    assert not page.errors, page.errors
