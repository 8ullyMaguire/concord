"""The curation queue (docs/plans/curation-queue.md), in a browser.

The page is the only reader of GET /api/v1/curation and POST /api/v1/curation/{kind}/{id}/vote.
Both have Go tests against the store and the handlers; neither of those can see that the page

  * opens on work-to-do. The first draft of the page defaulted its state filter to the literal
    "pending", and moderation acts have no pending phase -- they exist only once somebody has
    done one. The page therefore listed every proposal and NO act, which is the half of the
    queue the owner asked for, and it looked completely fine. The server now answers a state of
    "open" (pending proposals + applied acts) and the page asks for that.
  * renders the act's reason rather than its JSON. `audit_log.detail` is an object, and a page
    that prints it shows `{"reason": "spam", ...}`.
  * reverses an act from the queue. A contest records disagreement; reversing is a deliberate
    act that needs a reason, and the route refuses one without it. A page that posted an empty
    reason would get a 400 and read as broken.
  * drops a row from the queue once you have voted on it. That is the question "what is left to
    do", and a row that stays after your consent answers it wrong.

None of those is reachable from Go: a ReferenceError parses cleanly, so `node --check` proves
nothing about whether the page runs.
"""

import sqlite3
import tempfile

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

# One fixed port per suite; the cross-file gate parses this line with int().
PORT = 8460
BASE = f"http://127.0.0.1:{PORT}"
SLUG = "curation-demo"
OWNER = "curation-owner"
PASSWORD = "correct-horse-battery"


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-cur-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-cur-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    seed(db)
    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


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
    return token, uid, user


def seed(db):
    """A project with two arena entries, one removal act, and one taxonomy proposal.

    The act goes through the real route rather than an INSERT: `arena.restore` reads the prior
    status and the entry's four numbers back out of the act's `detail`, so a hand-written audit
    row would make Undo fail for a reason that has nothing to do with the page.
    """
    token, uid, _ = _register(OWNER)
    now = 1790000000
    con = sqlite3.connect(db)
    try:
        pid = con.execute(
            "INSERT INTO projects (slug, name, description, governance_model, license,"
            " created_at, updated_at, visibility) VALUES (?,?,?,'collective','MIT',?,?,?)",
            (SLUG, "Curation Demo", "fixture", now, now, "public")).lastrowid
        con.execute(
            "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,'owner',?)",
            (pid, uid, now))
        # Trust 1 is CONCORD_MOD_TRUST_MIN: enough to moderate, by the instance path.
        con.execute("UPDATE users SET trust_level = 1 WHERE id = ?", (uid,))

        features = []
        for title in ("An idea to remove", "An idea to keep"):
            features.append(con.execute(
                "INSERT INTO features (project_id, author_id, title, body, effort, status,"
                " created_at, updated_at) VALUES (?,?,?,'a body','M','discussion',?,?)",
                (pid, uid, title, now, now)).lastrowid)
        arena = con.execute(
            "INSERT INTO arenas (type, project_id, feature_id, question, use_case, created_at)"
            " VALUES ('feature-priority',?,0,'Which idea should we build next?','fixture',?)",
            (pid, now)).lastrowid
        for fid in features:
            con.execute(
                "INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd, sigma,"
                " games, is_baseline, parent_entry_id, updated_at)"
                " VALUES (?,'feature',?,1500.0,350.0,0.06,0,0,NULL,?)",
                (arena, fid, now))
        con.commit()
    finally:
        con.close()

    removed = harness.api(BASE, "POST", f"/api/v1/projects/{SLUG}/moderation/arena/remove", {
        "feature_id": features[0], "reason": "duplicate of the other idea", "confirm": True,
    }, token=token)
    assert removed, "the removal route answered nothing"

    proposed = harness.api(BASE, "POST", "/api/v1/taxonomy/proposals", {
        "action": "create", "target_tag": "topic:curation", "value": "",
        "rationale": "the queue needs a label of its own",
    }, token=token)
    assert proposed, "the taxonomy proposal answered nothing"


def _page(browser, token=None, user=None, **kw):
    ctx = browser.new_context(extra_http_headers={"X-Forwarded-For": "10.96.7.7"}, **kw)
    pg = ctx.new_page()
    pg.errors = []
    pg.on("pageerror", lambda e: pg.errors.append("PAGEERROR: " + str(e)))
    pg.on("console", lambda m: pg.errors.append("CONSOLE: " + m.text)
          if m.type == "error" and "Failed to load resource" not in m.text else None)
    pg.goto(BASE + "/")
    if token:
        pg.evaluate("t => window.localStorage.setItem('concord.token', t)", token)
    if user:
        pg.evaluate("u => window.localStorage.setItem('concord.user', JSON.stringify(u))", user)
    return pg


def open_queue(page):
    page.goto(BASE + "/curation")
    page.wait_for_selector("#curation-list li", timeout=15000)
    return page.locator("#curation-list")


def test_the_queue_is_reachable_and_lists_the_act(browser, server):
    """Anonymous, because the queue is a read of public work.

    The act must be there. This is the assertion the page's first draft failed: the default
    filter was "pending" and no act has ever been pending.
    """
    pg = _page(browser)
    nav = pg.locator("nav a[href='/curation']")
    expect(nav).to_have_count(1)

    lst = open_queue(pg)
    expect(lst.locator("[data-kind='act']")).to_have_count(1)
    expect(lst.locator("[data-kind='taxonomy']")).to_have_count(1)

    act = lst.locator("[data-kind='act']").first
    # The reason, not the JSON object it lives in.
    expect(act).to_contain_text("duplicate of the other idea")
    expect(act).not_to_contain_text('"reason"')
    assert pg.errors == [], pg.errors
    pg.close()


