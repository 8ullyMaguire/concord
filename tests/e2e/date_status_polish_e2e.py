"""Targeted browser gate for humanized statuses and ISO document dates."""

import json
import re
import sqlite3
import time
import tempfile

import pytest
from playwright.sync_api import sync_playwright

import harness

PORT = 8468
BASE = f"http://127.0.0.1:{PORT}"


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-dsp-db-")
    logdir = tempfile.mkdtemp(prefix="concord-dsp-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    con = sqlite3.connect(db)
    now = time.time()
    con.execute(
        "INSERT INTO projects (slug,name,description,governance_model,license,"
        "created_at,updated_at,visibility) VALUES (?,?,?,'collective','MIT',?,?,'public')",
        ("concord", "Concord", "polish fixture", now, now))
    con.commit()
    con.close()
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
    yield context
    context.close()


def fulfill(route, payload):
    route.fulfill(status=200, content_type="application/json", body=json.dumps(payload))


def test_stats_statuses_are_humanized(ctx, server):
    pg = ctx.new_page()
    pg.route("**/api/v1/stats/summary", lambda route: fulfill(route, {
        "features_by_status": [
            {"value": "in_progress", "total": 3},
            {"value": "stand_aside", "total": 1},
        ],
        "complaints_by_status": [{"value": "awaiting_response", "total": 2}],
    }))
    for path in ("/api/v1/stats", "/api/v1/stats/rising", "/api/v1/stats/responsiveness"):
        pg.route("**" + path, lambda route: fulfill(route, [] if path.endswith(("rising", "responsiveness")) else {}))
    pg.goto(BASE + "/stats")
    pg.wait_for_function(
        "() => document.querySelector('#stats-summary')?.innerText.includes('In progress')",
        timeout=10000)
    text = pg.locator("#stats-summary").inner_text()
    assert "Stand aside" in text
    assert "Awaiting response" in text
    assert "in_progress" not in text
    assert "stand_aside" not in text


def test_document_dates_use_one_iso_day(ctx, server):
    pg = ctx.new_page()
    docs = [{
        "id": 1, "kind": "spec", "slug": "spec", "title": "Dated document",
        "revision": 2, "updated_at": 1700000000, "body": "# Dated document\n\nA body.",
    }]
    pg.route(re.compile(r"/api/v1/projects/[^/]+/documents\?fields=meta"), lambda route: fulfill(route, docs))
    pg.goto(BASE + "/projects/concord/documents")
    seen = []
    pg.on("request", lambda r: seen.append(r.url) if "/documents" in r.url else None)
    try:
        pg.wait_for_function(
            "() => document.querySelector('#project-documents')?.innerText.includes('2023-11-14')",
            timeout=10000)
    except Exception:
        raise AssertionError((seen, pg.locator("#project-documents").inner_text()[:400]))
    text = pg.locator("#project-documents").inner_text()
    assert "2023-11-14" in text
    assert "11/14/2023" not in text
