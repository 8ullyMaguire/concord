"""axe-core WCAG 2.1 A/AA sweep in a real browser.

A canary proves the instrument can fail before the sweep's clean result is trusted.
Both colour schemes and both widths are exercised; axe-core is vendored, so the
gate has no network dependency.
"""

import json
import sqlite3
import tempfile
import time
from pathlib import Path

import pytest
from playwright.sync_api import sync_playwright

import harness

PORT = 8462
BASE = f"http://127.0.0.1:{PORT}"
SLUG = "a11y"
AXE = Path("scripts/parity/vendor/axe.min.js").resolve()
REPORT = Path("docs/evidence/2026-11-21-axe-report.json")
TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]
ROUTES = [
    "/", "/login", "/register", "/notifications", "/scout", "/finder", "/projects",
    f"/projects/{SLUG}", f"/projects/{SLUG}/board", f"/projects/{SLUG}/consensus",
    f"/projects/{SLUG}/criteria", f"/projects/{SLUG}/settings", "/taxonomy", "/stats", "/lists",
]
MODES = [("light", 1440, 1000), ("light", 390, 844), ("dark", 1440, 1000), ("dark", 390, 844)]


RUN_AXE = """async () => {
  const r = await axe.run(document, { runOnly: { type: 'tag', values: %s } });
  return r.violations.map(v => ({ id: v.id, impact: v.impact, help: v.help,
    summary: v.nodes[0] && v.nodes[0].failureSummary,
    nodes: v.nodes.length, targets: v.nodes.slice(0,3).map(n => n.target) }));
}""" % json.dumps(TAGS)


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-a11y-db-")
    logdir = tempfile.mkdtemp(prefix="concord-a11y-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    con = sqlite3.connect(db)
    now = time.time()
    con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " visibility, created_at, updated_at) VALUES (?,?,?,'collective','AGPL-3.0',"
        "'public',?,?)", (SLUG, "A11y fixture", "Fixture project", now, now))
    con.commit()
    con.close()
    yield {"base": base, "db": db}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser():
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


def prepared(browser, scheme, width, height):
    ctx = browser.new_context(viewport={"width": width, "height": height}, color_scheme=scheme, reduced_motion="reduce")
    ctx.add_cookies([{"name": "concord_consent", "value": "1", "url": BASE + "/"}])
    # CDP init script, not a script tag: the site's strict CSP refuses inline scripts.
    ctx.add_init_script(path=str(AXE))
    page = ctx.new_page()
    page.goto(BASE + "/")
    return ctx, page


def test_canary_is_detected(browser, server):
    ctx, page = prepared(browser, "light", 1440, 1000)
    try:
        page.evaluate("() => { const b=document.createElement('button'); b.id='axe-canary'; document.body.appendChild(b); }")
        found = page.evaluate(RUN_AXE)
        assert any(v["id"] == "button-name" for v in found), [v["id"] for v in found]
    finally:
        ctx.close()


def test_no_wcag_aa_violations(browser, server):
    findings = []
    for scheme, width, height in MODES:
        ctx, page = prepared(browser, scheme, width, height)
        try:
            for route in ROUTES:
                page.goto(BASE + route)
                page.wait_for_timeout(150)
                for violation in page.evaluate(RUN_AXE):
                    findings.append({"scheme": scheme, "width": width, "route": route, **violation})
        finally:
            ctx.close()
    REPORT.parent.mkdir(parents=True, exist_ok=True)
    REPORT.write_text(json.dumps({"routes": len(ROUTES), "modes": len(MODES),
                                  "violations": findings}, indent=2) + "\n")
    compact = [(f["scheme"], f["width"], f["route"], f["id"], f["nodes"]) for f in findings]
    assert not findings, compact[:20]
