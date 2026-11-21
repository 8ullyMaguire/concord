"""Targeted browser gate for the charter form's labels, help and save state."""

import sqlite3
import tempfile
import time

import pytest
from playwright.sync_api import sync_playwright

import harness

PORT = 8463
BASE = f"http://127.0.0.1:{PORT}"
SLUG = "settings-fields"


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-settings-fields-db-")
    logdir = tempfile.mkdtemp(prefix="concord-settings-fields-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    con = sqlite3.connect(db)
    now = time.time()
    con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " visibility, created_at, updated_at) VALUES (?,?,?,'collective','AGPL-3.0',"
        "'public',?,?)", (SLUG, "Settings fields", "Fixture project", now, now))
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
    pg.goto(BASE + f"/projects/{SLUG}/settings")
    pg.wait_for_selector("#charter-form input[name]", timeout=10000)
    yield pg
    ctx.close()


def test_labels_are_human_readable(page):
    labels = page.locator("#charter-form .form-label").all_text_contents()
    assert labels
    assert not [x for x in labels if "_" in x], labels


def test_ranking_fields_have_inline_help(page):
    assert len(page.locator("#charter-form .field-help").all_text_contents()) >= 4
    for key, word in (("glicko_tau", "volatility"), ("lam", "decay"),
                      ("mu", "baseline"), ("pain_halflife_days", "half")):
        wrapper = page.locator(f"#charter-form input[name='{key}']").locator("xpath=..")
        assert word in wrapper.locator(".field-help").inner_text().lower(), key


def test_actions_are_sticky_with_dirty_state(page):
    actions = page.locator("#charter-form .charter-actions")
    assert actions.count() == 1
    assert page.evaluate("e => getComputedStyle(e).position", actions.element_handle()) == "sticky"
    dirty = page.locator("#charter-form [data-dirty]")
    assert dirty.count() == 1
    assert "saved" in dirty.inner_text().lower()


def test_edit_marks_dirty_and_reset_restores(page):
    field = page.locator("#charter-form input[name='glicko_tau']")
    assert field.count() == 1
    original = field.input_value()
    field.fill("9.75")
    assert "unsaved" in page.locator("#charter-form [data-dirty]").inner_text().lower()
    page.locator("#charter-form [data-reset]").click()
    assert field.input_value() == original
    assert "saved" in page.locator("#charter-form [data-dirty]").inner_text().lower()
