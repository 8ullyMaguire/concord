"""Targeted browser gate for grouped navigation and a real footer."""

import tempfile

import pytest
from playwright.sync_api import sync_playwright

import harness

PORT = 8467
BASE = f"http://127.0.0.1:{PORT}"


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-nf-db-")
    logdir = tempfile.mkdtemp(prefix="concord-nf-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    yield {"base": base, "db": db}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser():
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


@pytest.fixture()
def page(browser, server):
    ctx = browser.new_context(viewport={"width": 1440, "height": 1000},
                              extra_http_headers={"X-Forwarded-For": "10.77.0.9"})
    pg = ctx.new_page()
    pg.goto(BASE + "/")
    pg.wait_for_selector(".site-header", timeout=10000)
    yield pg
    ctx.close()


def test_nav_links_are_grouped_without_losing_hooks(page):
    groups = page.locator(".site-nav [role='group']")
    assert groups.count() == 3
    assert [g.get_attribute("aria-label") for g in groups.all()] == [
        "Explore", "Community", "Account"]
    hrefs = page.locator(".site-nav a").evaluate_all("els => els.map(a => a.getAttribute('href'))")
    assert hrefs == ["/scout", "/finder", "/search", "/projects", "/taxonomy",
                     "/stats", "/curation", "/notifications", "/login"]
    assert page.locator("[data-notifications-link]").count() == 1
    assert page.locator("[data-auth-link='login']").count() == 1


def test_footer_has_grouped_destinations_and_version(page):
    footer = page.locator(".site-footer")
    assert footer.locator("[role='group']").count() == 3
    assert footer.locator("a").count() >= 8
    assert "Concord — a forge for discussions." in footer.inner_text()
    assert page.evaluate(
        "() => document.documentElement.scrollWidth - document.documentElement.clientWidth") == 0
