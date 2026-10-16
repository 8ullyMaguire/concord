"""End-to-end tests for §5's arena loop on the alternatives panel — the controls
§29 added, driving real headless Chromium.

Prerequisites:  make build
Run:            pytest tests/e2e/arena_e2e.py -v

Why these exist on top of the Go tests in internal/httpapi/arenas_test.go and
internal/store/arenas_test.go:

  - the HTTP tests prove each route answers correctly on its own;
  - the store tests prove the replay reproduces the live ratings;
  - NEITHER proves the panel puts the loop in front of a reader. The whole finding
    of this pass was a feature that was complete at every layer it was tested at
    and unreachable one layer up.

The fixture seeds the database directly with sqlite rather than through the API, so
a broken write path cannot produce the state these tests assert on. The vote test
does cast through the UI, and that is the point of it: the panel has to be able to
move the ranking it displays.

Nothing here asserts the ABSENCE of the old state. A vote reloads the page, and a
reload satisfies any "the previous content is gone" assertion -- which is how a
swapped endpoint survived the feature suite in §25. Every assertion below is about
something that must APPEAR.
"""

import base64
import hashlib
import sqlite3
import tempfile
import time

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

PORT = 8496
BASE = f"http://127.0.0.1:{PORT}"

PROJECT_SLUG = "arena-e2e"


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-arena-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-arena-e2e-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    seed(db)
    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


def _mint_token(db, username):
    """A token row for `username`, the way the login handler would write one.

    api_tokens.token_hash is base64 RawStd of the sha256 digest (not hex, whatever
    0001_init.sql's comment says) and the raw value must start with `clt_` for
    ResolveToken to look it up at all.
    """
    raw = "clt_arena-" + username
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


_xff = {"n": 0}


def _context(browser, token=None):
    """A browser context, signed in with both credentials.

    The cookie carries the actor on the page NAVIGATION; the localStorage token is
    what the page's own fetches present. They are different credentials.
    """
    _xff["n"] += 1
    n = _xff["n"]
    ctx = browser.new_context(extra_http_headers={"X-Forwarded-For": f"10.66.{n // 200}.{n % 200}"})
    if token:
        ctx.add_init_script("localStorage.setItem('concord.token', %r);" % token)
        ctx.add_cookies([{"name": "concord_nav", "value": token, "url": BASE + "/"}])
    return ctx


@pytest.fixture()
def page(browser, server):
    ctx = _context(browser, _mint_token(server["db"], "arena-owner"))
    pg = ctx.new_page()
    errors = []
    pg.on("pageerror", lambda e: errors.append(str(e)))
    pg.on("console", lambda m: errors.append(m.text) if m.type == "error" else None)
    pg.errors = errors
    yield pg
    ctx.close()


