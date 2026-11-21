"""Targeted browser gate for the compact consent bar and 44px header targets."""

import tempfile

import pytest
from playwright.sync_api import sync_playwright

import harness

PORT = 8466
BASE = f"http://127.0.0.1:{PORT}"


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-hc-db-")
    logdir = tempfile.mkdtemp(prefix="concord-hc-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    yield {"base": base, "db": db}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser():
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


@pytest.fixture()
def ctx(browser, server):
    context = browser.new_context(viewport={"width": 1440, "height": 1000},
                                  extra_http_headers={"X-Forwarded-For": "10.77.0.9"})
    context.clear_cookies()
    yield context
    context.close()


def test_notice_is_a_compact_bar_with_dismiss(ctx, server):
    pg = ctx.new_page()
    pg.goto(BASE + "/")
    pg.wait_for_selector("#cookie-notice", timeout=10000)
    bar = pg.locator("#cookie-notice")
    h = bar.evaluate("e => Math.round(e.getBoundingClientRect().height)")
    assert h <= 110, h
    assert pg.locator("#cookie-dismiss").count() == 1
    assert pg.locator("#cookie-accept").evaluate(
        "e => Math.round(e.getBoundingClientRect().height)") >= 40

    pg.locator("#cookie-dismiss").click()
    assert bar.count() == 0
    assert pg.locator("body").get_attribute("data-consent") == "required"


def test_accept_still_records_consent(ctx, server):
    pg = ctx.new_page()
    pg.goto(BASE + "/")
    pg.wait_for_selector("#cookie-accept", timeout=10000)
    pg.locator("#cookie-accept").click()
    pg.wait_for_function("() => document.body.getAttribute('data-consent') === 'accepted'", timeout=10000)
    assert pg.locator("#cookie-notice").count() == 0


def test_header_targets_are_44px_without_growing_header(ctx, server):
    pg = ctx.new_page()
    pg.goto(BASE + "/")
    pg.wait_for_selector(".site-header", timeout=10000)
    measures = pg.evaluate("""() => {
      const links = [...document.querySelectorAll('.site-nav a')];
      return {
        header: Math.round(document.querySelector('.site-header').getBoundingClientRect().height),
        brand: Math.round(document.querySelector('.brand').getBoundingClientRect().height),
        minLink: Math.min(...links.map(a => Math.round(a.getBoundingClientRect().height))),
        overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth
      };
    }""")
    assert measures["brand"] >= 44, measures
    assert measures["minLink"] >= 44, measures
    assert measures["header"] <= 82, measures
    assert measures["overflow"] == 0, measures
