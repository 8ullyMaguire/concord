"""Targeted browser gate for board/list filtering, dedupe and pagination."""

import sqlite3
import tempfile
import time

import pytest
from playwright.sync_api import sync_playwright

import harness

PORT = 8465
BASE = f"http://127.0.0.1:{PORT}"
SLUG = "nav-demo"


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-bl-nav-db-")
    logdir = tempfile.mkdtemp(prefix="concord-bl-nav-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    con = sqlite3.connect(db)
    now = time.time()
    con.execute(
        "INSERT INTO users (username, display_name, created_at, password_hash, trust_level)"
        " VALUES (?,?,?,?,?)", ("nav-owner", "Nav Owner", now, "x", 3))
    owner = con.execute("SELECT id FROM users WHERE username='nav-owner'").fetchone()[0]
    con.execute(
        "INSERT INTO projects (slug,name,description,governance_model,license,"
        "created_at,updated_at,visibility) VALUES (?,?,?,'collective','MIT',?,?,'public')",
        (SLUG, "Nav demo", "fixture", now, now))
    pid = con.execute("SELECT id FROM projects WHERE slug=?", (SLUG,)).fetchone()[0]
    con.execute("INSERT INTO board_columns (project_id,phase,position,wip_limit) VALUES (?,?,0,NULL)",
                (pid, "proposed"))
    col = con.execute("SELECT id FROM board_columns WHERE project_id=?", (pid,)).fetchone()[0]
    for i in range(30):
        con.execute(
            "INSERT INTO features (project_id,title,body,status,author_id,created_at,updated_at)"
            " VALUES (?,?,?,?,?,?,?)",
            (pid, f"Add a feature {i:02d}", "d", "draft", owner, now, now))
        fid = con.execute("SELECT id FROM features WHERE project_id=? AND title=?", (pid, f"Add a feature {i:02d}")).fetchone()[0]
        con.execute("INSERT INTO board_cards (project_id,kind,feature_id,column_id,entered_at)"
                    " VALUES (?,'feature',?,?,?)", (pid, fid, col, now))
    con.execute("INSERT INTO lists (project_id,slug,title,description,status,created_by,created_at,updated_at)"
                " VALUES (NULL,?,?,?,?,?,?,?)", ("instance-nav", "Instance list", "fixture", "open", owner, now, now))
    con.execute("INSERT INTO lists (project_id,slug,title,description,status,created_by,created_at,updated_at)"
                " VALUES (?,?,?,?,?,?,?,?)", (pid, "nav", "Nav list", "fixture", "open", owner, now, now))
    lid = con.execute("SELECT id FROM lists WHERE project_id=? AND slug='nav'", (pid,)).fetchone()[0]
    entries = []
    for i in range(30):
        entries.append((f"https://example.invalid/{i:02d}", f"Entry {i:02d}", "runtime"))
    entries.append(("https://example.invalid/00-dup", "Entry 00", "runtime"))  # duplicate title
    for i, (url, title, cat) in enumerate(entries):
        con.execute(
            "INSERT INTO list_entries (list_id,url,title,description,category,status,"
            "elo_r,elo_rd,elo_vol,proposed_by,created_at,updated_at)"
            " VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
            (lid, url, title, "d", cat, "accepted", 1600 - i, 30, 0.08, owner, now, now))
    con.commit()
    con.close()
    yield {"base": base, "db": db}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser():
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


@pytest.fixture()
def page(browser, server):
    ctx = browser.new_context(viewport={"width": 1440, "height": 1000})
    ctx.add_cookies([{"name": "concord_consent", "value": "1", "url": BASE + "/"}])
    yield ctx.new_page(), ctx


def open_board(browser, server, slug=SLUG):
    ctx = browser.new_context(viewport={"width": 1440, "height": 1000})
    ctx.add_cookies([{"name": "concord_consent", "value": "1", "url": BASE + "/"}])
    pg = ctx.new_page()
    pg.goto(BASE + f"/projects/{slug}/board")
    pg.wait_for_selector("#kanban-board .kanban-card", timeout=10000)
    return pg, ctx


def open_list(browser, server):
    con = sqlite3.connect(server["db"])
    lid = con.execute("SELECT id FROM lists WHERE slug='nav'").fetchone()[0]
    con.close()
    ctx = browser.new_context(viewport={"width": 1440, "height": 1000})
    ctx.add_cookies([{"name": "concord_consent", "value": "1", "url": BASE + "/"}])
    pg = ctx.new_page()
    errors = []
    pg.on("pageerror", lambda e: errors.append(str(e)))
    pg.on("console", lambda m: errors.append(m.text) if m.type == "error" else None)
    pg.goto(BASE + f"/projects/{SLUG}/lists/{lid}")
    try:
        pg.wait_for_selector("#list-root .list-entry", timeout=10000)
    except Exception:
        raise AssertionError(("list render errors", errors, lid, pg.url, pg.locator("#list-root").inner_text()))
    return pg, ctx


def test_board_filter_and_show_all(browser, server):
    pg, ctx = open_board(browser, server)
    try:
        assert pg.locator("#board-filter").count() == 1
        assert pg.locator(".kanban-card").count() == 30
        pg.locator("#board-filter").fill("feature 01")
        assert pg.locator(".kanban-card").count() == 1
        pg.locator("#board-filter").fill("")
        pg.locator("#board-show-all").click()
        assert pg.locator(".kanban-card").count() == 30
    finally:
        ctx.close()


def test_list_dedupe_filter_and_pagination(browser, server):
    pg, ctx = open_list(browser, server)
    try:
        rows = pg.locator("#list-root .list-entry")
        assert rows.count() == 25, rows.count()  # 31 seeded, one duplicate title, 25 per page
        assert pg.locator("#list-filter").count() == 1
        pg.locator("#list-filter").fill("Entry 03")
        assert rows.count() == 1
        pg.locator("#list-filter").fill("")
        assert pg.locator("#list-page").inner_text().startswith("Page 1")
        pg.locator("#list-next").click()
        assert pg.locator("#list-page").inner_text().startswith("Page 2")
    finally:
        ctx.close()
