"""Targeted browser gates for the 2026-11-20 site-review repairs.

Each test reproduces one measured defect from the 34-route sweep: raw page 404,
the linked-but-missing /lists, the duplicate Scout form, the run-together sub-nav,
a zero home count, and the consensus calls panel refusing a valid list envelope.
"""

import sqlite3
import tempfile
import time

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

PORT = 8460
BASE = f"http://127.0.0.1:{PORT}"


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-site-review-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-site-review-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    con = sqlite3.connect(db)
    now = time.time()
    uid = con.execute(
        "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
        ("site-review", "Site Review", now)).lastrowid
    pid = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " visibility, created_at, updated_at) VALUES (?,?,?,'collective','AGPL-3.0',"
        "'public',?,?)",
        ("site-review", "Site Review", "Fixture project", now, now)).lastrowid
    con.execute(
        "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,'owner',?)",
        (pid, uid, now))
    con.commit()
    con.close()
    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


@pytest.fixture
def page(browser):
    ctx = browser.new_context(viewport={"width": 1440, "height": 1000})
    pg = ctx.new_page()
    yield pg
    ctx.close()


def first_slug(server):
    slug = "site-review"
    con = sqlite3.connect(server["db"])
    try:
        row = con.execute("select slug from projects where slug = ?", (slug,)).fetchone()
    finally:
        con.close()
    assert row, "the harness must seed the site-review project"
    return slug


def test_lists_is_a_page_not_raw_json(page):
    page.goto(BASE + "/lists")
    expect(page.locator("h1")).to_have_text("Lists")
    assert "raw JSON" not in page.content()
    assert page.locator("nav a").count() > 0


def test_unknown_page_uses_the_site_shell(page):
    response = page.goto(BASE + "/no-such-page")
    assert response.status == 404
    expect(page.locator("h1")).to_have_text("Not found")
    assert page.locator("nav a").count() > 0
    assert "no such route" not in page.content()


def test_api_unknown_path_keeps_the_json_envelope(page):
    response = page.goto(BASE + "/api/v1/no-such-route")
    assert response.status == 404
    assert "no such route" in page.content()


def test_scout_has_one_form(page):
    page.goto(BASE + "/scout")
    expect(page.locator("#scout-root")).to_be_visible()
    assert page.locator("form").count() == 1
    assert page.locator("textarea").count() == 1


def test_home_project_count_is_the_true_total(page):
    page.goto(BASE + "/")
    page.wait_for_function(
        "() => /^[1-9]/.test(document.getElementById('stat-projects').innerText)",
        timeout=10000)
    total = harness.api(BASE, "GET", "/api/v1/search?sort=updated")["total"]
    assert int(page.locator("#stat-projects").inner_text()) == total


def test_consensus_calls_accept_the_list_envelope(page, server):
    page.goto(BASE + "/projects/" + first_slug(server) + "/consensus")
    assert "Could not load this project" not in page.locator("main").inner_text()


@pytest.mark.parametrize("suffix", ["criteria", "documents"])
def test_project_sub_nav_does_not_overflow_at_390px(page, server, suffix):
    page.set_viewport_size({"width": 390, "height": 844})
    page.goto(BASE + "/projects/" + first_slug(server) + "/" + suffix)
    overflow = page.evaluate("() => document.documentElement.scrollWidth - document.documentElement.clientWidth")
    assert overflow == 0, f"{suffix} overflowed by {overflow}px"


def test_feature_detail_does_not_end_on_loading(page, server):
    page.goto(BASE + "/projects/" + first_slug(server) + "/features/99999999")
    page.wait_for_function(
        "() => !document.getElementById('feature-root').innerText.includes('Loading the feature')",
        timeout=12000)


def test_milestone_detail_does_not_end_on_loading(page, server):
    page.goto(BASE + "/projects/" + first_slug(server) + "/milestones/99999999")
    page.wait_for_function(
        "() => !document.getElementById('milestone').innerText.includes('Loading milestone')",
        timeout=12000)