def test_the_kind_filter_narrows_to_one_kind(browser, server):
    pg = _page(browser)
    open_queue(pg)
    pg.locator("#curation-kind").select_option("taxonomy")
    # The list is replaced, not appended to: the act must be gone rather than below the fold.
    expect(pg.locator("#curation-list [data-kind='act']")).to_have_count(0)
    expect(pg.locator("#curation-list [data-kind='taxonomy']")).to_have_count(1)
    assert pg.errors == [], pg.errors
    pg.close()


def _make_contributor(db, username):
    """Membership has no write route, so it comes from the database (board_e2e.py's shape).

    A taxonomy proposal is consented to by a contributor of its project -- instance-wide
    proposals by a contributor somewhere -- so a plain account is refused with "only an
    eligible tagger can consent", which is a rule working, not a page failing.
    """
    con = sqlite3.connect(db)
    try:
        uid = con.execute("SELECT id FROM users WHERE username = ?", (username,)).fetchone()[0]
        pid = con.execute("SELECT id FROM projects WHERE slug = ?", (SLUG,)).fetchone()[0]
        con.execute(
            "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,'contributor',?)",
            (pid, uid, 1790000000))
        con.commit()
    finally:
        con.close()


def test_a_signed_in_reader_without_standing_is_told_so(browser, server):
    """The page must not offer a button the server refuses.

    This is the assertion that caught the first version: it rendered Consent for everybody, and
    a reader with no standing got a red "permission denied: only an eligible tagger can consent"
    where a button had been.
    """
    token, _, user = _register("curation-outsider")
    pg = _page(browser, token=token, user=user)
    lst = open_queue(pg)
    card = lst.locator("[data-kind='taxonomy']").first
    expect(card.locator("button", has_text="Consent")).to_have_count(0)
    expect(card).to_contain_text("no standing")
    # The Undo control takes the same standing as the restore route, and it took the browser to
    # notice it was offered to a reader the route would refuse.
    expect(lst.locator("button", has_text="Undo this removal")).to_have_count(0)
    assert pg.errors == [], pg.errors
    pg.close()


def test_consent_leaves_the_queue_and_the_act_does_not(browser, server):
    token, uid, user = _register("curation-voter")
    _make_contributor(server["db"], "curation-voter")
    pg = _page(browser, token=token, user=user)
    lst = open_queue(pg)
    expect(lst.locator("[data-kind='taxonomy']")).to_have_count(1)

    card = lst.locator("[data-kind='taxonomy']").first
    card.locator("button", has_text="Consent").click()

    # Below quorum the server answers 202 and the page must say so as information, not as an
    # error: "Recorded." is the 200 path, "Recorded. Not decided yet" the 202 one.
    expect(pg.locator("#curation-status")).to_contain_text("Recorded", timeout=15000)
    # And the row is gone from the work list: it is what this reader has already voted on.
    expect(pg.locator("#curation-list [data-kind='taxonomy']")).to_have_count(0)
    # The act is untouched by somebody else's consent.
    expect(pg.locator("#curation-list [data-kind='act']")).to_have_count(1)
    assert pg.errors == [], pg.errors
    pg.close()


def test_undo_from_the_queue_reverses_the_removal(browser, server):
    """The other half of the review: a contest records disagreement, Undo reverses.

    Only a reader with standing sees the control -- the owner here, at trust 1 -- and the
    reversal asks for a reason because the route refuses one without it.
    """
    token, _, user = _register("curation-reviewer")
    con = sqlite3.connect(server["db"])
    try:
        con.execute("UPDATE users SET trust_level = 1 WHERE username = ?", ("curation-reviewer",))
        con.commit()
    finally:
        con.close()

    pg = _page(browser, token=token, user=user)
    lst = open_queue(pg)
    act = lst.locator("[data-kind='act']").first
    act.locator("button", has_text="Undo this removal").click()

    # An empty reason is refused in the page, before the round trip, with the reason the route
    # would give.
    act.locator("button", has_text="Reverse it").click()
    expect(pg.locator("#curation-status")).to_contain_text("needs a reason")

    act.locator("input[type='text']").fill("wrong idea removed, it is the sharper one")
    act.locator("button", has_text="Reverse it").click()
    expect(pg.locator("#curation-status")).to_contain_text("Reversed", timeout=15000)

    # The removal is no longer open -- it is undone -- and a queue that still listed it would
    # be lying about the state of the arena.
    expect(pg.locator("#curation-list [data-kind='act']")).to_have_count(0)

    # The act that did it is in the LOG, which is this same list with the filters opened up.
    # It is NOT in the work list even then by default, because its actor is the reader: a queue
    # that shows you your own work never empties.
    pg.locator("#curation-unvoted").uncheck()
    pg.locator("#curation-state").select_option("")
    expect(pg.locator("#curation-list")).to_contain_text("arena.restore", timeout=15000)
    expect(pg.locator("#curation-list")).to_contain_text("undone")
    assert pg.errors == [], pg.errors
    pg.close()