def seed(db):
    """One project with an alternatives arena holding two competing projects.

    Three projects and TWO owners, deliberately: `refuseOwnVote` treats a project's
    owner/maintainer as its author, so if one identity owned the project AND its
    competitors then every vote in this arena would be refused as a self-vote, and
    the vote test would be asserting against a refusal.

    The two competitors are close on purpose (1600 vs 1580): a comparison the arena
    asks about is one that could change the answer, and the vote test needs the
    rating it reads afterwards to be able to move.
    """
    con = sqlite3.connect(db)
    cur = con.cursor()
    now = time.time()

    def user(username):
        cur.execute("INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
                    (username, username, now))
        return cur.lastrowid

    def project(slug, name, owner_id):
        cur.execute(
            "INSERT INTO projects (slug, name, description, governance_model, license,"
            " visibility, created_at, updated_at)"
            " VALUES (?,?,?,'maintainer_led','MIT','public',?,?)",
            (slug, name, "Fixture project", now, now))
        pid = cur.lastrowid
        cur.execute("INSERT INTO members (project_id, user_id, role, joined_at)"
                    " VALUES (?,?,'owner',?)", (pid, owner_id, now))
        return pid

    owner = user("arena-owner")
    rival_owner = user("arena-rival-owner")
    voter = user("arena-voter")

    pid = project(PROJECT_SLUG, "Arena E2E", owner)
    cur.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,'contributor',?)",
                (pid, voter, now))
    rival_a = project("arena-rival-a", "Rival A", rival_owner)
    rival_b = project("arena-rival-b", "Rival B", rival_owner)

    def arena(use_case):
        cur.execute(
            "INSERT INTO arenas (type, project_id, feature_id, question, use_case, created_at)"
            " VALUES ('alternatives',?,0,?,?,?)",
            (pid, "Which is the better alternative for this use case?", use_case, now))
        return cur.lastrowid

    def entry(arena_id, entity, rating, rd, games):
        cur.execute(
            "INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd, sigma,"
            " games, is_baseline, parent_entry_id, updated_at)"
            " VALUES (?,'project',?,?,?,0.06,?,0,NULL,?)",
            (arena_id, entity, rating, rd, games, now))

    # TWO arenas, because the two verdicts the panel can state are different states
    # and a test that has to run after another test to see the second one is
    # asserting the suite's order rather than the panel.
    #
    #   "teams needing audit logs" -- ranked, with a vote log: a comparison the
    #       judge test can cast, and (because its ratings are hand-written and the
    #       log does not produce them) a ranking the verification must REFUSE.
    #   "solo maintainers" -- never judged, so every entry sits at the Glicko prior
    #       and an empty log reproduces it exactly: the verification must AGREE.
    ranked = arena("teams needing audit logs")
    entry(ranked, rival_a, 1600.0, 120.0, 1)
    entry(ranked, rival_b, 1580.0, 120.0, 1)
    # One recorded comparison with a reason, so the log test has something to read
    # that no write in the test could have produced.
    cur.execute(
        "INSERT INTO pairwise_votes (arena_id, entity_type, a, b, voter_id, outcome,"
        " weight, reason, created_at) VALUES (?,'project',?,?,?,'a',1.0,?,?)",
        (ranked, rival_a, rival_b, voter, "audit logs are the whole point", now))

    untouched = arena("solo maintainers")
    entry(untouched, rival_a, 1500.0, 350.0, 0)
    entry(untouched, rival_b, 1500.0, 350.0, 0)

    # And one with NOTHING in it. Both live instances carry arenas in this state (the
    # laptop's database has 55 and no entries at all), and the verification answers
    # `agrees: true` over an empty set -- which the panel must not render as agreement.
    arena("nobody has been here")

    con.commit()
    con.close()


def _arena(page, use_case):
    """The .alt-arena block for one use case. Two exist, and which one a test reads
    has to be stated: `.first` would make every assertion depend on their order."""
    block = page.locator(".alt-arena", has=page.locator(".alt-usecase", has_text=use_case)).first
    expect(block).to_be_visible()
    return block


def _open_project(page, base=BASE):
    page.goto(f"{base}/projects/{PROJECT_SLUG}")
    expect(page.locator(".panel-alternatives")).to_be_visible()


def test_the_panel_offers_the_arena_loop_beside_the_ranking(page, server):
    """The three controls have to be RENDERED, not merely referenced.

    Before §29 the API could not hand a voter a pair, had no route for the log and
    none for a verification, so this panel could only display a ranking. A control
    that exists in the script but never reaches the page would leave it that way.
    """
    _open_project(page, server["base"])

    arena = _arena(page, "teams needing audit logs")
    # The ranking itself: both competitors, labelled.
    expect(page.locator(".alt-arena")).to_have_count(3)
    expect(arena.locator(".alt-title")).to_have_count(2)
    expect(arena.locator(".alt-title").first).to_have_text("Rival A")

    expect(arena.locator("[data-arena-pair]")).to_be_visible()
    expect(arena.locator("[data-arena-log]")).to_be_visible()
    expect(arena.locator("[data-arena-verify]")).to_be_visible()
    assert not page.errors, page.errors


def _ratings(block):
    """{competitor title: rating} read off the leaderboard rows."""
    out = {}
    for row in block.locator(".alt-row").all():
        out[row.locator(".alt-title").inner_text()] = float(row.locator(".alt-rating").inner_text())
    return out


