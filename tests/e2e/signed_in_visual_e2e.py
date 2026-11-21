"""Targeted visual gate for the signed-in surfaces the 2026-11-20 sweep could not see.

A real account is registered over HTTP, its token is installed exactly as the
browser installs it (navigation cookie + concord.user), six surfaces are walked
at two widths, and the session is revoked at the end. No user-supplied secret
is involved and no production account is created.
"""

import json
import sqlite3
import tempfile
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path

import pytest
from playwright.sync_api import sync_playwright

import harness

PORT = 8461
BASE = f"http://127.0.0.1:{PORT}"
SLUG = "signed-in-visual"
GATE = "signed-in reader|Sign in to|needs a session|A session is required"
ROUTES = [
    "/",
    "/notifications",
    f"/projects/{SLUG}/settings",
    f"/projects/{SLUG}/board",
    f"/projects/{SLUG}/rank",
    f"/projects/{SLUG}/consensus",
]
class Redacted:
    """A token that pytest's fixture repr cannot print."""
    def __init__(self, value):
        self.value = value
    def __repr__(self):
        return "***"


REPORT = Path("docs/evidence/2026-11-21-signed-in-visual.json")
SHOTS = Path("/tmp/signed-in-visual-shots")


def status(method, path, token=None):
    headers = {"Cookie": "concord_consent=1"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(BASE + path, data=None, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=10) as response:
            return response.status
    except urllib.error.HTTPError as error:
        return error.code


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-signed-visual-db-")
    logdir = tempfile.mkdtemp(prefix="concord-signed-visual-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    con = sqlite3.connect(db)
    now = time.time()
    con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " visibility, created_at, updated_at) VALUES (?,?,?,'collective','AGPL-3.0',"
        "'public',?,?)",
        (SLUG, "Signed-in visual", "Fixture project for the signed-in sweep", now, now),
    )
    con.commit()
    con.close()
    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser():
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


@pytest.fixture()
def anonymous(browser):
    ctx = browser.new_context(viewport={"width": 1440, "height": 1000})
    ctx.add_cookies([{"name": "concord_consent", "value": "1", "url": BASE + "/"}])
    page = ctx.new_page()
    yield page
    ctx.close()


@pytest.fixture()
def signed(browser, server):
    """A browser signed in through the app's own register + auth/me path (D-215)."""
    name = "sv-" + uuid.uuid4().hex[:10]
    body = harness.api(
        server["base"], "POST", "/api/v1/auth/register",
        {"username": name, "display_name": name, "password": "correct-horse-battery"},
    )
    token = body["token"]
    user_id = body["user"]["id"]
    me = harness.api(server["base"], "GET", "/api/v1/auth/me", token=token)

    con = sqlite3.connect(server["db"])
    pid = con.execute("SELECT id FROM projects WHERE slug = ?", (SLUG,)).fetchone()[0]
    con.execute(
        "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,'owner',?)",
        (pid, user_id, time.time()),
    )
    con.commit()
    con.close()

    ctx = browser.new_context(viewport={"width": 1440, "height": 1000})
    ctx.add_cookies([
        {"name": "concord_consent", "value": "1", "url": BASE + "/"},
        {"name": "concord_nav", "value": token, "url": BASE + "/"},
    ])
    page = ctx.new_page()
    errors = []
    page.on("pageerror", lambda e: errors.append(str(e)))
    page.on("console", lambda m: errors.append(m.text) if m.type == "error" else None)
    bad = []
    page.on("response", lambda r: bad.append((r.status, r.request.method, r.url)) if r.status >= 400 else None)
    page.bad_responses = bad
    page.goto(BASE + "/")
    page.evaluate("u => localStorage.setItem('concord.user', JSON.stringify(u))", me)
    page.reload()
    yield {"page": page, "token": Redacted(token), "errors": errors, "bad": bad, "user": name}
    status("POST", "/api/v1/auth/logout", token=token)
    ctx.close()


def test_signed_out_gate_is_real_and_signed_in_removes_it(anonymous, signed):
    anonymous.goto(BASE + "/notifications")
    assert "signed-in reader" in anonymous.locator("main").inner_text()
    signed["page"].goto(BASE + "/notifications")
    assert not any(word in signed["page"].locator("main").inner_text().lower()
                   for word in ("signed-in reader", "sign in to", "needs a session"))


def test_six_signed_in_surfaces_render_at_both_widths(signed):
    SHOTS.mkdir(parents=True, exist_ok=True)
    page = signed["page"]
    findings = []
    for route in ROUTES:
        for width, height in ((1440, 1000), (390, 844)):
            page.set_viewport_size({"width": width, "height": height})
            page.goto(BASE + route)
            page.wait_for_timeout(200)
            text = page.locator("main").inner_text()
            overflow = page.evaluate(
                "() => document.documentElement.scrollWidth - document.documentElement.clientWidth")
            findings.append({
                "route": route,
                "width": width,
                "title": page.title(),
                "text_len": len(text),
                "gate": bool(__import__("re").search(GATE, text, __import__("re").I)),
                "overflow": overflow,
            })
            page.screenshot(path=str(SHOTS / ((route.strip("/").replace("/", "-") or "home") + f"-{width}.png")))
    for item in findings:
        assert item["text_len"] > 20, item
        assert not item["gate"], item
        assert item["overflow"] == 0, item
    # D-216: a fixture project with no comparable features answers 409 on
    # /votes/next. rank.js renders that as the honest empty state; the browser's
    # "Failed to load resource 409" console line is not a JavaScript error.
    real_errors = [e for e in signed["errors"] if "409 (Conflict)" not in e]
    assert real_errors == [], (real_errors, signed["bad"])
    unexpected = [b for b in signed["bad"]
                  if not (b[0] == 409 and b[1] == "GET" and b[2].endswith("/votes/next"))]
    assert unexpected == [], unexpected
    REPORT.parent.mkdir(parents=True, exist_ok=True)
    REPORT.write_text(json.dumps({"session": "minted via register, revoked via logout",
                                  "routes": findings}, indent=2) + "\n")
    assert len(findings) == len(ROUTES) * 2


def test_session_is_deleted_after_the_walk(signed):
    assert status("POST", "/api/v1/auth/logout", token=signed["token"].value) in (200, 204)
    assert status("GET", "/api/v1/auth/me", token=signed["token"].value) == 401


def test_personalized_page_is_not_cacheable(signed):
    headers = signed["page"].evaluate(
        "async () => { const r = await fetch('/notifications', {credentials:'same-origin'});"
        " const o = {}; r.headers.forEach((v,k) => { o[k.toLowerCase()] = v; }); return o; }")
    assert headers.get("cache-control") == "no-store", headers.get("cache-control")
