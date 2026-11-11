"""Reversing an arena removal from the project's audit page.

The audit page is the per-project view of the same acts the curation queue lists instance-wide.
Before this suite it rendered `audit_log.detail` raw -- a reader saw `{"reason": "spam", "prev":
{...}}` -- and offered no way to reverse anything, so the only place a removal could be undone was
the feature page whose panel wrote it. That is the half of the owner's request ("there should
probably be confirmation or some way to undo a deletion") that the queue closed for maintainers
and left open for everyone looking at a project's own log.

What only a browser can prove here:

  * the reason is rendered, not the JSON object it lives in;
  * a reader who may not moderate is offered nothing -- the read carries `can_undo`, and a page
    that ignored it would show a button that answers 403;
  * the reversal asks for a reason BEFORE the round trip, because the route refuses an empty one;
  * after the reversal the log says so: the removal is marked `undone` and the restore act names
    what it reversed. A log that shows two removals and no direction is a log you cannot audit.
"""

import sqlite3
import tempfile

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

PORT = 8461
BASE = f"http://127.0.0.1:{PORT}"
SLUG = "audit-undo-demo"
OWNER = "audit-undo-owner"
PASSWORD = "correct-horse-battery"


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-auditundo-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-auditundo-log-")
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
        "username": username, "password": PASSWORD, "display_name": username,
    })
    assert reg and reg.get("token"), f"register returned no token: {reg}"
    user = reg.get("user") or {}
    return reg["token"], (user.get("id") or reg.get("user_id") or reg.get("id")), user


def seed(db):
    """A project with two ranked features, one of them removed through the real route.

    The removal goes through the API rather than an INSERT: the restore act reads the prior
    status and the entry's four numbers back out of `detail`, so a hand-written row would make
    the reversal fail for a reason that has nothing to do with the page.
    """
    token, uid, _ = _register(OWNER)
    now = 1790000000
    con = sqlite3.connect(db)
    try:
        pid = con.execute(
            "INSERT INTO projects (slug, name, description, governance_model, license,"
            " created_at, updated_at, visibility) VALUES (?,?,?,'collective','MIT',?,?,'public')",
            (SLUG, "Audit Undo Demo", "fixture", now, now)).lastrowid
        con.execute(
            "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,'owner',?)",
            (pid, uid, now))
        con.execute("UPDATE users SET trust_level = 1 WHERE id = ?", (uid,))

        features = []
        for title in ("Idea to reverse", "Idea to keep"):
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
        "feature_id": features[0], "reason": "duplicate of the sharper idea", "confirm": True,
    }, token=token)
    assert removed, "the removal route answered nothing"


def _page(browser, token=None, user=None):
    ctx = browser.new_context(extra_http_headers={"X-Forwarded-For": "10.97.9.9"})
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
    pg.goto(f"{BASE}/projects/{SLUG}/audit")
    expect(pg.locator("#audit-root")).to_have_attribute("aria-busy", "false", timeout=15000)
    return pg


def test_the_row_shows_the_reason_not_the_json(browser, server):
    pg = _page(browser)
    root = pg.locator("#audit-root")
    expect(root).to_contain_text("duplicate of the sharper idea")
    expect(root).not_to_contain_text('"reason"')
    assert pg.errors == [], pg.errors
    pg.close()


def test_an_anonymous_reader_is_offered_no_undo(browser, server):
    pg = _page(browser)
    expect(pg.locator("[data-undo-open]")).to_have_count(0)
    assert pg.errors == [], pg.errors
    pg.close()


def _login(username):
    """The owner is created by seed(); registering again is a 409, not a new account."""
    res = harness.api(BASE, "POST", "/api/v1/auth/login", {
        "username": username, "password": PASSWORD,
    })
    assert res and res.get("token"), f"login returned no token: {res}"
    user = res.get("user") or {}
    return res["token"], user


def test_a_moderator_reverses_the_removal_and_the_log_says_so(browser, server):
    token, user = _login(OWNER)
    pg = _page(browser, token=token, user=user)

    pg.locator("[data-undo-open]").first.click()
    # An empty reason is refused in the page, with the sentence the route would give.
    pg.locator("[data-undo-confirm]").first.click()
    expect(pg.locator("[data-audit-note]")).to_contain_text("needs a reason")

    pg.locator("[data-undo-reason]").first.fill("it was the sharper idea after all")
    pg.locator("[data-undo-confirm]").first.click()
    expect(pg.locator("[data-audit-note]")).to_contain_text("Reversed", timeout=15000)

    root = pg.locator("#audit-root")
    expect(root).to_contain_text("undone")
    expect(root).to_contain_text("reversal of #")
    # The reason the reversal was given is on the restore act, not only in the note.
    expect(root).to_contain_text("it was the sharper idea after all")
    # And the removal no longer offers Undo: it has been reversed.
    expect(pg.locator("[data-undo-open]")).to_have_count(0)
    assert pg.errors == [], pg.errors
    pg.close()