def test_judging_a_pair_moves_the_rating_and_records_the_vote(page, server):
    """The loop, end to end in the browser: ask for a pair, judge it, and see the
    ranking respond and the log grow.

    The vote is cast for side A and the assertion is DIRECTIONAL -- A's rating must
    RISE and B's must FALL. An assertion that the rating merely CHANGED would pass
    for a draw, and `both` is what an outcome field read from the wrong place
    produces: a test that cannot tell a win from a draw does not test the vote.
    """
    _open_project(page, server["base"])
    arena = _arena(page, "teams needing audit logs")
    before = _ratings(arena)

    arena.locator("[data-arena-pair]").click()
    pair = page.locator(".pair-form").first
    expect(pair).to_be_visible()
    expect(pair.locator(".pair-question")).to_be_visible()
    # Both sides labelled: a voter shown two unlabelled blobs is being asked to guess.
    expect(pair.locator("[data-pair-side]")).to_have_count(2)
    sides = pair.locator(".pair-label").all_inner_texts()
    assert all(s.strip() for s in sides), sides
    expect(pair.locator(".pair-rating").first).to_be_visible()
    expect(pair.locator(".pair-confidence").first).to_be_visible()

    side_a = pair.locator(".pair-side").first.locator(".pair-label").inner_text()
    side_b = pair.locator(".pair-side").nth(1).locator(".pair-label").inner_text()
    assert side_a in before and side_b in before, (side_a, side_b, before)

    pair.locator("input[name=outcome][value=a]").check()
    pair.locator("input[name=reason]").fill("fewer moving parts")
    pair.locator("button[type=submit]").click()

    # The vote reloads the page (a vote changes two ratings and can change the
    # order), so everything below is read off the new document.
    page.wait_for_load_state("load")
    _open_project(page, server["base"])
    arena = _arena(page, "teams needing audit logs")
    after = _ratings(arena)

    assert after[side_a] > before[side_a], (
        f"{side_a} won the comparison and its rating went {before[side_a]} -> {after[side_a]}")
    assert after[side_b] < before[side_b], (
        f"{side_b} lost the comparison and its rating went {before[side_b]} -> {after[side_b]}")

    # And the log the ranking is derived from now carries it, with the reason the
    # voter wrote -- §5.2's audit view is what an appeal is argued from.
    arena.locator("[data-arena-log]").click()
    log = arena.locator(".arena-log-list")
    expect(log).to_be_visible()
    expect(log).to_contain_text("fewer moving parts")
    expect(log).to_contain_text("audit logs are the whole point")
    assert not page.errors, page.errors


def test_verifying_states_that_the_log_reproduces_the_ranking(page, server):
    """§5.1's "Anyone can verify a ranking", rendered.

    The verdict is a positive string, and it is asserted for the state the fixture
    is really in. The replay itself is pinned by the store tests; what this proves
    is that a reader can reach it and that the panel states it.
    """
    _open_project(page, server["base"])

    arena = _arena(page, "solo maintainers")
    arena.locator("[data-arena-verify]").click()
    out = arena.locator(".arena-verify")
    expect(out.locator(".arena-agrees")).to_be_visible()
    expect(out.locator(".arena-agrees")).to_contain_text("reproduces every rating")
    expect(arena.locator("[data-arena-recompute]")).to_be_visible()
    assert not page.errors, page.errors


def test_a_ranking_the_log_does_not_produce_is_reported_and_repairable(page, server):
    """The verification has to be able to say NO.

    A hand-written rating is what drift looks like: the vote log is append-only, so
    a stored value the log does not produce can only come from a write that scored
    something the log does not contain. The test then REPAIRS it, which is both the
    second half of the claim and what leaves the fixture consistent for any later
    test in this file.
    """
    _open_project(page, server["base"])
    # The ranked arena's ratings are hand-written and its log does not produce them,
    # which is what drift IS. No perturbation is needed to create it -- and the
    # repair below is what makes the state consistent again.
    arena = _arena(page, "teams needing audit logs")
    arena.locator("[data-arena-verify]").click()
    out = arena.locator(".arena-verify")
    expect(out.locator(".arena-disagrees")).to_be_visible()
    expect(out.locator(".arena-drift-list")).to_be_visible()

    arena.locator("[data-arena-recompute]").click()
    expect(out.locator(".arena-note")).to_contain_text("repaired")

    # And the repair is real: a fresh verification agrees.
    arena.locator("[data-arena-verify]").click()
    expect(arena.locator(".arena-verify").locator(".arena-agrees")).to_be_visible()
    assert not page.errors, page.errors


def test_an_arena_with_no_competitors_does_not_report_agreement(page, server):
    """The vacuous case, which both live instances are full of.

    `agrees: true` over an empty set is the same shape as the verification that
    covered 200 of an arena's 327 entries: a check reporting a verdict about a
    comparison set it did not examine. The panel states which case it is.
    """
    _open_project(page, server["base"])
    arena = _arena(page, "nobody has been here")
    # The ranking area itself says so, before anyone asks for a verdict -- in the
    # leaderboard's own words. The two places state it separately on purpose: the
    # ranking having nothing in it and the verification having nothing to check are
    # different facts, and one merged sentence reads as "nothing to see here" twice.
    expect(arena.locator(".card-text")).to_contain_text("No projects entered yet")

    arena.locator("[data-arena-verify]").click()
    out = arena.locator(".arena-verify")
    expect(out.locator(".arena-empty")).to_be_visible()
    expect(out.locator(".arena-empty")).to_contain_text("no competitors to verify")
    expect(out.locator(".arena-agrees")).to_have_count(0)
    assert not page.errors, page.errors
