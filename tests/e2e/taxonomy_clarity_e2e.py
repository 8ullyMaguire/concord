"""Targeted browser gate for taxonomy dedupe and hidden internal IDs."""

import sqlite3
import tempfile
import time

import pytest
from playwright.sync_api import sync_playwright

import harness

PORT = 8464
BASE = f"http://127.0.0.1:{PORT}"


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-tax-clarity-db-")
    logdir = tempfile.mkdtemp(prefix="concord-tax-clarity-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    con = sqlite3.connect(db)
    for name in ("forum", "forum", "forum", "performance8103685", "performance8103685"):
        con.execute("INSERT INTO tags (project_id, name, namespace, suggested) VALUES (NULL,?,?,0)",
                    (name, "topic"))
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
    pg = ctx.new_page()
    pg.goto(BASE + "/taxonomy")
    pg.wait_for_selector("ul.tags a", timeout=10000)
    yield pg
    ctx.close()


def test_server_chips_are_deduplicated_with_counts(page):
    labels = page.locator("ul.tags a").all_text_contents()
    clean = [x.replace("×", "").strip() for x in labels]
    assert len(clean) == len(set(clean)), labels
    forum = page.locator("ul.tags a", has_text="forum")
    assert forum.count() == 1
    assert "3" in forum.first.inner_text()


def test_no_raw_id_suffix_in_visible_labels_or_tooltips(page):
    page.wait_for_function(
        "() => {const r=document.getElementById('taxonomy-root'); return r && !r.getAttribute('aria-busy')}",
        timeout=10000)
    titles = page.locator("[data-tag][title]").evaluate_all("els => els.map(e => e.getAttribute('title'))")
    assert not [t for t in titles if t and t[-1].isdigit()], titles
    text = page.locator("main").inner_text()
    assert "performance8103685" not in text
